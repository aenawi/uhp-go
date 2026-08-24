package uhp_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

// drain reads every event a decoder yields, stopping at EOF and failing on
// anything else. It returns the events so a test can assert on the sequence
// rather than on one event at a time.
func drain(t *testing.T, d *uhp.EventDecoder) []uhp.Event {
	t.Helper()
	var got []uhp.Event
	for {
		ev, err := d.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, ev)
	}
}

func decode(t *testing.T, stream string) []uhp.Event {
	t.Helper()
	return drain(t, uhp.NewEventDecoder(strings.NewReader(stream)))
}

func TestDecodesAStream(t *testing.T) {
	got := decode(t, `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m","output":[]}}

data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Sum"}

data: {"type":"response.completed","sequence_number":2}

`)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Type != uhp.EventResponseCreated || got[0].Response == nil {
		t.Errorf("first event = %+v, want response.created carrying a response", got[0])
	}
	if got[1].Delta != "Sum" || got[1].SequenceNumber != 1 {
		t.Errorf("second event = %+v, want delta %q at sequence 1", got[1], "Sum")
	}
	// The zero index has to survive: it is the value a plain int with omitempty
	// would have dropped, and it names the only item in the stream.
	if got[1].OutputIndex == nil || *got[1].OutputIndex != 0 {
		t.Errorf("output_index = %v, want a pointer to 0", got[1].OutputIndex)
	}
	if !got[2].IsTerminal() {
		t.Errorf("last event %q should be terminal", got[2].Type)
	}
}

// TestKeepAlivesAreNotEvents is the reason comments are stripped rather than
// surfaced. A server proves a slow socket is alive by writing one, and a client
// that saw it as an event would have to invent a rule for ignoring it.
func TestKeepAlivesAreNotEvents(t *testing.T) {
	got := decode(t, `: keep-alive

: keep-alive

data: {"type":"response.completed","sequence_number":0}

`)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 — comments must not dispatch", len(got))
	}
}

// TestFramesWithoutDataAreDiscarded covers the other frame that carries no
// event: a bare `id:` or `retry:`, which the specification says to discard
// rather than dispatch.
func TestFramesWithoutDataAreDiscarded(t *testing.T) {
	got := decode(t, "retry: 3000\n\nid: 7\n\ndata: {\"type\":\"error\",\"sequence_number\":8}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].SequenceNumber != 8 {
		t.Errorf("sequence_number = %d, want 8", got[0].SequenceNumber)
	}
}

// TestMultiLineDataIsJoined covers a payload split across data lines. UHP sends
// one JSON object per frame, but nothing stops a writer or a proxy from
// splitting it, and a reader that assumed one line would see a valid stream as
// truncated.
func TestMultiLineDataIsJoined(t *testing.T) {
	got := decode(t, "data: {\"type\":\"response.completed\",\ndata: \"sequence_number\":4}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != uhp.EventResponseCompleted || got[0].SequenceNumber != 4 {
		t.Errorf("got %+v, want the two halves joined into one event", got[0])
	}
}

// TestLineTerminators covers all three Server-Sent Events allows. bufio's own
// line splitter treats a lone CR as ordinary text, which would swallow a whole
// stream into a single line.
//
// Two frames, not one. A single frame is not a test of this at all: with no
// blank line to find, a splitter that ignored the terminator entirely would
// still hand the payload back — the trailing CR is JSON whitespace and parses
// away. It takes a second frame for the separator to carry any weight.
func TestLineTerminators(t *testing.T) {
	first := `data: {"type":"response.output_text.delta","sequence_number":0,"delta":"a"}`
	second := `data: {"type":"response.completed","sequence_number":1}`

	for name, eol := range map[string]string{
		"LF":   "\n",
		"CRLF": "\r\n",
		"CR":   "\r",
	} {
		t.Run(name, func(t *testing.T) {
			got := decode(t, first+eol+eol+second+eol+eol)
			if len(got) != 2 {
				t.Fatalf("got %d events, want 2", len(got))
			}
			if got[0].Delta != "a" || !got[1].IsTerminal() {
				t.Errorf("got %+v, want a delta then a terminal event", got)
			}
		})
	}
}

// TestUnknownEventTypeIsDelivered is UHP's second client rule.
//
// A server MAY add event types within a published version, so an unrecognised
// type is a well-formed event this client has not been taught about — not an
// error. A decoder that refused it would break on the first additive change the
// protocol explicitly permits.
func TestUnknownEventTypeIsDelivered(t *testing.T) {
	got := decode(t, `data: {"type":"response.telepathy.delta","sequence_number":3,"delta":"…"}

`)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != "response.telepathy.delta" || got[0].Delta != "…" {
		t.Errorf("got %+v, want the unknown type passed through intact", got[0])
	}
	if got[0].IsTerminal() {
		t.Error("an unknown type must not be reported as terminal")
	}
}

