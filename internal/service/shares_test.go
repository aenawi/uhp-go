package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func sharingSvc(t *testing.T, opts ...Option) *TaskService {
	t.Helper()
	return NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		append([]Option{WithSessionSharing()}, opts...)...)
}

// A deployment that has not opted in refuses every share method, and refuses
// them the same way: a client reads `session_sharing` in the discovery document
// and gets the same answer from the endpoints.
func TestSharingIsRefusedWhenItIsNotEnabled(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger())
	task := runOnce(t, svc, CreateTaskRequest{Input: "hi", HarnessID: "echo"})
	ctx := context.Background()

	if svc.SessionSharingEnabled() {
		t.Fatal("sharing is on without WithSessionSharing")
	}
	if _, err := svc.ShareSession(ctx, task.SessionID); !errors.Is(err, ErrSessionSharingUnsupported) {
		t.Errorf("ShareSession = %v", err)
	}
	if _, err := svc.SessionShare(ctx, task.SessionID); !errors.Is(err, ErrSessionSharingUnsupported) {
		t.Errorf("SessionShare = %v", err)
	}
	if _, err := svc.RevokeShare(ctx, task.SessionID); !errors.Is(err, ErrSessionSharingUnsupported) {
		t.Errorf("RevokeShare = %v", err)
	}
	// The read path too. A server that stopped offering sharing must stop
	// serving the views it minted while it did.
	if _, err := svc.SharedSession(ctx, "shr_whatever"); !errors.Is(err, ErrSessionSharingUnsupported) {
		t.Errorf("SharedSession = %v", err)
	}
}

func TestSharingASessionMintsACapability(t *testing.T) {
	svc := sharingSvc(t)
	task := runOnce(t, svc, CreateTaskRequest{Input: "hi", HarnessID: "echo"})
	ctx := context.Background()

	sh, err := svc.ShareSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("ShareSession: %v", err)
	}
	if sh.SessionID != task.SessionID {
		t.Errorf("share names session %q, want %q", sh.SessionID, task.SessionID)
	}
	if sh.ID == task.SessionID || sh.CreatedAt == 0 {
		t.Errorf("share = %+v", sh)
	}

	view, err := svc.SharedSession(ctx, sh.ID)
	if err != nil {
		t.Fatalf("SharedSession: %v", err)
	}
	if view.Session.ID != task.SessionID {
		t.Errorf("the share resolved to session %q", view.Session.ID)
	}
	if view.Harness == nil || view.Harness.ID != "chrn_echo" {
		t.Errorf("shared view harness = %+v", view.Harness)
	}
}

// Sessions §5 says a share is per session. A second POST returns the one that
// already exists rather than minting a second live id, because the client is
// only ever told about — and can only ever revoke — one.
func TestSharingTwiceReturnsTheSameShare(t *testing.T) {
	svc := sharingSvc(t)
	task := runOnce(t, svc, CreateTaskRequest{Input: "hi", HarnessID: "echo"})
	ctx := context.Background()

	first, err := svc.ShareSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("first share: %v", err)
	}
	second, err := svc.ShareSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("second share: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("a second share minted a second id: %s then %s", first.ID, second.ID)
	}
	if first.CreatedAt != second.CreatedAt {
		t.Errorf("the existing share was re-stamped: %d then %d", first.CreatedAt, second.CreatedAt)
	}
}

func TestSharingASessionThatIsNotThere(t *testing.T) {
	svc := sharingSvc(t)
	ctx := context.Background()
	if _, err := svc.ShareSession(ctx, "sess_nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("ShareSession on a missing session = %v", err)
	}
	if _, err := svc.SessionShare(ctx, "sess_nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("SessionShare on a missing session = %v", err)
	}
	if _, err := svc.RevokeShare(ctx, "sess_nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("RevokeShare on a missing session = %v", err)
	}
}

// An unshared session is not an error on read — it is a session with no share —
// and the distinction is what lets a client tell "wrong id" from "not shared".
func TestReadingTheShareOfAnUnsharedSession(t *testing.T) {
	svc := sharingSvc(t)
	task := runOnce(t, svc, CreateTaskRequest{Input: "hi", HarnessID: "echo"})
	if _, err := svc.SessionShare(context.Background(), task.SessionID); !errors.Is(err, ErrShareNotFound) {
		t.Errorf("SessionShare on an unshared session = %v", err)
	}
}

