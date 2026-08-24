package http

import (
	"context"
	"fmt"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// errStorage is what a store hands back when it cannot be read, wrapped the way
// the service layer wraps it, so the test exercises errors.Is rather than
// equality — a sentinel compared with == would pass for a bug that stopped
// wrapping.
func errStorage() error { return fmt.Errorf("disk gone: %w", service.ErrStorage) }

// A store that cannot be read is the server's failure, not the client's.
// Errors §4 makes the class the retry signal, so every endpoint that reads must
// answer 500 rather than dress the failure up as a 404 or an empty result — a
// client told "there are no harnesses" stops asking, and one told "not found"
// never retries.
//
// It is one table rather than one test per endpoint because the rule is one
// rule: writeServiceError's ErrStorage arm, reached from every handler that
// goes near the store. A case that drifts is a handler that stopped routing its
// error through it.
//
// Only a stand-in can produce this, and being precise about what that buys is
// the point: the table pins routing — that each handler hands its error to
// writeServiceError rather than ruling on it — and routing alone is not the
// answer a real client gets. Trusting it for the whole answer is the mistake
// that let this bug live, because the service layer underneath was flattening
// every store error into a not-found and both layers passed their own tests.
// TestStorageFailureReaches500ThroughTheRealService asks the end-to-end
// question; this one says which handler to look at when that one goes red.
//
// The in-memory store the rest of the package tests against never fails, which
// is why these paths were unreachable before the transport depended on an
// interface (issue #10).
func TestStorageFailureIsAlways500(t *testing.T) {
	failing := func() *fakeService {
		return &fakeService{
			listHarnesses: func(context.Context) ([]uhpgo.Harness, error) {
				return nil, errStorage()
			},
			getHarness: func(context.Context, string) (uhpgo.Harness, bool, error) {
				return uhpgo.Harness{}, false, errStorage()
			},
			listSessions: func(context.Context, domain.SessionFilter) (domain.SessionPage, error) {
				return domain.SessionPage{}, errStorage()
			},
			getSession: func(context.Context, string) (*domain.Session, error) {
				return nil, errStorage()
			},
			sessionTurns: func(context.Context, string) ([]uhp.Turn, error) {
				return nil, errStorage()
			},
			getTask: func(context.Context, string) (*domain.Task, error) {
				return nil, errStorage()
			},
			// The odd one out, and deliberately not a failure: the cancel
			// signal lands, and it is the read-back that follows it which this
			// table is asking about. A cancel that failed on its own would
			// never reach that read.
			cancelTask: func(context.Context, string) error { return nil },
		}
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "list harnesses", method: "GET", path: "/v1/harnesses"},
		{name: "get harness", method: "GET", path: "/v1/harnesses/echo"},
		{name: "harness models", method: "GET", path: "/v1/harnesses/echo/models"},
		{name: "list models", method: "GET", path: "/v1/models"},
		{name: "list sessions", method: "GET", path: "/v1/sessions"},
		{name: "get session", method: "GET", path: "/v1/sessions/sess_1"},
		{name: "session turns", method: "GET", path: "/v1/sessions/sess_1/turns"},
		{name: "get task", method: "GET", path: "/v1/responses/resp_1"},
		{name: "cancel task read-back", method: "POST", path: "/v1/responses/resp_1/cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := callJSON(t, newFakeServer(failing()), tc.method, tc.path, "")
			if status != 500 {
				t.Fatalf("%s: expected 500, got %d: %v", tc.path, status, body)
			}
			if code := errorCode(t, body); code != vendorCodeStorageFailure {
				t.Fatalf("%s: expected %s, got %s", tc.path, vendorCodeStorageFailure, code)
			}
		})
	}
}

// A capability the harness does not advertise is the client's request being
// wrong, not the server being broken or unconfigured: a 501 would say this
// deployment cannot do it at all, and a retry-shaped 5xx would say to try
// again. The detail names the capability so a client can line the refusal up
// against the `capabilities` list discovery already gave it.
//
// `param` is the half that has to differ per endpoint. Errors §1 wants the
// dotted path to the offending field "whenever there is one", and a cancel has
// no body — naming a field the request does not contain sends a client looking
// for something it cannot find.
func TestUnsupportedCapabilityIs422WithTheCapabilityNamed(t *testing.T) {
	refusal := func(c uhpgo.Capability) *service.CapabilityError {
		return &service.CapabilityError{
			HarnessID: "chrn_grok", Capability: c, Consequence: "it cannot",
		}
	}

	for _, tc := range []struct {
		name      string
		srv       *Server
		method    string
		path      string
		body      string
		capabilit uhpgo.Capability
		wantParam any // a string, or nil for "no such field in this request"
	}{
		{
			name: "continuation on a harness without sessions",
			srv: newFakeServer(&fakeService{
				startTask: func(context.Context, service.CreateTaskRequest) (*domain.Task, *service.Run, error) {
					return nil, nil, refusal(uhpgo.CapSessions)
				},
			}),
			method:    "POST",
			path:      "/v1/responses",
			body:      `{"input":"hi","previous_response_id":"resp_0","metadata":{"harness_id":"grok-cli"}}`,
			capabilit: uhpgo.CapSessions,
			wantParam: "previous_response_id",
		},
		{
			name: "cancel on a harness without cancellation",
			srv: newFakeServer(&fakeService{
				cancelTask: func(context.Context, string) error { return refusal(uhpgo.CapCancellation) },
			}),
			method:    "POST",
			path:      "/v1/responses/resp_1/cancel",
			capabilit: uhpgo.CapCancellation,
			wantParam: nil,
		},
		{
			name: "session cancel on a harness without cancellation",
			srv: newFakeServer(&fakeService{
				cancelSession: func(context.Context, string) error { return refusal(uhpgo.CapCancellation) },
			}),
			method:    "POST",
			path:      "/v1/sessions/sess_1/cancel",
			capabilit: uhpgo.CapCancellation,
			wantParam: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := callJSON(t, tc.srv, tc.method, tc.path, tc.body)
			if status != 422 {
				t.Fatalf("expected 422, got %d: %v", status, body)
			}
			if code := errorCode(t, body); code != vendorCodeCapabilityUnsupported {
				t.Fatalf("expected %s, got %s", vendorCodeCapabilityUnsupported, code)
			}
			envelope, _ := body["error"].(map[string]any)
			detail, ok := envelope["detail"].(map[string]any)
			if !ok {
				t.Fatalf("refusal carries no detail: %v", body)
			}
			if detail["capability"] != string(tc.capabilit) {
				t.Errorf("detail.capability = %v, want %q", detail["capability"], tc.capabilit)
			}
			if detail["harness"] != "chrn_grok" {
				t.Errorf("detail.harness = %v, want %q", detail["harness"], "chrn_grok")
			}
			if envelope["param"] != tc.wantParam {
				t.Errorf("param = %v, want %v", envelope["param"], tc.wantParam)
			}
		})
	}
}
