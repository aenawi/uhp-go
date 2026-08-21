package http

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sseEvent is one parsed frame off the wire: the `id:` line the resumption
// mechanism depends on, plus the JSON payload.
type sseEvent struct {
	ID   string
	Type string
	Data map[string]any
}

// readSSE reads n events off a live stream, failing rather than hanging if the
// stream dries up first.
func readSSE(t *testing.T, body *bufio.Reader, n int) []sseEvent {
	t.Helper()
	var evs []sseEvent
	cur := sseEvent{}
	for len(evs) < n {
		line, err := body.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended after %d of %d events: %v", len(evs), n, err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "id: "):
			cur.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.Data); err != nil {
				t.Fatalf("event data is not JSON: %v", err)
			}
		case line == "":
			if cur.Type != "" {
				evs = append(evs, cur)
			}
			cur = sseEvent{}
		}
	}
	return evs
}

// openFeed subscribes to a harness's live event feed against a real listener.
// httptest's recorder buffers the whole response, and a live feed never ends.
func openFeed(t *testing.T, ts *httptest.Server, harnessID, lastEventID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", ts.URL+"/v1/harnesses/"+harnessID+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// postTaskTo starts a task against a running listener and returns its id.
func postTaskTo(t *testing.T, ts *httptest.Server, input string) string {
	t.Helper()
	body := `{"input":"` + input + `","model":"m","metadata":{"harness_id":"echo"}}`
	resp, err := http.Post(ts.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("create task: %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return got["id"].(string)
}

// fillFeedPastItsWindow runs enough tasks that the harness feed has evicted
// its earliest events. Nothing is retained before that happens, so there is no
// gap to fall into and no window to be outside of.
func fillFeedPastItsWindow(t *testing.T, ts *httptest.Server) {
	t.Helper()
	// The echo harness produces eight events per task, and a feed keeps at
	// least its floor of 512 with a batch of slack above it before it trims.
	for i := 0; i < 110; i++ {
		postTaskTo(t, ts, "x")
	}
}

// Issue #8: a client that lost its connection — or that never held one — must
// be able to follow a harness's work without polling GET /v1/responses/{id}.
func TestHarnessEventsStreamsTheWorkOfThatHarness(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	t.Cleanup(ts.Close)

	id := postTaskTo(t, ts, "hello")

	resp := openFeed(t, ts, "echo", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	evs := readSSE(t, bufio.NewReader(resp.Body), 8)
	if evs[0].Type != "response.created" {
		t.Fatalf("first event is %q, want response.created", evs[0].Type)
	}
	for i, ev := range evs {
		if ev.ID != strconv.Itoa(i) {
			t.Fatalf("event %d carries id %q; without one there is nothing to resume from", i, ev.ID)
		}
		if ev.Data["response_id"] != id {
			t.Fatalf("event %d belongs to %v, want %s", i, ev.Data["response_id"], id)
		}
		if ev.Data["session_id"] == nil {
			t.Fatalf("event %d carries no session_id", i)
		}
	}
}

// A feed for a harness that does not exist would look exactly like one that
// has nothing to say, and the client would wait on it forever.
func TestHarnessEventsRefusesAnUnknownHarness(t *testing.T) {
	w := do(t, newTestServer(), "GET", "/v1/harnesses/chrn_nope/events", nil)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if code := body["error"].(map[string]any)["code"]; code != "harness_not_found" {
		t.Fatalf("code = %v, want harness_not_found", code)
	}
}

// Issue #8: resumption MUST start after the given sequence number and MUST NOT
// replay events the client already saw.
func TestHarnessEventsResumesAfterLastEventID(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	t.Cleanup(ts.Close)

	postTaskTo(t, ts, "one")
	second := postTaskTo(t, ts, "two")

	// The first task produces sequence numbers 0..7, so a client that saw all
	// of them reconnects with 7 and must be handed 8 onwards.
	resp := openFeed(t, ts, "echo", "7")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	evs := readSSE(t, bufio.NewReader(resp.Body), 1)
	if evs[0].ID != "8" {
		t.Fatalf("resumed at id %q, want 8", evs[0].ID)
	}
	if evs[0].Data["response_id"] != second {
		t.Fatalf("resumption replayed %v, which the client already had", evs[0].Data["response_id"])
	}
}

// A header this server could not have written is refused rather than ignored:
// ignoring it replays the stream from the beginning, which is the exact
// failure resumption exists to prevent, and the client cannot tell.
func TestHarnessEventsRefusesAnUnusableLastEventID(t *testing.T) {
	srv := newTestServer()
	// The last two are the overflow: an id at the top of the range parses
	// cleanly and is non-negative, and adding one to it wraps to a negative
	// resume point that every check downstream reads as "from the beginning".
	for _, raw := range []string{"not-a-number", "-1", "9223372036854775807", "2147483648"} {
		req := httptest.NewRequest("GET", "/v1/harnesses/echo/events", nil)
		req.Header.Set("Last-Event-ID", raw)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("Last-Event-ID %q: status = %d, want 400", raw, w.Code)
		}
	}
}

// A resume point older than the retained window is a gap, and the refusal has
// to name where the stream can be picked up — otherwise the client can only
// choose between replaying what it has and being refused forever.
func TestHarnessEventsRefusesADiscardedResumePoint(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	t.Cleanup(ts.Close)
	fillFeedPastItsWindow(t, ts)

	resp := openFeed(t, ts, "echo", "0")
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	payload := body["error"].(map[string]any)
	if payload["code"] != "uhpgo_event_gap" {
		t.Fatalf("code = %v, want uhpgo_event_gap", payload["code"])
	}
	detail, ok := payload["detail"].(map[string]any)
	if !ok || detail["oldest_retained"] == nil {
		t.Fatalf("detail = %v, want an oldest_retained the client can resume from", payload["detail"])
	}
}

// A subscriber that sends no Last-Event-ID is not resuming from zero — it is
// not resuming at all. Treating the two the same refuses every fresh
// subscriber to a busy harness for a gap it never asked to bridge, which turns
// the endpoint's ordinary use into its error case.
func TestHarnessEventsServesAFreshSubscriberToABusyFeed(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	t.Cleanup(ts.Close)
	fillFeedPastItsWindow(t, ts)

	resp := openFeed(t, ts, "echo", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 — a feed past its window still has a live stream", resp.StatusCode)
	}

	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	evs := readSSE(t, bufio.NewReader(resp.Body), 1)
	if evs[0].ID == "" {
		t.Fatal("the first event carries no id")
	}
}

// A resume point past the end of the stream names an event that was never
// produced. Opening a stream for it yields a 200 that carries nothing, which
// is indistinguishable from a harness that has gone quiet — so the client
// waits on a mistake it will never be told about.
func TestHarnessEventsRefusesAResumePointPastTheEnd(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	t.Cleanup(ts.Close)
	postTaskTo(t, ts, "hello")

	resp := openFeed(t, ts, "echo", "9000")
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	detail, ok := body["error"].(map[string]any)["detail"].(map[string]any)
	if !ok || detail["next_sequence_number"] == nil {
		t.Fatalf("detail = %v, want a next_sequence_number", body["error"])
	}
}

// A task's own stream carries ids too. Without them a reconnecting client has
// nothing to send back, and the retained log it would resume from may as well
// not exist.
func TestTaskStreamCarriesEventIDs(t *testing.T) {
	srv := newTestServer()
	resp := openStream(t, srv)

	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	evs := readSSE(t, bufio.NewReader(resp.Body), 2)
	if evs[0].ID != "0" || evs[1].ID != "1" {
		t.Fatalf("ids = %q,%q, want 0,1", evs[0].ID, evs[1].ID)
	}
}

// A POST that is not a replay starts a fresh task whose stream begins at 0.
// Honouring a resume point against it would silently swallow the beginning of
// a stream the client has never seen — its `response.created` and every delta
// before the resume point.
//
// A key that is merely well-formed is not enough. Keys live in memory and
// expire, so a retry can arrive with a perfectly good one that no longer names
// anything, and that is the case that actually happens.
func TestCreateTaskRefusesLastEventIDThatResumesNothing(t *testing.T) {
	for _, key := range []string{"", "never-used-before"} {
		srv := newTestServer()
		req := httptest.NewRequest("POST", "/v1/responses",
			strings.NewReader(`{"input":"hi","model":"m","stream":true,"metadata":{"harness_id":"echo"}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Last-Event-ID", "3")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}

		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("Idempotency-Key %q: status = %d, want 400", key, w.Code)
		}
	}
}

// The pair that does work: a retry carrying the original's key resumes the
// original's stream, and is handed only what it has not seen.
func TestIdempotentRetryResumesTheStream(t *testing.T) {
	ts := httptest.NewServer(newTestServer().Handler())
	t.Cleanup(ts.Close)

	const body = `{"input":"hi","model":"m","stream":true,"metadata":{"harness_id":"echo"}}`
	post := func(lastEventID string) *http.Response {
		req, err := http.NewRequest("POST", ts.URL+"/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key-resume")
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/responses: %v", err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	first := post("")
	if first.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", first.StatusCode)
	}
	readSSE(t, bufio.NewReader(first.Body), 4)

	retry := post("3")
	if retry.StatusCode != 200 {
		t.Fatalf("retry status = %d, want 200", retry.StatusCode)
	}
	evs := readSSE(t, bufio.NewReader(retry.Body), 1)
	if evs[0].ID != "4" {
		t.Fatalf("retry resumed at id %q, want 4", evs[0].ID)
	}

	// Past the end of the original's stream, the retry is refused rather than
	// handed a 200 that carries nothing.
	if past := post("9000"); past.StatusCode != 400 {
		t.Fatalf("resume past the end: status = %d, want 400", past.StatusCode)
	}
}
