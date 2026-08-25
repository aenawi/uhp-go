package http

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// plainAdapter is a runtime that enforces none of a harness's configuration
// natively, which is what the MCP refusal turns on.
type plainAdapter struct{ echoAdapter }

func (plainAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "chrn_plain", Base: "plain", Object: "harness", Name: "Plain"}}
}
func (plainAdapter) Delivery() harness.Delivery { return harness.Delivery{} }

func newManagedTestServer(t *testing.T) *Server {
	t.Helper()
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	reg.Register(plainAdapter{})
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

	// 200 and a body, not 204: the OpenAPI specifies `{id, deleted}` for both
	// deletion endpoints, and a client written against it decodes one (#59).
	status, deleted := callJSON(t, srv, "DELETE", "/v1/harnesses/"+id, "")
	if status != 200 {
		t.Fatalf("delete returned %d: %v", status, deleted)
	}
	if deleted["id"] != id || deleted["deleted"] != true {
		t.Fatalf("delete envelope = %v, want {id: %s, deleted: true}", deleted, id)
	}
	if status, _ := callJSON(t, srv, "GET", "/v1/harnesses/"+id, ""); status != 404 {
		t.Fatalf("a deleted harness still resolves (%d)", status)
	}
}

// The alias path. A harness is reachable by its friendly base name as well as
// its chrn_ id, and the deletion envelope must name the canonical one: that is
// the id the client is holding and will now get a 404 for, and echoing back
// "echo" would leave it unable to match the answer to what it lost.
func TestDeletingAHarnessByAliasNamesTheCanonicalID(t *testing.T) {
	srv := newManagedTestServer(t)

	status, deleted := callJSON(t, srv, "DELETE", "/v1/harnesses/echo", "")
	// A compiled-in harness is not the API's to delete, so this refusal is the
	// expected one — what is being checked is that the alias resolved at all
	// rather than being treated as an unknown id.
	if status == 404 {
		t.Fatalf("the alias did not resolve: %v", deleted)
	}
	if code := errorCode(t, deleted); code != vendorCodeHarnessNotManaged {
		t.Fatalf("code = %q, want %s", code, vendorCodeHarnessNotManaged)
	}

	// Now the same thing on a harness that *is* the API's to delete, created
	// with a name of its own.
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"mine","base":"echo"}`)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create did not return an id: %v", created)
	}
	status, body := callJSON(t, srv, "DELETE", "/v1/harnesses/"+id, "")
	if status != 200 || body["id"] != id {
		t.Fatalf("delete = %d %v, want 200 naming %s", status, body, id)
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

// PATCH merges, PUT replaces. The difference is the whole reason both exist:
// §5.2's replacement is what the specification defines, and a client that means
// to change one field should not have to resend a skill folder to keep it.
func TestPatchMergesAndPutReplaces(t *testing.T) {
	srv := newManagedTestServer(t)
	create := `{"name":"x","base":"echo","system_prompt":"be brief","disabled_tools":["WebSearch"],
		"skills":[{"name":"manual","files":[{"path":"SKILL.md","content":"---\nname: manual\n---\n"}]}]}`
	status, created := callJSON(t, srv, "POST", "/v1/harnesses", create)
	if status != 200 {
		t.Fatalf("create returned %d: %v", status, created)
	}
	id := created["id"].(string)

	status, patched := callJSON(t, srv, "PATCH", "/v1/harnesses/"+id, `{"name":"renamed"}`)
	if status != 200 {
		t.Fatalf("patch returned %d: %v", status, patched)
	}
	if patched["name"] != "renamed" {
		t.Fatalf("the rename did not take: %v", patched)
	}
	if patched["systemPrompt"] != "be brief" {
		t.Fatalf("patch cleared a field the client did not send: %v", patched)
	}
	if len(patched["skills"].([]any)) != 1 {
		t.Fatalf("patch dropped the skill folder: %v", patched["skills"])
	}
	if len(patched["disabledTools"].([]any)) != 1 {
		t.Fatalf("patch dropped disabledTools: %v", patched["disabledTools"])
	}

	// The same body through PUT clears everything it omits.
	status, replaced := callJSON(t, srv, "PUT", "/v1/harnesses/"+id, `{"name":"replaced","base":"echo"}`)
	if status != 200 {
		t.Fatalf("put returned %d: %v", status, replaced)
	}
	if replaced["systemPrompt"] != "" || len(replaced["skills"].([]any)) != 0 {
		t.Fatalf("a replacing update kept fields the client did not send: %v", replaced)
	}
}

// An explicit null clears; an absent key does not. Without the distinction a
// client can set a budget but never remove one.
func TestPatchDistinguishesNullFromAbsent(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses",
		`{"name":"x","base":"echo","max_step":12,"timeout_seconds":30}`)
	id := created["id"].(string)
	if created["maxStep"] != float64(12) {
		t.Fatalf("max_step was not stored: %v", created)
	}

	_, patched := callJSON(t, srv, "PATCH", "/v1/harnesses/"+id, `{"name":"y"}`)
	if patched["maxStep"] != float64(12) || patched["timeoutSeconds"] != float64(30) {
		t.Fatalf("an absent budget was cleared: %v", patched)
	}

	_, cleared := callJSON(t, srv, "PATCH", "/v1/harnesses/"+id, `{"max_step":null}`)
	if cleared["maxStep"] != nil {
		t.Fatalf("an explicit null did not clear the budget: %v", cleared)
	}
	if cleared["timeoutSeconds"] != float64(30) {
		t.Fatalf("clearing one budget cleared the other: %v", cleared)
	}
}

func TestPatchRefusesADifferentBase(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	id := created["id"].(string)

	status, body := callJSON(t, srv, "PATCH", "/v1/harnesses/"+id, `{"base":"something-else"}`)
	if status != 422 {
		t.Fatalf("expected 422, got %d: %v", status, body)
	}
	if body["error"].(map[string]any)["param"] != "base" {
		t.Fatalf("the refusal does not say which field was wrong: %v", body["error"])
	}
}

// A patch that names the base it already has is a no-op, not a conflict.
func TestPatchAcceptsTheSameBase(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	id := created["id"].(string)

	status, patched := callJSON(t, srv, "PATCH", "/v1/harnesses/"+id, `{"name":"y","base":"echo"}`)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, patched)
	}
	if patched["name"] != "y" {
		t.Fatalf("the rename did not take: %v", patched)
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

// F-03: the skill files endpoint returns the complete folder, nested and
// binary members included.
func TestSkillFilesEndpoint(t *testing.T) {
	srv := newManagedTestServer(t)
	create := `{"name":"x","base":"echo","skills":[{"name":"uhp-conformance-skill","enabled":true,"files":[
		{"path":"SKILL.md","content":"---\nname: uhp-conformance-skill\n---\n"},
		{"path":"references/data.md","content":"nested reference file\n"},
		{"path":"assets/blob.bin","content_b64":"AAECAwQF"}]}]}`
	status, created := callJSON(t, srv, "POST", "/v1/harnesses", create)
	if status != 200 {
		t.Fatalf("create returned %d: %v", status, created)
	}
	id := created["id"].(string)

	status, body := callJSON(t, srv, "GET",
		"/v1/harnesses/"+id+"/skills/uhp-conformance-skill/files", "")
	if status != 200 {
		t.Fatalf("skill files endpoint returned %d: %v", status, body)
	}
	files, _ := body["files"].([]any)
	var paths []string
	for _, f := range files {
		p, _ := f.(map[string]any)["path"].(string)
		paths = append(paths, p)
	}
	sort.Strings(paths)
	want := []string{"SKILL.md", "assets/blob.bin", "references/data.md"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("the folder did not round-trip: got %v, expected %v", paths, want)
	}
	raw, _ := json.Marshal(files)
	if !strings.Contains(string(raw), "AAECAwQF") {
		t.Fatalf("the binary member's content_b64 was not preserved: %s", raw)
	}
}

func TestSkillFilesEndpointUnknownSkill(t *testing.T) {
	srv := newManagedTestServer(t)
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", `{"name":"x","base":"echo"}`)
	id := created["id"].(string)

	status, body := callJSON(t, srv, "GET", "/v1/harnesses/"+id+"/skills/nope/files", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != vendorCodeSkillNotFound {
		t.Fatalf("expected %s, got %q", vendorCodeSkillNotFound, code)
	}
}

// F-06: MCP servers and disabled tools survive a round trip, and `enabled:
// false` in particular — a client that cannot tell a disabled entry from an
// enabled one cannot tell whether a third party is contacted.
func TestMcpAndDisabledToolsRoundTrip(t *testing.T) {
	srv := newManagedTestServer(t)
	create := `{"name":"x","base":"echo","disabled_tools":["WebSearch"],
		"mcp_servers":[{"name":"conformance-mcp","url":"https://mcp.example.invalid/mcp",
		"transport":"http","enabled":false}]}`
	status, created := callJSON(t, srv, "POST", "/v1/harnesses", create)
	if status != 200 {
		t.Fatalf("create returned %d: %v", status, created)
	}
	id := created["id"].(string)

	_, got := callJSON(t, srv, "GET", "/v1/harnesses/"+id, "")
	servers, _ := got["mcpServers"].([]any)
	if len(servers) == 0 {
		t.Fatalf("mcpServers came back empty after being set: %v", got)
	}
	first := servers[0].(map[string]any)
	if first["enabled"] != false {
		t.Fatalf("`enabled: false` was not preserved: %v", first)
	}
	if first["name"] != "conformance-mcp" || first["url"] != "https://mcp.example.invalid/mcp" {
		t.Fatalf("the entry did not round-trip: %v", first)
	}
	tools, _ := got["disabledTools"].([]any)
	if len(tools) != 1 || tools[0] != "WebSearch" {
		t.Fatalf("disabledTools round-tripped as %v", tools)
	}
}

