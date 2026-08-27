package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// waitForResult is the non-streaming path: it waits for the supervised run to
// reach a terminal state and reads the finished task back.
//
// It consumes nothing and drives nothing. If the client disconnects, this
// returns and the run carries on — the task still reaches a terminal state,
// because the supervisor owns it.
func (s *Server) waitForResult(ctx context.Context, task *domain.Task, run *service.Run) (*domain.Task, error) {
	if err := run.Wait(ctx); err != nil {
		return nil, err
	}
	// A `store: false` response is gone from the store by the time the run is
	// terminal, so the run is asked first. It answers for that case and only
	// that case — Result is nil for a retained response, which then reads from
	// the store exactly as it always has.
	//
	// This is also what makes an idempotent retry work on an unretained
	// response. Tasks §6 requires a retry to be given the first request's
	// answer, and the first request's answer was the whole response object; a
	// 404 here would make the replay differ from the original, which is the one
	// thing §6 forbids.
	if final := run.Result(); final != nil {
		return final, nil
	}
	return s.tasks.GetTask(context.WithoutCancel(ctx), task.ID)
}

// writeAccepted is the `background: true` path: the response object as it
// stands, written now, instead of the finished one written later. It is
// waitForResult's opposite number and the whole of what the field asks for —
// nothing about the run changes, only whether this request waits for it.
//
// The task is read back from the store rather than marshalled from the pointer
// StartTask returned. That pointer belongs to the supervisor from the moment
// the run is handed over, so encoding it here would be a data race against the
// goroutine writing the task's status and output. Reading it back is also the
// truthful answer: a run that finished between acceptance and this line is
// reported as finished, which is more than the client asked for and never less.
//
// Everything below turns on the retained/not-retained split, and the reason it
// is decided here rather than in handleCreateTask is that here is where the
// truth is. handleCreateTask can only see the body that arrived, and an
// idempotent retry is not obliged to repeat the `store: false` its first request
// sent — so a body carrying nothing but `background: true` can name a run whose
// response will never be retained, and the pre-flight refusal cannot tell. The
// accepted task can, because `store` is on it.
//
// So a retained response is answered from the store, which mid-run is the only
// thing holding the task. An unretained one is answered from the run, which
// keeps the terminal task precisely because the store will not — and is refused,
// exactly as the same pair of fields is refused up front, when the run has not
// got there yet and the store's copy is one this server has already undertaken
// to drop.
func (s *Server) writeAccepted(w http.ResponseWriter, r *http.Request, task *domain.Task, run *service.Run) {
	accepted, err := s.tasks.GetTask(r.Context(), task.ID)
	if err == nil && accepted.Store {
		writeJSON(w, http.StatusOK, accepted)
		return
	}
	// Tasks §6 owes a retry the first request's answer, and for an unretained
	// response the run is the only place that answer still exists. Asked before
	// the refusal below, so a run that has finished is answered rather than
	// turned away over a record that is already gone.
	if run != nil {
		if final, over := run.Settled(); over && final != nil {
			writeJSON(w, http.StatusOK, final)
			return
		}
	}
	if err == nil {
		// Readable, unretained, and still running: the one state where this
		// request has nothing to promise. The record goes when the run ends,
		// this request is not waiting for that, and so every read after it
		// would be a 404.
		//
		// The id is on the refusal for the same reason it is on the 500 below:
		// the run this request named is going either way, and a client told
		// only "no" is holding one it can neither follow nor stop.
		writeErrorFull(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"background: true names a response that is not retained — it is dropped when the "+
				"run ends and this request will not be here to receive it; stream it instead, "+
				"or ask for one that is stored", "background",
			map[string]any{"response_id": task.ID})
		return
	}
	if !errors.Is(err, service.ErrResponseNotFound) {
		// The task was accepted and the run is on its way to a terminal state,
		// so a client told only "500" is holding a run slot it can neither poll
		// nor cancel. The id is the handle for both, and it is the one field
		// safe to read from here: it is written once, before the supervisor is
		// handed the task, and never again.
		writeErrorDetail(w, http.StatusInternalServerError, typeServerError, vendorCodeStorageFailure,
			"the task was accepted and is running, but this server could not read its response back",
			map[string]any{"response_id": task.ID})
		return
	}
	writeServiceError(w, err)
}