// TestUnknownFieldsAreIgnored is the first client rule: ignore unknown fields,
// not warn and not error.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	got := decode(t, `data: {"type":"response.completed","sequence_number":0,"invented_later":{"a":1}}

`)
	if len(got) != 1 || got[0].Type != uhp.EventResponseCompleted {
		t.Fatalf("got %+v, want the event decoded with the unknown field ignored", got)
	}
}

// TestUndecodableFrameIsRecoverable checks both halves of the contract: the bad
// frame is reported, and the decoder keeps working afterwards.
//
// Reporting rather than skipping is deliberate — malformed JSON is a broken
// stream, not an unknown event type — but a caller that would rather tolerate
// it must be able to, and it cannot if the decoder has given up.
func TestUndecodableFrameIsRecoverable(t *testing.T) {
	d := uhp.NewEventDecoder(strings.NewReader("data: {not json\n\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\n"))

	_, err := d.Next()
	var frame *uhp.FrameError
	if !errors.As(err, &frame) {
		t.Fatalf("first Next err = %v, want a *uhp.FrameError", err)
	}
	if frame.Data != "{not json" {
		t.Errorf("FrameError.Data = %q, want the payload as it arrived", frame.Data)
	}

	ev, err := d.Next()
	if err != nil {
		t.Fatalf("second Next after a bad frame: %v", err)
	}
	if ev.Type != uhp.EventResponseCompleted {
		t.Errorf("second event = %+v, want the stream to have carried on", ev)
	}
}

// TestLastEventID tracks the resume point, including the server's way of
// clearing it.
//
// An empty `id:` is not a no-op: a server whose retained window has moved past
// the client sends one to say "reconnect from the beginning of what I still
// have". Leaving the stale id in place instead would have the client reconnect,
// be refused, and — for an EventSource — fail permanently on the non-2xx.
func TestLastEventID(t *testing.T) {
	d := uhp.NewEventDecoder(strings.NewReader(
		"id: 4\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":4}\n\n" +
			"id: \ndata: {\"type\":\"error\",\"sequence_number\":5}\n\n"))

	if _, err := d.Next(); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got := d.LastEventID(); got != "4" {
		t.Errorf("LastEventID = %q, want %q", got, "4")
	}

	if _, err := d.Next(); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got := d.LastEventID(); got != "" {
		t.Errorf("LastEventID = %q after an empty id field, want it cleared", got)
	}
}

// TestFinalFrameWithoutBlankLine covers a writer that closed without the
// trailing blank line. The bytes arrived and they parse; dropping them would
// lose a terminal event to a missing newline.
func TestFinalFrameWithoutBlankLine(t *testing.T) {
	got := decode(t, `data: {"type":"response.completed","sequence_number":0}`)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
}

// TestLongEventExceedsBufioDefault is why the decoder sets its own buffer.
//
// bufio.Scanner caps a line at 64 KiB by default and fails past it, and a
// terminal event carries the complete final response — every word the agent
// produced. That is not an edge case, it is the normal end of a long run.
func TestLongEventExceedsBufioDefault(t *testing.T) {
	text := strings.Repeat("a", 256<<10) // 256 KiB, comfortably past bufio's 64 KiB
	stream := `data: {"type":"response.output_text.done","sequence_number":0,"text":"` + text + `"}` + "\n\n"

	got := decode(t, stream)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if len(got[0].Text) != len(text) {
		t.Errorf("text length = %d, want %d", len(got[0].Text), len(text))
	}
}

// TestEventNameFillsAnAbsentType covers the one repair the decoder makes.
//
// Streaming §1 requires the payload itself to carry `type`, so this is not the
// normal path — but the SSE `event:` line holds the same string, and handing
// back an event with no type at all when the answer is one line above is worse
// than using it.
func TestEventNameFillsAnAbsentType(t *testing.T) {
	got := decode(t, "event: response.completed\ndata: {\"sequence_number\":0}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != uhp.EventResponseCompleted {
		t.Errorf("Type = %q, want it filled from the event: line", got[0].Type)
	}
}

// TestPayloadTypeWinsOverEventName is the other half: the payload is the
// protocol object and the SSE line is framing, so where they disagree the
// object is authoritative.
func TestPayloadTypeWinsOverEventName(t *testing.T) {
	got := decode(t, "event: response.failed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0}\n\n")
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != uhp.EventResponseCompleted {
		t.Errorf("Type = %q, want the payload's own type", got[0].Type)
	}
}

func TestEmptyStreamIsEOF(t *testing.T) {
	if got := decode(t, ""); len(got) != 0 {
		t.Fatalf("got %d events from an empty stream, want 0", len(got))
	}
}
