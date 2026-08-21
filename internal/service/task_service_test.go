package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
)

// echoAdapter completes immediately.
type echoAdapter struct{}

func (echoAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_echo", Base: "echo", Object: "harness", Name: "Echo",
		Capabilities: []domain.Capability{domain.CapStreaming}}
}

// otherAdapter is a second, distinct harness, for the mismatch test.
type otherAdapter struct{ echoAdapter }

func (otherAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_other", Base: "other", Object: "harness", Name: "Other"}
}
func (echoAdapter) HealthCheck(context.Context) error { return nil }
func (echoAdapter) Run(_ context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "hello " + req.Input}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}
func (echoAdapter) Cancel(context.Context, string) error { return nil }

// echoAdapter stands in for a runtime that enforces everything natively, so
// the configuration-delivery paths are exercised rather than skipped. See
// plainAdapter for the opposite case.
func (echoAdapter) Delivery() harness.Delivery {
	return harness.Delivery{MCPServers: true, ToolBlock: true, Skills: true}
}

// slowAdapter models a real CLI harness: it emits one delta, then runs until
// its own Cancel is called, then reports "cancelled".
//
// It keeps its own per-task cancel exactly as the shared runner does, rather
// than watching the context it was handed. That fidelity matters: cancelling
// the caller's context is what the runner treats as "the consumer is gone",
// and a double that conflated the two would hide the bug where an explicit
// cancel drops the terminal update and the task is recorded as failed.
type slowAdapter struct {
	started chan struct{}
	once    sync.Once

	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

func newSlowAdapter() *slowAdapter {
	return &slowAdapter{started: make(chan struct{}), cancel: make(map[string]context.CancelFunc)}
}

func (a *slowAdapter) Info() domain.Harness              { return domain.Harness{ID: "slow", Name: "Slow"} }
func (a *slowAdapter) HealthCheck(context.Context) error { return nil }

func (a *slowAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel[req.TaskID] = cancel
	a.mu.Unlock()

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		defer cancel()
		// Guard sends on the caller's context, as the real runner does.
		send := func(u harness.RunUpdate) bool {
			select {
			case ch <- u:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(harness.RunUpdate{Type: harness.UpdateDelta, Delta: "partial"}) {
			return
		}
		a.once.Do(func() { close(a.started) })
		<-runCtx.Done()
		send(harness.RunUpdate{Type: harness.UpdateCancelled})
	}()
	return ch, nil
}

func (a *slowAdapter) Cancel(_ context.Context, taskID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.cancel[taskID]; ok {
		c()
		delete(a.cancel, taskID)
		return nil
	}
	return errNoSuchTask
}

var errNoSuchTask = errors.New("no such task")

// neverTerminalAdapter closes its channel without ever reporting terminal —
// an adapter bug the router must survive.
type neverTerminalAdapter struct{}

func (neverTerminalAdapter) Info() domain.Harness              { return domain.Harness{ID: "bad"} }
func (neverTerminalAdapter) HealthCheck(context.Context) error { return nil }
func (neverTerminalAdapter) Run(context.Context, harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 1)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "half an answer"}
	close(ch)
	return ch, nil
}
func (neverTerminalAdapter) Cancel(context.Context, string) error { return nil }

func newSvc(t *testing.T, _ string, a harness.Adapter) *TaskService {
	t.Helper()
	return NewTaskService(newRegistryWith(a), newMemStore(), testLogger())
}

func newRegistryWith(as ...harness.Adapter) *harness.Registry {
	reg := harness.NewRegistry()
	for _, a := range as {
		reg.Register(a)
	}
	return reg
}

func newMemStore() Store       { return store.NewMemoryStore() }
func testLogger() *slog.Logger { return slog.Default() }

func collect(t *testing.T, run *Run) []domain.Event {
	t.Helper()
	var evs []domain.Event
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run.Events(ctx, 0, IdleTick{}, func(ev domain.Event) error {
		evs = append(evs, ev)
		return nil
	}); err != nil {
		t.Fatalf("Events: %v", err)
	}
	return evs
}

