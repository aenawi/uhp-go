package http

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

// createResponse sends a body against srv and returns the decoded response
// object.
func createResponse(t *testing.T, srv *Server, body string) uhp.Response {
	t.Helper()
	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(body))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp uhp.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// The eighth of thirteen. #48 called `store` the odd one out: not merely
// unimplemented but inert — the field existed on the task, was hardcoded true,
// and the request's own value was never read.
func TestAStoreFalseResponseIsAnsweredInFullAndThenIsGone(t *testing.T) {
	srv := newTestServer()
	resp := createResponse(t, srv, `{"input":"hi","store":false,"metadata":{"harness_id":"chrn_echo"}}`)

	// Answered in full: this is the one delivery the client gets, and it has
	// to be the whole object rather than a receipt.
	if resp.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q", resp.Status, uhp.StatusCompleted)
	}
	if resp.Store {
		t.Error("store = true on the response to a store:false request")
	}
	if len(resp.Output) == 0 {
		t.Error("the response carries no output")
	}

	// Tasks §4 permits exactly this, which is what makes reading the field
	// worth anything.
	w := do(t, srv, "GET", "/v1/responses/"+resp.ID, nil)
	if w.Code != 404 {
		t.Fatalf("GET after the run = %d, want 404: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "response_not_found" {
		t.Errorf("error.code = %v, want response_not_found", errObj["code"])
	}
}

// The default is true, and a request that says nothing about `store` behaves
// exactly as every request did before the field was read.
func TestAResponseIsStillReadableWhenTheRequestSaysNothing(t *testing.T) {
	srv := newTestServer()
	resp := createResponse(t, srv, `{"input":"hi","metadata":{"harness_id":"chrn_echo"}}`)
	if !resp.Store {
		t.Error("store = false for a request that never mentioned it")
	}
	if w := do(t, srv, "GET", "/v1/responses/"+resp.ID, nil); w.Code != 200 {
		t.Fatalf("GET = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// `store: true` is the default said out loud, and must behave as the default.
func TestAnExplicitStoreTrueIsRetained(t *testing.T) {
	srv := newTestServer()
	resp := createResponse(t, srv, `{"input":"hi","store":true,"metadata":{"harness_id":"chrn_echo"}}`)
	if !resp.Store {
		t.Error("store = false for an explicit store:true request")
	}
	if w := do(t, srv, "GET", "/v1/responses/"+resp.ID, nil); w.Code != 200 {
		t.Fatalf("GET = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// The input items endpoint reads the same record, so it goes with it. This is
// asserted rather than assumed: "the response is gone" has to mean every read
// of it, not the one endpoint that was tested.
func TestADroppedResponseTakesItsInputItemsWithIt(t *testing.T) {
	srv := newTestServer()
	resp := createResponse(t, srv, `{"input":"hi","store":false,"metadata":{"harness_id":"chrn_echo"}}`)
	if w := do(t, srv, "GET", "/v1/responses/"+resp.ID+"/input_items", nil); w.Code != 404 {
		t.Fatalf("GET input_items = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// Tasks §6: a retry gets the first request's answer. The row is gone by then,
// and the answer is not — anything else makes the replay differ from the
// original.
func TestAnIdempotentRetryOfADroppedResponseStillAnswers(t *testing.T) {
	srv := newTestServer()
	const body = `{"input":"hi","store":false,"metadata":{"harness_id":"chrn_echo"}}`

	first := do(t, srv, "POST", "/v1/responses", strings.NewReader(body))
	if first.Code != 200 {
		t.Fatalf("first = %d: %s", first.Code, first.Body.String())
	}
	// The same key twice, which is what a client recovering from a timeout
	// sends.
	keyed := func() (int, uhp.Response) {
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "retry-me")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		var resp uhp.Response
		if w.Body.Len() > 0 {
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
		}
		return w.Code, resp
	}
	code, original := keyed()
	if code != 200 {
		t.Fatalf("keyed first = %d", code)
	}
	code, replay := keyed()
	if code != 200 {
		t.Fatalf("keyed retry = %d, want 200: a dropped response must not break §6 replay", code)
	}
	if replay.ID != original.ID {
		t.Errorf("the retry started a second task: %q != %q", replay.ID, original.ID)
	}
	if len(replay.Output) == 0 {
		t.Error("the replay carries no output; the client was answered with less than the original")
	}
}
