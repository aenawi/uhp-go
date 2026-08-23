package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/store"
)

// capsAdapter is an echo harness that advertises exactly the capabilities it is
// given, so a test can state what a harness promises a client and then check
// that the router either keeps the promise or refuses.
type capsAdapter struct {
	echoAdapter
	id   string
	base string
	caps []domain.Capability
}

func (a *capsAdapter) Info() domain.Harness {
	return domain.Harness{
		ID: a.id, Base: a.base, Object: "harness", Name: a.base,
		Capabilities: a.caps,
	}
}

// capsSlowAdapter is the cancellable double with a capability list, for the
// cancel path: a run has to still be in flight for a refusal to mean anything.
type capsSlowAdapter struct {
	*slowAdapter
	caps []domain.Capability
}

func (a *capsSlowAdapter) Info() domain.Harness {
	return domain.Harness{
		ID: "chrn_slow", Base: "slow", Object: "harness", Name: "Slow",
		Capabilities: a.caps,
	}
}

// `files_in` and `files_out` are delivered by this router for every harness —
// attachments are written into the session working directory and that directory
// is diffed afterwards, without either step asking an adapter anything — so no
// harness declares them and every harness advertises them, on the deployments
// where they are true.
//
// Which is the half that used to be missing. Both need a workspace, so on a
// server started without one the router delivers neither, discovery reports
// `files_input: false`, and a task carrying a file is refused. A harness object
// that went on listing `files_in` there would contradict the discovery document
// a client read moments earlier and promise a request that is refused.
//
// Every path that turns an adapter or a stored configuration into a harness
// object is checked, because the rule is only worth as much as the least
// careful of them: a capability added on the listing but not on the fetch is a
// client that sees a different harness depending on which endpoint it asked.
func TestFileCapabilitiesFollowTheConfiguredWorkspace(t *testing.T) {
	fileCaps := []domain.Capability{domain.CapFilesIn, domain.CapFilesOut}
	for _, tc := range []struct {
		name      string
		workspace bool
	}{
		{"with a workspace, the router delivers both", true},
		{"without one, it delivers neither", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Declares neither, exactly as the five shipped bases do.
			a := &capsAdapter{id: "chrn_plain", base: "plain",
				caps: []domain.Capability{domain.CapStreaming}}
			hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
			if err != nil {
				t.Fatalf("harness store: %v", err)
			}
			opts := []Option{WithHarnessStore(hs)}
			if tc.workspace {
				opts = append(opts, WithWorkspace(t.TempDir()))
			}
			svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), opts...)

			if svc.FilesEnabled() != tc.workspace {
				t.Fatalf("FilesEnabled() = %v, want %v", svc.FilesEnabled(), tc.workspace)
			}

			created, err := svc.CreateHarness(ctx, HarnessSpec{Name: "derived", Base: "plain"})
			if err != nil {
				t.Fatalf("create harness: %v", err)
			}
			listed, err := svc.ListHarnesses(ctx)
			if err != nil {
				t.Fatalf("list harnesses: %v", err)
			}
			byAlias, ok, err := svc.GetHarness(ctx, "plain")
			if err != nil || !ok {
				t.Fatalf("get by alias: ok=%v err=%v", ok, err)
			}
			byID, ok, err := svc.GetHarness(ctx, created.ID)
			if err != nil || !ok {
				t.Fatalf("get managed by id: ok=%v err=%v", ok, err)
			}

			seen := append(listed, byAlias, byID, created)
			if len(seen) != 5 {
				t.Fatalf("expected the compiled-in and the managed harness on every path, got %v", seen)
			}
			for _, h := range seen {
				for _, c := range fileCaps {
					if got := h.HasCapability(c); got != tc.workspace {
						t.Errorf("harness %q advertises %q = %v, want %v",
							h.ID, c, got, tc.workspace)
					}
				}
			}
		})
	}
}

