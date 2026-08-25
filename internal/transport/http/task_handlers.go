package http

import (
	"encoding/json"
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

// handleTaskInputItems answers GET /v1/responses/{id}/input_items (Tasks §4):
// "the input the task was created with, for clients that need to reconstruct a
// transcript without having stored it themselves".
//
// The envelope is the OpenAPI's — `{object: "list", data: [...]}` — and `data`
// is always an array, never null, because a client iterating it should not have
// to special-case a task that carried no items.
func (s *Server) handleTaskInputItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.tasks.TaskInputItems(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}

// handleDeleteTask answers DELETE /v1/responses/{id} (Tasks §4).
//
// It does not cancel. The specification is explicit that it must not — "a
// client cannot clean up history without stopping work" otherwise — so a run
// that is in flight when its record is deleted keeps running to a terminal
// state with nobody left to report it to. That is the intended behaviour and
// not an oversight; see TestDeletingAResponseDoesNotStopTheRun.
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.DeleteTask(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	// 200 with a body, not 204: the OpenAPI specifies `{id, deleted}` for both
	// deletion endpoints, and a client written against it decodes the response.
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
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
