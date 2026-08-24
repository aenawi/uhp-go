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

	// log is this task's own stream, retained in full: it dies with the task,
	// so there is nothing to bound.
	log *eventLog

	// feed is the harness-wide stream this run also publishes to, so a client
	// that never held this request can still follow the work. It is not
	// optional — every run belongs to a harness, and a run that could be
	// created without one would be a run whose events silently went nowhere.
	feed *Feed

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

	// release gives back the run slot this run holds. The run owns the slot
	// for exactly as long as it owns the harness process.
	release func()
}

func newRun(taskID, sessionID string, feed *Feed, cancel, release func()) *Run {
	return &Run{
		TaskID:    taskID,
		SessionID: sessionID,
		log:       newEventLog(0),
		feed:      feed,
		cancel:    cancel,
		done:      make(chan struct{}),
		release:   release,
	}
}

// publish appends an event to this task's retained log and to its harness's
// feed, and wakes the subscribers of both. It never blocks on a subscriber: a
// slow or vanished client cannot stall the task, which is the whole point of
// separating the two.
//
// Both writes happen here, in the one place an event enters the world, so
// there is no path that reaches a task's own stream without also reaching the
// harness feed — the two cannot disagree about what happened.
func (r *Run) publish(ev domain.Event) {
	r.log.append(ev)
	r.feed.publish(ev, r.TaskID, r.SessionID)
}

// finish gives back the run's slot, marks it terminal, and wakes everyone for
// the last time.
func (r *Run) finish() {
	// The slot goes back first, ahead of both ways a caller learns the run is
	// over — the finished flag Events reads and the done channel Wait reads.
	// A client that receives its answer and immediately asks the next question
	// must not be refused for capacity this very run has already stopped using,
	// and releasing afterwards makes that a race the client usually loses.
	r.release()

	r.log.close()
	close(r.done)
}

// Events calls fn for every event of this run numbered `from` or later, until
// the run is terminal or ctx is cancelled. Zero is the whole stream.
//
// Because the log is retained in full, a subscriber that attaches late still
// sees everything, and the sequence numbers it sees are the same ones every
// other subscriber sees. Streaming and non-streaming therefore cannot
// disagree: they read the same log. A reconnecting client passes the number it
// last saw plus one and is handed the rest, with nothing replayed.
func (r *Run) Events(ctx context.Context, from int, idle IdleTick, fn func(domain.Event) error) error {
	return r.log.subscribe(ctx, from, idle, fn)
}

// Oldest is the oldest sequence number this run can still replay, which is
// always zero: a task's log is retained whole for as long as the task is.
func (r *Run) Oldest() int { return r.log.retained() }

// Head is one past the newest sequence number this run has published. A
// resumption from anything later names an event the client cannot have seen.
func (r *Run) Head() int { return r.log.head() }

// terminated reports whether the run is over, without blocking. It answers the
// question Wait answers, for a caller that cannot afford to wait for it.
func (r *Run) terminated() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
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

// supervisor tracks live runs so cancellation can reach them, so a session can
// refuse to run two tasks at once, and so the machine is not asked to run more
// harness processes than it was configured for.
type supervisor struct {
	mu     sync.Mutex
	byTask map[string]*Run
	// bySession enforces Lifecycle §5: "A server MUST NOT run two tasks
	// concurrently in the same session."
	bySession map[string]*Run

	// live counts the run slots currently held and maxLive bounds them.
	//
	// This is a bound on processes, not on requests, which is why it lives here
	// rather than in the transport: the per-session rule above already refuses a
	// second task in one conversation, but nothing stopped an unbounded number
	// of *different* sessions, and every one of them forks a CLI. maxLive is
	// fixed at construction, so reading it needs no lock.
	live    int
	maxLive int

	// feeds is one live event stream per harness, and unlike the two maps above
	// its entries outlive the runs that write to them: a client following a
	// harness is waiting precisely when nothing is running on it.
	//
	// A deleted harness leaves an entry behind — see closeFeed for why — but
	// the entry it leaves holds no events, so what accumulates is one small
	// closed feed per harness ever deleted rather than the window each one was
	// retaining.
	feeds map[string]*Feed
}

func newSupervisor(maxLive int) *supervisor {
	if maxLive <= 0 {
		maxLive = DefaultMaxConcurrentRuns
	}
	return &supervisor{
		byTask:    make(map[string]*Run),
		bySession: make(map[string]*Run),
		feeds:     make(map[string]*Feed),
		maxLive:   maxLive,
	}
}

// feed returns a harness's live event stream, minting it if this is the first
// anyone has asked.
//
// It is created on demand rather than alongside the harness because a harness
// can also arrive from a file on disk at startup or from another process
// writing the harness store, and a feed that only existed for harnesses this
// process created would be missing for exactly those.
func (s *supervisor) feed(harnessID string) *Feed {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.feeds[harnessID]
	if !ok {
		f = newFeed(feedRetention)
		s.feeds[harnessID] = f
	}
	return f
}

// closeFeed ends every subscription to a harness's feed. A subscriber left
// waiting on a harness that no longer exists would wait forever: no event is
// coming, and nothing else would ever tell it why.
//
// A closed feed stays in the map rather than the entry being deleted.
// Deleting it would leave a window in which a subscription that had already
// checked the harness exists mints a fresh, open feed for one that has just
// been deleted — and hangs on it. Leaving a closed one makes that race end the
// subscription instead, which is the answer it was going to get a moment
// earlier anyway. Nothing reuses the entry, because a `chrn_` id is random per
// create and the compiled-in harnesses cannot be deleted at all.
//
// What stays is a *new* empty feed, not the one being closed. The retained
// window is the expensive part — every `response.created` in it pins a whole
// task — and a marker that says "this harness is gone" needs to remember
// nothing at all. The real feed is closed too, for the subscribers already on
// it.
func (s *supervisor) closeFeed(harnessID string) {
	s.mu.Lock()
	previous := s.feeds[harnessID]
	tombstone := newFeed(feedRetention)
	s.feeds[harnessID] = tombstone
	s.mu.Unlock()

	tombstone.close()
	if previous != nil {
		previous.close()
	}
}

// acquire reserves a run slot, reporting whether there was one to reserve.
//
// The returned release gives the slot back. It is idempotent because the two
// callers that give a slot back — StartTask on a path that fails before the
// supervisor takes ownership, and the supervisor when the run ends — are
// exclusive by construction rather than by anything a reader can check locally,
// and a double release would silently raise the bound for the life of the
// process.
func (s *supervisor) acquire() (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live >= s.maxLive {
		return nil, false
	}
	s.live++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.live--
			s.mu.Unlock()
		})
	}, true
}

// capacity is the configured maximum number of concurrent harness runs.
func (s *supervisor) capacity() int { return s.maxLive }

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
func (s *TaskService) supervise(ctx context.Context, run *Run, task *domain.Task, updates <-chan harness.RunUpdate, rs *runState) {
	defer func() {
		s.runs.remove(run)
		run.finish()
	}()

	seq := newSequencer()
	run.publish(seq.next(domain.Event{Type: "response.created", Response: cloneTask(task)}))

	for upd := range updates {
		evs, err := s.applyUpdate(ctx, task, upd, seq, rs)
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
			Type:    domain.ErrorTypeHarness,
			Code:    "harness_error",
			Message: "the harness closed its update stream without reporting a terminal state",
		}
		task.UpdatedAt = time.Now().UTC()
		evs, err := s.terminal(ctx, task, seq, "response.failed", rs)
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
