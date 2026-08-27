package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
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
func (s *TaskService) SessionTurns(ctx context.Context, id string) ([]uhp.Turn, error) {
	tasks, err := s.sessionTasks(ctx, id)
	if err != nil {
		return nil, err
	}
	turns := make([]uhp.Turn, 0, len(tasks))
	for _, t := range tasks {
		turns = append(turns, uhp.Turn{
			ResponseID: t.ID,
			Status:     t.Status,
			Model:      t.Model,
			Input:      t.Input,
			Output:     t.Text(),
			CreatedAt:  t.CreatedAt,
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
	if err := s.requireHarnessCapability(ctx, sess.HarnessID, uhpgo.CapCancellation, whyNoCancellation); err != nil {
		return err
	}
	run.requestCancel()
	return nil
}

// sessionReapTimeout bounds how long the reaper waits for a cancelled run
// before removing its working directory anyway.
//
// A harness that ignores cancellation, or one whose wind-down wedges, would
// otherwise keep a deleted session's files on disk for the life of the process
// — which is the one outcome deletion exists to prevent. Two minutes is
// generous against Sessions §4's one-second budget for *acknowledging* a
// cancel, and short enough that an operator auditing the workspace after a
// delete is not looking at yesterday's files.
const sessionReapTimeout = 2 * time.Minute

// DeleteSession answers DELETE /v1/traces/{id} (Sessions §6): it cancels
// whatever is running in the session, then disposes of the session, its tasks
// and its working directory.
//
// The asymmetry with [TaskService.DeleteTask] is deliberate and is the thing
// most easily got backwards. Deleting one response MUST NOT cancel — that is
// history cleanup, and Tasks §4 forbids conflating it with stopping work.
// Deleting the trace is disposing of the whole conversation, and Sessions §6
// couples cancellation to it "due to ownership concerns": there is no owner
// left to report the run to, and its output has nowhere to be written.
//
// The cancel is best-effort and is not gated on the harness advertising
// CapCancellation, unlike [TaskService.CancelSession]. Refusing here would mean
// a session on a harness that cannot be stopped could never be deleted at all,
// and the client would be told its disposal failed because of a promise it
// never asked for. What it asked for is that this server stop holding the
// conversation, and that is not the harness's to refuse.
//
// Deletion is not synchronous with the harness, and the ordering below is where
// that decision lives. Cancellation is asynchronous by design, so a delete that
// waited for the run would inherit an unbounded wait for one request; instead
// the rows go now — which is the whole of "unreadable after deletion" — and the
// directory is reaped once the run is actually dead.
func (s *TaskService) DeleteSession(ctx context.Context, id string) error {
	if _, err := s.GetSession(ctx, id); err != nil {
		return err
	}

	if run, running := s.runs.bySessionRun(id); running {
		run.requestCancel()
		// The rows first, then the directory once the run lets go of it. A
		// RemoveAll racing a harness that is still writing removes some of the
		// files and recreates none of them, so the reaper waits.
		if err := s.deleteSessionRows(ctx, id); err != nil {
			return err
		}
		s.reapWhenRunEnds(run, id)
		return nil
	}

	// Nothing is running, so the directory goes first.
	//
	// Both orderings have a failure mode and neither is pleasant: this way a
	// failed row delete leaves a readable session whose artifacts' bytes are
	// gone, and the other way a failed removal leaves an unreadable session
	// whose files are still on disk. The first is chosen because it is the one
	// a retry can finish — RemoveAll on an absent path succeeds, so the client's
	// second DELETE reaches the correct end state. The other way round, the
	// retry answers 404 for a session whose files are still there: the client is
	// told it succeeded, twice, having deleted nothing, and no one is left
	// holding an id to try again with.
	if err := s.workspace.removeSession(id); err != nil {
		return fmt.Errorf("%w: remove workspace for session %q: %w", ErrStorage, id, err)
	}
	if err := s.deleteSessionRows(ctx, id); err != nil {
		return err
	}

	// And once more, because a task can be accepted into this session between
	// the check above and the rows going. StartTask resolves a continuation by
	// reading the *task* it names, so nothing on that path re-reads the session
	// this one has just removed, and the run it starts would own a working
	// directory belonging to a conversation nobody can read any more — files on
	// disk with no record left to reach them by, which is the exact outcome
	// deleting a trace exists to prevent. The window is small and the check is
	// two map lookups.
	if run, running := s.runs.bySessionRun(id); running {
		run.requestCancel()
		s.reapWhenRunEnds(run, id)
	}
	return nil
}

// deleteSessionRows removes the session and its tasks, classifying the two ways
// that fails.
//
// The store's `found` flag is not redundant with the GetSession above it. Two
// deletes of the same id can be in flight at once, and exactly one of them
// removed the row; the other has deleted nothing and must be told so, for the
// same reason a sequential second DELETE is a 404 rather than a second
// `deleted: true`.
func (s *TaskService) deleteSessionRows(ctx context.Context, id string) error {
	found, err := s.store.DeleteSession(ctx, id)
	if err != nil {
		return fmt.Errorf("%w: delete session %q: %w", ErrStorage, id, err)
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	return nil
}

// reapWhenRunEnds removes a deleted session's working directory once its run is
// over.
//
// It runs on its own goroutine, on a context detached from the request, for the
// reason the supervisor's own goroutine is: the request that asked for the
// deletion is gone, and the work it is waiting on is not the request's to
// outlive. Removal happens whether or not the wait succeeded — a run that
// ignored its cancel does not get to keep the files — and the two cases are
// logged differently so an operator can tell a wedged harness from an ordinary
// one.
//
// What this does not survive is the process dying in between. The rows are
// already gone, so nothing on the next start knows the directory is orphaned,
// and it stays until an operator removes it. A sweep at startup would close
// that, and is deliberately not attempted here: with the in-memory store a
// restart holds no sessions at all, so "every directory with no session row" is
// every directory there is, and the sweep would delete the workspace rather than
// tidy it. The exposure is one narrow case — deleted while a task was in flight,
// and the process died before that task did — and naming it is better than a
// sweep that has to be right about which store it is running against.
func (s *TaskService) reapWhenRunEnds(run *Run, sessionID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sessionReapTimeout)
		defer cancel()
		if err := run.Wait(ctx); err != nil {
			s.log.Warn("removing a deleted session's workspace while its run is still going",
				"session_id", sessionID, "after", sessionReapTimeout)
		}
		if err := s.workspace.removeSession(sessionID); err != nil {
			// Nobody is left to answer: the client was told the session was
			// deleted, and it was. What is left is files on an operator's disk
			// that should not be there, which is exactly what a log is for.
			s.log.Error("remove a deleted session's workspace", "error", err, "session_id", sessionID)
		}
	}()
}

// markSessionStatus records a session's status as its latest task settles, so a
// listing can show what happened without reading every task.
func (s *TaskService) markSessionStatus(ctx context.Context, sessionID string, status uhp.ResponseStatus, responseID string) {
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
	// The schema types a session's status as an unconstrained string, so the
	// conversion is explicit here rather than hidden in the field's type: this
	// server happens to reuse the response vocabulary, and a different
	// conformant server need not.
	sess.Status = string(status)
	sess.LastResponseID = responseID
	sess.UpdatedAt = time.Now().UTC().Unix()
	if err := s.store.UpdateSession(ctx, sess); err != nil {
		s.log.Error("persist session status", "error", err, "session_id", sessionID)
	}
}
