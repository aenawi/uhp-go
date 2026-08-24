package uhp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultMaxEventBytes bounds one Server-Sent Events line by default.
//
// It is generous because a terminal event carries the complete final response,
// assistant text and all, and a long agent run produces a lot of it. Truncating
// that would turn the one event a client can rely on into the one it cannot
// parse.
const DefaultMaxEventBytes = 8 << 20 // 8 MiB

// EventDecoder reads a UHP event stream and frames it into [Event] values.
//
// Framing is all it does. There is no HTTP here, no reconnection, no retry
// policy and no authentication: hand it the body of a streaming response, or a
// file, or a buffer of captured events, and it will hand back events. What to
// do with a dropped connection is a policy decision that belongs to whoever
// owns the transport.
//
// # Unknown event types are not this type's problem, and that is the point
//
// UHP's second client rule says a client MUST ignore event types it does not
// recognise — skip and continue, never error. A decoder cannot apply that rule
// on your behalf, because it has no idea which types *you* recognise. What it
// can do is make breaking the rule take effort: [Event] tolerates unknown
// fields, an unrecognised type is just a string in [Event].Type, and nothing
// below inspects the type at all. A hand-rolled reader that switches on a
// closed set of types and errors in the default arm is the failure this
// replaces, and it fails on the first event a server adds within a version it
// was allowed to add it to.
//
// # Sequence numbers are read, not enforced
//
// [Event].SequenceNumber starts at 0 and increases by exactly 1, which is what
// lets a client detect a dropped event. This decoder does not check it. A gap
// means something different depending on where the stream came from — a task's
// own stream must be gapless, a feed a client resumed with Last-Event-ID starts
// wherever it was told to — and a decoder that refused the second case would be
// wrong about a stream the specification explicitly provides for.
//
// # Errors
//
// [EventDecoder.Next] returns [io.EOF] at the end of the stream and a
// [*FrameError] for a frame whose data is not JSON. The decoder stays usable
// after a FrameError: the next call reads the next frame, so a caller that
// wants maximum tolerance can log and continue, and one that wants strictness
// can stop. Both are reasonable and neither is decided here.
//
// A stream that ends without a terminal event is malformed for a task
// (Streaming §4), and Next still reports plain io.EOF for it. It has to: a
// harness feed has no terminal event to end with, and the decoder is not told
// which of the two it is reading.
type EventDecoder struct {
	// MaxEventBytes bounds a single line. It is read once, when the first
	// event is decoded, and changing it afterwards does nothing. Zero means
	// [DefaultMaxEventBytes].
	MaxEventBytes int

	r  io.Reader
	sc *bufio.Scanner

	lastEventID string
}

// NewEventDecoder returns a decoder reading a UHP event stream from r.
func NewEventDecoder(r io.Reader) *EventDecoder {
	return &EventDecoder{r: r}
}

// LastEventID is the most recent SSE `id:` field the stream carried, which for
// a UHP stream is the last event's sequence number.
//
// It is what belongs in a Last-Event-ID header on reconnection, and resumption
// starts at the event *after* the one named. An empty result means either that
// no id has been seen or that the server sent an empty `id:` field, which
// clears the resume point deliberately — a server whose retained window has
// moved past the client uses it to say "reconnect from the beginning of what I
// still have" rather than leaving a stale id in place to be refused forever.
func (d *EventDecoder) LastEventID() string { return d.lastEventID }

// FrameError is a frame whose data could not be decoded as a UHP event.
//
// It carries the offending payload so that a caller can log what actually
// arrived. An unknown event *type* never produces one of these — that is a
// well-formed event this client happens not to know, and it is returned
// normally.
type FrameError struct {
	// Data is the frame's payload, as it arrived.
	Data string
	// Err is the underlying decode failure.
	Err error
}

func (e *FrameError) Error() string {
	return fmt.Sprintf("uhp: undecodable event frame: %v", e.Err)
}

func (e *FrameError) Unwrap() error { return e.Err }

