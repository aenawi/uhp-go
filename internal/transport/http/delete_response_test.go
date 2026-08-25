package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
)

func deleteResponse(t *testing.T, srv *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/v1/responses/"+id, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// The OpenAPI specifies 200 with `{id, deleted}` for this path, not 204: a
// client written against it decodes the body.
func TestDeletingAResponseAnswersWithTheDeletionEnvelope(t *testing.T) {
	srv := newTestServer()
	id := createTask(t, srv, `{"input":"hi","metadata":{"harness_id":"echo"}}`)

	w := deleteResponse(t, srv, id)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %s", w.Body.String())
	}
	if body["id"] != id {
		t.Errorf("id = %v, want %s", body["id"], id)
	}
	if body["deleted"] != true {
		t.Errorf("deleted = %v, want true", body["deleted"])
	}

	// Gone means gone: the read that worked a moment ago is now a 404.
	req := httptest.NewRequest("GET", "/v1/responses/"+id, nil)
	got := httptest.NewRecorder()
	srv.Handler().ServeHTTP(got, req)
	if got.Code != 404 {
		t.Fatalf("GET after delete = %d, want 404: %s", got.Code, got.Body.String())
	}
}

// Deleting twice is a 404, not a second success. `deleted: true` is a report of
// what this request did, and answering it for a response that was already gone
// would tell a client it had just removed something it had not.
func TestDeletingATwiceDeletedResponseIsNotFound(t *testing.T) {
	srv := newTestServer()
	id := createTask(t, srv, `{"input":"hi","metadata":{"harness_id":"echo"}}`)

	if w := deleteResponse(t, srv, id); w.Code != 200 {
		t.Fatalf("first delete = %d, want 200", w.Code)
	}
	w := deleteResponse(t, srv, id)
	if w.Code != 404 {
		t.Fatalf("second delete = %d, want 404: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if code := errorCode(t, body); code != "response_not_found" {
		t.Errorf("code = %s, want response_not_found", code)
	}
}

// The MUST, and the one this endpoint is easiest to get wrong on. Tasks §4:
// "A server MUST NOT let this cancel a running task — cancellation and deletion
// are different intentions, and conflating them means a client cannot clean up
// history without stopping work."
//
// An implementation that cancelled first would pass every other test in this
// file. This is the only one that would catch it, and it asserts on the
// adapter's own view of the run rather than on anything the wire can show,
// because from the wire a cancelled run and a deleted record look identical.
func TestDeletingAResponseDoesNotStopTheRun(t *testing.T) {
	a := newBlockingAdapter()
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	srv := NewServer(svc, slog.Default(), nil, 0)

	// Started through the service rather than over HTTP, so the run is in
	// flight while this test holds its id. Driving it with a streaming POST
	// would mean reading a ResponseRecorder another goroutine is still writing
	// to, which is a data race in the test rather than a property of the server.
	task, run, err := svc.StartTask(context.Background(), service.CreateTaskRequest{
		Input: "hi", HarnessID: "chrn_echo",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-a.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the run never started")
	}

	if got := deleteResponse(t, srv, task.ID); got.Code != 200 {
		t.Fatalf("delete during a run = %d, want 200: %s", got.Code, got.Body.String())
	}

	// The property: the adapter was never asked to stop. An implementation that
	// cancelled first would have called Cancel before the delete returned, and
	// would pass every other test in this file.
	if a.cancelled() {
		t.Fatal("deleting a response cancelled the run; Tasks §4 forbids it")
	}

	// And it still finishes. "Not cancelled" is only half the requirement — a
	// run that had been orphaned rather than stopped would also fail to
	// terminate, and the client would never get its slot back.
	a.release <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("the run did not reach a terminal state after its record was deleted: %v", err)
	}
}
