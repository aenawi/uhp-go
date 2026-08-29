package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Run is a supervised task: one goroutine owns the task's lifetime and is the
// only writer of its state, cancelAsked below excepted — that one records
// something only a request goroutine can know.
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

	// cancelAsked records that somebody asked for this run to stop — a client
	// cancelling the task or its session, or the session being deleted out
	// from under it — as opposed to the budget below stopping it on the
	// server's own initiative.
	//
	// It is the one piece of a run's state the supervisor goroutine is not the
	// only writer of, because the asking happens on a request goroutine, so it
	// is atomic. What it buys is that a stop somebody asked for stays a fact
	// for the rest of the run: the teardown a budget starts is seconds long,
	// and without this a cancel arriving inside it was reported as the budget's
	// doing (#76).
	cancelAsked atomic.Bool

	// release gives back the run slot this run holds. The run owns the slot
	// for exactly as long as it owns the harness process.
	release func()

	// budget is the wall clock this run has, resolved once at creation from
	// the request, the harness and the deployment. It is always positive:
	// Security §5 requires a task to be bounded, so "no budget" is not a state
	// a run can be in. See resolveBudget, and supervise for what enforces it.
	budget time.Duration

	// maxStep is the step ceiling this run has, resolved once at creation from
	// the request, the harness and the deployment — or nil for unbounded, which
	// is the ordinary case and is not the wall clock's situation at all (#72).
	//
	// Zero is a real ceiling and means no tool call is permitted, so it cannot
	// stand in for "no ceiling". See resolveStepBudget.
	maxStep *int

	// stepEdge is which end of a tool call this run's base narrates, and so
	// whether maxStep is reached when a call is asked for or when one finishes.
	// StepEdgeNative is the base that enforces its own and is not counted here.
	stepEdge harness.StepEdge

	// result is the terminal task of a run whose response was not retained,
	// and nil for every run whose response was.
	//
	// A `store: false` response is deleted from the store the moment it is
	// terminal, and the client still has to be handed it exactly once — in the
	// POST body it is waiting on, or in the reply to an idempotent retry. This
	// is where that copy lives, and the retention window it lives for is the
	// run's own: it dies with the Run, as the event log beside it does.
	//
	// Written by the supervisor goroutine before finish() closes done, and read
	// only after a Wait on done has returned. That ordering is the whole of the
	// synchronisation, and it is the same one firstStart uses: closing a channel
	// happens-before a receive from it, so a reader cannot see a half-written
	// answer and no lock is held while a run is in flight.
	result *domain.Task
}

func newRun(
	taskID, sessionID string,
	feed *Feed,
	budget time.Duration,
	maxStep *int,
	stepEdge harness.StepEdge,
	cancel, release func(),
) *Run {
	return &Run{
		TaskID:    taskID,
		SessionID: sessionID,
		log:       newEventLog(0),
		feed:      feed,
		cancel:    cancel,
		done:      make(chan struct{}),
		release:   release,
		budget:    budget,
		maxStep:   maxStep,
		stepEdge:  stepEdge,
	}
}

