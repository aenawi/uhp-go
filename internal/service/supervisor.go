package service

import (
	"context"
	"sync"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
)

// Run is a supervised task: one goroutine owns the task's lifetime and is the
// only writer of its state.
//
// This is the central design decision of the router. Previously the HTTP
// handler goroutine consumed the adapter's update channel and persisted state,
// which coupled how long a task lives to how long a client stays connected.
// Three separate defects came from that one coupling:
//
//   - a client disconnect left the task "in_progress" forever, because the
//     only thing that would have written a terminal status had returned;
//   - cancel and the update stream both did read-modify-write on the same task,
//     so whichever wrote last silently discarded the other's change;
//   - streaming and non-streaming were two implementations of "consume the
//     run", and disagreed with each other about sequence numbering.
//
// The specification requires the behaviour this design produces:
// "A dropped connection MUST NOT abort the task. The work continues
// server-side." (Streaming §5)
//
// So: the request observes the run, it does not drive it. Subscribers read a
// retained event log and may come and go freely; none of them can affect
// whether the task finishes.
type Run struct {
	TaskID    string
	SessionID string

	mu     sync.Mutex
	events []domain.Event
	// notify is closed and replaced on every publish, which wakes every
	// waiting subscriber without the supervisor ever blocking on one of them.
	notify   chan struct{}
	finished bool

	// cancel stops the underlying harness run.
	//
	// It calls the adapter's own Cancel rather than cancelling the context
	// handed to Run, and that distinction is load-bearing. The shared runner
	// guards its sends on the *caller's* context precisely so that an explicit
	// cancel can still deliver the terminal "cancelled" update to a consumer
	// that is still listening. Cancelling the caller's context instead makes
	// that guard fire, the terminal update is dropped, the channel closes with
	// nothing terminal on it, and the task is recorded as "failed" — which is
	// exactly the outcome UHP forbids for a cancelled task.
	cancel func()
	done   chan struct{}
}

func newRun(taskID, sessionID string, cancel func()) *Run {
	return &Run{
		TaskID:    taskID,
		SessionID: sessionID,
		notify:    make(chan struct{}),
		cancel:    cancel,
		done:      make(chan struct{}),
	}
}

// publish appends an event to the retained log and wakes every subscriber.
// It never blocks on a subscriber: a slow or vanished client cannot stall the
// task, which is the whole point of separating the two.
func (r *Run) publish(ev domain.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	close(r.notify)
	r.notify = make(chan struct{})
	r.mu.Unlock()
}

// finish marks the run terminal and wakes everyone for the last time.
func (r *Run) finish() {
	r.mu.Lock()
	r.finished = true
	close(r.notify)
	r.notify = make(chan struct{})
	r.mu.Unlock()
	close(r.done)
}

