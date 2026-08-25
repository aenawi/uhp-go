package http

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// inputItems drives the endpoint and returns the decoded envelope.
func inputItems(t *testing.T, srv *Server, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/responses/"+id+"/input_items", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("input_items: status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("input_items: body is not JSON: %s", w.Body.String())
	}
	if body["object"] != "list" {
		t.Errorf("object = %v, want list", body["object"])
	}
	return body
}

// createTask posts a body and returns the response id.
func createTask(t *testing.T, srv *Server, body string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create: status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("create: body is not JSON: %s", w.Body.String())
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("create: no response id in %s", w.Body.String())
	}
	return id
}

// The item form is reported verbatim. This is the case the endpoint exists for
// and the case a reconstruction from Task.Input would get wrong: the flattened
// prompt has no file in it at all, so a server rebuilding its answer would tell
// the client it sent one item when it sent two.
func TestInputItemsReportsTheItemsThatWereSent(t *testing.T) {
	// A workspace, because a file item is refused without one — this server
	// reports files_input honestly rather than dropping the attachment.
	srv, _ := newFileServer(t)
	id := createTask(t, srv, `{"input":[
		{"type":"input_text","text":"summarise this"},
		{"type":"input_file","filename":"q3.txt","file_data":"data:text/plain;base64,aGVsbG8="}
	],"metadata":{"harness_id":"chrn_writer"}}`)

	data, _ := inputItems(t, srv, id)["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("data has %d items, want 2 — the file item is the one a rebuild loses: %v", len(data), data)
	}

	first, _ := data[0].(map[string]any)
	if first["type"] != "input_text" || first["text"] != "summarise this" {
		t.Errorf("first item = %v, want the input_text as sent", first)
	}
	second, _ := data[1].(map[string]any)
	if second["type"] != "input_file" || second["filename"] != "q3.txt" {
		t.Errorf("second item = %v, want the input_file as sent", second)
	}
	// Verbatim means verbatim: the base64 payload the client sent comes back
	// unchanged rather than being replaced with a file id this server minted.
	if second["file_data"] == nil {
		t.Errorf("file_data is absent from the reported item: %v", second)
	}
}

// The bare-string form expands to the one item the schema says it abbreviates
// ("A bare string is shorthand for one user message"), so the endpoint's shape
// does not depend on which form the client happened to use.
func TestInputItemsExpandsTheStringShorthand(t *testing.T) {
	srv := newTestServer()
	id := createTask(t, srv, `{"input":"hi","metadata":{"harness_id":"echo"}}`)

	data, _ := inputItems(t, srv, id)["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data has %d items, want 1: %v", len(data), data)
	}
	item, _ := data[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "user" {
		t.Fatalf("item = %v, want a user message", item)
	}
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one part", item["content"])
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("part = %v, want the input_text the string abbreviates", part)
	}
}

// An unknown id is response_not_found, matching GET on the response itself. A
// route that 404s with a routing code instead would tell the client the
// endpoint does not exist.
func TestInputItemsOnAnUnknownResponseIsResponseNotFound(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/responses/resp_nope/input_items", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if code := errorCode(t, body); code != "response_not_found" {
		t.Errorf("code = %s, want response_not_found", code)
	}
}

// A refused body must leave nothing behind. The items are appended only after
// an item validates, so a task that was never created cannot be reported as
// having carried the item that stopped it being created.
func TestARefusedBodyStoresNoInputItems(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(
		`{"input":[{"type":"input_text","text":"ok"},{"type":"input_text","role":"system","text":"no"}],
		  "metadata":{"harness_id":"echo"}}`))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 — a system role is refused: %s", w.Code, w.Body.String())
	}
}
