package service

import "github.com/aenawi/uhp-go/internal/domain"

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
func (s *sequencer) next(ev domain.Event) domain.Event {
	ev.Seq = s.n
	s.n++
	return ev
}
