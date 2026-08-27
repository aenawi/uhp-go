package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Issue #60: `session_busy` is a refusal whose whole content is "come back
// later", and a client that is not told anything more has only a fixed sleep or
// a spin left. The refusal now names the response that is in the way, which is
// the one thing this package knows that a client can act on.
func TestSessionBusyNamesTheRunHoldingTheSession(t *testing.T) {
	a := newSlowAdapter()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(), WithTaskBudget(time.Minute))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	defer drain(t, svc, task.ID, run)

	_, _, err = svc.StartTask(ctx, CreateTaskRequest{
		Input: "y", HarnessID: "slow", PreviousResponseID: task.ID,
	})

	var busy *SessionBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("error = %v, want a *SessionBusyError", err)
	}
	// The sentinel still matches, because every arm that reads it — the
	// transport's fallback among them — was written against it.
	if !errors.Is(err, ErrSessionBusy) {
		t.Errorf("the typed refusal no longer matches ErrSessionBusy: %v", err)
	}
	if busy.SessionID != task.SessionID {
		t.Errorf("SessionID = %q, want %q", busy.SessionID, task.SessionID)
	}
	// The id is what lets a client stop polling altogether: it can watch the
	// response that holds the session instead of asking again.
	if busy.TaskID != task.ID {
		t.Errorf("TaskID = %q, want %q", busy.TaskID, task.ID)
	}
}
