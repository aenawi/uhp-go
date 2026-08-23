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
	page, err := s.store.ListSessions(ctx, f)
	if err != nil {
		// Wrapped rather than passed through: an unclassified error reaches the
		// transport's default arm and becomes 502 harness_unavailable, which
		// blames a harness for a disk. A listing has no row to be missing, so
		// there is nothing here but the server's own failure.
		return domain.SessionPage{}, fmt.Errorf("%w: list sessions: %w", ErrStorage, err)
	}
	return page, nil
}

// GetSession answers GET /v1/sessions/{id}. A store that could not be read is
// ErrStorage; only an absent row is ErrSessionNotFound. See GetTask.
func (s *TaskService) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	sess, found, err := s.store.GetSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: read session %q: %w", ErrStorage, id, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	return sess, nil
}

// SessionTurns answers GET /v1/sessions/{id}/turns: the ordered task history of
// a session, so a client can rebuild a transcript it did not store.
func (s *TaskService) SessionTurns(ctx context.Context, id string) ([]domain.Turn, error) {
	tasks, err := s.sessionTasks(ctx, id)
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

// sessionTasks reads a session's tasks, having first established that the
// session exists at all.
//
// The two steps are one operation: without the existence check, a session that
// is not there and a session with no tasks yet both come back as an empty list,
// and a client asking for the history of an id it typed wrong is told the
// conversation is simply empty. Every caller wants both halves, and each error
// on the way is already classified — the miss as ErrSessionNotFound by
// GetSession, the read as ErrStorage here — so no caller has to re-decide what
// a failure means.
func (s *TaskService) sessionTasks(ctx context.Context, id string) ([]*domain.Task, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return nil, err
	}
	tasks, err := s.store.ListSessionTasks(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: list tasks for session %q: %w", ErrStorage, id, err)
	}
	return tasks, nil
}

// CancelSession stops whatever is running in a session.
//
// Sessions §4 defines two deliberately distinct scopes — cancelling a task and
// cancelling a session — and is explicit that cancelling MUST NOT delete the
// session: the conversation remains continuable.
func (s *TaskService) CancelSession(ctx context.Context, id string) error {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return err
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
	// Best-effort bookkeeping, so both a failed read and a session that is not
	// there end the same way: nothing to record onto. The distinction the read
	// endpoints need does not exist here, because nobody is being answered.
	sess, found, err := s.store.GetSession(ctx, sessionID)
	if err != nil || !found {
		return
	}
	sess.Status = status
	sess.LastResponseID = responseID
	sess.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSession(ctx, sess); err != nil {
		s.log.Error("persist session status", "error", err, "session_id", sessionID)
	}
}
