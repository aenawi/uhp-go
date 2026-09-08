package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aenawi/uhp-go/internal/service"
)

// deleteSession sends the DELETE Sessions §6 names: /v1/sessions/{id}, the
// same path every read of the session uses.
func deleteSession(t *testing.T, srv *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/v1/sessions/"+id, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// The older spelling, /v1/traces/{id}, is the path §6 used to name and now
// keeps as an alias a server MAY serve. This one does, on the same handler, so
// a client shipped against the old path is not broken by the rename. The test
// is the only reason the alias cannot be dropped by accident.
func TestDeletingASessionAtTheOlderTracesPathStillWorks(t *testing.T) {
	srv := newTestServer()
	responseID := createTask(t, srv, `{"input":"hi","metadata":{"harness_id":"echo"}}`)
	_, created := callJSON(t, srv, "GET", "/v1/responses/"+responseID, "")
	id := sessionID(t, created)

	req := httptest.NewRequest("DELETE", "/v1/traces/"+id, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %s", w.Body.String())
	}
	if body["id"] != id || body["deleted"] != true {
		t.Errorf("body = %v, want {id: %s, deleted: true}", body, id)
	}

	req = httptest.NewRequest("GET", "/v1/sessions/"+id, nil)
	got := httptest.NewRecorder()
	srv.Handler().ServeHTTP(got, req)
	if got.Code != 404 {
		t.Errorf("GET after delete via the alias = %d, want 404: %s", got.Code, got.Body.String())
	}
}

// The id a client deletes with is the id it was reading with, at the same path.
// So what has to disappear is everything those reads answered — the session,
// its turns, and the responses that made them up.
func TestDeletingASessionAnswersWithTheDeletionEnvelopeAndEverythingItHeldGoes(t *testing.T) {
	srv := newTestServer()
	responseID := createTask(t, srv, `{"input":"hi","metadata":{"harness_id":"echo"}}`)

	_, created := callJSON(t, srv, "GET", "/v1/responses/"+responseID, "")
	id := sessionID(t, created)

	w := deleteSession(t, srv, id)
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

	// The artifact listing is in here deliberately. It is the one read that
	// does not go through the session row — it walks the session's tasks — so a
	// delete that removed the session and left its tasks behind would answer
	// 404 for the three paths above and still list a deleted conversation's
	// files.
	for _, path := range []string{
		"/v1/sessions/" + id,
		"/v1/sessions/" + id + "/turns",
		"/v1/sessions/" + id + "/files",
		"/v1/responses/" + responseID,
	} {
		req := httptest.NewRequest("GET", path, nil)
		got := httptest.NewRecorder()
		srv.Handler().ServeHTTP(got, req)
		if got.Code != 404 {
			t.Errorf("GET %s after delete = %d, want 404: %s", path, got.Code, got.Body.String())
		}
	}
}

// Deleting twice is not-found rather than a second success, for the reason
// DELETE /v1/responses/{id} gives.
func TestDeletingAnUnknownSessionIsSessionNotFound(t *testing.T) {
	srv := newTestServer()
	w := deleteSession(t, srv, "sess_nope")
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if code := errorCode(t, body); code != "session_not_found" {
		t.Errorf("code = %s, want session_not_found", code)
	}
}

// The endpoint is behind the same credential as every other one that touches a
// principal's data. A delete reachable without one is worse than a read that
// is: it destroys rather than discloses.
func TestDeletingASessionRequiresACredential(t *testing.T) {
	srv := newAuthServer([]string{"k1"}, 0)
	w := deleteSession(t, srv, "sess_x")
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401: %s", w.Code, w.Body.String())
	}
}

// A storage failure is the server's fault and must not be reported as the
// client's: a 404 here would tell a client its id was wrong and that retrying
// never helps, when the opposite is true.
func TestDeletingASessionReportsAStorageFailureAsServerError(t *testing.T) {
	srv := newFakeServer(&fakeService{
		deleteSession: func(context.Context, string) error { return service.ErrStorage },
	})
	w := deleteSession(t, srv, "sess_x")
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
}
