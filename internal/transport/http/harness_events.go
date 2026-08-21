package http

import (
	"net/http"

	"github.com/aenawi/uhp-go/internal/service"
)

// vendorCodeEventGap is a resumption whose starting point the server has
// already discarded.
//
// Errors §3 has no entry for it — the specification describes resumption but
// not its refusal — so the code carries the vendor prefix rather than
// borrowing a standard one that means something else.
const vendorCodeEventGap = "uhpgo_event_gap"

// handleHarnessEvents answers GET /v1/harnesses/{id}/events with the live
// event stream of one harness.
//
// UHP Streaming §5 already required that a dropped connection not abort the
// task, and this server already honoured it — the supervisor owns the run and
// a disconnect only unsubscribes. What was missing was any way to *follow* the
// work again afterwards short of polling GET /v1/responses/{id}. This is that
// way, and it is scoped to a harness rather than to a task so a client can
// also see the tasks it did not start.
func (s *Server) handleHarnessEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	feed, ok, err := s.tasks.HarnessFeed(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		writeHarnessNotFound(w, id)
		return
	}

	at, usable := resumeFrom(r)
	if !usable {
		writeInvalidLastEventID(w)
		return
	}

	// A client that is not resuming gets everything the feed still holds, and
	// says so rather than naming a number: reading the feed's position here and
	// passing it as a resume point would let an eviction in between refuse a
	// fresh subscriber for a gap it never asked to bridge. Starting it at zero
	// instead would refuse it outright, because a feed that has evicted
	// anything no longer has an event zero — turning the endpoint's ordinary
	// use into its error case.
	from := service.FromOldest
	if at.present {
		// A feed keeps a reconnection window, not a history, so a resume point
		// can fall off either end of what it holds.
		if !resumable(w, at.from, feed.Oldest(), feed.Head()) {
			return
		}
		from = at.from
	}

	s.streamSSE(w, r, from, feed.Events)
}
