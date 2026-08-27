package http

import (
	"context"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
)

// Issue #60: Errors §4 lists `session_busy` as retryable "once in-flight task
// reaches terminal state", so the refusal is an instruction to wait and
// `retry_after_ms` is the only part of it a client can act on. This server sent
// neither the field nor anything else, and a client's honest options were a
// fixed sleep or a spin.
func TestSessionBusyCarriesARetryHint(t *testing.T) {
	srv := newFakeServer(&fakeService{
		startTask: func(context.Context, service.CreateTaskRequest) (*domain.Task, *service.Run, error) {
			return nil, nil, &service.SessionBusyError{SessionID: "sess_1", TaskID: "resp_held"}
		},
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses",
		`{"input":"hi","previous_response_id":"resp_held","metadata":{"harness_id":"slow"}}`)
	if status != 409 {
		t.Fatalf("expected 409, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "session_busy" {
		t.Fatalf("expected session_busy, got %s", code)
	}
	envelope, _ := body["error"].(map[string]any)
	detail, ok := envelope["detail"].(map[string]any)
	if !ok {
		t.Fatalf("refusal carries no detail: %v", body)
	}
	if want := float64(retryAfterSessionBusy.Milliseconds()); detail["retry_after_ms"] != want {
		t.Errorf("detail.retry_after_ms = %v, want %v", detail["retry_after_ms"], want)
	}
	// The response holding the session is what lets a client stop retrying
	// altogether and watch that one go terminal instead.
	if detail["response_id"] != "resp_held" {
		t.Errorf("detail.response_id = %v, want resp_held", detail["response_id"])
	}
}

// RFC 9110 §10.2.3 defines Retry-After for 503, 429 and the 3xx redirects, and
// this refusal is a 409. The wait goes in the body field the protocol names for
// it and nowhere else; a header on a status the RFC does not define it for is
// an invitation for an intermediary to act on something it should not.
func TestSessionBusySendsNoRetryAfterHeader(t *testing.T) {
	srv := newFakeServer(&fakeService{
		startTask: func(context.Context, service.CreateTaskRequest) (*domain.Task, *service.Run, error) {
			return nil, nil, &service.SessionBusyError{SessionID: "sess_1", TaskID: "resp_held"}
		},
	})

	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(
		`{"input":"hi","previous_response_id":"resp_held","metadata":{"harness_id":"slow"}}`))
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q on a 409, want it absent", got)
	}
}

// The bare sentinel is the fallback for a refusal with no run behind it to
// name. Nothing in this server produces one — the typed error above is the only
// path there is — and the arm stays because the alternative for a future one is
// the default arm's `502`, which would call a busy session a broken server.
// What it must not do is invent a wait: Lifecycle §6 says to omit the field
// rather than guess. Only a stand-in can provoke it, for that reason.
func TestSessionBusyWithoutARunInventsNoWait(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelTask: func(context.Context, string) error { return service.ErrSessionBusy },
	})

	status, body := callJSON(t, srv, "POST", "/v1/responses/resp_1/cancel", "")
	if status != 409 {
		t.Fatalf("expected 409, got %d: %v", status, body)
	}
	envelope, _ := body["error"].(map[string]any)
	if detail, ok := envelope["detail"]; ok && detail != nil {
		t.Errorf("detail = %v, want none for a refusal with no run behind it", detail)
	}
}
