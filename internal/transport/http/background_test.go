package http

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/aenawi/uhp-go/uhp"
)

// newBlockingServer is a server whose only harness holds its run open until the
// test releases it, which is what makes "answered before the run finished" a
// property rather than a race the echo harness happens to lose.
func newBlockingServer(t *testing.T) (*Server, *blockingAdapter) {
	t.Helper()
	a := newBlockingAdapter()
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	return NewServer(svc, slog.Default(), nil, 0), a
}

// The whole of #78 in one assertion: a `background: true` POST is answered as
// soon as the task is accepted, rather than being held open until the run is
// over. Against the blocking harness the run cannot possibly have finished, so
// a server that still waited would hang here rather than return the wrong
// status.
func TestABackgroundTaskIsAnsweredAsSoonAsItIsAccepted(t *testing.T) {
	srv, a := newBlockingServer(t)
	t.Cleanup(func() { close(a.release) })

	resp := createResponse(t, srv,
		`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`)

	if resp.Status != uhp.StatusInProgress {
		t.Fatalf("status = %q, want %q: the run is still blocked", resp.Status, uhp.StatusInProgress)
	}
	if !strings.HasPrefix(resp.ID, "resp_") {
		t.Errorf("id = %q, want a resp_ id to follow the task with", resp.ID)
	}
	// Tasks §3 makes the session id a MUST on every response, and a background
	// one is the response a client has most need of it on: it is the only thing
	// it was given, and everything it does next is addressed by id.
	if resp.Metadata["session_id"] == nil {
		t.Errorf("metadata.session_id is absent: %v", resp.Metadata)
	}
	if resp.Metadata["harness_id"] != "chrn_echo" {
		t.Errorf("metadata.harness_id = %v, want chrn_echo", resp.Metadata["harness_id"])
	}
	// The resolved budget is on the accepted object too, which is what makes it
	// actionable: a client that will not see this task again until it polls has
	// to be told now how long it may be waiting.
	if resp.Metadata["timeout_seconds"] == nil {
		t.Errorf("metadata.timeout_seconds is absent: %v", resp.Metadata)
	}
	// And it is a starting point rather than a result. Documented in ADR-0005
	// because a client reading the body as a finished response gets an empty
	// answer rather than an error.
	if len(resp.Output) != 0 {
		t.Errorf("output = %v on a task that has not run, want empty", resp.Output)
	}
}

// The half that makes the first half worth anything. Returning early is only
// useful if the work carries on and the result can be collected, and the
// collection point is the read endpoint that already answers mid-run.
func TestABackgroundTaskKeepsRunningAndIsReadBackWhenItIsDone(t *testing.T) {
	srv, a := newBlockingServer(t)

	resp := createResponse(t, srv,
		`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`)

	// Still in flight, and readable while it is: the POST returning is not the
	// run ending.
	select {
	case <-a.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the run never started")
	}
	mid := getResponse(t, srv, resp.ID)
	if mid.Status != uhp.StatusInProgress {
		t.Fatalf("status mid-run = %q, want %q", mid.Status, uhp.StatusInProgress)
	}

	a.release <- struct{}{}
	if got := awaitTerminal(t, srv, resp.ID); got.Status != uhp.StatusCompleted {
		t.Fatalf("status after the run finished = %q, want %q", got.Status, uhp.StatusCompleted)
	}
	if a.cancelled() {
		t.Error("the run was cancelled; a background POST returning must not stop the work")
	}
}

// `background` with `stream` is the one combination the schema permits and does
// not describe. It streams, exactly as it always did — the run is already
// detached and the stream already resumes from a `Last-Event-ID`, which is what
// `background` asks for in the only sense that differs from holding the POST
// open. So the field is honoured here rather than dropped.
func TestABackgroundStreamingRequestIsStillAStream(t *testing.T) {
	srv, a := newBlockingServer(t)
	t.Cleanup(func() { close(a.release) })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/responses", "application/json", strings.NewReader(
		`{"input":"hi","stream":true,"background":true,"metadata":{"harness_id":"chrn_echo"}}`))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream: background must not turn a "+
			"stream into a receipt", ct)
	}

	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended before response.created: %v", err)
		}
		if strings.TrimRight(line, "\r\n") == "event: response.created" {
			return
		}
	}
}

