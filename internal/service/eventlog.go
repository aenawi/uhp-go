package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
)

// EventGapError is a resumption whose starting point has already been
// discarded.
//
// It carries the oldest sequence number still retained, because a client told
// only "too late" has nowhere to go: dropping its resume point and starting
// over replays events it already has, and keeping it gets the same refusal
// forever. With this it knows exactly where the stream can be picked up.
type EventGapError struct {
	// From is the sequence number the subscriber asked to start at.
	From int
	// Oldest is the oldest sequence number the log still holds.
	Oldest int
}

func (e *EventGapError) Error() string {
	return fmt.Sprintf("service: events before %d are no longer retained (asked for %d)", e.Oldest, e.From)
}

// eventLog is a retained event stream that subscribers read rather than
// consume.
//
// Both things that stream events in this server are one of these — a task's
// run and a harness's feed — and they differ only in what they retain and in
// whether they ever end. Sharing the mechanism is what makes a resumed stream
// indistinguishable from a fresh one on either of them, and is why a
// resumption is a starting sequence number rather than a second code path.
//
// The writer never blocks on a reader: publishing appends and wakes everyone,
// and a slow or vanished client cannot stall the task behind it.
type eventLog struct {
	mu     sync.Mutex
	events []domain.Event
	// notify is closed and replaced on every append, which wakes every waiting
	// subscriber without the writer ever blocking on one of them.
	notify   chan struct{}
	finished bool

	// retain is how many of the most recent events are always available. Zero
	// retains everything, which is what a run wants: its log dies with the task
	// that owns it. A feed outlives every task on its harness and would
	// otherwise grow for the life of the process.
	//
	// It is a floor and not a ceiling: the log is allowed to run over before it
	// evicts, so the number a caller passes is the number a caller can promise
	// its clients. A ceiling would make the guarantee smaller than the constant
	// and the documentation quietly wrong.
	retain int

	// oldest is the sequence number of the oldest event still held. It is what
	// lets a resumption tell "you are early" from "that is gone" — without it
	// a log that had dropped events would answer a stale resume point with a
	// silently later one, which is a gap the client cannot see.
	oldest int
}

func newEventLog(retain int) *eventLog {
	return &eventLog{notify: make(chan struct{}), retain: retain}
}

// append adds an event and wakes every subscriber.
func (l *eventLog) append(ev domain.Event) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.evict()
	close(l.notify)
	l.notify = make(chan struct{})
	l.mu.Unlock()
}

// close marks the log terminal and wakes everyone for the last time. Every
// subscriber returns once it has been handed what is left.
func (l *eventLog) close() {
	l.mu.Lock()
	l.finished = true
	close(l.notify)
	l.notify = make(chan struct{})
	l.mu.Unlock()
}

// evict drops the oldest events once the log has run far enough past its
// floor. It is called with the lock held.
//
// A batch goes at a time rather than one event per append: dropping singly
// copies the whole retained window on every publish for the life of the
// process. The slack is taken above `retain` rather than below it, so that
// `retain` is what is always still there — trimming to less than the floor and
// growing back to it would leave the guarantee true only immediately before an
// eviction, which is exactly when a reconnecting client turns up.
func (l *eventLog) evict() {
	if l.retain <= 0 || len(l.events) <= l.retain+l.retain/4 {
		return
	}
	drop := len(l.events) - l.retain
	l.oldest = l.events[drop].Seq
	copy(l.events, l.events[drop:])
	clear(l.events[l.retain:])
	l.events = l.events[:l.retain]
}

// retained reports the oldest sequence number still held.
func (l *eventLog) retained() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.oldest
}

// head reports the sequence number this log has not reached yet: one past the
// newest event it holds.
//
// It bounds a resume point from above. A client cannot have seen an event that
// was never produced, so a `Last-Event-ID` past this names nothing, whether or
// not the stream is still running — and honouring it opens a stream that will
// never deliver anything the client can recognise.
func (l *eventLog) head() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.events); n > 0 {
		return l.events[n-1].Seq + 1
	}
	return l.oldest
}

