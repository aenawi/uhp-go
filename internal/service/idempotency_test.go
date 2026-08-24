package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp"
)

// countingAdapter records how many runs were actually started and holds each
// one open until the test releases it.
//
// The count is the assertion that matters throughout this file. Tasks §6's
// "MUST NOT start a second execution" is not observable from the response —
// two executions and one execution both produce a plausible-looking task — so
// every test here checks the adapter rather than the answer.
type countingAdapter struct {
	echoAdapter

	// started carries one value per run. It is buffered so a test that never
	// reads it cannot deadlock the adapter.
	started chan struct{}
	release chan struct{}

	mu   sync.Mutex
	runs int
}

func newCountingAdapter() *countingAdapter {
	return &countingAdapter{started: make(chan struct{}, 16), release: make(chan struct{})}
}

// completesImmediately releases every run as soon as it starts, for the tests
// that care about how many runs there were rather than when they ended.
func (a *countingAdapter) completesImmediately() *countingAdapter {
	close(a.release)
	return a
}

func (a *countingAdapter) Run(ctx context.Context, _ harness.RunRequest) (<-chan harness.RunUpdate, error) {
	a.mu.Lock()
	a.runs++
	a.mu.Unlock()
	a.started <- struct{}{}

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		send := func(u harness.RunUpdate) bool {
			select {
			case ch <- u:
				return true
			case <-ctx.Done():
				return false
			}
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return
		}
		if !send(harness.RunUpdate{Type: harness.UpdateDelta, Delta: "done"}) {
			return
		}
		send(harness.RunUpdate{Type: harness.UpdateCompleted})
	}()
	return ch, nil
}

func (a *countingAdapter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs
}

func waitFor(t *testing.T, run *Run) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("run never reached a terminal state: %v", err)
	}
}

// Tasks §6: for a repeated key a server MUST return the result of the first
// request and MUST NOT start a second execution.
//
// The second request deliberately carries different input. The specification
// binds the answer to the key, not to the body, so the first result is the
// right answer even when the retry asks for something else.
func TestRepeatedIdempotencyKeyReturnsTheFirstResult(t *testing.T) {
	a := newCountingAdapter().completesImmediately()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	first, run1, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run1)

	second, run2, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "something else entirely", HarnessID: "echo", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("repeat returned response %s, want the first request's %s", second.ID, first.ID)
	}
	if run2 != run1 {
		t.Error("repeat returned a different run; the retry must observe the first one")
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times; a retry carrying an idempotency key must not run the work again", n)
	}
}

// Tasks §6: if the first request is still running, the server MUST wait for it
// and return its result rather than returning a partial or a conflict.
//
// Without the key this is exactly the Lifecycle §5 session-busy case, so the
// test is also that idempotency is resolved before that refusal.
func TestRepeatedKeyWaitsForAnInFlightFirstRequest(t *testing.T) {
	a := newCountingAdapter()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	first, _, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.started

	second, run2, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("a repeat arriving while the first request is still running was refused: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("repeat returned response %s, want the in-flight %s", second.ID, first.ID)
	}

	close(a.release)
	waitFor(t, run2)

	got, err := svc.GetTask(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCompleted {
		t.Fatalf("status = %q, want completed; the retry must get the finished result, not a partial", got.Status)
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times; the retry started a second execution", n)
	}
}

// Two clients racing on one key is the case a retry actually produces: the
// first request timed out on the client side but is still on its way in.
func TestConcurrentRequestsWithOneKeyStartOneExecution(t *testing.T) {
	a := newCountingAdapter().completesImmediately()
	svc := newSvc(t, "echo", a)

	const racers = 8
	ids := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			task, _, err := svc.StartTask(context.Background(), CreateTaskRequest{
				Input: "x", HarnessID: "echo", IdempotencyKey: "k1",
			})
			errs[i] = err
			if task != nil {
				ids[i] = task.ID
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("racer %d got response %s, racer 0 got %s; one key must yield one response", i, ids[i], ids[0])
		}
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times for %d concurrent requests sharing one key", n, racers)
	}
}

// A key is only a promise about requests that carry it. Nothing changes for
// the ones that do not.
func TestRequestsWithoutAKeyAreNeverDeduplicated(t *testing.T) {
	a := newCountingAdapter().completesImmediately()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	first, run1, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run1)
	second, run2, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run2)

	if second.ID == first.ID {
		t.Fatal("two keyless requests were collapsed into one response")
	}
	if n := a.count(); n != 2 {
		t.Fatalf("the harness ran %d times; two keyless requests are two executions", n)
	}
}

