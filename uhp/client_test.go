package uhp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubServer is a UHP server that answers whatever a test tells it to. It is
// deliberately not this repository's server: what these tests check is that the
// client follows the protocol, and pointing them at the implementation next
// door would let both halves drift together and stay green.
func stubServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "test-key")
	c.RetryBase = time.Millisecond
	return c, srv
}

func TestClientSendsTheProtocolHeaders(t *testing.T) {
	var got *http.Request
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("UHP-Version", Version)
		_ = json.NewEncoder(w).Encode(Discovery{Object: "uhp.discovery"})
	})

	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got.Header.Get("UHP-Version") != Version {
		t.Errorf("UHP-Version = %q, want %q", got.Header.Get("UHP-Version"), Version)
	}
	if got.Header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
	}
}

// Lifecycle §1: "It MUST NOT silently serve a different version." Silently is
// the operative word — a client that ignores the answer is decoding one
// version's shapes out of another's bytes and will not find out until a field
// is missing.
func TestClientRefusesAVersionItDidNotAskFor(t *testing.T) {
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("UHP-Version", "2029-01-01")
		_ = json.NewEncoder(w).Encode(Discovery{Object: "uhp.discovery"})
	})

	_, err := c.Discover(context.Background())
	var mismatch *VersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected a VersionMismatchError, got %v", err)
	}
	if mismatch.Served != "2029-01-01" {
		t.Errorf("Served = %q", mismatch.Served)
	}
}

// A server that sends no version header at all is tolerated: the header is a
// MUST on the server, but a proxy that strips unknown headers is a real
// deployment, and failing every request over one is worse than proceeding.
func TestClientToleratesAMissingVersionHeader(t *testing.T) {
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Discovery{Object: "uhp.discovery"})
	})
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}
}

func TestClientDecodesTheErrorEnvelope(t *testing.T) {
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("UHP-Version", Version)
		w.WriteHeader(http.StatusConflict)
		param := "previous_response_id"
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: Error{
			Type: ErrorTypeInvalidRequest, Code: CodeSessionBusy,
			Message: "a task is already running in this session", Param: &param,
		}})
	})

	_, err := c.Get(context.Background(), "resp_x")
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("expected a *uhp.Error, got %T: %v", err, err)
	}
	if e.Code != CodeSessionBusy || e.Type != ErrorTypeInvalidRequest {
		t.Errorf("error = %+v", e)
	}
	if e.Param == nil || *e.Param != "previous_response_id" {
		t.Errorf("param = %v", e.Param)
	}
}

// A proxy or load balancer that never read Errors §1 still has to produce
// something a caller can switch on, or every call site needs a second branch
// for "and sometimes it is not the envelope".
func TestClientBuildsAnErrorFromANonEnvelopeBody(t *testing.T) {
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	})
	c.MaxRetries = -1

	_, err := c.Get(context.Background(), "resp_x")
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("expected a *uhp.Error, got %T: %v", err, err)
	}
	if e.Type != ErrorTypeServerError {
		t.Errorf("type = %q, want %s — the status is the only signal here", e.Type, ErrorTypeServerError)
	}
}

// Errors §4 makes the *class* the retry signal. A 503 is worth retrying
// whatever the server called it, which is what makes an unrecognised code safe.
func TestClientRetriesAServerError(t *testing.T) {
	var calls int32
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("UHP-Version", Version)
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: Error{
				Type: ErrorTypeServerError, Code: CodeHarnessUnavailable, Message: "no capacity",
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(Response{ID: "resp_ok", Object: "response"})
	})

	resp, err := c.Get(context.Background(), "resp_ok")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.ID != "resp_ok" {
		t.Errorf("id = %q", resp.ID)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

// `quota_exhausted` arrives as a 429, a status this would otherwise retry, and
// means the opposite of "come back shortly": Errors §4 says retrying unchanged
// will not help. Retrying it burns the caller's own budget on nothing.
func TestClientDoesNotRetryQuotaExhausted(t *testing.T) {
	var calls int32
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("UHP-Version", Version)
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: Error{
			Type: ErrorTypeRateLimit, Code: CodeQuotaExhausted, Message: "budget spent",
		}})
	})

	if _, err := c.Get(context.Background(), "resp_x"); err == nil {
		t.Fatal("expected a failure")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 — quota_exhausted is not worth retrying", got)
	}
}

