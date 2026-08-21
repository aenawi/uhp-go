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

// Current behaviour, recorded rather than endorsed: handleGetTask maps every
// error to 404 response_not_found instead of going through writeServiceError,
// so a store that could not be read is reported as a response that does not
// exist. Errors §4 makes the class the retry signal, and 404 tells a client
// not to bother. Left as it is because it is not what issue #10 is about; the
// test exists so that fixing it changes a red line rather than a silent one,
// and it is deliberately absent from the storage-failure table in
// errors_test.go, which records the endpoints that already get this right.
func TestGetTaskReportsAStorageFailureAsNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		getTask: func(context.Context, string) (*domain.Task, error) {
			return nil, fmt.Errorf("disk gone: %w", service.ErrStorage)
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/responses/resp_1", "")
	if status != 404 {
		t.Fatalf("expected the documented 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "response_not_found" {
		t.Fatalf("expected response_not_found, got %s", code)
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

// Unlike the retrieve path, cancel routes its own failure through
// writeServiceError, so a task already in a terminal state is a 409 rather than
// a 404. Only a stand-in can produce it here without racing a real run.
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
// own. The handler answers 404 for it, which is the same shape as the retrieve
// path above and is recorded here for the same reason.
func TestCancelTaskReportsAFailedReadBackAsNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelTask: func(context.Context, string) error { return nil },
		getTask: func(context.Context, string) (*domain.Task, error) {
			return nil, fmt.Errorf("read back: %w", service.ErrStorage)
		},
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses/resp_1/cancel", "")
	if status != 404 {
		t.Fatalf("expected the documented 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "response_not_found" {
		t.Fatalf("expected response_not_found, got %s", code)
	}
}
