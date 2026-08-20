package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
)

// plainAdapter is a runtime that enforces nothing natively — no MCP mechanism,
// no tool block, no skill loading. It is the case the standing-instruction
// fallback exists for.
type plainAdapter struct{ echoAdapter }

func (plainAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_plain", Base: "plain", Object: "harness", Name: "Plain"}
}

// Overriding the embedded echoAdapter's answer, which is the capable one.
func (plainAdapter) Delivery() harness.Delivery { return harness.Delivery{} }

// deliveringService wires a workspace plus one capable base ("echo") and one
// base that enforces nothing ("plain").
func deliveringService(t *testing.T) (*TaskService, string) {
	t.Helper()
	workspace := t.TempDir()
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	reg.Register(plainAdapter{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		WithHarnessStore(hs), WithWorkspace(workspace))
	return svc, workspace
}

func skillBundle() domain.Skill {
	return domain.Skill{Name: "uhp-conformance-skill", Files: []domain.SkillFile{
		{Path: "SKILL.md", Content: "---\nname: uhp-conformance-skill\n---\n\nSee references/data.md.\n"},
		{Path: "references/data.md", Content: "nested reference file\n"},
		{Path: "assets/blob.bin", ContentB64: "AAECAwQF"},
	}}
}

// runOn starts a task and waits for it, returning the finished task.
func runOn(t *testing.T, svc *TaskService, harnessID, input string) *domain.Task {
	t.Helper()
	ctx := context.Background()
	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: input, HarnessID: harnessID})
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	final, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return final
}

// The whole folder reaches the disk, not just the manifest. Materialising only
// SKILL.md breaks every skill that carries references, scripts or data.
func TestSkillFolderIsMaterialized(t *testing.T) {
	svc, workspace := deliveringService(t)
	ctx := context.Background()
	h, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "echo", Skills: []domain.Skill{skillBundle()}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	task := runOn(t, svc, h.ID, "go")
	dir := filepath.Join(workspace, task.SessionID, ".uhp", "skills", "uhp-conformance-skill")

	manifest, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md was not written: %v", err)
	}
	if !strings.Contains(string(manifest), "uhp-conformance-skill") {
		t.Fatalf("SKILL.md content is wrong: %q", manifest)
	}
	nested, err := os.ReadFile(filepath.Join(dir, "references", "data.md"))
	if err != nil {
		t.Fatalf("the nested member was not written: %v", err)
	}
	if string(nested) != "nested reference file\n" {
		t.Fatalf("nested member content is wrong: %q", nested)
	}

	// Binary is preserved byte-for-byte, not re-encoded.
	blob, err := os.ReadFile(filepath.Join(dir, "assets", "blob.bin"))
	if err != nil {
		t.Fatalf("the binary member was not written: %v", err)
	}
	if want := []byte{0, 1, 2, 3, 4, 5}; string(blob) != string(want) {
		t.Fatalf("binary member is %v, want %v", blob, want)
	}
}

// `enabled: false` suppresses a skill, and suppression has to mean "not on
// disk": a folder the agent can read is available whether or not anything
// pointed at it.
func TestDisabledSkillIsNotMaterialized(t *testing.T) {
	svc, workspace := deliveringService(t)
	ctx := context.Background()
	off := false
	bundle := skillBundle()
	bundle.Enabled = &off
	h, err := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo", Skills: []domain.Skill{bundle}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	task := runOn(t, svc, h.ID, "go")
	dir := filepath.Join(workspace, task.SessionID, ".uhp", "skills", "uhp-conformance-skill")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a disabled skill was materialized anyway (err=%v)", err)
	}
}

// A runtime that cannot load a folder itself is told where the folder is;
// otherwise it sits on disk unread.
func TestSkillsAreAnnouncedWhenTheRuntimeCannotLoadThem(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()
	h, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "plain", Skills: []domain.Skill{skillBundle()}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// echoAdapter echoes its input, so the task text is what the harness saw.
	got := runOn(t, svc, h.ID, "go").Text()
	if !strings.Contains(got, "uhp-conformance-skill") || !strings.Contains(got, "SKILL.md") {
		t.Fatalf("a runtime that cannot load skills was not told where they are: %q", got)
	}

	// A runtime that loads them natively is not also told in the prompt.
	native, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "y", Base: "echo", Skills: []domain.Skill{skillBundle()}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := runOn(t, svc, native.ID, "go").Text(); strings.Contains(got, "SKILL.md") {
		t.Fatalf("a runtime that loads skills natively was told about them anyway: %q", got)
	}
}

