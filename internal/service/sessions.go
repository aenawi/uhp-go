package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
)

// maxTitleRunes bounds a generated session title.
const maxTitleRunes = 60

// titleFor derives a human-readable session title from the first task's input.
// The title is a label for a list, not data, so it is truncated on a rune
// boundary and collapsed onto one line.
func titleFor(input string) string {
	t := strings.Join(strings.Fields(input), " ")
	r := []rune(t)
	if len(r) > maxTitleRunes {
		return strings.TrimSpace(string(r[:maxTitleRunes])) + "…"
	}
	return t
}

// ListSessions answers GET /v1/sessions.
func (s *TaskService) ListSessions(ctx context.Context, f domain.SessionFilter) (domain.SessionPage, error) {
	if f.HarnessID != "" {
		// Accept a base-name alias here too, so filtering works with whichever
		// form the caller has.
		if canonical, ok := s.registry.Resolve(f.HarnessID); ok {
			f.HarnessID = canonical
		}
	}
	return s.store.ListSessions(ctx, f)
}

// GetSession answers GET /v1/sessions/{id}.
func (s *TaskService) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	return sess, nil
}

// SessionTurns answers GET /v1/sessions/{id}/turns: the ordered task history of
// a session, so a client can rebuild a transcript it did not store.
func (s *TaskService) SessionTurns(ctx context.Context, id string) ([]domain.Turn, error) {
	if _, err := s.store.GetSession(ctx, id); err != nil {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	tasks, err := s.store.ListSessionTasks(ctx, id)
	if err != nil {
		return nil, err
	}
	turns := make([]domain.Turn, 0, len(tasks))
	for _, t := range tasks {
		turns = append(turns, domain.Turn{
			ResponseID: t.ID,
			Status:     t.Status,
			Model:      t.Model,
			Input:      t.Input,
			Output:     t.Text(),
			CreatedAt:  t.CreatedAt.Unix(),
		})
	}
	return turns, nil
}

// CancelSession stops whatever is running in a session.
//
// Sessions §4 defines two deliberately distinct scopes — cancelling a task and
// cancelling a session — and is explicit that cancelling MUST NOT delete the
// session: the conversation remains continuable.
func (s *TaskService) CancelSession(ctx context.Context, id string) error {
	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	run, ok := s.runs.bySessionRun(id)
	if !ok {
		// Nothing running. Cancelling an idle session succeeds and changes
		// nothing, for the same reason cancelling a terminal task does — and
		// for the same reason it is not checked against the harness's
		// capabilities: there is no work being promised a stop.
		return nil
	}
	if err := s.requireHarnessCapability(ctx, sess.HarnessID, domain.CapCancellation, whyNoCancellation); err != nil {
		return err
	}
	run.cancel()
	return nil
}

// markSessionStatus records a session's status as its latest task settles, so a
// listing can show what happened without reading every task.
func (s *TaskService) markSessionStatus(ctx context.Context, sessionID string, status domain.TaskStatus, responseID string) {
	if sessionID == "" {
		return
	}
	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return
	}
	sess.Status = status
	sess.LastResponseID = responseID
	sess.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSession(ctx, sess); err != nil {
		s.log.Error("persist session status", "error", err, "session_id", sessionID)
	}
}