// subscription is how a stream is read, whichever stream it is: a task's own
// run or a harness's feed. Both take the same four arguments and mean the same
// thing by them, so the transport writes SSE once rather than once per source.
type subscription func(context.Context, int, service.IdleTick, func(uhpgo.Event) error) error

// vendorCodeStreamingUnsupported is namespaced because Errors §3 has no entry
// for "this server's own response writer cannot flush", and requires an
// additional code to carry a vendor prefix so a future version of the
// specification cannot collide with it.
//
// The near miss is `harness_unavailable`, and it does not fit: no harness is
// involved, and answering it would send a client retrying against a backend
// that was never the problem.
const vendorCodeStreamingUnsupported = "uhpgo_streaming_unsupported"

// streamSSE subscribes to an event log and writes it as Server-Sent Events,
// starting at sequence number `from`. Disconnecting merely unsubscribes.
func (s *Server) streamSSE(w http.ResponseWriter, r *http.Request, from int, events subscription) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, typeServerError, vendorCodeStreamingUnsupported,
			"response writer does not support flushing")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Streaming §1: servers behind a proxy MUST disable response buffering.
	// This is the single most common UHP deployment error and it is
	// indistinguishable from a slow harness.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	idle := service.IdleTick{
		Every: s.keepAlive,
		Do: func() error {
			if err := writeKeepAlive(w); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		},
	}
	err := events(r.Context(), from, idle, func(ev uhpgo.Event) error {
		if err := writeSSE(w, ev); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err == nil || r.Context().Err() != nil {
		return
	}

	// A subscriber can fall out of a bounded feed's window while it is reading,
	// if it is slower than the harness is busy. The status line is long gone by
	// then, so the only place left to say so is the stream itself — and a
	// stream that simply stops is indistinguishable from a dropped socket, so
	// the client would reconnect, be refused for the same gap, and never learn
	// why. One error event before the end is what turns that into something it
	// can act on.
	//
	// Streaming §4 requires an `error` event to be followed by a terminal
	// event, and this one is not. That rule is about a task's stream, which
	// ends terminally; a feed has no terminal event to follow anything with,
	// and staying silent is the worse of the two departures.
	var gap *service.EventGapError
	if errors.As(err, &gap) {
		// Numbered where the subscriber had got to, not at the recovery point:
		// gap.Oldest is behind what it has already consumed, and a
		// sequence_number that goes backwards is dropped by any client that
		// deduplicates on it — losing the one event that explains what
		// happened. The recovery point is in the message, where it can be read
		// without being mistaken for a position in the stream.
		_ = writeSSEClearingID(w, uhpgo.Event{Event: uhp.Event{
			Type: "error", SequenceNumber: gap.From,
			Code: uhpgo.CodeEventGap,
			Message: fmt.Sprintf("this stream has moved on; events before %d are no longer "+
				"retained — reconnect without a Last-Event-ID to resume from there", gap.Oldest),
		}})
		flusher.Flush()
		return
	}
	s.log.Error("stream failed", "error", err)
}

// maxEventID bounds a `Last-Event-ID` to a number this server could plausibly
// have written.
//
// Without a bound, the largest int parses cleanly, passes the sign check and
// wraps to the smallest int when the +1 below is applied — which reads as a
// negative resume point and defeats every check downstream that assumes one
// counts upwards. A billion events on one stream is far beyond anything this
// server produces, so refusing above it costs nothing real.
//
// It is kept under the 32-bit `int` range rather than at it, so that this
// package still builds and behaves the same way on a 32-bit target.
const maxEventID = 1 << 30

// resume is where a stream should start, and whether the client asked at all.
//
// The two are not the same question and collapsing them is a bug: "no header"
// and "resume from 0" both mean a starting sequence number of zero, but a feed
// that has evicted its first events must refuse the second and serve the
// first. `present` is what keeps them apart.
type resume struct {
	from    int
	present bool
}

// resumeFrom reads the SSE `Last-Event-ID` header, reporting where to start
// and whether the header was usable at all.
//
// Issue #8: resumption MUST start at the event *after* the one named and MUST
// NOT replay what the client already has, which is why `from` is the id plus
// one rather than the id.
//
// A header that is not a number this server could have issued is refused
// rather than ignored. Ignoring it would replay the stream from the beginning
// — the precise thing resumption exists to avoid — and the client would have
// no way to tell that its resume point was quietly dropped.
func resumeFrom(r *http.Request) (resume, bool) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		return resume{}, true
	}
	last, err := strconv.Atoi(raw)
	if err != nil || last < 0 || last >= maxEventID {
		return resume{}, false
	}
	return resume{from: last + 1, present: true}, true
}