// Events calls fn for every event of this run, starting from the first one,
// until the run is terminal or ctx is cancelled.
//
// Because the log is retained and replayed from index zero, a subscriber that
// attaches late still sees the whole stream, and the sequence numbers it sees
// are the same ones every other subscriber sees. Streaming and non-streaming
// therefore cannot disagree: they read the same log.
func (r *Run) Events(ctx context.Context, fn func(domain.Event) error) error {
	i := 0
	for {
		r.mu.Lock()
		for i < len(r.events) {
			ev := r.events[i]
			i++
			r.mu.Unlock()
			if err := fn(ev); err != nil {
				return err
			}
			r.mu.Lock()
		}
		if r.finished {
			r.mu.Unlock()
			return nil
		}
		wait := r.notify
		r.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Wait blocks until the run reaches a terminal state, or ctx is cancelled.
// Cancelling ctx abandons the wait; it does not abandon the run.
func (r *Run) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// supervisor tracks live runs so cancellation can reach them and so a session
// can refuse to run two tasks at once.
type supervisor struct {
	mu     sync.Mutex
	byTask map[string]*Run
	// bySession enforces Lifecycle §5: "A server MUST NOT run two tasks
	// concurrently in the same session."
	bySession map[string]*Run
}

func newSupervisor() *supervisor {
	return &supervisor{
		byTask:    make(map[string]*Run),
		bySession: make(map[string]*Run),
	}
}

func (s *supervisor) add(r *Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byTask[r.TaskID] = r
	if r.SessionID != "" {
		s.bySession[r.SessionID] = r
	}
}

func (s *supervisor) remove(r *Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byTask, r.TaskID)
	if cur, ok := s.bySession[r.SessionID]; ok && cur == r {
		delete(s.bySession, r.SessionID)
	}
}

func (s *supervisor) get(taskID string) (*Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byTask[taskID]
	return r, ok
}

// bySessionRun returns the live run for a session, if any.
func (s *supervisor) bySessionRun(sessionID string) (*Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.bySession[sessionID]
	return r, ok
}

func (s *supervisor) sessionBusy(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.bySession[sessionID]
	return ok
}

// supervise consumes the adapter's updates and is the only writer of this
// task's state. It runs on a context detached from the request, so the work
// continues whether or not anyone is still listening.
func (s *TaskService) supervise(ctx context.Context, run *Run, task *domain.Task, updates <-chan harness.RunUpdate) {
	defer func() {
		s.runs.remove(run)
		run.finish()
	}()

	seq := newSequencer()
	run.publish(seq.next(domain.Event{Type: "response.created", Response: cloneTask(task)}))

	for upd := range updates {
		evs, err := s.applyUpdate(ctx, task, upd, seq)
		if err != nil {
			s.log.Error("apply update failed", "error", err, "task_id", task.ID)
			continue
		}
		for _, ev := range evs {
			run.publish(ev)
		}
		if upd.Type.Terminal() {
			// Keep draining so the adapter's goroutine is never left blocked
			// on a send, but the task's status is now settled.
			for range updates {
			}
			return
		}
	}

	// The channel closed without a terminal update. That is an adapter bug,
	// but the task must still reach a terminal state: the specification
	// requires every task to end terminal, and a task stuck "in_progress"
	// forever is the exact defect this design exists to prevent.
	if !isTerminalStatus(task.Status) {
		task.Status = domain.StatusFailed
		task.Error = &domain.TaskError{
			Type:    "harness_error",
			Code:    "harness_error",
			Message: "the harness closed its update stream without reporting a terminal state",
		}
		task.UpdatedAt = time.Now().UTC()
		evs, err := s.terminal(ctx, task, seq, "response.failed")
		if err != nil {
			s.log.Error("persist terminal state failed", "error", err, "task_id", task.ID)
		}
		for _, ev := range evs {
			run.publish(ev)
		}
	}
}

func isTerminalStatus(st domain.TaskStatus) bool {
	switch st {
	case domain.StatusCompleted, domain.StatusFailed, domain.StatusCancelled, domain.StatusIncomplete:
		return true
	}
	return false
}

// cloneTask copies a task for embedding in an event, so that a later mutation
// by the supervisor cannot retroactively change an event a subscriber has
// already been handed.
func cloneItem(it *domain.OutputItem) *domain.OutputItem {
	cp := *it
	cp.Content = append([]domain.ContentPart(nil), it.Content...)
	return &cp
}

func cloneTask(t *domain.Task) *domain.Task {
	cp := *t
	cp.Output = append([]domain.OutputItem(nil), t.Output...)
	for i := range cp.Output {
		cp.Output[i].Content = append([]domain.ContentPart(nil), t.Output[i].Content...)
	}
	if t.Metadata != nil {
		cp.Metadata = make(map[string]any, len(t.Metadata))
		for k, v := range t.Metadata {
			cp.Metadata[k] = v
		}
	}
	if t.Artifacts != nil {
		cp.Artifacts = append([]domain.Artifact(nil), t.Artifacts...)
	}
	if t.Error != nil {
		e := *t.Error
		cp.Error = &e
	}
	return &cp
}