// F-04: renaming a harness by PUTting back what was read must not empty its
// skill folder. This is the failure the round trip exists to catch — a user
// cannot tell the contents are gone until an agent behaves oddly weeks later.
func TestRenameDoesNotDestroySkillContents(t *testing.T) {
	srv := newManagedTestServer(t)
	create := `{"name":"x","base":"echo","skills":[{"name":"uhp-conformance-skill","enabled":true,"files":[
		{"path":"SKILL.md","content":"---\nname: uhp-conformance-skill\n---\n"},
		{"path":"references/data.md","content":"nested reference file\n"},
		{"path":"assets/blob.bin","content_b64":"AAECAwQF"}]}]}`
	_, created := callJSON(t, srv, "POST", "/v1/harnesses", create)
	id := created["id"].(string)

	body, err := json.Marshal(map[string]any{
		"name": "uhp-conformance-renamed", "base": created["base"],
		"skills": created["skills"], "mcp_servers": created["mcpServers"],
		"disabled_tools": created["disabledTools"],
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if status, resp := callJSON(t, srv, "PUT", "/v1/harnesses/"+id, string(body)); status != 200 {
		t.Fatalf("rename returned %d: %v", status, resp)
	}

	_, after := callJSON(t, srv, "GET",
		"/v1/harnesses/"+id+"/skills/uhp-conformance-skill/files", "")
	files, _ := after["files"].([]any)
	if len(files) != 3 {
		t.Fatalf("after renaming the harness the folder holds %d files; the round trip lost contents", len(files))
	}
}

// A base with no per-run MCP mechanism refuses the configuration instead of
// accepting it and running without the servers.
func TestMcpRefusedOnAnIncapableBase(t *testing.T) {
	srv := newManagedTestServer(t)
	body := `{"name":"x","base":"plain","mcp_servers":[{"name":"vault","url":"https://mcp.example.invalid/mcp"}]}`
	status, resp := callJSON(t, srv, "POST", "/v1/harnesses", body)
	if status != 422 {
		t.Fatalf("expected 422, got %d: %v", status, resp)
	}
	if code := errorCode(t, resp); code != vendorCodeMcpUndeliverable {
		t.Fatalf("expected %s, got %q", vendorCodeMcpUndeliverable, code)
	}
}