// writeInvalidLastEventID refuses a header this server could not have written.
func writeInvalidLastEventID(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
		"the Last-Event-ID header must be a sequence number this server issued")
}

// resumable checks a resume point against what a stream actually holds, and
// reports whether the stream may be opened.
//
// Both bounds are answered here, before any of the streaming headers are
// written, because afterwards there is no way left to say "no" in a form a
// client reads as a refusal: a 200 that carries nothing is indistinguishable
// from a harness with nothing to say.
func resumable(w http.ResponseWriter, from, oldest, head int) bool {
	if from < oldest {
		// `oldest_retained` is the actionable half: without it a client can
		// only choose between replaying what it already has and being refused
		// forever.
		writeErrorDetail(w, http.StatusBadRequest, typeInvalidRequest, vendorCodeEventGap,
			"the events after the given Last-Event-ID are no longer retained by this server",
			map[string]any{"oldest_retained": oldest})
		return false
	}
	if from > head {
		// A client cannot have seen an event that was never produced, so this
		// is a mistake rather than a client that is merely early — and it is
		// worth saying, because the alternative is an open stream that never
		// delivers anything the client recognises.
		writeErrorDetail(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the given Last-Event-ID is past the last event this stream has produced",
			map[string]any{"next_sequence_number": head})
		return false
	}
	return true
}

// writeKeepAlive emits an SSE comment line.
//
// It carries nothing, and that is the whole design: a comment is discarded by
// every conformant client, so the only thing it changes is that bytes moved.
// That is what a client running an inactivity timeout (Errors §5) needs from a
// harness that has been thinking for two minutes — proof the socket is alive,
// not a phantom event it has to reason about.
func writeKeepAlive(w io.Writer) error {
	_, err := io.WriteString(w, ": keep-alive\n\n")
	return err
}

// writeSSE emits one event. Every event goes through the same shape, so the
// stream has one schema rather than a bare task for the first event and a
// wrapper for the rest.
//
// The `id:` line is the event's own sequence number, and it is what makes
// resumption work without the client having to parse anything: an SSE client
// remembers the last id it saw and sends it back as `Last-Event-ID` when it
// reconnects, which is the mechanism issue #8 asks for. Emitting it is also
// how a client discovers resumption is on offer — a stream with no ids has
// nothing to resume from.
func writeSSE(w io.Writer, ev uhpgo.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.SequenceNumber, ev.Type, b)
	return err
}

// writeSSEClearingID emits an event and clears the client's resume point.
//
// The empty `id:` field is doing the work, and the three options here are not
// close. A real id would hand the client a resume point that is still outside
// the window, so every reconnection recovers one sequence number instead of
// rejoining. *No* id line leaves the client's own stale id in place — which
// looks harmless until you follow it through EventSource: it reconnects with
// that id, is refused with a 400, and a non-2xx makes the user agent fail the
// connection permanently rather than retry. An empty value resets the buffer,
// so the automatic reconnection carries no `Last-Event-ID` at all and is
// served from the oldest event still retained, which is exactly what this
// notice tells the client to do.
func writeSSEClearingID(w io.Writer, ev uhpgo.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: \nevent: %s\ndata: %s\n\n", ev.Type, b)
	return err
}
