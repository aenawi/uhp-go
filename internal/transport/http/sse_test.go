package http

import (
	"bufio"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
