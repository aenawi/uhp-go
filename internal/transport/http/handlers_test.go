package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

type echoAdapter struct{}

func (echoAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{ID: "chrn_echo", Base: "echo", Name: "Echo", Object: "harness"},
		// Reported, because a real adapter computes it from a health check and
		// never leaves it empty. It became load-bearing with issue #53:
		// DefaultHarness counts the *ready* harnesses, so a double with no
		// status is a server with nothing to run — which is not the thing any
		// test here means to set up.
		Status: uhpgo.HarnessReady,
	}
}
func (echoAdapter) HealthCheck(ctx context.Context) error { return nil }
func (echoAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "ok"}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}
func (echoAdapter) Cancel(ctx context.Context, taskID string) error { return nil }

// echoAdapter stands in for a runtime that enforces everything natively, so
// the configuration-delivery paths are exercised rather than skipped. See
// plainAdapter for the opposite case.
func (echoAdapter) Delivery() harness.Delivery {
	return harness.Delivery{MCPServers: true, ToolBlock: true, Skills: true}
}

func newTestServer() *Server {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	memStore := store.NewMemoryStore()
	svc := service.NewTaskService(reg, memStore, slog.Default())
	return NewServer(svc, slog.Default(), nil, 0)
}

func TestCreateTaskNonStreaming(t *testing.T) {
	srv := newTestServer()
	body := `{"input":"hi","model":"m","metadata":{"harness_id":"echo"}}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var task domain.Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if task.Text() != "ok" {
		t.Fatalf("expected output 'ok', got %q", task.Text())
	}
	if task.Status != uhp.StatusCompleted {
		t.Fatalf("expected completed status, got %s", task.Status)
	}
}

func TestListHarnesses(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/harnesses", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// Healthz is unauthenticated and carries no JSON, which is what a container
// probe needs: no credential to configure and nothing to parse. It is served
// against a stand-in that implements nothing, because the point is that it
// answers without asking the service anything at all — a probe that went to
// the store would report a busy server as an unhealthy one.
func TestHealthz(t *testing.T) {
	w := do(t, newFakeServer(&fakeService{}), "GET", "/healthz", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "ok" {
		t.Fatalf("expected %q, got %q", "ok", got)
	}
}

// Tasks §1.2: a task that names no harness is not a malformed task. This used
// to assert the 400 the server answered, which was the defect rather than the
// contract (issue #53) — `{"input":"hi"}` is the smallest body the schema
// permits and the first thing anyone reading the specification will send.
func TestATaskNamingNoHarnessIsServedByTheDefault(t *testing.T) {
	srv := newTestServer()
	body := `{"input":"hi"}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp uhp.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The half of the MUST that is easy to drop: a server that chooses
	// silently leaves the client unable to tell what ran.
	if got := resp.Metadata["harness_id"]; got != "chrn_echo" {
		t.Fatalf("metadata.harness_id = %v, want chrn_echo", got)
	}
}

// The refusal that remains legitimate: two ready harnesses and no configured
// default. It must name the field the client omitted and list what it should
// have chosen from, because the client is being refused for exercising a
// permission the specification gave it.
func TestAnAmbiguousDefaultIsRefusedWithTheCandidates(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	reg.Register(secondEchoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	srv := NewServer(svc, slog.Default(), nil, 0)

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":"hi"}`))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var env uhp.ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Param == nil || *env.Error.Param != "metadata.harness_id" {
		t.Fatalf("param = %v, want metadata.harness_id", env.Error.Param)
	}
	harnesses, _ := env.Error.Detail["harnesses"].([]any)
	if len(harnesses) != 2 {
		t.Fatalf("detail.harnesses = %v, want both ids so the client can choose", env.Error.Detail["harnesses"])
	}
}

// secondEchoAdapter is a second ready harness, distinct only in id.
type secondEchoAdapter struct{ echoAdapter }

func (secondEchoAdapter) Info() uhpgo.Harness {
	info := echoAdapter{}.Info()
	info.ID = "chrn_echo2"
	info.Base = "echo2"
	return info
}

// blockingAdapter keeps its run in flight until the test releases it, so the
// server can be observed while a task genuinely holds a run slot.
type blockingAdapter struct {
	echoAdapter
	once    sync.Once
	started chan struct{}
	release chan struct{}

	// stopped records whether Cancel was ever called. It is the only way to
	// tell "this run was asked to stop" from "this run ended on its own": both
	// look the same from the wire, which is what makes an endpoint that
	// wrongly cancels so easy to ship. See TestDeletingAResponseDoesNotStopTheRun.
	mu      sync.Mutex
	stopped bool
}

func newBlockingAdapter() *blockingAdapter {
	return &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
}

func (a *blockingAdapter) Cancel(context.Context, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	return nil
}

func (a *blockingAdapter) cancelled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

