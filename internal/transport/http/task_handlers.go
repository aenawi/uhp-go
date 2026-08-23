package http

import (
	"net/http"
)

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.tasks.GetTask(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.CancelTask(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	task, err := s.tasks.GetTask(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}