// Errors §4: "Retries of POST /v1/responses MUST carry an Idempotency-Key."
// A client that retries one without a key runs expensive, side-effecting work a
// second time while the first attempt may still be going — so this client does
// not retry that request at all.
func TestClientDoesNotRetryATaskWithoutAnIdempotencyKey(t *testing.T) {
	var calls int32
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("UHP-Version", Version)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: Error{
			Type: ErrorTypeServerError, Code: CodeHarnessUnavailable, Message: "no capacity",
		}})
	})

	if _, err := c.Create(context.Background(), CreateResponseRequest{Input: "hi"}, ""); err == nil {
		t.Fatal("expected a failure")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 — a keyless task creation must not be retried", got)
	}

	atomic.StoreInt32(&calls, 0)
	if _, err := c.Create(context.Background(), CreateResponseRequest{Input: "hi"}, "key-1"); err == nil {
		t.Fatal("expected a failure")
	}
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Errorf("calls = %d, want 4 — with a key, the same failure is retried", got)
	}
}

// A retried request has to send its body again. A *http.Request's Body is
// consumed by the first send, so replaying one without rewinding sends an empty
// body — which the server reports as invalid input, on a retry of a request
// that was fine.
func TestClientReplaysTheBodyOnARetry(t *testing.T) {
	var bodies []string
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("UHP-Version", Version)
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(Response{ID: "resp_ok", Object: "response"})
	})

	if _, err := c.Create(context.Background(),
		CreateResponseRequest{Input: "summarise"}, "key-1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	if bodies[0] != bodies[1] {
		t.Errorf("the retry sent a different body:\n first: %s\nsecond: %s", bodies[0], bodies[1])
	}
	if !strings.Contains(bodies[1], "summarise") {
		t.Errorf("the retry sent an empty body: %q", bodies[1])
	}
}

// A base URL with a path prefix is a normal deployment, and dropping the prefix
// sends every request to the wrong place.
func TestClientPreservesABaseURLPathPrefix(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("UHP-Version", Version)
		_ = json.NewEncoder(w).Encode(Discovery{Object: "uhp.discovery"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/uhp/", "")
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if path != "/uhp/v1/uhp" {
		t.Errorf("path = %q, want /uhp/v1/uhp", path)
	}
}

// Streaming §2: sequence numbers start at 0 and increase by exactly 1, "so a
// client can detect a dropped event rather than silently rendering a gap".
// Detecting it is worth nothing unless the client reports it.
func TestStreamReportsAGap(t *testing.T) {
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("UHP-Version", Version)
		for _, n := range []int{0, 1, 5} {
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":%d,\"delta\":\"x\"}\n\n", n)
		}
	})

	stream, err := c.Stream(context.Background(), CreateResponseRequest{Input: "hi"}, "key-1")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	for i := 0; i < 2; i++ {
		if _, err := stream.Next(); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	_, err = stream.Next()
	var gap *GapError
	if !errors.As(err, &gap) {
		t.Fatalf("expected a GapError, got %v", err)
	}
	if gap.Expected != 2 || gap.Got != 5 {
		t.Errorf("gap = %+v, want expected 2 got 5", gap)
	}
}

// Resumption starts at the event *after* the one named, so a resume point of n
// is sent as `Last-Event-ID: n-1`. Getting this off by one either replays an
// event the client has already rendered or skips one it never saw.
func TestResumingAStreamSendsTheEventBeforeTheResumePoint(t *testing.T) {
	var lastEventID string
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		lastEventID = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("UHP-Version", Version)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":7}\n\n")
	})

	stream, err := c.StreamHarness(context.Background(), "chrn_x", 7)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if lastEventID != "6" {
		t.Fatalf("Last-Event-ID = %q, want 6 — resumption starts after the id named", lastEventID)
	}
	// And the sequence check starts from the resume point rather than from
	// zero, or the first resumed event would look like a gap.
	ev, err := stream.Next()
	if err != nil {
		t.Fatalf("first resumed event: %v", err)
	}
	if !ev.IsTerminal() {
		t.Errorf("event = %+v, want the terminal event", ev)
	}
}

// A resume point of zero means "from the beginning" and must send no header:
// `Last-Event-ID: -1` is a number no server ever issued.
func TestStreamingFromZeroSendsNoResumeHeader(t *testing.T) {
	var present bool
	c, _ := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Last-Event-Id"]
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("UHP-Version", Version)
	})

	stream, err := c.StreamHarness(context.Background(), "chrn_x", 0)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	_ = stream.Close()
	if present {
		t.Error("a stream from the beginning sent a Last-Event-ID")
	}
}
