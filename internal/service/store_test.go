package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

func boolPtr(b bool) *bool { return &b }

// `store` is the field #48 singled out as inert rather than merely
// unimplemented: domain.Task.Store existed, was hardcoded true, and the
// request's own value was never consulted. Tasks §4 makes the 404 a MAY, which
// is what permits acting on it now.
func TestAStoreFalseResponseIsGoneOnceTheRunIsOver(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "chrn_echo", Store: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	if _, err := svc.GetTask(ctx, task.ID); !errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("GetTask err = %v, want ErrResponseNotFound", err)
	}
}

// The default is true and nothing about it changes: a request that says nothing
// about `store` is retained, which is every request this server has ever had.
func TestAResponseIsRetainedWhenTheRequestSaysNothing(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	task := runOn(t, svc, "chrn_echo", "x")
	if !task.Store {
		t.Errorf("store = false for a request that did not ask for it")
	}
	if _, err := svc.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
}

// The field answers "will this be here afterwards", which is settled when the
// task is created — so it is echoed from the request and not from the outcome.
// Reporting true until the drop would make a streaming client watch the value
// change under it.
func TestStoreIsEchoedFromTheRequestFromTheStart(t *testing.T) {
	svc, _ := deliveringService(t)
	task, run, err := svc.StartTask(context.Background(), CreateTaskRequest{
		Input: "x", HarnessID: "chrn_echo", Store: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if task.Store {
		t.Errorf("store = true on a store:false task at creation")
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if task.Store {
		t.Errorf("store = true on a store:false task at the end")
	}
}

// The client is still owed the answer exactly once. The run keeps it, which is
// what the non-streaming POST and an idempotent retry both read.
func TestTheRunKeepsTheAnswerADroppedResponseNoLongerHas(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	_, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "chrn_echo", Store: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	final := run.Result()
	if final == nil {
		t.Fatal("the run kept no result for a response it dropped")
	}
	if final.Status != uhp.StatusCompleted {
		t.Errorf("kept status = %q, want %q", final.Status, uhp.StatusCompleted)
	}
	if final.Text() == "" {
		t.Error("the kept result carries no output")
	}
}

// Nil for a retained response, so the ordinary path still reads from the store
// and there are not two answers to the same question.
func TestARetainedResponseLeavesNoCopyOnTheRun(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	_, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "chrn_echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if run.Result() != nil {
		t.Error("a retained response left a copy on the run")
	}
}

// The Session outlives the response. It owns the working directory and the
// harness binding, and the harness's own session id lives on it rather than
// only on the task — so dropping the response does not cost the conversation
// its ability to resume.
func TestDroppingAResponseLeavesItsSessionAlone(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "chrn_echo", Store: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	if _, err := svc.GetSession(ctx, task.SessionID); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// Turns are derived from stored tasks, so a dropped response leaves the
	// session with no history of it. That is the whole of what was asked for.
	turns, err := svc.SessionTurns(ctx, task.SessionID)
	if err != nil {
		t.Fatalf("SessionTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("session has %d turns, want 0: a dropped response is not a Turn", len(turns))
	}
}

// A dropped response cannot be continued from, which follows from its being
// gone rather than being a second rule.
func TestADroppedResponseCannotBeContinued(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	first, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "chrn_echo", Store: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	_, _, err = svc.StartTask(ctx, CreateTaskRequest{
		Input: "y", HarnessID: "chrn_echo", PreviousResponseID: first.ID,
	})
	if !errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("continuation err = %v, want ErrResponseNotFound", err)
	}
}

// Tasks §6: a retry is given the first request's answer. The first request's
// answer was the whole response object, so a retry inside the retention window
// still gets it even though the row is gone — anything else makes the replay
// differ from the original, which is the one thing §6 forbids.
func TestAnIdempotentRetryStillAnswersForADroppedResponse(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()

	req := CreateTaskRequest{
		Input: "x", HarnessID: "chrn_echo", Store: boolPtr(false), IdempotencyKey: "k-1",
	}
	first, run, err := svc.StartTask(ctx, req)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	retried, retriedRun, err := svc.StartTask(ctx, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.ID != first.ID {
		t.Errorf("retry started a second task: %q != %q", retried.ID, first.ID)
	}
	if retriedRun.Result() == nil {
		t.Fatal("the retry cannot reach the answer the first request was given")
	}
	if retriedRun.Result().Text() != run.Result().Text() {
		t.Error("the replay differs from the original answer")
	}
}