func TestContinuationIsRefusedWhenTheHarnessDoesNotAdvertiseSessions(t *testing.T) {
	ctx := context.Background()
	a := &capsAdapter{id: "chrn_amnesiac", base: "amnesiac",
		caps: []domain.Capability{domain.CapStreaming}}
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

	first, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "one", HarnessID: "amnesiac"})
	if err != nil {
		t.Fatalf("first task: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	_, _, err = svc.StartTask(ctx, CreateTaskRequest{
		Input: "two", HarnessID: "amnesiac", PreviousResponseID: first.ID,
	})
	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("continuation error = %v, want a *CapabilityError", err)
	}
	if capErr.Capability != domain.CapSessions {
		t.Errorf("refused capability = %q, want %q", capErr.Capability, domain.CapSessions)
	}
	if capErr.HarnessID != "chrn_amnesiac" {
		t.Errorf("refusal names harness %q, want the canonical id", capErr.HarnessID)
	}
}

func TestContinuationIsAllowedWhenTheHarnessAdvertisesSessions(t *testing.T) {
	ctx := context.Background()
	a := &capsAdapter{id: "chrn_recalls", base: "recalls",
		caps: []domain.Capability{domain.CapStreaming, domain.CapSessions}}
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

	first, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "one", HarnessID: "recalls"})
	if err != nil {
		t.Fatalf("first task: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	second, run2, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "two", HarnessID: "recalls", PreviousResponseID: first.ID,
	})
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if err := run2.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if second.SessionID != first.SessionID {
		t.Errorf("continuation landed in session %q, want %q", second.SessionID, first.SessionID)
	}
}

// A harness that does not advertise `cancellation` and is asked to stop must
// say so. Answering 200 and leaving the agent running is the failure: the
// client believes the work stopped, and it is still spending money.
func TestCancelTaskIsRefusedWhenTheHarnessDoesNotAdvertiseCancellation(t *testing.T) {
	ctx := context.Background()
	a := &capsSlowAdapter{slowAdapter: newSlowAdapter(),
		caps: []domain.Capability{domain.CapStreaming}}
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

	task, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "work", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-a.started

	err = svc.CancelTask(ctx, task.ID)
	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("cancel error = %v, want a *CapabilityError", err)
	}
	if capErr.Capability != domain.CapCancellation {
		t.Errorf("refused capability = %q, want %q", capErr.Capability, domain.CapCancellation)
	}

	// Refused means unchanged, not half-cancelled.
	stored, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != domain.StatusInProgress {
		t.Errorf("task status after a refused cancel = %q, want %q",
			stored.Status, domain.StatusInProgress)
	}
	// Leave nothing running behind the test.
	_ = a.slowAdapter.Cancel(ctx, task.ID)
}

// Sessions §4: "Cancelling an already-terminal task MUST succeed and change
// nothing." That rule outranks the capability check — there is no work to stop,
// so nothing is being promised that cannot be delivered.
func TestCancellingATerminalTaskSucceedsWithoutTheCapability(t *testing.T) {
	ctx := context.Background()
	a := &capsAdapter{id: "chrn_amnesiac", base: "amnesiac",
		caps: []domain.Capability{domain.CapStreaming}}
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "one", HarnessID: "amnesiac"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := svc.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("cancelling a terminal task: %v", err)
	}
}

func TestCancelSessionIsRefusedWhenTheHarnessDoesNotAdvertiseCancellation(t *testing.T) {
	ctx := context.Background()
	a := &capsSlowAdapter{slowAdapter: newSlowAdapter(),
		caps: []domain.Capability{domain.CapStreaming}}
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

	task, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "work", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-a.started

	err = svc.CancelSession(ctx, task.SessionID)
	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("cancel session error = %v, want a *CapabilityError", err)
	}
	if capErr.Capability != domain.CapCancellation {
		t.Errorf("refused capability = %q, want %q", capErr.Capability, domain.CapCancellation)
	}
	_ = a.slowAdapter.Cancel(ctx, task.ID)
}

// An idle session has nothing to stop, so the answer is the same one an
// already-terminal task gets, whatever the harness advertises.
func TestCancellingAnIdleSessionSucceedsWithoutTheCapability(t *testing.T) {
	ctx := context.Background()
	a := &capsAdapter{id: "chrn_amnesiac", base: "amnesiac",
		caps: []domain.Capability{domain.CapStreaming}}
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "one", HarnessID: "amnesiac"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := svc.CancelSession(ctx, task.SessionID); err != nil {
		t.Fatalf("cancelling an idle session: %v", err)
	}
}