func TestStartTaskCompletes(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "world", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if task.SessionID == "" {
		t.Fatal("expected a session id")
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != domain.StatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.Text() != "hello world" {
		t.Fatalf("output = %q, want %q", got.Text(), "hello world")
	}
}

// UHP Streaming §1: sequence_number MUST start at 0 and increase by exactly 1.
// Both paths read the same event log, so they cannot disagree.
func TestSequenceNumbersStartAtZeroAndAreGapless(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	_, run, err := svc.StartTask(context.Background(), CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	evs := collect(t, run)
	if len(evs) < 2 {
		t.Fatalf("expected several events, got %d", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != i {
			t.Fatalf("event %d has sequence_number %d; numbering must start at 0 and be gapless: %+v", i, ev.Seq, evs)
		}
	}
	if evs[0].Type != "response.created" {
		t.Errorf("first event = %q, want response.created", evs[0].Type)
	}
}

// Two subscribers to the same run must see byte-identical event streams —
// this is what makes "streaming and non-streaming agree" structural.
func TestAllSubscribersSeeTheSameStream(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	_, run, err := svc.StartTask(context.Background(), CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	a := collect(t, run)
	// b subscribes only after the run is over, and must still get everything.
	b := collect(t, run)
	if len(a) != len(b) {
		t.Fatalf("subscribers saw different lengths: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Seq != b[i].Seq {
			t.Fatalf("event %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// Issue #7: a client disconnecting must not strand the task. The supervisor
// owns the lifetime, so abandoning the subscription changes nothing.
func TestClientDisconnectDoesNotStrandTheTask(t *testing.T) {
	a := newSlowAdapter()
	svc := newSvc(t, "slow", a)

	// A request-scoped context, as a handler would have.
	reqCtx, disconnect := context.WithCancel(context.Background())
	task, run, err := svc.StartTask(reqCtx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	<-a.started
	// The client goes away mid-stream.
	disconnect()

	// The task is still ours to cancel, and must still reach a terminal state.
	if err := svc.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("run never reached a terminal state after disconnect: %v", err)
	}

	got, err := svc.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status == domain.StatusInProgress {
		t.Fatal("task is still in_progress after the client disconnected: it has been stranded")
	}
	if got.Status != domain.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	// Sessions §4: output produced before cancellation MUST be retained.
	if !strings.Contains(got.Text(), "partial") {
		t.Errorf("partial output was discarded: %q", got.Text())
	}
}

// Issue #14: cancel used to read-modify-write the same task the update loop
// was writing, so one of the two changes was silently lost. Only the
// supervisor writes now, so the terminal state is whatever it decided.
func TestCancelDoesNotRaceTheUpdateStream(t *testing.T) {
	for i := 0; i < 50; i++ {
		a := newSlowAdapter()
		svc := newSvc(t, "slow", a)
		ctx := context.Background()

		task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		<-a.started
		if err := svc.CancelTask(ctx, task.ID); err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := run.Wait(wctx); err != nil {
			cancel()
			t.Fatalf("iteration %d: run never terminated: %v", i, err)
		}
		cancel()

		got, err := svc.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Status != domain.StatusCancelled {
			t.Fatalf("iteration %d: status = %q, want cancelled (a lost update)", i, got.Status)
		}
		if got.Error != nil {
			t.Fatalf("iteration %d: cancelled task carries an error: %+v", i, got.Error)
		}
	}
}

// Sessions §4: cancelling an already-terminal task MUST succeed and change
// nothing. Conformance check C-02.
func TestCancelTerminalTaskSucceedsAndChangesNothing(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()
	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	before, _ := svc.GetTask(ctx, task.ID)

	if err := svc.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("cancelling a terminal task returned an error: %v", err)
	}
	after, _ := svc.GetTask(ctx, task.ID)
	if after.Status != before.Status {
		t.Fatalf("status changed from %q to %q; a terminal state must not be left", before.Status, after.Status)
	}
	if after.Text() != before.Text() {
		t.Fatalf("output changed after cancelling a terminal task")
	}
}

// Lifecycle §3: a server MUST NOT transition out of a terminal state.
func TestAdapterClosingWithoutTerminalStillEndsTerminal(t *testing.T) {
	svc := newSvc(t, "bad", neverTerminalAdapter{})
	ctx := context.Background()
	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "bad"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := run.Wait(wctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := svc.GetTask(ctx, task.ID)
	if got.Status == domain.StatusInProgress {
		t.Fatal("adapter closed without a terminal update and the task was left in_progress forever")
	}
	if got.Status != domain.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	// Terminal responses MUST retain whatever output was produced.
	if got.Text() != "half an answer" {
		t.Errorf("partial output discarded: %q", got.Text())
	}
}

// Lifecycle §5: a server MUST NOT run two tasks concurrently in one session.
func TestSecondTaskInABusySessionIsRefused(t *testing.T) {
	a := newSlowAdapter()
	svc := newSvc(t, "slow", a)
	ctx := context.Background()

	task1, run1, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.started

	_, _, err = svc.StartTask(ctx, CreateTaskRequest{
		Input: "y", HarnessID: "slow", PreviousResponseID: task1.ID,
	})
	if err == nil {
		t.Fatal("a second concurrent task in the same session was accepted")
	}
	if !strings.Contains(err.Error(), "session busy") {
		t.Fatalf("error = %v, want a session-busy error", err)
	}

	_ = svc.CancelTask(ctx, task1.ID)
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = run1.Wait(wctx)
}

func TestSessionContinuationReusesTheSession(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	task1, run1, _ := svc.StartTask(ctx, CreateTaskRequest{Input: "a", HarnessID: "echo"})
	_ = run1.Wait(ctx)

	task2, run2, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "b", HarnessID: "echo", PreviousResponseID: task1.ID,
	})
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	_ = run2.Wait(ctx)

	if task2.SessionID != task1.SessionID {
		t.Fatalf("session id = %s, want %s", task2.SessionID, task1.SessionID)
	}
}

// Lifecycle §4: continuing a session with a different harness must fail
// loudly rather than quietly starting a new conversation.
func TestHarnessMismatchOnContinuation(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	reg.Register(otherAdapter{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	ctx := context.Background()

	task1, run1, _ := svc.StartTask(ctx, CreateTaskRequest{Input: "a", HarnessID: "echo"})
	_ = run1.Wait(ctx)

	_, _, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "b", HarnessID: "other", PreviousResponseID: task1.ID,
	})
	if err == nil {
		t.Fatal("continuing a session with a different harness was allowed")
	}
	if !strings.Contains(err.Error(), "harness mismatch") {
		t.Fatalf("error = %v, want a harness-mismatch error", err)
	}
}

func TestUnknownHarnessAndResponse(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	if _, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "nope"}); err == nil {
		t.Error("unknown harness accepted")
	}
	if _, err := svc.GetTask(ctx, "resp_nope"); err == nil {
		t.Error("unknown response accepted")
	}
}
