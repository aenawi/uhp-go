package uhp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Stream is an open event stream: a task's own, or a harness's feed.
//
// Read it with [Stream.Next] until io.EOF, and Close it when you stop early.
// Every event is delivered in order with no gaps — [Stream.Next] checks the
// sequence numbers itself, so a dropped event is an error rather than a hole in
// whatever you render.
type Stream struct {
	body io.ReadCloser
	dec  *EventDecoder

	// next is the sequence number the next event must carry. Streaming §2:
	// "sequence_number MUST start at 0 and increase by exactly 1 per event."
	next int

	// resumeFrom is where a reconnection would restart, which is one past the
	// last event actually delivered.
	resumeFrom int

	closed bool
}

// GapError is a stream that skipped a sequence number.
//
// Streaming §2 makes this detectable on purpose — "so a client can detect a
// dropped event rather than silently rendering a gap" — and detecting it is
// worth nothing unless somebody reports it, so [Stream.Next] does rather than
// renumbering or continuing.
type GapError struct {
	Expected int
	Got      int
}

func (e *GapError) Error() string {
	return fmt.Sprintf("uhp: stream skipped from %d to %d; an event was dropped",
		e.Expected, e.Got)
}

// Next returns the next event, or io.EOF at the end of the stream.
//
// The end of the stream is not the same thing as the end of the task. A
// conformant stream carries exactly one terminal event — `response.completed`,
// `response.incomplete` or `response.failed` — and that event carries the whole
// final Response. A stream that ends without one ended early: the connection
// dropped, and the task is very likely still running. Use [Event.IsTerminal] to
// tell the two apart, and [Client.Get] to find out what happened either way.
func (s *Stream) Next() (Event, error) {
	ev, err := s.dec.Next()
	if err != nil {
		return Event{}, err
	}
	if ev.SequenceNumber != s.next {
		return Event{}, &GapError{Expected: s.next, Got: ev.SequenceNumber}
	}
	s.next = ev.SequenceNumber + 1
	s.resumeFrom = s.next
	return ev, nil
}

// ResumeFrom is the sequence number a reconnection should ask for, which is one
// past the last event this stream delivered. Pass it back as `Last-Event-ID`
// minus one — or use [Client.StreamHarness], which does the arithmetic.
func (s *Stream) ResumeFrom() int { return s.resumeFrom }

// LastEventID is the id the server put on the last event, for a resume that
// wants the server's own token rather than a computed number.
func (s *Stream) LastEventID() string { return s.dec.LastEventID() }

// Close releases the connection. Calling it twice is safe.
func (s *Stream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.body.Close()
}

// Stream posts POST /v1/responses with `stream: true` and returns the open
// stream.
//
// Nothing is buffered: the events arrive as the harness produces them, which is
// the point — a server that buffers a stream to completion is the single most
// common UHP deployment error, and a client that buffers one hides it.
//
// idempotencyKey should not be empty, for the reason [Client.Create] gives. A
// stream is not retried on failure whether or not it carries one: a retry would
// restart from sequence zero and re-render everything already shown. Reconnect
// with [Client.ResumeStream] instead, which picks up where this left off.
func (c *Client) Stream(
	ctx context.Context, req CreateResponseRequest, idempotencyKey string,
) (*Stream, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("uhp: encode request: %w", err)
	}

	header := http.Header{"Content-Type": []string{"application/json"}}
	if idempotencyKey != "" {
		header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.openEventStream(ctx, "/v1/responses", header, bytes.NewReader(body), 0)
}

// ResumeStream reopens a task's stream that dropped, starting after the last
// event the caller saw.
//
// `from` is [Stream.ResumeFrom] of the stream that died — the sequence number
// to start at, not the last one seen. Resumption is only possible against a
// stream the server still remembers, which for a task means the request that
// started it must be repeated with its original idempotency key: a POST without
// one starts a fresh task whose stream begins at zero.
//
// Servers MAY support this. One that does not answers an error, and the
// fallback is [Client.Get] — a stored response is always authoritative, and a
// stream is only ever an optimisation over polling for one.
func (c *Client) ResumeStream(
	ctx context.Context, req CreateResponseRequest, idempotencyKey string, from int,
) (*Stream, error) {
	if idempotencyKey == "" {
		return nil, errors.New(
			"uhp: resuming a task stream needs the Idempotency-Key of the request that started it")
	}
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("uhp: encode request: %w", err)
	}

	header := http.Header{
		"Content-Type":    []string{"application/json"},
		"Idempotency-Key": []string{idempotencyKey},
	}
	setLastEventID(header, from)
	return c.openEventStream(ctx, "/v1/responses", header, bytes.NewReader(body), from)
}

// StreamHarness opens GET /v1/harnesses/{id}/events: every task running on one
// harness, rather than the one task a request started.
//
// This is how a client that never held the original request follows the work —
// a dashboard, a second process, a page reloaded after the POST returned.
// `from` is where to start; zero is the beginning of what the server retains.
//
// A harness feed is bounded where a task's own stream is not, so a resume point
// the server has evicted is refused rather than served from wherever it happens
// to still hold. Reconnect from zero when that happens.
func (c *Client) StreamHarness(ctx context.Context, harnessID string, from int) (*Stream, error) {
	header := http.Header{}
	setLastEventID(header, from)
	return c.openEventStream(ctx,
		"/v1/harnesses/"+url.PathEscape(harnessID)+"/events", header, nil, from)
}

// setLastEventID writes the resume header, which names the last event *seen*
// rather than the next one wanted.
//
// The off-by-one is the specification's and is worth stating: resumption starts
// at the event after the one named, so asking to start at n means sending n-1.
// A `from` of zero means "from the beginning" and sends no header at all —
// sending `Last-Event-ID: -1` would be a number no server issued.
func setLastEventID(header http.Header, from int) {
	if from <= 0 {
		return
	}
	header.Set("Last-Event-ID", strconv.Itoa(from-1))
}

// openEventStream issues the request and wraps the body in a decoder.
func (c *Client) openEventStream(
	ctx context.Context, endpoint string, header http.Header, body io.Reader, from int,
) (*Stream, error) {
	if header == nil {
		header = http.Header{}
	}
	header.Set("Accept", "text/event-stream")
	header.Set("Cache-Control", "no-cache")

	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	rc, err := c.openStream(ctx, method, endpoint, header, body)
	if err != nil {
		return nil, err
	}
	return &Stream{
		body:       rc,
		dec:        NewEventDecoder(rc),
		next:       from,
		resumeFrom: from,
	}, nil
}
