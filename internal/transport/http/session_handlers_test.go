package http

import (
	"context"
	"fmt"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
)

func newSession(id string) *domain.Session {
	return &domain.Session{Session: uhp.Session{
		ID:        id,
		Object:    "session",
		HarnessID: "chrn_echo",
		Status:    string(uhp.StatusCompleted),
		CreatedAt: 1700000000,
		UpdatedAt: 1700000001,
	}}
}

// jsonArray asserts a field decoded as an array rather than as null.
//
// The distinction survives into map[string]any and nowhere else: `[]` becomes a
// non-nil []any and `null` becomes an untyped nil, so the type assertion is
// what tells them apart. Asserting on len() alone would pass for both, and
// `null` is what a client iterating the field crashes on.
func jsonArray(t *testing.T, body map[string]any, field string) []any {
	t.Helper()
	got, ok := body[field].([]any)
	if !ok {
		t.Fatalf("%s: expected an array, got %#v", field, body[field])
	}
	return got
}

func TestListSessionsEmptyIsAnArrayNotNull(t *testing.T) {
	srv := newFakeServer(&fakeService{
		listSessions: func(context.Context, domain.SessionFilter) (domain.SessionPage, error) {
			return domain.SessionPage{}, nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if got := jsonArray(t, body, "sessions"); len(got) != 0 {
		t.Fatalf("expected no sessions, got %v", got)
	}

	// Present and null, never absent: a client must not have to infer the end
	// of a listing from a short page, because that inference is wrong whenever
	// a page is exactly full.
	next, present := body["next_cursor"]
	if !present {
		t.Fatal("next_cursor is absent; it must be present and null on the last page")
	}
	if next != nil {
		t.Fatalf("expected next_cursor null on the last page, got %#v", next)
	}
}

func TestListSessionsCarriesNextCursor(t *testing.T) {
	srv := newFakeServer(&fakeService{
		listSessions: func(context.Context, domain.SessionFilter) (domain.SessionPage, error) {
			return domain.SessionPage{
				Sessions:   []*domain.Session{newSession("sess_1")},
				NextCursor: "sess_1",
			}, nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if body["next_cursor"] != "sess_1" {
		t.Fatalf("expected next_cursor sess_1, got %#v", body["next_cursor"])
	}
	if got := jsonArray(t, body, "sessions"); len(got) != 1 {
		t.Fatalf("expected one session, got %d", len(got))
	}
}

// The query string is the only way a client pages or filters, so a handler that
// parses it and then drops it is indistinguishable from one that works right up
// until the second page.
func TestListSessionsPassesTheQueryToTheFilter(t *testing.T) {
	var got domain.SessionFilter
	srv := newFakeServer(&fakeService{
		listSessions: func(_ context.Context, f domain.SessionFilter) (domain.SessionPage, error) {
			got = f
			return domain.SessionPage{}, nil
		},
	})

	callJSON(t, srv, "GET", "/v1/sessions?limit=7&cursor=sess_9&harness=chrn_echo", "")

	want := domain.SessionFilter{HarnessID: "chrn_echo", Limit: 7, Cursor: "sess_9"}
	if got != want {
		t.Fatalf("filter: got %+v, want %+v", got, want)
	}
}

// A limit that is not a number leaves the field at zero rather than refusing the
// request: the handler discards strconv's error, and the service applies its own
// default to a zero limit.
func TestListSessionsIgnoresAnUnparseableLimit(t *testing.T) {
	var got domain.SessionFilter
	srv := newFakeServer(&fakeService{
		listSessions: func(_ context.Context, f domain.SessionFilter) (domain.SessionPage, error) {
			got = f
			return domain.SessionPage{}, nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions?limit=lots", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if got.Limit != 0 {
		t.Fatalf("expected limit 0, got %d", got.Limit)
	}
}

func TestGetSession(t *testing.T) {
	asked := ""
	srv := newFakeServer(&fakeService{
		getSession: func(_ context.Context, id string) (*domain.Session, error) {
			asked = id
			return newSession(id), nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions/sess_1", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if asked != "sess_1" {
		t.Fatalf("handler asked for %q, want sess_1", asked)
	}
	if body["id"] != "sess_1" {
		t.Fatalf("expected sess_1, got %#v", body["id"])
	}
}

func TestGetSessionNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		getSession: func(context.Context, string) (*domain.Session, error) {
			return nil, service.ErrSessionNotFound
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions/sess_missing", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "session_not_found" {
		t.Fatalf("expected session_not_found, got %s", code)
	}
}

// A session with no turns answers with an empty array, not null: a client
// rebuilding a transcript iterates the field, and null would make the ordinary
// "nothing has run yet" case its crash.
func TestSessionTurnsEmptyIsAnArrayNotNull(t *testing.T) {
	srv := newFakeServer(&fakeService{
		sessionTurns: func(context.Context, string) ([]uhp.Turn, error) { return nil, nil },
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions/sess_1/turns", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if got := jsonArray(t, body, "turns"); len(got) != 0 {
		t.Fatalf("expected no turns, got %v", got)
	}
}

func TestSessionTurns(t *testing.T) {
	srv := newFakeServer(&fakeService{
		sessionTurns: func(context.Context, string) ([]uhp.Turn, error) {
			return []uhp.Turn{{
				ResponseID: "resp_1",
				Status:     uhp.StatusCompleted,
				Model:      "m",
				Input:      "hi",
				Output:     "ok",
			}}, nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions/sess_1/turns", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	turns := jsonArray(t, body, "turns")
	if len(turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(turns))
	}
	first, _ := turns[0].(map[string]any)
	if first["response_id"] != "resp_1" || first["output"] != "ok" {
		t.Fatalf("unexpected turn: %#v", turns[0])
	}
}

func TestSessionTurnsNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		sessionTurns: func(context.Context, string) ([]uhp.Turn, error) {
			return nil, service.ErrSessionNotFound
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/sessions/sess_missing/turns", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "session_not_found" {
		t.Fatalf("expected session_not_found, got %s", code)
	}
}

// Sessions §4: cancelling a session MUST NOT delete it. The session coming back
// in the response is how a client sees the conversation is still there.
func TestCancelSessionReturnsTheSession(t *testing.T) {
	cancelled := ""
	srv := newFakeServer(&fakeService{
		cancelSession: func(_ context.Context, id string) error {
			cancelled = id
			return nil
		},
		getSession: func(_ context.Context, id string) (*domain.Session, error) {
			s := newSession(id)
			s.Status = string(uhp.StatusCancelled)
			return s, nil
		},
	})

	status, body := callJSON(t, srv, "POST", "/v1/sessions/sess_1/cancel", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if cancelled != "sess_1" {
		t.Fatalf("cancelled %q, want sess_1", cancelled)
	}
	if body["id"] != "sess_1" || body["status"] != string(uhp.StatusCancelled) {
		t.Fatalf("unexpected session: %v", body)
	}
}

func TestCancelSessionNotFound(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelSession: func(context.Context, string) error { return service.ErrSessionNotFound },
	})

	status, body := callJSON(t, srv, "POST", "/v1/sessions/sess_missing/cancel", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "session_not_found" {
		t.Fatalf("expected session_not_found, got %s", code)
	}
}

// The handler reads the session back after cancelling, and that second call can
// fail on its own. It is tested here rather than in the storage-failure table
// because it is the *second* call that fails: without this the failure would
// surface as a 200 carrying `null`, which a client reads as a session that no
// longer exists — the one thing Sessions §4 promises cancellation does not do.
func TestCancelSessionReportsAFailedReadBack(t *testing.T) {
	srv := newFakeServer(&fakeService{
		cancelSession: func(context.Context, string) error { return nil },
		getSession: func(context.Context, string) (*domain.Session, error) {
			return nil, fmt.Errorf("read back: %w", service.ErrStorage)
		},
	})

	status, body := callJSON(t, srv, "POST", "/v1/sessions/sess_1/cancel", "")
	if status != 500 {
		t.Fatalf("expected 500, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != vendorCodeStorageFailure {
		t.Fatalf("expected %s, got %s", vendorCodeStorageFailure, code)
	}
}