func TestDifferentKeysAreIndependent(t *testing.T) {
	a := newCountingAdapter().completesImmediately()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	first, run1, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run1)
	second, run2, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k2"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run2)

	if second.ID == first.ID {
		t.Fatal("two different keys returned one response")
	}
	if n := a.count(); n != 2 {
		t.Fatalf("the harness ran %d times, want 2", n)
	}
}

// A request that was refused before anything ran must leave the key free.
// Binding it would make the refusal permanent for that key, which is the
// opposite of what Errors §4 tells a client to do about a retryable failure.
func TestARefusedRequestDoesNotBindTheKey(t *testing.T) {
	a := newCountingAdapter().completesImmediately()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	if _, _, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "nope", IdempotencyKey: "k1",
	}); err == nil {
		t.Fatal("a request naming an unknown harness was accepted")
	}

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("a key claimed by a request that never ran is still bound: %v", err)
	}
	waitFor(t, run)
	got, _ := svc.GetTask(ctx, task.ID)
	if got.Status != uhp.StatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times, want 1", n)
	}
}

// Errors §4 tells a client to retry a 503 — and to carry the same
// Idempotency-Key when it does. A key bound by the refusal would answer that
// retry with the same 503 for the next 24 hours.
func TestNoCapacityDoesNotBindTheKey(t *testing.T) {
	a := newCountingAdapter()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), WithMaxConcurrentRuns(1))
	ctx := context.Background()

	_, run1, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.started

	_, _, err = svc.StartTask(ctx, CreateTaskRequest{Input: "y", HarnessID: "echo", IdempotencyKey: "k1"})
	var noCapacity *NoCapacityError
	if !errors.As(err, &noCapacity) {
		t.Fatalf("error = %v, want a no-capacity refusal", err)
	}

	close(a.release)
	waitFor(t, run1)

	_, run2, err := svc.StartTask(ctx, CreateTaskRequest{Input: "y", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("the retry Errors §4 asks for was refused because the 503 bound the key: %v", err)
	}
	waitFor(t, run2)
}

// Retention runs from the answer, not from the request.
//
// An agent task can work for longer than the window. Dating the key from the
// request means the retry that finally comes to collect the result is the very
// thing that evicts it — the key expires, the claim is free, and the request
// that came to avoid a second execution starts one.
func TestALongRunKeepsItsKeyAfterItFinishes(t *testing.T) {
	a := newCountingAdapter()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	now := time.Now().UTC()
	svc.idempotency.clock = func() time.Time { return now }

	first, run1, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.started

	// The agent works for longer than the whole retention window, then finishes.
	now = now.Add(IdempotencyRetention + time.Hour)
	close(a.release)
	waitFor(t, run1)

	second, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("the key expired the moment the run ended, so the retry that came to collect " +
			"the result started a second execution instead")
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times, want 1", n)
	}
}

// Tasks §6: keys SHOULD be retained for at least 24 hours. They are not kept
// forever, or the map is a leak.
func TestKeysAreRetainedForTwentyFourHoursAndThenForgotten(t *testing.T) {
	a := newCountingAdapter().completesImmediately()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	now := time.Now().UTC()
	svc.idempotency.clock = func() time.Time { return now }

	first, run1, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run1)

	now = now.Add(IdempotencyRetention - time.Minute)
	second, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("the key was forgotten after %s, short of the 24 hours Tasks §6 asks for",
			IdempotencyRetention-time.Minute)
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times inside the retention window", n)
	}

	// Far enough past the window that no reading of "at least 24 hours" still
	// keeps the key. The exact expiry depends on when a sweep first saw the run
	// finish, and asserting on that would be testing the sweep's schedule
	// rather than the promise made to a client.
	now = now.Add(3 * IdempotencyRetention)
	third, run3, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run3)
	if third.ID == first.ID {
		t.Fatal("the key outlived its retention window; nothing ever evicts it and the map only grows")
	}
	if n := a.count(); n != 2 {
		t.Fatalf("the harness ran %d times, want 2 once the key expired", n)
	}
}

// Retention counts from the claim, and an agent task can outlast it. Evicting
// a key whose run is still going would let the next retry start the second
// execution the key exists to prevent.
func TestAnInFlightKeyIsNotEvictedByRetention(t *testing.T) {
	a := newCountingAdapter()
	svc := newSvc(t, "echo", a)
	ctx := context.Background()

	now := time.Now().UTC()
	svc.idempotency.clock = func() time.Time { return now }

	first, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.started

	now = now.Add(2 * IdempotencyRetention)
	second, run2, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a still-running task's key was evicted, so the retry started a second execution")
	}
	if n := a.count(); n != 1 {
		t.Fatalf("the harness ran %d times, want 1", n)
	}

	close(a.release)
	waitFor(t, run2)
}