// The requirement Sessions §5 states outright, and the one a happy-path test
// would never reach.
func TestRevokingAShareStopsItResolving(t *testing.T) {
	svc := sharingSvc(t)
	task := runOnce(t, svc, CreateTaskRequest{Input: "hi", HarnessID: "echo"})
	ctx := context.Background()

	sh, err := svc.ShareSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	revoked, err := svc.RevokeShare(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked != sh.ID {
		t.Fatalf("revoke reported %q, want the share it withdrew (%s)", revoked, sh.ID)
	}

	if _, err := svc.SharedSession(ctx, sh.ID); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("a revoked share still resolves: %v", err)
	}
	if _, err := svc.SharedTurns(ctx, sh.ID); !errors.Is(err, ErrShareNotFound) {
		t.Errorf("a revoked share still serves turns: %v", err)
	}
	if _, err := svc.SharedFiles(ctx, sh.ID); !errors.Is(err, ErrShareNotFound) {
		t.Errorf("a revoked share still serves files: %v", err)
	}
	// And revoking again reports that there was nothing to revoke, rather than
	// a second success.
	if _, err := svc.RevokeShare(ctx, task.SessionID); !errors.Is(err, ErrShareNotFound) {
		t.Errorf("second revoke = %v", err)
	}
}

// Deleting the trace has to take the capability with it. Sessions §6 makes a
// deleted session unreadable and its files unreachable; a surviving share id is
// the anonymous route back to both.
func TestDeletingASessionRevokesItsShare(t *testing.T) {
	root := t.TempDir()
	svc := sharingSvc(t, WithWorkspace(root))
	task := runOnce(t, svc, CreateTaskRequest{Input: "hi", HarnessID: "echo"})
	ctx := context.Background()

	sh, err := svc.ShareSession(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	if err := svc.DeleteSession(ctx, task.SessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := svc.SharedSession(ctx, sh.ID); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("the deleted session's share still resolves: %v", err)
	}
}

// A share is a read path over one session, and the file lookup must not be able
// to reach out of it. The container id is never taken from the caller here —
// it is derived from the share — so this pins that the derivation is what
// happens rather than a filter over something the caller supplied.
func TestASharedFileLookupCannotLeaveItsSession(t *testing.T) {
	root := t.TempDir()
	svc := sharingSvc(t, WithWorkspace(root))
	ctx := context.Background()

	shared := runOnce(t, svc, CreateTaskRequest{Input: "one", HarnessID: "echo"})
	other := runOnce(t, svc, CreateTaskRequest{Input: "two", HarnessID: "echo"})

	// A file in each session's directory, recorded the way a run records one.
	sharedFile := writeArtifact(t, svc, shared.ID, shared.SessionID, "shared.txt", "visible")
	otherFile := writeArtifact(t, svc, other.ID, other.SessionID, "secret.txt", "private")

	sh, err := svc.ShareSession(ctx, shared.SessionID)
	if err != nil {
		t.Fatalf("share: %v", err)
	}

	if _, f, err := svc.OpenSharedArtifact(ctx, sh.ID, sharedFile); err != nil {
		t.Fatalf("open the shared session's own file: %v", err)
	} else {
		_ = f.Close()
	}

	// The other session's artifact id is a real id this server minted. It must
	// not resolve through this share.
	if _, f, err := svc.OpenSharedArtifact(ctx, sh.ID, otherFile); !errors.Is(err, ErrArtifactNotFound) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatalf("a share served another session's file: %v", err)
	}
}

// writeArtifact puts a file in a session's working directory and records it
// against a task, which is what a run does at the end of capture.
func writeArtifact(t *testing.T, svc *TaskService, taskID, sessionID, name, body string) string {
	t.Helper()
	dir, err := svc.workspace.sessionDir(sessionID)
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	task, err := svc.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	svc.captureArtifacts(context.Background(), task, &runState{workDir: dir, before: dirSnapshot{}})
	for _, a := range task.Artifacts {
		if a.Filename == name {
			return a.ID
		}
	}
	t.Fatalf("artifact %q was not captured", name)
	return ""
}
