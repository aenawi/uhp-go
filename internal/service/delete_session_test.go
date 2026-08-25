package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// stubbornAdapter starts, ignores Cancel, and finishes only when the test says
// so. It stands in for the two cases that make deletion hard: a harness whose
// wind-down takes longer than the request can wait for, and one that does not
// implement cancellation at all — note the absent CapCancellation.
//
// slowAdapter cannot stand in for either, because it stops the moment it is
// cancelled: a synchronous delete would return promptly against it and the test
// would prove nothing.
type stubbornAdapter struct {
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	cancelled bool
}

func newStubbornAdapter() *stubbornAdapter {
	return &stubbornAdapter{started: make(chan struct{}), release: make(chan struct{})}
}

func (a *stubbornAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness:      uhp.Harness{ID: "chrn_stubborn", Base: "stubborn", Object: "harness", Name: "Stubborn"},
		Capabilities: []uhpgo.Capability{uhpgo.CapStreaming},
		Status:       uhpgo.HarnessReady,
	}
}

func (a *stubbornAdapter) HealthCheck(context.Context) error { return nil }

func (a *stubbornAdapter) Run(context.Context, harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 1)
	go func() {
		defer close(ch)
		a.once.Do(func() { close(a.started) })
		<-a.release
		ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	}()
	return ch, nil
}

func (a *stubbornAdapter) Cancel(context.Context, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelled = true
	return nil
}

func (a *stubbornAdapter) wasCancelled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelled
}

// waitGone polls for a path to disappear. The reaper runs on its own goroutine
// once the run is dead, so there is no moment the test can synchronise on
// without reaching into the service.
func waitGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the working directory %s was never removed", path)
}

// Sessions §6: "Makes session unreadable after deletion." The history goes with
// it — the turns are the conversation — so a task of a deleted session is not
// readable either.
func TestDeleteSessionMakesItAndItsHistoryUnreadable(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()
	task := runOnce(t, svc, CreateTaskRequest{Input: "a", HarnessID: "echo"})

	if err := svc.DeleteSession(ctx, task.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	if _, err := svc.GetSession(ctx, task.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession after delete = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.SessionTurns(ctx, task.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SessionTurns after delete = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.GetTask(ctx, task.ID); !errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("the session's task outlived it: GetTask = %v, want ErrResponseNotFound", err)
	}
}

// Deleting twice is not-found, not a second success, for the reason
// DELETE /v1/responses/{id} gives: `deleted: true` reports what this request
// did.
func TestDeleteUnknownSession(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	if err := svc.DeleteSession(ctx, "sess_nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("deleting an unknown session = %v, want ErrSessionNotFound", err)
	}

	task := runOnce(t, svc, CreateTaskRequest{Input: "a", HarnessID: "echo"})
	if err := svc.DeleteSession(ctx, task.SessionID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := svc.DeleteSession(ctx, task.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second delete = %v, want ErrSessionNotFound", err)
	}
}

// The half that is easy to forget and the half that matters: "deleted" has to
// describe the disk and not only the database. The files an agent wrote are the
// most sensitive thing a session holds.
func TestDeleteSessionRemovesItsWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(), WithWorkspace(root))
	ctx := context.Background()
	task := runOnce(t, svc, CreateTaskRequest{Input: "a", HarnessID: "echo"})

	dir := filepath.Join(root, task.SessionID)
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("what the agent wrote"), 0o600); err != nil {
		t.Fatalf("seed the working directory: %v", err)
	}

	if err := svc.DeleteSession(ctx, task.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the working directory survived the delete: %v", err)
	}
}

// Sessions §6 couples cancellation with deletion, and this is what the coupling
// is worth: the run stops rather than being orphaned, and its directory is
// reaped once it is actually dead.
//
// Deletion is *not* synchronous, and this test is where that decision is
// asserted. Cancellation is asynchronous by design — Sessions §4 gives a server
// one second to answer a cancel even when the harness takes longer to wind down
// — so a delete that waited out the harness would inherit an unbounded wait.
// The session is made unreadable at once and the directory is reaped afterwards,
// which is what "unreadable after deletion" asks for. The adapter here ignores
// Cancel entirely, so a delete that waited would hang until the test released
// it.
func TestDeleteSessionCancelsTheRunAndReapsAfterwards(t *testing.T) {
	a := newStubbornAdapter()
	root := t.TempDir()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), WithWorkspace(root))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "a", HarnessID: "chrn_stubborn"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	select {
	case <-a.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the run never started")
	}
	dir := filepath.Join(root, task.SessionID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the run has no working directory to reap: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- svc.DeleteSession(ctx, task.SessionID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DeleteSession: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DeleteSession waited for the harness to wind down")
	}

	// Unreadable at once, while the run it cancelled is still going.
	if _, err := svc.GetSession(ctx, task.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession after delete = %v, want ErrSessionNotFound", err)
	}
	// The run was asked to stop. A delete that only removed rows would pass
	// every other assertion here and leave the harness running.
	if !a.wasCancelled() {
		t.Fatal("deleting a session did not cancel its in-flight run; Sessions §6 couples the two")
	}

	close(a.release)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := run.Wait(waitCtx); err != nil {
		t.Fatalf("the run never reached a terminal state: %v", err)
	}
	waitGone(t, dir)
}

// The other half of "stopped rather than orphaned", which the test above cannot
// show: there, the adapter ignores its cancel and the test has to release the
// run itself, so what is proved is that Cancel was called and not that anything
// stopped. Here the harness does what a real one does — it winds down when
// cancelled — and nobody releases anything. The run reaching a terminal state is
// therefore the delete's doing, and the directory going with it is the reaper
// running to completion on a run that ended by itself.
func TestDeleteSessionStopsTheRunRatherThanOrphaningIt(t *testing.T) {
	a := newSlowAdapter()
	root := t.TempDir()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), WithWorkspace(root))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "a", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	select {
	case <-a.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the run never started")
	}

	if err := svc.DeleteSession(ctx, task.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := run.Wait(waitCtx); err != nil {
		t.Fatalf("the run was orphaned: it never reached a terminal state after its session was deleted: %v", err)
	}
	waitGone(t, filepath.Join(root, task.SessionID))
}