// Next reads the next event from the stream.
//
// It returns [io.EOF] when the stream ends, and a [*FrameError] for a frame
// that is not decodable — after which the decoder remains usable.
func (d *EventDecoder) Next() (Event, error) {
	if d.sc == nil {
		max := d.MaxEventBytes
		if max <= 0 {
			max = DefaultMaxEventBytes
		}
		d.sc = bufio.NewScanner(d.r)
		// bufio.Scanner's own default caps a line at 64 KiB and reports
		// ErrTooLong past it, which a terminal event carrying a long response
		// reaches routinely.
		d.sc.Buffer(make([]byte, 0, 64<<10), max)
		d.sc.Split(scanEventLines)
	}

	var data []byte
	// eventName is the SSE `event:` field, which UHP sets to the same string
	// the payload carries in `type`.
	var eventName string
	// haveData, not len(data) > 0: `data:` with an empty value is a frame the
	// specification dispatches, and it is not the same as a frame with no data
	// field at all, which it discards.
	haveData := false

	for d.sc.Scan() {
		line := d.sc.Bytes()

		// A blank line dispatches whatever has accumulated.
		if len(line) == 0 {
			if !haveData {
				// A frame with no data field is discarded rather than
				// dispatched — this is how a bare `id:` or `retry:` arrives,
				// and how a run of keep-alive comments looks once they are
				// stripped. Reset and keep reading.
				eventName = ""
				continue
			}
			return d.dispatch(data, eventName)
		}

		// A line beginning with a colon is a comment, and carries nothing by
		// design. It is how a server proves the socket is alive during a long
		// silence without emitting a phantom event the client has to reason
		// about, so it must be dropped here rather than surfaced.
		if line[0] == ':' {
			continue
		}

		field, value := splitField(line)
		switch string(field) {
		case "data":
			// Multiple data lines in one frame are joined with newlines and
			// the trailing one is dropped. UHP sends one JSON object per
			// frame, but a proxy or a writer is entitled to split it, and a
			// reader that assumed one line would see valid streams as
			// truncated.
			if haveData {
				data = append(data, '\n')
			}
			data = append(data, value...)
			haveData = true
		case "event":
			eventName = string(value)
		case "id":
			// An id containing NUL is ignored outright by the SSE
			// specification. An id that is merely *empty* is not ignored: it
			// clears the resume point, which is a thing a server does on
			// purpose. See [EventDecoder.LastEventID].
			if !bytes.ContainsRune(value, 0) {
				d.lastEventID = string(value)
			}
		}
		// Every other field name, `retry:` included, is ignored. Reconnection
		// is not this type's job.
	}

	if err := d.sc.Err(); err != nil {
		return Event{}, err
	}
	// A stream can end without the blank line that would have dispatched its
	// last frame. Delivering it is the right answer — the bytes arrived and
	// they parse — and the alternative is dropping a terminal event because
	// the writer closed without a final newline.
	if haveData {
		return d.dispatch(data, eventName)
	}
	return Event{}, io.EOF
}

// dispatch decodes one frame's payload.
func (d *EventDecoder) dispatch(data []byte, eventName string) (Event, error) {
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return Event{}, &FrameError{Data: string(data), Err: err}
	}
	// Streaming §1 requires the payload itself to carry `type`, so this is a
	// repair rather than the normal path. It is worth making: the SSE `event:`
	// line holds the same string, a client that has one and not the other can
	// still route the event, and the alternative is handing back an event with
	// no type at all.
	if ev.Type == "" {
		ev.Type = eventName
	}
	return ev, nil
}

// splitField splits an SSE line into its field name and value, stripping the
// single optional space after the colon.
//
// A line with no colon is a field name with an empty value, which is what makes
// a bare `data` line legal.
func splitField(line []byte) (field, value []byte) {
	i := bytes.IndexByte(line, ':')
	if i < 0 {
		return line, nil
	}
	value = line[i+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return line[:i], value
}

// scanEventLines splits on the three terminators Server-Sent Events allows:
// CRLF, LF, and a lone CR.
//
// bufio.ScanLines handles the first two and treats a lone CR as ordinary text,
// which would swallow a whole frame into one line on a stream that used it.
func scanEventLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			// One more byte decides whether this is CRLF or a lone CR, and
			// guessing wrong emits a spurious blank line — which in SSE means
			// dispatching a frame early.
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			return 0, nil, nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
