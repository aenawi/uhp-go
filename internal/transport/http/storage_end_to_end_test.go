package http

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
)

// unreadableStore is a store whose two single-row reads fail. Everything else is
// a real MemoryStore, so the server above it is otherwise ordinary.
type unreadableStore struct {
	service.Store
}

func (unreadableStore) GetTask(context.Context, string) (*domain.Task, bool, error) {
	return nil, false, errors.New("disk gone")
}

func (unreadableStore) GetSession(context.Context, string) (*domain.Session, bool, error) {
	return nil, false, errors.New("disk gone")
}

// The same rule as TestStorageFailureIsAlways500, asserted through the real
// service instead of a stand-in for it.
//
// The distinction is the whole reason this test exists. That table hands the
// transport a service.ErrStorage and checks the transport does the right thing
// with it — which is worth pinning, and is exactly half the question. For a
// while it was the only half anyone checked, and the other half was wrong:
// TaskService flattened every store error into ErrResponseNotFound before the
// transport was ever shown one, so both layers passed their own tests and a
// real client still got 404 for a disk that had stopped answering.
//
// So this one starts at the bottom, with a store that cannot read, and asks the
// only question a client can ask: what comes back over the wire. Two green
// halves are not a green whole, and nothing short of the whole stack can say so.
func TestStorageFailureReaches500ThroughTheRealService(t *testing.T) {
	srv := NewServer(
		service.NewTaskService(
			harness.NewRegistry(),
			unreadableStore{Store: store.NewMemoryStore()},
			slog.Default(),
		),
		slog.Default(), nil, 0,
	)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "get task", method: "GET", path: "/v1/responses/resp_1"},
		{name: "cancel task", method: "POST", path: "/v1/responses/resp_1/cancel"},
		{name: "get session", method: "GET", path: "/v1/sessions/sess_1"},
		{name: "session turns", method: "GET", path: "/v1/sessions/sess_1/turns"},
		{name: "cancel session", method: "POST", path: "/v1/sessions/sess_1/cancel"},
		{name: "session files", method: "GET", path: "/v1/sessions/sess_1/files"},
		{name: "session archive", method: "GET", path: "/v1/sessions/sess_1/files/archive"},
		// The artifact endpoints answer file_not_found for everything they
		// cannot resolve, which is right for every reason but this one: they
		// reach the store through SessionFiles, and a store that did not answer
		// is not a file that is not there.
		{name: "artifact content", method: "GET", path: "/v1/containers/cntr_1/files/file_x/content"},
		{name: "artifact pdf", method: "GET", path: "/v1/containers/cntr_1/files/file_x/pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := callJSON(t, srv, tc.method, tc.path, "")
			if status != 500 {
				t.Fatalf("%s %s: expected 500, got %d: %v", tc.method, tc.path, status, body)
			}
			if code := errorCode(t, body); code != vendorCodeStorageFailure {
				t.Fatalf("%s %s: expected %s, got %s", tc.method, tc.path, vendorCodeStorageFailure, code)
			}
		})
	}
}

// The other half, through the same stack: a row that genuinely is not there is
// still a 404. A fix that answered 500 for everything would have traded one
// wrong status for another, and only a real store — which can tell the two
// apart — can show that it did not.
func TestGenuineMissStillReaches404ThroughTheRealService(t *testing.T) {
	srv := NewServer(
		service.NewTaskService(harness.NewRegistry(), store.NewMemoryStore(), slog.Default()),
		slog.Default(), nil, 0,
	)

	for _, tc := range []struct {
		name   string
		method string
		path   string
		code   string
	}{
		{name: "get task", method: "GET", path: "/v1/responses/resp_missing", code: "response_not_found"},
		{name: "cancel task", method: "POST", path: "/v1/responses/resp_missing/cancel", code: "response_not_found"},
		{name: "get session", method: "GET", path: "/v1/sessions/sess_missing", code: "session_not_found"},
		{name: "session turns", method: "GET", path: "/v1/sessions/sess_missing/turns", code: "session_not_found"},
		{name: "cancel session", method: "POST", path: "/v1/sessions/sess_missing/cancel", code: "session_not_found"},
		// Still the one answer for "no such container", "no such file" and "not
		// yours" — routing these through writeServiceError widened what an
		// unreadable store says, not what a miss says (Files §5).
		{name: "artifact content", method: "GET", path: "/v1/containers/cntr_1/files/file_x/content", code: "file_not_found"},
		{name: "artifact pdf", method: "GET", path: "/v1/containers/cntr_1/files/file_x/pdf", code: "file_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := callJSON(t, srv, tc.method, tc.path, "")
			if status != 404 {
				t.Fatalf("%s %s: expected 404, got %d: %v", tc.method, tc.path, status, body)
			}
			if code := errorCode(t, body); code != tc.code {
				t.Fatalf("%s %s: expected %s, got %s", tc.method, tc.path, tc.code, code)
			}
		})
	}
}
