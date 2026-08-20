package http

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
)

func newManagedTestServer(t *testing.T) *Server {
	t.Helper()
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(), service.WithHarnessStore(hs))
	return NewServer(svc, slog.Default(), nil, 0)
}

// callJSON issues a request and returns the status and the decoded body.
func callJSON(t *testing.T, srv *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := do(t, srv, method, path, strings.NewReader(body))
	var decoded map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s %s: body is not JSON (%d): %s", method, path, w.Code, w.Body.String())
		}
	}
	return w.Code, decoded
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no error envelope: %v", body)
	}
	code, _ := e["code"].(string)
	return code
}

// The whole F-01 shape in one test: create, rename, delete, and confirm the
// deleted harness stops resolving.
func TestHarnessLifecycle(t *testing.T) {
	srv := newManagedTestServer(t)

	status, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"Research agent","base":"echo"}`)
	if status != 200 {
		t.Fatalf("create returned %d: %v", status, created)
	}
	id, _ := created["id"].(string)
	if !strings.HasPrefix(id, "chrn_") {
		t.Fatalf("create did not return a chrn_ id: %v", created)
	}
	if created["object"] != "harness" || created["base"] != "echo" {
		t.Fatalf("the created harness object is wrong: %v", created)
	}
	if _, ok := created["createdAt"].(float64); !ok {
		t.Fatalf("createdAt is missing or not a number: %v", created)
	}

	status, updated := callJSON(t, srv, "PUT", "/v1/harnesses/"+id, `{"name":"renamed","base":"echo"}`)
	if status != 200 {
		t.Fatalf("update returned %d: %v", status, updated)
	}
	if updated["name"] != "renamed" {
		t.Fatalf("the rename did not take: %v", updated)
	}
	if updated["base"] != "echo" {
		t.Fatalf("update changed the harness base: %v", updated)
	}

	status, _ = callJSON(t, srv, "DELETE", "/v1/harnesses/"+id, "")
	if status != 204 {
		t.Fatalf("delete returned %d", status)
	}
	if status, _ := callJSON(t, srv, "GET", "/v1/harnesses/"+id, ""); status != 404 {
		t.Fatalf("a deleted harness still resolves (%d)", status)
	}
}

// F-02, and the way F-01 discovers a base it can use: the refusal has to name
// the bases that would have worked.
func TestCreateHarnessUnsupportedBase(t *testing.T) {
	srv := newManagedTestServer(t)

	status, body := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"definitely-not-a-real-harness"}`)
	if status != 422 {
		t.Fatalf("expected 422, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "unsupported_base" {
		t.Fatalf("expected code unsupported_base, got %q", code)
	}
	e := body["error"].(map[string]any)
	detail, ok := e["detail"].(map[string]any)
	if !ok {
		t.Fatalf("the refusal carries no detail: %v", e)
	}
	supported, ok := detail["supported"].([]any)
	if !ok || len(supported) == 0 {
		t.Fatalf("detail.supported is missing or empty: %v", detail)
	}
	if e["param"] != "base" {
		t.Fatalf("the refusal does not say which field was wrong: %v", e)
	}
	if supported[0] != "echo" {
		t.Fatalf("expected the registered base to be listed, got %v", supported)
	}
}

func TestCreateHarnessWithoutABase(t *testing.T) {
	srv := newManagedTestServer(t)
	status, body := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x"}`)
	if status != 400 {
		t.Fatalf("expected 400, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %q", code)
	}
	if body["error"].(map[string]any)["param"] != "base" {
		t.Fatalf("the refusal does not say which field was missing: %v", body["error"])
	}
}

// F-05.
func TestCreateHarnessRefusesASkillWithoutAManifest(t *testing.T) {
	srv := newManagedTestServer(t)
	body := `{"name":"x","base":"echo","skills":[{"name":"no-manifest","enabled":true,
		"files":[{"path":"notes.md","content":"no manifest here"}]}]}`
	status, resp := callJSON(t, srv, "POST", "/v1/harnesses", body)
	if status != 422 {
		t.Fatalf("expected 422, got %d: %v", status, resp)
	}
	if code := errorCode(t, resp); code != vendorCodeInvalidSkill {
		t.Fatalf("expected %s, got %q", vendorCodeInvalidSkill, code)
	}
}