func (a *blockingAdapter) Run(ctx context.Context, _ harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		select {
		case <-a.release:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- harness.RunUpdate{Type: harness.UpdateCompleted}:
		case <-ctx.Done():
		}
	}()
	a.once.Do(func() { close(a.started) })
	return ch, nil
}

// Issue #5: a saturated server must answer 503 `harness_unavailable`, not a
// 4xx. Errors §4 makes the class the retry signal, and the request is not
// wrong — it arrived at a bad moment, and retrying is exactly what will work.
func TestSaturatedServerRefusesWith503(t *testing.T) {
	a := newBlockingAdapter()
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		service.WithMaxConcurrentRuns(1))
	srv := NewServer(svc, slog.Default(), nil, 0)

	const body = `{"input":"hi","model":"m","metadata":{"harness_id":"echo"}}`
	inFlight := make(chan struct{})
	go func() {
		defer close(inFlight)
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}()
	select {
	case <-a.started:
	case <-time.After(10 * time.Second):
		// Rather than block here until the package timeout kills the whole run
		// and reports nothing about which test was stuck.
		t.Fatal("the first request never reached the adapter, so no slot was ever held")
	}
	defer func() {
		close(a.release)
		<-inFlight
	}()

	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(body))
	var decoded map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body is not JSON (%d): %s", w.Code, w.Body.String())
	}
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503: %v", w.Code, decoded)
	}
	if code := errorCode(t, decoded); code != "harness_unavailable" {
		t.Fatalf("code = %q, want harness_unavailable", code)
	}
	// Told to retry and not told when is an invitation to hot-loop.
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Error("a retryable 503 carries no Retry-After")
	}
	detail, _ := decoded["error"].(map[string]any)["detail"].(map[string]any)
	if detail["max_concurrent_runs"] != float64(1) {
		t.Errorf("detail.max_concurrent_runs = %v, want 1; a client cannot size its own "+
			"concurrency against a bound it is not told", detail["max_concurrent_runs"])
	}
}

// postTask sends one POST /v1/responses, optionally carrying an idempotency
// key. `do` cannot set an arbitrary header, and the header is the whole point
// of the tests below.
func postTask(t *testing.T, srv *Server, key string) (int, map[string]any) {
	t.Helper()
	const body = `{"input":"hi","model":"m","metadata":{"harness_id":"echo"}}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var decoded map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("body is not JSON (%d): %s", w.Code, w.Body.String())
		}
	}
	return w.Code, decoded
}

// Issue #7 / Tasks §6: a retry carrying the first request's Idempotency-Key
// gets the first request's response.
func TestIdempotentRetryReturnsTheFirstResponse(t *testing.T) {
	srv := newTestServer()

	code, first := postTask(t, srv, "retry-me")
	if code != 200 {
		t.Fatalf("status = %d, want 200: %v", code, first)
	}
	code, second := postTask(t, srv, "retry-me")
	if code != 200 {
		t.Fatalf("retry status = %d, want 200: %v", code, second)
	}
	if second["id"] != first["id"] {
		t.Fatalf("retry returned response %v, want the first request's %v", second["id"], first["id"])
	}
}

// The header is opt-in, and a client that omits it gets what it has always
// got: a new task per request.
func TestPostsWithoutAnIdempotencyKeyAreIndependent(t *testing.T) {
	srv := newTestServer()

	_, first := postTask(t, srv, "")
	_, second := postTask(t, srv, "")
	if second["id"] == first["id"] {
		t.Fatalf("two keyless posts both returned %v", first["id"])
	}
}

// A key is remembered for a day, so an absurd one is refused rather than
// stored. Errors §3.1's `invalid_input` is the closest code and it fits: the
// request is malformed, and no retry of it unchanged will work.
func TestOverlongIdempotencyKeyIsRefused(t *testing.T) {
	srv := newTestServer()

	code, body := postTask(t, srv, strings.Repeat("k", maxIdempotencyKeyBytes+1))
	if code != 400 {
		t.Fatalf("status = %d, want 400: %v", code, body)
	}
	if got := errorCode(t, body); got != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", got)
	}

	code, body = postTask(t, srv, strings.Repeat("k", maxIdempotencyKeyBytes))
	if code != 200 {
		t.Fatalf("a key exactly at the limit was refused: %d %v", code, body)
	}
}

// Lifecycle §2: a capability MUST report what the server actually does. This
// one read `false` for as long as it was unimplemented; it must move now that
// it is not.
func TestIdempotencyIsAdvertised(t *testing.T) {
	srv := newTestServer()
	_, doc := callJSON(t, srv, "GET", "/v1/uhp", "")
	caps, _ := doc["capabilities"].(map[string]any)
	if caps["idempotency"] != true {
		t.Fatalf("capabilities.idempotency = %v, want true", caps["idempotency"])
	}
}