// indexOf returns the position of the first retained event numbered seq or
// later. It is called with the lock held.
//
// The search is by sequence number and not by position because both can move:
// a bounded log drops events from the front, and a numbered event that is
// never published — an update whose persistence failed — leaves a hole. Either
// one makes "index i is sequence i" wrong, and a resumption that assumed it
// would hand the client the wrong events.
func (l *eventLog) indexOf(seq int) int {
	return sort.Search(len(l.events), func(i int) bool { return l.events[i].Seq >= seq })
}

// IdleTick asks a subscription to call Do every Every of silence. Its zero
// value asks for nothing, which is what a subscriber that only wants events
// passes.
//
// The two fields are one decision and are meaningless apart, so they travel as
// one value rather than as two arguments that have to be checked against each
// other.
type IdleTick struct {
	Every time.Duration
	Do    func() error
}

func (t IdleTick) wanted() bool { return t.Every > 0 && t.Do != nil }

// FromOldest asks a subscription to start at whatever the log still holds,
// rather than at a named sequence number.
//
// It is what a subscriber that is not resuming passes. Reading Oldest() and
// passing the answer instead leaves a window in which the log evicts in
// between, and the subscription is then refused for a gap the caller never
// asked to bridge — a fresh client turned away because the harness was busy
// while it was connecting.
const FromOldest = -1

// subscribe calls fn for every event numbered `from` or later, until the log
// is terminal or ctx is cancelled.
//
// `from` is the sequence number to start at, so zero is the whole stream of a
// log that retains everything: UHP numbers a stream from 0 upwards by exactly
// 1, so "resume after N" is "start at N+1". FromOldest is for a caller that
// has no number in mind at all.
//
// A run that is working says nothing, and silence is also what a dead
// connection sounds like. idle is how a subscriber hears the difference: its
// Do runs on the same goroutine as fn, in the gaps between events, and an
// error from it ends the subscription exactly as an error from fn does. What
// to put on the wire in that gap belongs to the transport; all this knows is
// that the gap happened.
func (l *eventLog) subscribe(ctx context.Context, from int, idle IdleTick, fn func(domain.Event) error) error {
	// A nil channel blocks forever in a select, so a subscriber that asked for
	// no tick reads exactly as it did before there was one.
	var tick *time.Ticker
	var ticks <-chan time.Time
	if idle.wanted() {
		tick = time.NewTicker(idle.Every)
		defer tick.Stop()
		ticks = tick.C
	}

	next := from
	for {
		l.mu.Lock()
		delivered := false
		for {
			// Resolved under the same lock as the check below, so that a
			// caller with no number in mind cannot be handed a gap that opened
			// between reading the log's position and subscribing to it.
			if next < 0 {
				next = l.oldest
			}
			// Re-checked on every pass, not only at the start: a bounded log
			// can evict while this subscriber is between events, and a reader
			// that has fallen out of the window has to be told rather than
			// quietly handed the stream from wherever it now begins.
			if l.oldest > next {
				gap := &EventGapError{From: next, Oldest: l.oldest}
				l.mu.Unlock()
				return gap
			}
			i := l.indexOf(next)
			if i >= len(l.events) {
				break
			}
			ev := l.events[i]
			next = ev.Seq + 1
			l.mu.Unlock()
			if err := fn(ev); err != nil {
				return err
			}
			delivered = true
			l.mu.Lock()
		}
		if l.finished {
			l.mu.Unlock()
			return nil
		}
		wait := l.notify
		l.mu.Unlock()

		// An event is itself proof the connection is alive, so the countdown
		// starts again from the last one rather than running on a fixed
		// schedule through a busy stream.
		//
		// The channel is drained first: a tick that landed while fn was
		// writing survives Reset, and would then fire on the very next select
		// — a keep-alive immediately after an event, which is the one case
		// this reset exists to avoid.
		if delivered && tick != nil {
			tick.Stop()
			select {
			case <-tick.C:
			default:
			}
			tick.Reset(idle.Every)
		}

		select {
		case <-wait:
		case <-ticks:
			if err := idle.Do(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
