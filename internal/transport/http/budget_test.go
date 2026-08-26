package http

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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

// defaultBudgetSeconds is the deployment's own bound, in the units the wire
// uses. newTestServer configures none, so this is what every task below that
// does not narrow it is given.
var defaultBudgetSeconds = int(service.DefaultTaskBudget / time.Second)

// createdWith posts a task and returns the response object it was answered
// with. The server is a real one: what these tests are about is a request field
// surviving the transport *and* being acted on, and a double that recorded the
// call would only prove the first half.
func createdWith(t *testing.T, body string) map[string]any {
	t.Helper()
	w := do(t, newTestServer(), "POST", "/v1/responses", strings.NewReader(body))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}

func budgetOf(t *testing.T, response map[string]any) float64 {
	t.Helper()
	meta, ok := response["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("the response carries no metadata object: %+v", response)
	}
	seconds, ok := meta["timeout_seconds"].(float64)
	if !ok {
		t.Fatalf("metadata.timeout_seconds = %v, want a number", meta["timeout_seconds"])
	}
	return seconds
}

// Issue #54: `timeout_seconds` was accepted, stored, echoed back and never
// applied. Reading it here is what makes the enforcement behind it mean
// something to a client.
func TestARequestBudgetIsReadAndReported(t *testing.T) {
	if got := budgetOf(t, createdWith(t, `{"input":"hi","timeout_seconds":45,"metadata":{"harness_id":"echo"}}`)); got != 45 {
		t.Errorf("metadata.timeout_seconds = %v, want 45", got)
	}
}

// A request that names no budget still gets one, because Security §5 makes
// bounding a task the server's obligation rather than the client's option.
func TestATaskThatNamesNoBudgetStillGetsOne(t *testing.T) {
	want := float64(defaultBudgetSeconds)
	if got := budgetOf(t, createdWith(t, `{"input":"hi","metadata":{"harness_id":"echo"}}`)); got != want {
		t.Errorf("metadata.timeout_seconds = %v, want the server default %v", got, want)
	}
}

// Refused rather than ignored. A zero-second budget has no useful reading —
// "stop before starting" is nobody's intent — and quietly substituting the
// server's own is the shape of defect #54 is about: a field the client set and
// the server discarded without saying so.
func TestANonPositiveRequestBudgetIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"input":"hi","timeout_seconds":0}`,
		`{"input":"hi","timeout_seconds":-30}`,
	} {
		t.Run(body, func(t *testing.T) {
			w := do(t, newTestServer(), "POST", "/v1/responses", strings.NewReader(body))
			if w.Code != 400 {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			var env uhp.ErrorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if env.Error.Code != uhp.CodeInvalidInput {
				t.Errorf("code = %q, want %q", env.Error.Code, uhp.CodeInvalidInput)
			}
			// Errors §1: the dotted path to the offending field, so a client
			// knows which of the thirteen request fields it got wrong.
			if env.Error.Param == nil || *env.Error.Param != "timeout_seconds" {
				t.Errorf("param = %v, want \"timeout_seconds\"", env.Error.Param)
			}
		})
	}
}

// stallingAdapter models a real CLI that has wedged: it says one thing and then
// runs until its own Cancel is called, exactly as internal/harness/process.go
// does — the terminal update comes from the runner after the process group is
// torn down, not from the caller's context.
type stallingAdapter struct {
	echoAdapter

	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

func newStallingAdapter() *stallingAdapter {
	return &stallingAdapter{cancel: make(map[string]context.CancelFunc)}
}

func (*stallingAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{ID: "chrn_stalling", Base: "echo", Object: "harness", Name: "Stalling"},
		Models:  []string{"echo-1"},
		Capabilities: []uhpgo.Capability{
			uhpgo.CapStreaming, uhpgo.CapSessions, uhpgo.CapCancellation,
		},
		Status: uhpgo.HarnessReady,
	}
}

func (a *stallingAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel[req.TaskID] = cancel
	a.mu.Unlock()

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		defer cancel()
		send := func(u harness.RunUpdate) bool {
			select {
			case ch <- u:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(harness.RunUpdate{Type: harness.UpdateDelta, Delta: "partial"}) {
			return
		}
		<-runCtx.Done()
		send(harness.RunUpdate{Type: harness.UpdateCancelled})
	}()
	return ch, nil
}

func (a *stallingAdapter) Cancel(_ context.Context, taskID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.cancel[taskID]; ok {
		c()
		delete(a.cancel, taskID)
	}
	return nil
}

// Streaming §3 lists `response.incomplete` as one of the three terminal events,
// and before #54 it was one this server could never emit. Read off a real
// socket rather than a recorder, because the terminal frame is the last thing
// on the wire and a buffered recorder cannot tell "last" from "only".
func TestAnExpiredStreamTerminatesWithResponseIncomplete(t *testing.T) {
	a := newStallingAdapter()
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		service.WithTaskBudget(150*time.Millisecond))
	srv := NewServer(svc, slog.Default(), nil, 0)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"input":"hi","stream":true,"metadata":{"harness_id":"chrn_stalling"}}`))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()

	// A stream that never terminates fails here rather than hanging until the
	// package timeout — which is the failure this test exists to catch.
	deadline := time.AfterFunc(20*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()

	var events []string
	var lastData string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			events = append(events, strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "data: "):
			lastData = strings.TrimPrefix(line, "data: ")
		}
	}
	if len(events) == 0 {
		t.Fatalf("the stream carried no events: %v", sc.Err())
	}
	if last := events[len(events)-1]; last != "response.incomplete" {
		t.Fatalf("terminal event = %q, want %q; the whole stream was %v",
			last, "response.incomplete", events)
	}

	var ev uhp.Event
	if err := json.Unmarshal([]byte(lastData), &ev); err != nil {
		t.Fatalf("unmarshal terminal event: %v", err)
	}
	if ev.Response == nil {
		t.Fatal("the terminal event carries no response object")
	}
	if ev.Response.Status != uhp.StatusIncomplete {
		t.Errorf("status = %q, want %q", ev.Response.Status, uhp.StatusIncomplete)
	}
	if ev.Response.Error != nil {
		t.Errorf("error = %+v, want null: a budget is not a failure", ev.Response.Error)
	}
	if ev.Response.IncompleteDetails["reason"] != "timeout" {
		t.Errorf("incomplete_details = %+v, want reason \"timeout\"", ev.Response.IncompleteDetails)
	}
	// Lifecycle §3: "Terminal responses MUST retain whatever output was
	// produced before they became terminal."
	if text := (&domain.Task{Response: *ev.Response}).Text(); text != "partial" {
		t.Errorf("output = %q, want %q", text, "partial")
	}
}
