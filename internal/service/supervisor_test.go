package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
)

// refusingAdapter fails to start, the way a CLI that is not installed does.
type refusingAdapter struct{ echoAdapter }

func (refusingAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_refusing", Base: "refusing", Object: "harness", Name: "Refusing"}
}

func (refusingAdapter) Run(context.Context, harness.RunRequest) (<-chan harness.RunUpdate, error) {
	return nil, errRefused
}

var errRefused = errors.New("the CLI is not installed")

// drain cancels a run and waits for it, so a test that deliberately leaves work
// in flight does not leak a goroutine into the next one.
func drain(t *testing.T, svc *TaskService, taskID string, run *Run) {
	t.Helper()
	_ = svc.CancelTask(context.Background(), taskID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("run %s never terminated: %v", taskID, err)
	}
}

// Issue #5: every accepted task forks a CLI process, and nothing else in the
// path says no. Auth is off unless UHP_API_KEYS is set, so an unbounded number
// of them is an unauthenticated fork bomb.
func TestConcurrentRunsAreBounded(t *testing.T) {
	a := newSlowAdapter()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), WithMaxConcurrentRuns(2))
	ctx := context.Background()

	type live struct {
		id  string
		run *Run
	}
	var running []live
	for i := 0; i < 2; i++ {
		task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
		if err != nil {
			t.Fatalf("StartTask %d: %v", i, err)
		}
		running = append(running, live{task.ID, run})
	}
	defer func() {
		for _, l := range running {
			drain(t, svc, l.id, l.run)
		}
	}()

	_, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err == nil {
		t.Fatal("a third run was accepted with a limit of two")
	}
	var full *NoCapacityError
	if !errors.As(err, &full) {
		t.Fatalf("error = %v, want a *NoCapacityError", err)
	}
	if full.Limit != 2 {
		t.Errorf("Limit = %d, want 2; the client is told a bound it cannot act on", full.Limit)
	}
}

// The bound is a bound, not a quota: a slot must come back when its run reaches
// a terminal state, and it must be back before the run is reported terminal —
// otherwise a client that waits for its answer and immediately asks the next
// question is refused for capacity that is already free.
func TestRunSlotIsReleasedWhenTheRunEnds(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(), WithMaxConcurrentRuns(1))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo"})
		if err != nil {
			t.Fatalf("run %d refused after %d finished runs: %v", i, i, err)
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := run.Wait(wctx); err != nil {
			cancel()
			t.Fatalf("run %d never terminated: %v", i, err)
		}
		cancel()
	}
}

// A slot is reserved before the work that leads to a fork, so every path that
// returns before the supervisor takes ownership has to give it back. An adapter
// that refuses to start is the one that actually happens: a missing CLI binary.
func TestRunSlotIsReleasedWhenTheStartFails(t *testing.T) {
	slow := newSlowAdapter()
	svc := NewTaskService(newRegistryWith(refusingAdapter{}, slow), newMemStore(), testLogger(),
		WithMaxConcurrentRuns(1))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "refusing"}); err == nil {
			t.Fatalf("start %d: refusing adapter reported success", i)
		}
	}

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("the only slot leaked on the failed starts: %v", err)
	}
	drain(t, svc, task.ID, run)
}

// A TaskService built without the option is still bounded. main.go always
// passes one, but it is not the only constructor, and an unbounded default puts
// the fork bomb back for anything that forgets.
func TestRunsAreBoundedWithoutConfiguration(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger())
	if got := svc.runs.capacity(); got != DefaultMaxConcurrentRuns {
		t.Fatalf("capacity = %d, want %d", got, DefaultMaxConcurrentRuns)
	}
}

// A saturated server must still give the request-specific answer to a request
// that is wrong on its own terms. "Busy, retry" is retryable, and retrying a
// harness that does not exist or a response id that never did never works — so
// a client told to retry those is being sent round a loop it cannot leave.
func TestSaturationDoesNotMaskRequestSpecificRefusals(t *testing.T) {
	a := newSlowAdapter()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), WithMaxConcurrentRuns(1))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	defer drain(t, svc, task.ID, run)

	cases := []struct {
		name string
		req  CreateTaskRequest
		want error
	}{
		{"unknown harness", CreateTaskRequest{Input: "y", HarnessID: "nope"}, ErrHarnessNotFound},
		{"unknown previous response",
			CreateTaskRequest{Input: "y", HarnessID: "slow", PreviousResponseID: "resp_nope"},
			ErrResponseNotFound},
		{"busy session",
			CreateTaskRequest{Input: "y", HarnessID: "slow", PreviousResponseID: task.ID},
			ErrSessionBusy},
	}
	for _, tc := range cases {
		_, _, err := svc.StartTask(ctx, tc.req)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
		var full *NoCapacityError
		if errors.As(err, &full) {
			t.Errorf("%s: answered with capacity, hiding why the request itself is wrong", tc.name)
		}
	}
}

// The refusal is meant to be the cheap path. Persisting a session for a task
// that was never allowed to start would leave a record nothing reads again, one
// per attempt, so a client politely retrying a saturated server would grow the
// store without ever running anything.
func TestRefusedTaskLeavesNoSessionBehind(t *testing.T) {
	a := newSlowAdapter()
	store := newMemStore()
	svc := NewTaskService(newRegistryWith(a), store, testLogger(), WithMaxConcurrentRuns(1))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	defer drain(t, svc, task.ID, run)

	for i := 0; i < 5; i++ {
		if _, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"}); err == nil {
			t.Fatalf("attempt %d was accepted past the limit of one", i)
		}
	}

	page, err := store.ListSessions(ctx, domain.SessionFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("store holds %d sessions, want 1: five refused requests each left one behind",
			len(page.Sessions))
	}
}

// A limit of zero or less is a misconfiguration, not an instruction to accept
// nothing: refusing every task is a worse outcome than the default bound.
func TestNonPositiveLimitFallsBackToTheDefault(t *testing.T) {
	for _, n := range []int{0, -1} {
		svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
			WithMaxConcurrentRuns(n))
		if got := svc.runs.capacity(); got != DefaultMaxConcurrentRuns {
			t.Errorf("WithMaxConcurrentRuns(%d): capacity = %d, want %d", n, got, DefaultMaxConcurrentRuns)
		}
	}
}
