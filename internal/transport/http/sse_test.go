package http

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
)

// openStream starts a streaming task against a real listener — httptest's
// recorder buffers the whole response, and a keep-alive that only shows up
// after the run has finished is not a keep-alive.
func openStream(t *testing.T, srv *Server) *http.Response {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())

	const body = `{"input":"hi","model":"m","stream":true,"metadata":{"harness_id":"echo"}}`
	resp, err := http.Post(ts.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		ts.Close()
		t.Fatalf("POST /v1/responses: %v", err)
	}
	t.Cleanup(func() {
		resp.Body.Close()
		ts.Close()
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return resp
}

// Issue #6 / UHP Errors §5: a server SHOULD emit a comment line at least every
// 30 seconds so a client running an inactivity timeout can tell a harness that
// is thinking from a connection that has dropped. An agent that spends two
// minutes on its first token used to produce two minutes of nothing, which on
// the wire is indistinguishable from a dead socket.
func TestStreamKeepsAliveWhileTheHarnessIsSilent(t *testing.T) {
	a := newBlockingAdapter()
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	srv := NewServer(svc, slog.Default(), nil, 0)
	srv.keepAlive = 5 * time.Millisecond
	t.Cleanup(func() { close(a.release) })

	resp := openStream(t, srv)

	// Closing the body unblocks the read, so a stream that never says anything
	// fails here rather than hanging until the package timeout.
	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before any keep-alive comment: %v", err)
		}
		if !strings.HasPrefix(line, ":") {
			continue
		}
		if got := strings.TrimRight(line, "\r\n"); got != ": keep-alive" {
			t.Fatalf("comment line = %q, want %q", got, ": keep-alive")
		}
		return
	}
}

// drippingAdapter says something and then keeps working, which is the shape of
// every real agent run: a first token long before a last one.
type drippingAdapter struct {
	echoAdapter
	release chan struct{}
}

func newDrippingAdapter() *drippingAdapter {
	return &drippingAdapter{release: make(chan struct{})}
}

func (a *drippingAdapter) Run(ctx context.Context, _ harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		send := func(u harness.RunUpdate) bool {
			select {
			case ch <- u:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(harness.RunUpdate{Type: harness.UpdateDelta, Delta: "first"}) {
			return
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return
		}
		send(harness.RunUpdate{Type: harness.UpdateCompleted})
	}()
	return ch, nil
}

// Issue #14 / Streaming §1: a delta must be on the wire when the harness
// produced it, not when the run is over.
//
// This is the invariant the conformance suite's S-09 is aiming at and cannot
// actually reach. S-09 measures the spread between the first and last events of
// a stream, and this server publishes `response.created` the instant a run
// starts — so the spread it measures is the whole duration of the task no
// matter what happens in between. Measured against grok on 2026-08-21: the
// suite passed S-09 twice, reporting spreads of 8.4s and 17.3s, and a stream
// from the same harness timed by hand carried `response.created` at 0.00s,
// nothing whatsoever until 8.09s, and its other 16 events inside the following
// 0.9s — silent for ninety per cent of the run, and passing.
//
// So the check cannot fail this server and cannot be relied on to notice if
// this stops being true. What can is a test that releases nothing: if the
// delta only arrives once the run has ended, this hangs until the deadline
// below rather than passing.
func TestADeltaReachesTheClientBeforeTheRunEnds(t *testing.T) {
	a := newDrippingAdapter()
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	srv := NewServer(svc, slog.Default(), nil, 0)
	// Long enough that a keep-alive cannot be mistaken for progress: if the
	// only thing this test reads is a comment line, it has proved nothing.
	srv.keepAlive = time.Hour
	t.Cleanup(func() { close(a.release) })

	resp := openStream(t, srv)

	// Closing the body unblocks the read, so a buffered stream fails here with
	// its own message rather than hanging until the package timeout.
	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the run is still in flight and no delta has arrived: %v", err)
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if strings.Contains(line, `"response.output_text.delta"`) {
			if !strings.Contains(line, `"first"`) {
				t.Fatalf("delta event does not carry the harness's text: %s", line)
			}
			return
		}
		if strings.Contains(line, `"response.completed"`) {
			t.Fatal("the run completed before any delta reached the client, which it cannot have: nothing released it")
		}
	}
}

// The interval is the one number the spec constrains, and every other test
// here overrides it to keep the clock out of the way — so without this, the
// default could drift past 30 seconds with the whole suite still green.
func TestDefaultKeepAliveBeatsTheProtocolBound(t *testing.T) {
	const bound = 30 * time.Second
	if defaultKeepAlive <= 0 || defaultKeepAlive >= bound {
		t.Fatalf("defaultKeepAlive = %s, want a positive interval under %s (Errors §5)",
			defaultKeepAlive, bound)
	}
	if srv := NewServer(nil, slog.Default(), nil, 0); srv.keepAlive != defaultKeepAlive {
		t.Fatalf("NewServer keepAlive = %s, want %s", srv.keepAlive, defaultKeepAlive)
	}
}

// A gap notice must clear the client's resume point, not set one and not leave
// the stale one in place. An EventSource reconnecting with an id that is
// already outside the window gets a 400, and a non-2xx makes the user agent
// fail the connection permanently — so leaving the id alone ends the very
// stream this notice exists to keep alive.
func TestGapNoticeClearsTheEventID(t *testing.T) {
	var sb strings.Builder
	if err := writeSSEClearingID(&sb, domain.Event{Type: "error", Seq: 12, Code: "uhpgo_event_gap"}); err != nil {
		t.Fatalf("writeSSEClearingID: %v", err)
	}
	if !strings.HasPrefix(sb.String(), "id: \nevent: error\n") {
		t.Fatalf("wrote %q, want an empty id line before the event", sb.String())
	}
}

// A comment is not an event. If it were parsed as one, a client counting
// events or reading `data:` would see a phantom with no type and no sequence
// number, which is worse than the silence it replaced.
func TestKeepAliveCarriesNoData(t *testing.T) {
	var sb strings.Builder
	if err := writeKeepAlive(&sb); err != nil {
		t.Fatalf("writeKeepAlive: %v", err)
	}
	if sb.String() != ": keep-alive\n\n" {
		t.Fatalf("wrote %q, want %q", sb.String(), ": keep-alive\n\n")
	}
}
