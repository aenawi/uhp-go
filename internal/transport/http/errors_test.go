package http

import (
	"context"
	"fmt"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
)

// errStorage is what a store hands back when it cannot be read, wrapped the way
// the service layer wraps it, so the test exercises errors.Is rather than
// equality — a sentinel compared with == would pass for a bug that stopped
// wrapping.
func errStorage() error { return fmt.Errorf("disk gone: %w", service.ErrStorage) }

// A store that cannot be read is the server's failure, not the client's.
// Errors §4 makes the class the retry signal, so every read endpoint must
// answer 500 rather than dress the failure up as a 404 or an empty result — a
// client told "there are no harnesses" stops asking, and one told "not found"
// never retries.
//
// It is one table rather than one test per endpoint because the rule is one
// rule: writeServiceError's ErrStorage arm, reached from five different
// handlers. A case that drifts is a handler that stopped routing its error
// through it.
//
// Only a stand-in can produce this at all. The in-memory store the rest of the
// package tests against never fails, which is why these paths were unreachable
// before the transport depended on an interface (issue #10).
func TestStorageFailureIsAlways500(t *testing.T) {
	failing := func() *fakeService {
		return &fakeService{
			listHarnesses: func(context.Context) ([]domain.Harness, error) {
				return nil, errStorage()
			},
			getHarness: func(context.Context, string) (domain.Harness, bool, error) {
				return domain.Harness{}, false, errStorage()
			},
			listSessions: func(context.Context, domain.SessionFilter) (domain.SessionPage, error) {
				return domain.SessionPage{}, errStorage()
			},
			getSession: func(context.Context, string) (*domain.Session, error) {
				return nil, errStorage()
			},
			sessionTurns: func(context.Context, string) ([]domain.Turn, error) {
				return nil, errStorage()
			},
		}
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"list harnesses", "/v1/harnesses"},
		{"get harness", "/v1/harnesses/echo"},
		{"harness models", "/v1/harnesses/echo/models"},
		{"list models", "/v1/models"},
		{"list sessions", "/v1/sessions"},
		{"get session", "/v1/sessions/sess_1"},
		{"session turns", "/v1/sessions/sess_1/turns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := callJSON(t, newFakeServer(failing()), "GET", tc.path, "")
			if status != 500 {
				t.Fatalf("%s: expected 500, got %d: %v", tc.path, status, body)
			}
			if code := errorCode(t, body); code != vendorCodeStorageFailure {
				t.Fatalf("%s: expected %s, got %s", tc.path, vendorCodeStorageFailure, code)
			}
		})
	}
}