// stepBudgetSpent reports whether a run that has narrated `steps` tool calls has
// used up its ceiling, and so must be stopped.
//
// **One comparison for every counted base**: the ceiling is spent by the event
// *after* the last one allowed, and the run is stopped then.
//
// What that stop can and cannot do is worth being exact about, because the
// obvious reading is too strong. This server reads a CLI's stdout and kills its
// process group; it has no way to make the CLI *wait*. By the time a `tool_use`
// line has been read, parsed and counted, the agent has already dispatched that
// tool. So `max_step` bounds how far a run proceeds — it does not guarantee a
// number of tool calls, and no arrangement of this loop could:
//
//   - On a **start** edge the tripping event is a request, so the stop is issued
//     at the earliest moment anything downstream could know about the call. The
//     call may still run; what it will not do is lead to another.
//   - On a **finish** edge it is a completion, so that call has certainly run.
//     `opencode` therefore overshoots by at least one.
//   - A base that narrates several calls on one line overshoots by up to a
//     batch. claude puts every tool of a parallel batch in a single `assistant`
//     message, so `max_step: 1` against a three-call batch counts one, two,
//     trips — and all three were dispatched before the first was counted.
//
// Overshooting is the tolerable direction: a run stops early rather than never,
// which is the failure a step budget exists to prevent. It is documented per
// base in the README rather than implied away here.
//
// The alternative for the finish edge was `steps >= max`, stopping on the
// ceiling'th call's own completion. It is wrong in the way that matters most: it
// kills a run that *complied*. An agent given five calls that uses exactly five
// is torn down at the moment the fifth finishes, before it can write its answer,
// and the client gets `incomplete` with nothing in it — while the identical
// request on claude completes. A bound that breaks the runs obeying it is worse
// than one that overshoots.
//
// So the edge does not change the comparison. What it changes is which of the
// two bases cannot honour `max_step: 0` at all — where a single overshot call is
// the whole of the budget — and requireStepBudget refuses those rather than
// letting one through as a matter of course.
func stepBudgetSpent(steps int, max int, edge harness.StepEdge) bool {
	switch edge {
	case harness.StepEdgeStart, harness.StepEdgeFinish:
		return steps > max
	default:
		// StepEdgeNative bounds itself and StepEdgeNone cannot be bounded at
		// all. Neither is counted here, and a base reaching this with a ceiling
		// set is refused at task creation rather than run unbounded — see
		// requireStepBudget.
		return false
	}
}

// requestCancel stops the run and remembers that the stop was asked for.
//
// Every caller acting on somebody's behalf goes through here rather than
// calling cancel directly; the budget in supervise is the one caller that does
// not, because it is this server stopping the run on its own initiative and
// the difference between those two is exactly what the flag records.
func (r *Run) requestCancel() {
	// Set before the stop, not after: cancel can deliver the adapter's
	// terminal update before this goroutine runs again, and a flag written
	// afterwards would be read by the supervisor a moment too late.
	r.cancelAsked.Store(true)
	r.cancel()
}

