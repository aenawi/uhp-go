package service

import (
	"context"
	"sync"

	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// feedRetention is how many of the most recent events a harness feed always
// keeps, so a dropped client can resume.
//
// A feed lives as long as the process and carries every task on its harness,
// so it cannot keep everything. The number is a reconnection window, not a
// history: it has to cover the seconds a client spends noticing a dead socket
// and dialling again, and a task that produces text produces a few hundred
// events, so this is roughly "the last task or two". History that outlives the
// window is what GET /v1/responses/{id} is for, and a resumption that falls
// outside it is refused rather than silently answered from wherever the log
// now starts.
const feedRetention = 512

// Feed is the live event stream of one harness: every event of every task that
// runs on it, in the order this server produced them.
//
// It exists because following a task is otherwise something only the request
// that created it can do. A dropped connection already does not abort the run
// (Streaming §5) — but the only way to learn what happened afterwards was to
// poll GET /v1/responses/{id}. A feed is how a client attaches again, and it
// attaches to a harness rather than to a request, so it also sees the tasks it
// did not start.
type Feed struct {
	log *eventLog

	// mu makes numbering and appending one step. Several runs publish into one
	// feed concurrently, and a sequence number handed out before the append it
	// belongs to would let two events reach the log in the opposite order to
	// their numbers — which breaks resumption, because the log is searched in
	// sequence order.
	mu  sync.Mutex
	seq *sequencer
}

func newFeed(retain int) *Feed {
	return &Feed{log: newEventLog(retain), seq: newSequencer()}
}

// publish forwards one run's event onto the feed.
//
// The event is renumbered. A feed multiplexes many runs and every run numbers
// its own stream from zero, so forwarding the run's number would hand a
// subscriber a stream whose sequence_number restarts at every task — and a
// `Last-Event-ID` would then name several different events rather than one.
// Streaming §1 numbers a stream, and this is a different stream.
// It is also where the two uhpgo extension fields are set, and the only place:
// a run's own log keeps the protocol event untouched, so the extension exists
// exactly where it earns its keep.
func (f *Feed) publish(ev uhpgo.Event, taskID, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := f.seq.next(ev.Event)
	out.ResponseID = taskID
	out.SessionID = sessionID
	f.log.append(out)
}

// Events calls fn for every event numbered `from` or later, until ctx is
// cancelled or the harness is deleted. See eventLog.subscribe.
func (f *Feed) Events(ctx context.Context, from int, idle IdleTick, fn func(uhpgo.Event) error) error {
	return f.log.subscribe(ctx, from, idle, fn)
}

// Oldest is the oldest sequence number this feed can still replay. A
// resumption from anything earlier has a gap in it, and the transport refuses
// it before opening a stream rather than discovering it halfway through one.
func (f *Feed) Oldest() int { return f.log.retained() }

// Head is one past the newest sequence number this feed has produced. A
// resumption from anything later names an event that does not exist.
func (f *Feed) Head() int { return f.log.head() }

// close ends every subscription. Only a deleted harness does this: a feed that
// ended for any other reason would look, to a client, exactly like a harness
// that had gone quiet.
func (f *Feed) close() { f.log.close() }