// A whole folder — nested member, binary member — must survive create and an
// unrelated rename. Materialising it for the agent is issue #4; losing it here
// would make that impossible.
func TestSkillFolderSurvivesCreateAndRename(t *testing.T) {
	srv := newManagedTestServer(t)
	create := `{"name":"x","base":"echo","skills":[{"name":"manual","enabled":true,"files":[
		{"path":"SKILL.md","content":"---\nname: manual\n---\n"},
		{"path":"references/data.md","content":"nested reference file\n"},
		{"path":"assets/blob.bin","content_b64":"AAECAwQF"}]}]}`
	status, created := callJSON(t, srv, "POST", "/v1/harnesses", create)
	if status != 200 {
		t.Fatalf("create returned %d: %v", status, created)
	}
	id := created["id"].(string)

	// Exactly what a client does when it PUTs back what it read.
	got, err := json.Marshal(map[string]any{
		"name": "renamed", "base": created["base"],
		"skills": created["skills"], "mcp_servers": created["mcpServers"],
		"disabled_tools": created["disabledTools"],
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	status, updated := callJSON(t, srv, "PUT", "/v1/harnesses/"+id, string(got))
	if status != 200 {
		t.Fatalf("rename returned %d: %v", status, updated)
	}

	paths := skillPaths(t, updated)
	want := []string{"SKILL.md", "assets/blob.bin", "references/data.md"}
	if len(paths) != len(want) {
		t.Fatalf("the folder did not round-trip: got %v, expected %v", paths, want)
	}
	blob, _ := json.Marshal(updated["skills"])
	if !strings.Contains(string(blob), "AAECAwQF") {
		t.Fatalf("the binary member was not preserved: %s", blob)
	}
}

func skillPaths(t *testing.T, harnessObj map[string]any) []string {
	t.Helper()
	skills, _ := harnessObj["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("expected one skill, got %v", skills)
	}
	files, _ := skills[0].(map[string]any)["files"].([]any)
	out := make([]string, 0, len(files))
	for _, f := range files {
		p, _ := f.(map[string]any)["path"].(string)
		out = append(out, p)
	}
	return out
}

func TestUpdateHarnessRefusesADifferentBase(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	id := created["id"].(string)

	status, body := callJSON(t, srv, "PUT", "/v1/harnesses/"+id, `{"name":"x","base":"something-else"}`)
	if status != 422 {
		t.Fatalf("expected 422, got %d: %v", status, body)
	}
	e := body["error"].(map[string]any)
	if e["param"] != "base" {
		t.Fatalf("the refusal does not say which field was wrong: %v", e)
	}
}

// A harness this server was started with is not the API's to change, and
// saying so is more useful than a 404 for something the client can see.
func TestManagingACompiledInHarnessIsRefused(t *testing.T) {
	srv := newManagedTestServer(t)

	status, body := callJSON(t, srv, "PUT", "/v1/harnesses/chrn_echo", `{"name":"x","base":"echo"}`)
	if status != 409 {
		t.Fatalf("expected 409 on update, got %d: %v", status, body)
	}
	status, body = callJSON(t, srv, "DELETE", "/v1/harnesses/chrn_echo", "")
	if status != 409 {
		t.Fatalf("expected 409 on delete, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != vendorCodeHarnessNotManaged {
		t.Fatalf("expected %s, got %q", vendorCodeHarnessNotManaged, code)
	}
}

func TestUpdateAndDeleteUnknownHarness(t *testing.T) {
	srv := newManagedTestServer(t)
	for _, tc := range [][2]string{{"PUT", `{"name":"x","base":"echo"}`}, {"DELETE", ""}} {
		status, body := callJSON(t, srv, tc[0], "/v1/harnesses/chrn_nope", tc[1])
		if status != 404 {
			t.Fatalf("%s expected 404, got %d: %v", tc[0], status, body)
		}
		if code := errorCode(t, body); code != "harness_not_found" {
			t.Fatalf("%s expected harness_not_found, got %q", tc[0], code)
		}
	}
}

// A created harness must appear in discovery alongside the compiled-in ones,
// or a client that just made it cannot find it.
func TestCreatedHarnessAppearsInTheListing(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"Research agent","base":"echo"}`)
	id := created["id"].(string)

	status, body := callJSON(t, srv, "GET", "/v1/harnesses", "")
	if status != 200 {
		t.Fatalf("list returned %d", status)
	}
	list, _ := body["harnesses"].([]any)
	var found bool
	for _, h := range list {
		if h.(map[string]any)["id"] == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("the created harness is missing from GET /v1/harnesses: %v", list)
	}
}

func TestCreatedHarnessCanRunATask(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	id := created["id"].(string)

	body := `{"input":"hi","metadata":{"harness_id":"` + id + `"}}`
	status, resp := callJSON(t, srv, "POST", "/v1/responses", body)
	if status != 200 {
		t.Fatalf("task on a created harness returned %d: %v", status, resp)
	}
	if resp["status"] != "completed" {
		t.Fatalf("expected completed, got %v", resp["status"])
	}
}

// Without a store the endpoints exist but refuse honestly, and discovery says
// so up front rather than letting a client find out by trying.
func TestHarnessManagementIsRefusedWithoutAStore(t *testing.T) {
	srv := newTestServer()

	status, body := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	if status != 501 {
		t.Fatalf("expected 501, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != vendorCodeHarnessManagementUnsupported {
		t.Fatalf("expected %s, got %q", vendorCodeHarnessManagementUnsupported, code)
	}

	_, doc := callJSON(t, srv, "GET", "/v1/uhp", "")
	caps := doc["capabilities"].(map[string]any)
	if caps["harness_management"] != false {
		t.Fatalf("discovery claims harness management without a store: %v", caps)
	}
}

func TestDiscoveryAdvertisesHarnessManagement(t *testing.T) {
	srv := newManagedTestServer(t)
	_, doc := callJSON(t, srv, "GET", "/v1/uhp", "")
	caps := doc["capabilities"].(map[string]any)
	if caps["harness_management"] != true {
		t.Fatalf("discovery does not advertise harness management: %v", caps)
	}
}

// PATCH is not in the specification: §5.2 defines the update as a PUT. A
// server that quietly accepted PATCH with replace semantics would silently
// clear the fields a merge-minded client left out.
func TestPatchIsNotAnUpdate(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	id := created["id"].(string)

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("PATCH", "/v1/harnesses/"+id, strings.NewReader(`{"name":"y"}`)))
	if w.Code != 405 {
		t.Fatalf("expected 405 for PATCH, got %d", w.Code)
	}
}

// A body larger than the configured limit is a 413, not a truncated harness.
func TestCreateHarnessBodyIsBounded(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(), service.WithHarnessStore(hs))
	srv := NewServer(svc, slog.Default(), nil, 64)

	body := `{"name":"` + strings.Repeat("x", 512) + `","base":"echo"}`
	status, resp := callJSON(t, srv, "POST", "/v1/harnesses", body)
	if status != 413 {
		t.Fatalf("expected 413, got %d: %v", status, resp)
	}
}

// Harnesses §4.1: a server must never return a resolved credential.
func TestMcpCredentialIsNotReturned(t *testing.T) {
	srv := newManagedTestServer(t)
	body := `{"name":"x","base":"echo","mcp_servers":[{"name":"vault",
		"url":"https://mcp.example.invalid/mcp","transport":"http","enabled":false,"auth":"secret-token"}]}`
	status, created := callJSON(t, srv, "POST", "/v1/harnesses", body)
	if status != 200 {
		t.Fatalf("create returned %d: %v", status, created)
	}
	raw, _ := json.Marshal(created)
	if strings.Contains(string(raw), "secret-token") {
		t.Fatalf("the create response handed the credential back: %s", raw)
	}
	servers := created["mcpServers"].([]any)
	first := servers[0].(map[string]any)
	// F-06: a client cannot tell a disabled entry from an enabled one unless
	// `enabled` survives, and the difference decides whether a third party is
	// contacted at all.
	if first["enabled"] != false {
		t.Fatalf("`enabled: false` was not preserved: %v", first)
	}
}