// §4.3: where the runtime cannot block a tool, the restriction MUST still
// reach the agent. Dropping it is the worst outcome — the operator believes a
// tool is off, and it is not.
func TestDisabledToolsAreConveyedWhenTheyCannotBeBlocked(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()
	h, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "plain", DisabledTools: []string{"WebSearch"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got := runOn(t, svc, h.ID, "go").Text()
	if !strings.Contains(got, "WebSearch") {
		t.Fatalf("the tool restriction never reached the agent: %q", got)
	}
	if !strings.Contains(got, "not enforced") {
		t.Fatalf("a soft restriction was not described as one: %q", got)
	}
}

// Where the runtime can block, the block is used and the prompt is left clean.
func TestDisabledToolsAreBlockedWhenTheRuntimeCan(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()
	h, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "echo", DisabledTools: []string{"WebSearch"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := runOn(t, svc, h.ID, "go").Text(); strings.Contains(got, "not enforced") {
		t.Fatalf("a hard block was also asked for in the prompt: %q", got)
	}
}

// Only enabled entries reach the generated configuration. A disabled entry
// must not be contacted at all — connected and then hidden still tells its
// operator the turn happened.
func TestOnlyEnabledMcpServersReachTheConfig(t *testing.T) {
	svc, workspace := deliveringService(t)
	ctx := context.Background()
	off, on := false, true
	h, err := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo", McpServers: []domain.McpServer{
		{Name: "live", URL: "https://live.example.invalid/mcp", Enabled: &on, Auth: "secret-token"},
		{Name: "shelved", URL: "https://shelved.example.invalid/mcp", Enabled: &off},
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	task := runOn(t, svc, h.ID, "go")
	raw, err := os.ReadFile(filepath.Join(workspace, task.SessionID, ".uhp", "mcp.json"))
	if err != nil {
		t.Fatalf("no mcp config was written: %v", err)
	}
	var doc struct {
		McpServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("mcp config is not JSON: %v", err)
	}
	if _, ok := doc.McpServers["shelved"]; ok {
		t.Fatalf("a disabled server reached the configuration: %s", raw)
	}
	live, ok := doc.McpServers["live"]
	if !ok {
		t.Fatalf("the enabled server is missing: %s", raw)
	}
	if live.Type != "http" || live.URL != "https://live.example.invalid/mcp" {
		t.Fatalf("the enabled server is wrong: %+v", live)
	}
	// The credential the client can never read back is what actually connects.
	if live.Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("the credential did not reach the runtime: %+v", live.Headers)
	}
}

// §4.1: a server MUST NOT advertise MCP support it cannot deliver. Refused at
// configuration time, while the client is still listening.
func TestMcpIsRefusedOnABaseThatCannotDeliverIt(t *testing.T) {
	svc, _ := deliveringService(t)
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{
		Name: "x", Base: "plain",
		McpServers: []domain.McpServer{{Name: "vault", URL: "https://mcp.example.invalid/mcp"}}})
	if !errors.Is(err, ErrMcpUndeliverable) {
		t.Fatalf("expected ErrMcpUndeliverable, got %v", err)
	}
}

// The router's own scaffolding is not the task's output. A skill folder and an
// MCP config written into the working directory must not come back as
// artifacts the agent produced.
func TestRouterScaffoldingIsNotAnArtifact(t *testing.T) {
	svc, _ := deliveringService(t)
	ctx := context.Background()
	h, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "echo",
		Skills:     []domain.Skill{skillBundle()},
		McpServers: []domain.McpServer{{Name: "vault", URL: "https://mcp.example.invalid/mcp"}}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, a := range runOn(t, svc, h.ID, "go").Artifacts {
		if strings.Contains(a.Path, "SKILL.md") || strings.Contains(a.Path, "mcp.json") ||
			strings.Contains(a.Path, ".uhp") {
			t.Fatalf("the router's own scaffolding came back as an artifact: %q", a.Path)
		}
	}
}

// Skills need somewhere to live. Running without them would produce a
// plausible answer from an agent that never got what it was configured with.
func TestSkillsWithoutAWorkspaceAreRefused(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default(), WithHarnessStore(hs))

	ctx := context.Background()
	h, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "echo", Skills: []domain.Skill{skillBundle()}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := svc.StartTask(ctx, CreateTaskRequest{Input: "go", HarnessID: h.ID}); !errors.Is(err, ErrFilesUnsupported) {
		t.Fatalf("expected ErrFilesUnsupported, got %v", err)
	}
}

// A skill name lands on a filesystem, so it is sanitised rather than trusted.
func TestSkillFolderNamesAreSanitized(t *testing.T) {
	for name, want := range map[string]string{
		"vault-manual":  "vault-manual",
		"../escape":     "-escape",
		"a/b":           "a-b",
		"..":            "",
		"with space":    "with-space",
		"Mixed_Case.99": "Mixed_Case.99",
	} {
		if got := sanitizeSkillName(name); got != want {
			t.Fatalf("sanitizeSkillName(%q) = %q, want %q", name, got, want)
		}
	}
}