// publish appends an event to this task's retained log and to its harness's
// feed, and wakes the subscribers of both. It never blocks on a subscriber: a
// slow or vanished client cannot stall the task, which is the whole point of
// separating the two.
//
// Both writes happen here, in the one place an event enters the world, so
// there is no path that reaches a task's own stream without also reaching the
// harness feed — the two cannot disagree about what happened.
func (r *Run) publish(ev uhpgo.Event) {
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
func (r *Run) Events(ctx context.Context, from int, idle IdleTick, fn func(uhpgo.Event) error) error {
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

// Result is the terminal task of a run whose response was not retained, or nil
// when it was — in which case the store holds the answer and is the place to
// read it from.
//
// Only safe to call after Wait has returned; before that it races the
// supervisor. Every caller does, because a non-terminal task is not the answer
// anyone is waiting for.
func (r *Run) Result() *domain.Task { return r.result }

// Settled is Result for a caller that cannot afford to wait: it reports whether
// the run is over, and only then hands back what Result would.
//
// The pairing is the point. Result is safe only after Wait has returned, and
// the one caller that must not block — a `background: true` POST, which exists
// precisely so that nothing waits for the run — would otherwise have to
// reproduce that check itself from outside this package, where `terminated` is
// not visible.
func (r *Run) Settled() (*domain.Task, bool) {
	if !r.terminated() {
		return nil, false
	}
	return r.result, true
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
//
// The empty id is nobody's session and is answered before the map is consulted.
// Nothing reaches here with one today — every task has a session by the time it
// is checked — and the guard is what keeps a future caller that does from
// matching the zero key and being told a stranger's run is its own.
func (s *supervisor) bySessionRun(sessionID string) (*Run, bool) {
	if sessionID == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.bySession[sessionID]
	return r, ok
}

// supervise consumes the adapter's updates and is the only writer of this
// task's state. It runs on a context detached from the request, so the work
// continues whether or not anyone is still listening.
func (s *TaskService) supervise(ctx context.Context, run *Run, task *domain.Task, updates <-chan harness.RunUpdate, rs *runState) {
	defer func() {
		s.runs.remove(run)
		// Persist terminal, capture, delete — in that order, and the order is
		// load-bearing rather than incidental. The loop below has already
		// written the terminal state; this takes the copy the waiting client
		// will be handed, and only then does the row go. Deleting first would
		// answer a POST that is still holding its connection open with a 404
		// for the task it just created.
		s.dropUnstored(ctx, run, task)
		run.finish()
	}()

	seq := newSequencer()
	run.publish(seq.next(uhp.Event{Type: "response.created", Response: responseOf(task)}))

	// The wall-clock budget, armed for as long as the run is (#54). Security §5
	// requires a server to bound task duration, and nothing here did: a wedged
	// CLI held its run slot for ever, and enough of them took the server
	// permanently to capacity.
	//
	// It fires into this goroutine rather than into a timer callback because
	// this goroutine is the task's only writer, which is what makes `expired`
	// below need no synchronisation at all.
	timer := time.NewTimer(run.budget)
	defer timer.Stop()
	budget := timer.C

	// budgetStop is which of this run's budgets stopped it, and "" while none
	// has. It replaces the bare `expired` flag now that there are two of them
	// (#72), and holding the *reason* rather than a boolean per budget is what
	// makes "first to fire wins" a property of the code rather than a claim
	// about it: both arms below write it only when it is empty, so the second
	// budget to fire finds the answer already given and changes nothing.
	//
	// No tie-break is needed beyond that. This goroutine is the task's only
	// writer and runs one `select`, so one of the two genuinely arrives first.
	// A cancel still outranks both, and that ranking lives in stoppedBy.
	budgetStop := ""

	// The step count, kept here for the same reason the wall clock is armed
	// here: this goroutine is the task's only writer, so it needs no
	// synchronisation at all.
	//
	// The counting is the supervisor's rather than each adapter's on purpose.
	// It already holds the resolved ceiling and already owns the
	// cancel-and-relabel path #54 built, where four adapter-local counters would
	// be four chances to disagree about what a step is — and the one base that
	// does count for itself, grok, is the one whose runtime enforces the
	// ceiling too.
	steps := 0

	for {
		var upd harness.RunUpdate
		select {
		case u, open := <-updates:
			if !open {
				stopped := stoppedBy(budgetStop, run.cancelAsked.Load())
				for _, ev := range s.settleUnreported(ctx, task, seq, rs, stopped, budgetStop) {
					run.publish(ev)
				}
				return
			}
			upd = u
		case <-budget:
			// Stopped through the adapter's own Cancel — the same path
			// CancelTask uses — so process-group teardown is not reimplemented
			// here and a runtime that needs more than a signal to stop gets
			// exactly what an explicit cancel would have given it.
			//
			// Without CancelTask's capability check, deliberately. That check
			// refuses a *client* a stop the harness never advertised; this is
			// the server keeping its own obligation under Security §5, and a
			// harness that declines to advertise cancellation cannot thereby
			// opt out of being bounded. What it can do is fail to stop — an
			// adapter whose Cancel does nothing keeps its slot — which is a
			// property of that adapter rather than something this can fix from
			// here. All five CLI harnesses stop on a process-group kill.
			//
			// The run is not settled here. The adapter still owes a terminal
			// update, and taking the answer from it is what keeps this from
			// reporting a task finished while its process is still writing into
			// the session's working directory — files that would then be
			// captured as some later task's artifacts. What bounds the wait is
			// the teardown itself: process.run kills the group and gives it
			// WaitDelay before closing the pipes regardless.
			//
			// Written only when nothing has stopped this run yet, which is what
			// gives the step budget below the same claim on being first: a wall
			// clock that expires during a step budget's teardown finds the
			// answer already given and leaves it alone.
			if budgetStop == "" {
				budgetStop = reasonTimeout
			}
			// Disarmed, so a second firing cannot re-enter this and cancel a
			// run that has since been given back.
			budget = nil
			s.log.Info("task budget expired; stopping the run",
				"task_id", task.ID, "budget", run.budget)
			run.cancel()
			continue
		}

		// One step, counted before anything else looks at this update (#72).
		//
		// The ceiling is checked on the way past rather than in applyUpdate,
		// which never sees an UpdateToolCall as anything but a no-op: a step is
		// not a change to the task's state, it is a fact about how much of a
		// budget the run has spent, and the only thing that acts on it is the
		// stop below.
		//
		// Counting stops the moment anything has stopped the run, which is what
		// keeps this from re-entering. A base can narrate several more calls
		// between the cancel going out and the process actually dying — the
		// teardown is a signal, a wait and a WaitDelay wide — and cancelling
		// again for each of them would stop a run already given back.
		if upd.Type == harness.UpdateToolCall && run.maxStep != nil && budgetStop == "" {
			steps++
			if stepBudgetSpent(steps, *run.maxStep, run.stepEdge) {
				budgetStop = harness.ReasonMaxStep
				// Stopped through the adapter's own Cancel, for the reasons the
				// wall clock is: process-group teardown is not reimplemented
				// here, and a runtime needing more than a signal gets what an
				// explicit cancel would have given it.
				//
				// The run is not settled here either. The adapter still owes a
				// terminal update, and taking the answer from it is what keeps
				// this from reporting a task finished while its process is
				// still writing into the session's working directory.
				s.log.Info("task step budget spent; stopping the run",
					"task_id", task.ID, "max_step", *run.maxStep, "steps", steps)
				run.cancel()
			}
		}

		// Once a budget has stopped a run, the terminal update the adapter
		// eventually produces is a report of the teardown *this* server caused,
		// whatever the adapter calls it — so it is relabelled rather than
		// believed.
		//
		// `cancelled` is the cooperative case, and it is not the one that
		// bites. `failed` is: process.run tests its scan error before it tests
		// its own cancellation, so a stdout read torn by the very kill the
		// budget issued comes back `failed`; and three of the five CLIs report
		// a problem by printing it, which parseLine turns into `failed` before
		// the runner's terminal switch is reached at all — so a wedged agent
		// that says anything on its way out lands here as a failure. Reporting
		// either as `failed` tells a client the work could not be done, when
		// what happened is that it ran out of time. That is the inversion
		// Lifecycle §3 forbids, on the most likely real teardown.
		//
		// `completed` is left alone, deliberately. An agent that finished
		// inside the window between the deadline firing and the kill landing
		// produced whole work, and the MUST is not to report `completed` for
		// work that was *truncated* — this work was not.
		//
		// Which stop it is relabelled as is stoppedBy's decision, and a cancel
		// outranks the budget there however long after the deadline it landed
		// (#76).
		//
		// That is not the tie-break it looks like. `budgetStop` is set when a
		// budget fires and stays set for the whole of the teardown that
		// follows — the adapter's Cancel, the signal, and the Wait that
		// process.run backstops with `cmd.WaitDelay = 5 * time.Second` — so
		// the window is seconds wide, not the scheduling quantum this comment
		// used to claim. A client calling POST /v1/responses/{id}/cancel
		// inside it was answered `incomplete` with reason `timeout`: the
		// status that tells a client the work is worth retrying, for work
		// somebody stopped on purpose, and on a wedged agent an invitation to
		// re-run the thing that wedged.
		//
		// The reason travels with the relabel rather than being assumed to be
		// the wall clock's (#72). There are two budgets now, and a step ceiling
		// reported as `reason: "timeout"` would tell a client to wait and retry
		// where what it needs to do is ask for more steps.
		if stopped := stoppedBy(budgetStop, run.cancelAsked.Load()); stopped != "" && relabelableTerminal(upd.Type) {
			if upd.Err != nil {
				// Not lost, only kept out of the response: neither `incomplete`
				// nor `cancelled` carries an error object, and an operator
				// reading the log is the one who needs to know what the CLI
				// said as it died.
				s.log.Info("harness reported a failure after its budget expired",
					"task_id", task.ID, "harness_error", upd.Err.Error())
			}
			upd.Err = nil
			if stopped == uhp.StatusCancelled {
				upd.Type = harness.UpdateCancelled
				upd.Reason = ""
			} else {
				upd.Type = harness.UpdateIncomplete
				upd.Reason = budgetStop
			}
		}

		evs, err := s.applyUpdate(ctx, task, upd, seq, rs)
		if err != nil {
			// A client may delete this task's record while the run is still
			// going — DELETE /v1/responses/{id}, which Tasks §4 requires not to
			// stop the work, or DELETE /v1/traces/{id}, which takes the whole
			// session's rows and cancels rather than orphans — and every write
			// from here on then fails against a row that is no longer there.
			// That is the client getting what it asked for, not this server
			// failing, and logging it at ERROR would fill an operator's logs
			// with alarms for a supported operation.
			if s.taskGone(ctx, task.ID) {
				s.log.Debug("this task's record was deleted mid-run; the run continues unreported",
					"task_id", task.ID)
				continue
			}
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
}

// dropUnstored honours `store: false` by removing the response now that the
// run is over, having first kept the copy this run still owes its client.
//
// Tasks §4 makes the resulting `404 response_not_found` a MAY rather than a
// MUST, which is what permits a server to retain everything instead — and is
// why this server did, echoing an accurate `store: true`, until the field was
// read at all. Reading it means acting on it.
//
// What goes is the response and only the response. The Session survives: it
// owns the working directory and the harness binding, and the harness's own
// session id is persisted on the session record rather than only here, so
// dropping the task does not cost the conversation its ability to resume. The
// run's artifacts survive too, on disk, because `store` is about response
// retention and erasing a run's files is a different thing that nobody asked
// for.
//
// What the client loses is every later read: GET on the response, its input
// items, its place in the session's turns, and its usability as a
// `previous_response_id`. That is the whole of what it asked for.
//
// A failed delete is logged and not retried. The alternative is a task that
// cannot reach a terminal state because its storage will not co-operate, and a
// response retained against the client's wishes is a smaller fault than a run
// that never finishes.
func (s *TaskService) dropUnstored(ctx context.Context, run *Run, task *domain.Task) {
	if task.Store {
		return
	}
	// A shallow copy, not the pointer: two clients can be handed this — the
	// original request and an idempotent retry — and neither should be able to
	// reach the other's object, however read-only both of them are today.
	snapshot := *task
	run.result = &snapshot
	if _, err := s.store.DeleteTask(ctx, task.ID); err != nil {
		s.log.Error("could not drop a store:false response; it stays readable",
			"error", err, "task_id", task.ID)
	}
}

// settleUnreported writes a terminal state for an adapter that closed its
// update stream without reporting one. That is an adapter bug, but the task
// must still reach a terminal state: the specification requires every task to
// end terminal, and a task stuck "in_progress" forever is the exact defect this
// design exists to prevent.
//
// What it settles as depends on whether this server had already stopped the
// run, and the three answers rank the same way the relabel above does. A
// wedged runtime that is killed on its deadline and then says nothing has not
// failed — the budget ended it, and Lifecycle §3 is explicit that a budget is
// not an error — so reporting `failed` there would tell a client the work was
// impossible when it was merely cut short. And within that, a stop somebody
// asked for outranks the budget, for the same reason it does above: silence
// from the adapter is no evidence against a cancel this server has a record
// of.
// `reason` is which budget did the stopping, and is read only when `stopped` is
// `incomplete` — the two always arrive together from the one caller, because
// stoppedBy derives the first from the second.
func (s *TaskService) settleUnreported(
	ctx context.Context, task *domain.Task, seq *sequencer, rs *runState,
	stopped uhp.ResponseStatus, reason string,
) []uhpgo.Event {
	if isTerminalStatus(task.Status) {
		return nil
	}

	evType := "response.failed"
	switch stopped {
	case uhp.StatusCancelled:
		task.Status = uhp.StatusCancelled
		task.Error = nil
		// Streaming §4: a cancelled task terminates with response.failed
		// carrying status "cancelled", so evType stays as it is.
	case uhp.StatusIncomplete:
		task.Status = uhp.StatusIncomplete
		task.Error = nil
		task.IncompleteDetails = map[string]any{"reason": reason}
		evType = "response.incomplete"
	default:
		task.Status = uhp.StatusFailed
		task.Error = &uhp.Error{
			Type:    uhp.ErrorTypeHarness,
			Code:    uhp.CodeHarnessError,
			Message: "the harness closed its update stream without reporting a terminal state",
		}
	}
	task.UpdatedAt = time.Now().UTC()

	evs, err := s.terminal(ctx, task, seq, evType, rs)
	// Same excuse as the loop above, and it needs stating twice because this is
	// the other place a write meets a deleted row: a task whose record went
	// while its adapter was misbehaving fails here rather than there, and an
	// ERROR would report the deletion as a storage fault.
	if err != nil && !s.taskGone(ctx, task.ID) {
		s.log.Error("persist terminal state failed", "error", err, "task_id", task.ID)
	}
	return evs
}

// stoppedBy names what stopped a run from outside the work itself, and so what
// a terminal state reached during the teardown is called. The empty status is
// "nothing stopped it", and whatever the adapter reports then stands as the
// work's own answer.
//
// The ranking is the whole of it, and it lives here rather than at the two
// places that read it so that the two cannot come to disagree about the order.
// A cancel outranks a budget, because what somebody asked for is a fact where
// which goroutine noticed first is a guess, and because `incomplete` is the
// status a client retries — the wrong thing to tell one about a stop it asked
// for on purpose (#76).
//
// A cancel on its own ranks nothing, and that asymmetry is the load-bearing
// part. Everything here rests on this server having caused the teardown, which
// only a budget establishes: a cancel arriving while an adapter fails for its
// own reasons has caused nothing, and relabelling that failure would report a
// real harness error as a stop and discard the error object with it.
// `budgetStop` is written and read by the supervisor goroutine alone, so it
// cannot race with the update it is being applied to; a cancel from a request
// goroutine can.
//
// It takes the budget's *reason* rather than a boolean per budget (#72). Two of
// them now stop a run and a third is imaginable, and a signature that grew a
// parameter each time would put the ranking's correctness in the hands of every
// caller remembering to pass them in the right order. Empty is "no budget
// stopped this run", which is every ordinary run.
func stoppedBy(budgetStop string, cancelAsked bool) uhp.ResponseStatus {
	switch {
	case budgetStop != "" && cancelAsked:
		return uhp.StatusCancelled
	case budgetStop != "":
		return uhp.StatusIncomplete
	default:
		return ""
	}
}

// relabelableTerminal reports whether a terminal update is one the teardown
// could have produced, rather than the work's own answer — so whether
// [stoppedBy]'s verdict may overwrite it.
//
// Everything terminal except `completed`, and `incomplete` itself — which an
// adapter that bounds its own steps would send, already carrying the reason
// that overwriting it would destroy.
func relabelableTerminal(t harness.UpdateType) bool {
	switch t {
	case harness.UpdateCancelled, harness.UpdateFailed:
		return true
	}
	return false
}

func isTerminalStatus(st uhp.ResponseStatus) bool {
	switch st {
	case uhp.StatusCompleted, uhp.StatusFailed, uhp.StatusCancelled, uhp.StatusIncomplete:
		return true
	}
	return false
}

// cloneTask copies a task for embedding in an event, so that a later mutation
// by the supervisor cannot retroactively change an event a subscriber has
// already been handed.
func cloneItem(it *uhp.OutputItem) *uhp.OutputItem {
	cp := *it
	cp.Content = append([]uhp.ContentPart(nil), it.Content...)
	return &cp
}

// taskGone reports whether a task's record has been deleted out from under a
// run that is still going.
//
// It exists to keep one supported client action out of the error log. A read
// that itself fails answers false: a store that cannot be read is a real
// failure and must not be excused as a deletion, so the ambiguous case falls
// through to the ERROR it would have produced anyway.
func (s *TaskService) taskGone(ctx context.Context, id string) bool {
	_, found, err := s.store.GetTask(ctx, id)
	return err == nil && !found
}

// responseOf snapshots the wire response a task will be reported as.
//
// An event carries the response object, not the task: the six internal fields
// have no place on a stream, and [uhp.Event].Response is typed as the protocol
// object precisely so they cannot travel by accident. The clone is what makes
// it a snapshot — the task keeps changing after the event is published.
func responseOf(t *domain.Task) *uhp.Response {
	cp := cloneTask(t)
	return &cp.Response
}

func cloneTask(t *domain.Task) *domain.Task {
	cp := *t
	cp.Output = append([]uhp.OutputItem(nil), t.Output...)
	for i := range cp.Output {
		cp.Output[i].Content = append([]uhp.ContentPart(nil), t.Output[i].Content...)
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
