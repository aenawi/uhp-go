package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
)

// waitForResult is the non-streaming path: it waits for the supervised run to
// reach a terminal state and reads the stored task back.
//
// It consumes nothing and drives nothing. If the client disconnects, this
// returns and the run carries on — the task still reaches a terminal state,
// because the supervisor owns it.
func (s *Server) waitForResult(ctx context.Context, task *domain.Task, run *service.Run) (*domain.Task, error) {
	if err := run.Wait(ctx); err != nil {
		return nil, err
	}
	return s.tasks.GetTask(context.WithoutCancel(ctx), task.ID)
}

// streamSSE subscribes to the run's event log and writes it as Server-Sent
// Events. Disconnecting merely unsubscribes.
func (s *Server) streamSSE(w http.ResponseWriter, r *http.Request, run *service.Run) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "server_error", "streaming_unsupported",
			"response writer does not support flushing")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Streaming §1: servers behind a proxy MUST disable response buffering.
	// This is the single most common UHP deployment error and it is
	// indistinguishable from a slow harness.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	err := run.Events(r.Context(), func(ev domain.Event) error {
		if err := writeSSE(w, ev); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		s.log.Error("stream failed", "error", err)
	}
}

// writeSSE emits one event. Every event goes through the same shape, so the
// stream has one schema rather than a bare task for the first event and a
// wrapper for the rest.
func writeSSE(w http.ResponseWriter, ev domain.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
	return err
}