// The one combination that cannot be honoured, refused rather than half-done.
// `store: false` drops the record when the run reaches a terminal state, so a
// POST that returns before that has delivered nothing and every later read is a
// 404 — the client would be told to come back for an answer this server has
// already promised to throw away.
func TestBackgroundWithStoreFalseAndNoStreamIsRefused(t *testing.T) {
	srv, a := newBlockingServer(t)
	t.Cleanup(func() { close(a.release) })

	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(
		`{"input":"hi","background":true,"store":false,"metadata":{"harness_id":"chrn_echo"}}`))
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal is not JSON: %s", w.Body.String())
	}
	if body.Error.Code != "invalid_input" {
		t.Errorf("error.code = %q, want invalid_input", body.Error.Code)
	}
	// Named, because the refusal is about a combination and the client has to
	// be told which half to change.
	if body.Error.Param != "background" {
		t.Errorf("error.param = %q, want background", body.Error.Param)
	}
}

// The same two fields on a stream are fine, and the reason is the delivery: the
// terminal event carries the whole response before the record is dropped, so
// nothing is promised to a client that will not arrive.
func TestBackgroundWithStoreFalseIsAllowedOnAStream(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/responses", "application/json", strings.NewReader(
		`{"input":"hi","stream":true,"background":true,"store":false,"metadata":{"harness_id":"chrn_echo"}}`))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	deadline := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended before response.completed: %v", err)
		}
		if strings.TrimRight(line, "\r\n") == "event: response.completed" {
			return
		}
	}
}

// The second way to follow a background task, and the one that loses nothing:
// repeat the POST with the same `Idempotency-Key` and `stream: true`, and Tasks
// §6 hands the retry the first request's own run — so the stream replays the
// whole event log from `response.created`, including everything that happened
// while nobody was listening.
func TestABackgroundTaskCanBeFollowedByStreamingItsIdempotencyKey(t *testing.T) {
	srv, a := newBlockingServer(t)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	post := func(body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("POST", ts.URL+"/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key-78")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/responses: %v", err)
		}
		return resp
	}

	accepted := post(`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`)
	defer accepted.Body.Close()
	if accepted.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", accepted.StatusCode)
	}
	select {
	case <-a.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the run never started")
	}

	followed := post(`{"input":"hi","background":true,"stream":true,"metadata":{"harness_id":"chrn_echo"}}`)
	defer followed.Body.Close()
	if ct := followed.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	deadline := time.AfterFunc(10*time.Second, func() { followed.Body.Close() })
	defer deadline.Stop()

	a.release <- struct{}{}
	br := bufio.NewReader(followed.Body)
	var seen []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended after %v, without response.completed: %v", seen, err)
		}
		name, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "event: ")
		if !ok {
			continue
		}
		seen = append(seen, name)
		if name == "response.completed" {
			break
		}
	}
	// From the beginning, not from wherever the run had got to: the opening
	// event is the one a client following a task it did not stream cannot
	// afford to miss.
	if len(seen) == 0 || seen[0] != "response.created" {
		t.Errorf("events = %v, want the replay to start at response.created", seen)
	}
}

// Tasks §6: a retry carrying a key this server has seen is given the first
// request's answer, "rather than a partial or a conflict". A `store: false`
// response is gone from the store by the time its run is terminal, so a
// background retry that only asked the store would answer 404 — which is
// neither the first request's answer nor a truthful statement about anything.
//
// The retry does not have to repeat `store: false` to get here, which is why
// the refusal in handleCreateTask does not cover this case: the body below sets
// only `background`, and the response it names was never retained.
func TestABackgroundRetryOfAnUnretainedResponseIsAnsweredNot404(t *testing.T) {
	srv := newTestServer()
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key-store-false")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}

	if got := post(`{"input":"hi","store":false,"metadata":{"harness_id":"chrn_echo"}}`); got.Code != 200 {
		t.Fatalf("first request = %d, want 200: %s", got.Code, got.Body.String())
	}

	retry := post(`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`)
	if retry.Code != 200 {
		t.Fatalf("background retry = %d, want 200: %s", retry.Code, retry.Body.String())
	}
	var resp uhp.Response
	if err := json.Unmarshal(retry.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q: the retry is owed the first request's answer",
			resp.Status, uhp.StatusCompleted)
	}
}

// The pre-flight refusal must not fire on a retry, or the retry that faithfully
// repeats its original body is the one turned away while the retry that dropped
// a field is served. Tasks §6 owes both the first request's answer, and for a
// run that is already over the answer exists and can be delivered.
func TestAFaithfulBackgroundRetryIsAnsweredRatherThanRefused(t *testing.T) {
	srv := newTestServer()
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key-faithful")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}

	if got := post(`{"input":"hi","store":false,"metadata":{"harness_id":"chrn_echo"}}`); got.Code != 200 {
		t.Fatalf("first request = %d, want 200: %s", got.Code, got.Body.String())
	}

	// The same body the first request sent, plus the field under test — which
	// is what a client that decided to stop waiting actually sends.
	retry := post(`{"input":"hi","store":false,"background":true,"metadata":{"harness_id":"chrn_echo"}}`)
	if retry.Code != 200 {
		t.Fatalf("faithful retry = %d, want 200: %s", retry.Code, retry.Body.String())
	}
	var resp uhp.Response
	if err := json.Unmarshal(retry.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q", resp.Status, uhp.StatusCompleted)
	}
}

