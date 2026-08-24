package service

import (
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// sequencer owns event numbering for one run.
//
// UHP Streaming §1: "sequence_number MUST start at 0 and increase by exactly 1
// per event within a stream." Numbering used to be done by whichever loop was
// consuming the run — the streaming path started at 1, the non-streaming path
// at 0 — so the two disagreed. Handing one sequencer to one supervisor makes
// that disagreement unrepresentable rather than merely fixed.
type sequencer struct{ n int }

func newSequencer() *sequencer { return &sequencer{} }

// next stamps the event with the next sequence number.
//
// It takes the protocol event and returns this server's, which is the shape the
// two extension fields actually have: a task's own stream leaves ResponseID and
// SessionID empty, and only [Feed.publish] fills them, because only a feed
// multiplexes enough runs for an event to need attributing. Numbering is
// protocol; attribution is not.
func (s *sequencer) next(ev uhp.Event) uhpgo.Event {
	ev.SequenceNumber = s.n
	s.n++
	return uhpgo.Event{Event: ev}
}
