package http

import (
	"net/http"
	"strconv"

	"github.com/aenawi/uhp-go/internal/domain"
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

	sessions := page.Sessions
	if sessions == nil {
		sessions = []*domain.Session{}
	}

	// next_cursor is null on the last page, never absent: a client must not
	// have to infer the end from a short page, because that inference is wrong
	// whenever a page is exactly full.
	var next *string
	if page.NextCursor != "" {
		c := page.NextCursor
		next = &c
	}

	writeJSON(w, http.StatusOK, struct {
		Sessions   []*domain.Session `json:"sessions"`
		NextCursor *string           `json:"next_cursor"`
	}{Sessions: sessions, NextCursor: next})
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
		turns = []domain.Turn{}
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