// The same refusal, reached by the route the body cannot describe. A retry is
// not obliged to repeat the `store: false` its first request sent, so a body
// carrying only `background: true` can name a run that will not be retained —
// and while that run is still in flight the store answers, so nothing about the
// request or the store looks wrong. Answering 200 `in_progress` there would
// promise a result that 404s for ever, which is the whole of what this refusal
// exists to prevent; the check therefore reads the accepted task rather than the
// body that arrived.
func TestABackgroundRetryOnAnUnretainedRunInFlightIsRefused(t *testing.T) {
	srv, a := newBlockingServer(t)
	t.Cleanup(func() { close(a.release) })
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key-in-flight")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}

	// The first request is streaming, so it is answered without this test
	// having to wait for a run that will not finish until the cleanup releases
	// it — and its response is not retained.
	go post(`{"input":"hi","stream":true,"store":false,"metadata":{"harness_id":"chrn_echo"}}`)
	select {
	case <-a.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the run never started")
	}

	retry := post(`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`)
	if retry.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", retry.Code, retry.Body.String())
	}
	var body struct {
		Error struct {
			Code   string         `json:"code"`
			Param  string         `json:"param"`
			Detail map[string]any `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal is not JSON: %s", retry.Body.String())
	}
	if body.Error.Code != "invalid_input" || body.Error.Param != "background" {
		t.Errorf("error = %+v, want invalid_input on background", body.Error)
	}
	// The run this request named is going either way, so a client told only
	// "no" is holding one it can neither follow nor stop.
	id, _ := body.Error.Detail["response_id"].(string)
	if !strings.HasPrefix(id, "resp_") {
		t.Errorf("error.detail.response_id = %v, want the id of the run that is running",
			body.Error.Detail)
	}
}

// A background POST that cannot read its own task back has still accepted the
// task, and the run is still going. A bare 500 leaves the client holding a run
// slot it can neither poll nor cancel, so the id — the handle for both — is on
// the refusal.
func TestABackgroundPostThatCannotReadItsTaskBackStillNamesIt(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(newBlockingAdapter())
	unreadable := &failingReadStore{Store: store.NewMemoryStore()}
	svc := service.NewTaskService(reg, unreadable, slog.Default())
	srv := NewServer(svc, slog.Default(), nil, 0)

	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(
		`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`))
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct {
			Detail map[string]any `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal is not JSON: %s", w.Body.String())
	}
	id, _ := body.Error.Detail["response_id"].(string)
	if !strings.HasPrefix(id, "resp_") {
		t.Errorf("error.detail.response_id = %v, want the id of the task that was accepted "+
			"and is still running", body.Error.Detail)
	}
}

// failingReadStore writes as normal and cannot read a task back, which is the
// shape of a transient storage failure between the write and the read a
// background POST makes.
type failingReadStore struct {
	service.Store
}

func (s *failingReadStore) GetTask(context.Context, string) (*domain.Task, bool, error) {
	return nil, false, errors.New("boom")
}

// `background: false` is what this server has always done, and it must go on
// doing it: the POST is held open until the run is over and the body is the
// finished response, not a receipt.
func TestBackgroundFalseStillHoldsThePostOpen(t *testing.T) {
	resp := createResponse(t, newTestServer(),
		`{"input":"hi","background":false,"metadata":{"harness_id":"chrn_echo"}}`)
	if resp.Status != uhp.StatusCompleted {
		t.Fatalf("status = %q, want %q", resp.Status, uhp.StatusCompleted)
	}
}

// getResponse reads a task back over the wire.
func getResponse(t *testing.T, srv *Server, id string) uhp.Response {
	t.Helper()
	w := do(t, srv, "GET", "/v1/responses/"+id, nil)
	if w.Code != 200 {
		t.Fatalf("GET /v1/responses/%s = %d: %s", id, w.Code, w.Body.String())
	}
	var resp uhp.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// awaitTerminal polls the read endpoint until the task is no longer running,
// which is exactly what a client of a background task does.
func awaitTerminal(t *testing.T, srv *Server, id string) uhp.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := getResponse(t, srv, id)
		if resp.Status != uhp.StatusInProgress {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task never reached a terminal state; last status %q", resp.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
