package http

import (
	"net/http"
	"strconv"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

// handleListSessions answers GET /v1/sessions?limit=&cursor=&harness=
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	page, err := s.tasks.ListSessions(r.Context(), domain.SessionFilter{
		HarnessID: q.Get("harness"),
		Limit:     limit,
		Cursor:    q.Get("cursor"),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// The wire object, not the page: [domain.SessionPage] holds pointers the
	// store owns and spells "no more pages" as an empty string, where the
	// protocol spells it as an explicit null. This is the one place the two
	// meet, and taking the embedded wire object off each row is the whole
	// conversion — there is no second copy of a session to keep in step.
	sessions := make([]uhp.Session, 0, len(page.Sessions))
	for _, sess := range page.Sessions {
		sessions = append(sessions, sess.Session)
	}

	// next_cursor is null on the last page, never absent: a client must not
	// have to infer the end from a short page, because that inference is wrong
	// whenever a page is exactly full.
	var next *string
	if page.NextCursor != "" {
		c := page.NextCursor
		next = &c
	}

	writeJSON(w, http.StatusOK, uhp.SessionList{Sessions: sessions, NextCursor: next})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.tasks.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleSessionTurns(w http.ResponseWriter, r *http.Request) {
	turns, err := s.tasks.SessionTurns(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if turns == nil {
		turns = []uhp.Turn{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"turns": turns})
}

// handleCancelSession stops whatever is running in a session, without deleting
// it — the conversation remains continuable.
func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.CancelSession(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	sess, err := s.tasks.GetSession(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// handleDeleteSession answers DELETE /v1/traces/{id} (Sessions §6): the whole
// conversation, its turns and its files.
//
// It cancels first, and that is the opposite of DELETE /v1/responses/{id},
// which MUST NOT. Getting the two the wrong way round is the obvious failure
// mode: deleting one response is history cleanup, and deleting the trace is
// disposing of the conversation the run belongs to — Sessions §6 couples
// cancellation to it "due to ownership concerns", because there is no owner
// left to report the run to. See [service.TaskService.DeleteSession].
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.DeleteSession(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	writeDeleted(w, id)
}
