package http

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
)

func newTask(id string, status domain.TaskStatus) *domain.Task {
	return &domain.Task{
		ID:        id,
		Object:    "response",
		Status:    status,
		Model:     "m",
		Store:     true,
		CreatedAt: time.Unix(1700000000, 0).UTC(),
		UpdatedAt: time.Unix(1700000001, 0).UTC(),
	}
}

func TestGetTask(t *testing.T) {
	asked := ""
	srv := newFakeServer(&fakeService{
		getTask: func(_ context.Context, id string) (*domain.Task, error) {
			asked = id
			return newTask(id, domain.StatusCompleted), nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/responses/resp_1", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if asked != "resp_1" {
		t.Fatalf("handler asked for %q, want resp_1", asked)
	}
	if body["id"] != "resp_1" || body["status"] != string(domain.StatusCompleted) {
		t.Fatalf("unexpected response: %v", body)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		getTask: func(context.Context, string) (*domain.Task, error) {
			return nil, service.ErrResponseNotFound
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/responses/resp_missing", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "response_not_found" {
		t.Fatalf("expected response_not_found, got %s", code)
	}
}

// A store that could not be read is the server's failure, not a response that
// does not exist. This is the endpoint where the difference bites hardest: a
// client polling for a task that is still running reads 404 as "it is gone" and
// stops, while the supervisor carries on running it.
//
// What this pins is the half the transport owns — the handler routes its error
// instead of choosing a status itself. It does not yet mean a real client sees
// the 500: TaskService.GetTask flattens every store error into
// ErrResponseNotFound before the transport is ever shown one, so end to end the
// 404 survives until that flattening goes too. Hence the stand-in, which is the
// only thing that can hand this handler an ErrStorage today.
//
// The general rule lives in TestStorageFailureIsAlways500; this stays here so
// it is read next to TestGetTaskNotFound above, which is the answer it has to
// be kept apart from.
func TestGetTaskReportsAStorageFailureAs500(t *testing.T) {
	srv := newFakeServer(&fakeService{
		getTask: func(context.Context, string) (*domain.Task, error) { return nil, errStorage() },
	})

	status, body := callJSON(t, srv, "GET", "/v1/responses/resp_1", "")
	if status != 500 {
		t.Fatalf("expected 500, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != vendorCodeStorageFailure {
		t.Fatalf("expected %s, got %s", vendorCodeStorageFailure, code)
	}
}

// Cancel answers with the task, so a client learns the terminal status from the
// same response rather than having to poll for it.
func TestCancelTaskReturnsTheTask(t *testing.T) {
	cancelled := ""
	srv := newFakeServer(&fakeService{
		cancelTask: func(_ context.Context, id string) error {
			cancelled = id
			return nil
		},
		getTask: func(_ context.Context, id string) (*domain.Task, error) {
			return newTask(id, domain.StatusCancelled), nil
		},
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses/resp_1/cancel", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if cancelled != "resp_1" {
		t.Fatalf("cancelled %q, want resp_1", cancelled)
	}
	if body["status"] != string(domain.StatusCancelled) {
		t.Fatalf("expected a cancelled task, got %v", body)
	}
}

func TestCancelTaskNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelTask: func(context.Context, string) error { return service.ErrResponseNotFound },
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses/resp_missing/cancel", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "response_not_found" {
		t.Fatalf("expected response_not_found, got %s", code)
	}
}

// A task already in a terminal state is a 409 rather than a 404: the response
// exists, it just cannot be cancelled now. Only a stand-in can produce it here
// without racing a real run.
func TestCancelTaskOnABusySessionIsAConflict(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelTask: func(context.Context, string) error { return service.ErrSessionBusy },
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses/resp_1/cancel", "")
	if status != 409 {
		t.Fatalf("expected 409, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "session_busy" {
		t.Fatalf("expected session_busy, got %s", code)
	}
}

// The task is read back after the cancel signal, and that read can fail on its
// own. The signal landed, so the response certainly exists — a 404 here would
// be a lie about the one thing this call just proved. Reachable only through a
// stand-in, for the same reason as the retrieve path above.
func TestCancelTaskReportsAFailedReadBackAs500(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelTask: func(context.Context, string) error { return nil },
		getTask: func(context.Context, string) (*domain.Task, error) {
			return nil, fmt.Errorf("read back: %w", service.ErrStorage)
		},
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses/resp_1/cancel", "")
	if status != 500 {
		t.Fatalf("expected 500, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != vendorCodeStorageFailure {
		t.Fatalf("expected %s, got %s", vendorCodeStorageFailure, code)
	}
}
