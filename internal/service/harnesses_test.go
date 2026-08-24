package service

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

func managedService(t *testing.T) (*TaskService, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "harnesses.json")
	hs, err := store.NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	return NewTaskService(reg, store.NewMemoryStore(), slog.Default(), WithHarnessStore(hs)), path
}

func TestHarnessManagementIsOffWithoutAStore(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default())

	if svc.HarnessManagementEnabled() {
		t.Fatal("harness management reported as enabled without a store")
	}
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{Name: "x", Base: "echo"})
	if !errors.Is(err, ErrHarnessManagementUnsupported) {
		t.Fatalf("expected ErrHarnessManagementUnsupported, got %v", err)
	}
}

func TestCreateHarnessRoundTrips(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()

	h, err := svc.CreateHarness(ctx, HarnessSpec{Name: "Research agent", Base: "echo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(h.ID, "chrn_") {
		t.Fatalf("id is not chrn_-prefixed: %q", h.ID)
	}
	if h.Object != "harness" || h.Base != "echo" || h.CreatedAt == 0 {
		t.Fatalf("harness object is incomplete: %+v", h)
	}

	got, ok, err := svc.GetHarness(ctx, h.ID)
	if err != nil || !ok {
		t.Fatalf("created harness does not resolve: ok=%v err=%v", ok, err)
	}
	if got.Name != "Research agent" {
		t.Fatalf("expected the configured name, got %q", got.Name)
	}

	list, err := svc.ListHarnesses(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, l := range list {
		if l.ID == h.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the created harness is missing from the listing: %+v", list)
	}
}

// F-02: a base the server cannot run must be refused at configuration time,
// not accepted and failed at the first task, after the client committed to it.
func TestCreateHarnessRefusesAnUnsupportedBase(t *testing.T) {
	svc, _ := managedService(t)

	_, err := svc.CreateHarness(context.Background(), HarnessSpec{Name: "x", Base: "definitely-not-real"})
	var unsupported *UnsupportedBaseError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected an UnsupportedBaseError, got %v", err)
	}
	if len(unsupported.Supported) == 0 {
		t.Fatal("the refusal did not say which bases are supported, so a client cannot recover")
	}
	if unsupported.Supported[0] != "echo" {
		t.Fatalf("expected the registered base to be listed, got %v", unsupported.Supported)
	}
}

// A harness id is not a base. Accepting one would store an opaque id in a
// field a client reads as a runtime name.
func TestCreateHarnessRefusesAHarnessIDAsABase(t *testing.T) {
	svc, _ := managedService(t)
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{Name: "x", Base: "chrn_echo"})
	var unsupported *UnsupportedBaseError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected an UnsupportedBaseError, got %v", err)
	}
}

func TestCreateHarnessRequiresABase(t *testing.T) {
	svc, _ := managedService(t)
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{Name: "x"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// The same reasoning as an unsupported base: a default model the base cannot
// serve is a configuration that fails at task time.
func TestCreateHarnessRefusesAnUnservableDefaultModel(t *testing.T) {
	svc, _ := managedService(t)
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{
		Name: "x", Base: "echo", DefaultModel: "no-such-model"})
	if !errors.Is(err, harness.ErrUnsupportedModel) {
		t.Fatalf("expected ErrUnsupportedModel, got %v", err)
	}
}

// The other half of that rule: a base advertising no models has not said it
// cannot serve this one, only that it does not know what it serves. Refusing
// there rejects a default that works, and tells the client "supported: []".
func TestCreateHarnessAcceptsADefaultModelWhenTheBaseListsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harnesses.json")
	hs, err := store.NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(otherAdapter{}) // base "other", advertising no models
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default(), WithHarnessStore(hs))

	h, err := svc.CreateHarness(context.Background(), HarnessSpec{
		Name: "x", Base: "other", DefaultModel: "whatever-the-cli-knows"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.DefaultModel != "whatever-the-cli-knows" {
		t.Errorf("DefaultModel = %q, want the one that was asked for", h.DefaultModel)
	}
}

// F-05: a bundle with no SKILL.md would be stored and then silently ignored at
// run time, which is the hardest kind of failure for a user to diagnose.
func TestCreateHarnessRefusesASkillWithoutAManifest(t *testing.T) {
	svc, _ := managedService(t)
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{
		Name: "x", Base: "echo",
		Skills: []uhp.Skill{{Name: "no-manifest", Files: []uhp.SkillFile{
			{Path: "notes.md", Content: "no manifest here"},
		}}},
	})
	if !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("expected ErrInvalidSkill, got %v", err)
	}
}

func TestCreateHarnessRefusesASkillPathThatEscapes(t *testing.T) {
	svc, _ := managedService(t)
	for _, bad := range []string{"../escape.md", "/etc/passwd", "nested/../../out.md", ""} {
		_, err := svc.CreateHarness(context.Background(), HarnessSpec{
			Name: "x", Base: "echo",
			Skills: []uhp.Skill{{Name: "escaper", Files: []uhp.SkillFile{
				{Path: "SKILL.md", Content: "---\nname: escaper\n---\n"},
				{Path: bad, Content: "x"},
			}}},
		})
		if !errors.Is(err, ErrInvalidSkill) {
			t.Fatalf("path %q was accepted: %v", bad, err)
		}
	}
}

func TestCreateHarnessRefusesUndecodableBinaryContent(t *testing.T) {
	svc, _ := managedService(t)
	_, err := svc.CreateHarness(context.Background(), HarnessSpec{
		Name: "x", Base: "echo",
		Skills: []uhp.Skill{{Name: "blobby", Files: []uhp.SkillFile{
			{Path: "SKILL.md", Content: "---\nname: blobby\n---\n"},
			{Path: "assets/blob.bin", ContentB64: "not base64!!"},
		}}},
	})
	if !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("expected ErrInvalidSkill, got %v", err)
	}
}

func TestCreateHarnessAcceptsAWholeSkillFolder(t *testing.T) {
	svc, _ := managedService(t)
	h, err := svc.CreateHarness(context.Background(), HarnessSpec{
		Name: "x", Base: "echo",
		Skills: []uhp.Skill{{Name: "manual", Files: []uhp.SkillFile{
			{Path: "SKILL.md", Content: "---\nname: manual\n---\n"},
			{Path: "references/data.md", Content: "nested\n"},
			{Path: "assets/blob.bin", ContentB64: "AAECAwQF"},
		}}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(h.Skills) != 1 || len(h.Skills[0].Files) != 3 {
		t.Fatalf("the folder did not survive create: %+v", h.Skills)
	}
	if h.Skills[0].Enabled == nil || !*h.Skills[0].Enabled {
		t.Fatalf("a skill with no explicit `enabled` should default to enabled: %+v", h.Skills[0])
	}
}

func TestUpdateHarnessKeepsTheImmutableFields(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{Name: "before", Base: "echo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := svc.UpdateHarness(ctx, created.ID, HarnessSpec{Name: "after", Base: "echo"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "after" {
		t.Fatalf("the rename did not take: %q", updated.Name)
	}
	if updated.ID != created.ID || updated.Base != created.Base || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("update changed an immutable field: %+v vs %+v", updated, created)
	}
}

// Changing a base would silently change the behaviour of every session already
// attached to the harness, so it is refused rather than ignored.
func TestUpdateHarnessRefusesADifferentBase(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, _ := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo"})

	_, err := svc.UpdateHarness(ctx, created.ID, HarnessSpec{Name: "x", Base: "something-else"})
	if !errors.Is(err, ErrBaseImmutable) {
		t.Fatalf("expected ErrBaseImmutable, got %v", err)
	}
}

// Update replaces the mutable configuration (Harnesses §5.2), so a field the
// client did not send is a field it does not want.
func TestUpdateHarnessReplacesConfiguration(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "echo", SystemPrompt: "be brief", DisabledTools: []string{"WebSearch"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.SystemPrompt != "be brief" {
		t.Fatalf("systemPrompt was not stored: %+v", created)
	}

	updated, err := svc.UpdateHarness(ctx, created.ID, HarnessSpec{Name: "x", Base: "echo"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.SystemPrompt != "" || len(updated.DisabledTools) != 0 {
		t.Fatalf("a replacing update kept fields the client did not send: %+v", updated)
	}
}

func TestUpdateAndDeleteRefuseACompiledInHarness(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()

	if _, err := svc.UpdateHarness(ctx, "chrn_echo", HarnessSpec{Name: "x", Base: "echo"}); !errors.Is(err, ErrHarnessNotManaged) {
		t.Fatalf("expected ErrHarnessNotManaged on update, got %v", err)
	}
	if err := svc.DeleteHarness(ctx, "chrn_echo"); !errors.Is(err, ErrHarnessNotManaged) {
		t.Fatalf("expected ErrHarnessNotManaged on delete, got %v", err)
	}
}

func TestUpdateAndDeleteReportAnUnknownHarness(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()

	if _, err := svc.UpdateHarness(ctx, "chrn_nope", HarnessSpec{Name: "x"}); !errors.Is(err, ErrHarnessNotFound) {
		t.Fatalf("expected ErrHarnessNotFound on update, got %v", err)
	}
	if err := svc.DeleteHarness(ctx, "chrn_nope"); !errors.Is(err, ErrHarnessNotFound) {
		t.Fatalf("expected ErrHarnessNotFound on delete, got %v", err)
	}
}

func TestDeleteHarnessRemovesIt(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, _ := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo"})

	if err := svc.DeleteHarness(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := svc.GetHarness(ctx, created.ID); ok {
		t.Fatal("a deleted harness still resolves")
	}
}

// Harnesses §5.3: history that disappears when configuration changes cannot be
// audited.
func TestDeleteHarnessKeepsItsSessionsAndResponses(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, _ := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo"})

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "hi", HarnessID: created.ID})
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}

	if err := svc.DeleteHarness(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("the response died with its harness: %v", err)
	}
	if _, err := svc.GetSession(ctx, task.SessionID); err != nil {
		t.Fatalf("the session died with its harness: %v", err)
	}
}

// A managed harness is a harness: a task can name it and it runs on its base.
func TestTaskRunsOnAManagedHarness(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo", SystemPrompt: "be brief"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "there", HarnessID: created.ID})
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
	if final.Status != uhp.StatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
	// echoAdapter echoes its input, so this proves the system prompt reached it.
	if !strings.Contains(final.Text(), "be brief") {
		t.Fatalf("the harness's system prompt never reached the base: %q", final.Text())
	}
}

// A harness created before a restart must still resolve after one.
func TestManagedHarnessesSurviveARestart(t *testing.T) {
	svc, path := managedService(t)
	ctx := context.Background()
	created, _ := svc.CreateHarness(ctx, HarnessSpec{Name: "durable", Base: "echo"})

	hs, err := store.NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	restarted := NewTaskService(reg, store.NewMemoryStore(), slog.Default(), WithHarnessStore(hs))

	got, ok, err := restarted.GetHarness(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("the harness did not survive a restart: ok=%v err=%v", ok, err)
	}
	if got.Name != "durable" {
		t.Fatalf("expected name 'durable', got %q", got.Name)
	}
}

// An MCP credential must never come back out: Harnesses §4.1 is explicit that
// a server never returns a resolved credential to a client.
func TestMcpCredentialsAreNeverReturned(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "x", Base: "echo",
		McpServers: []uhp.McpServer{{Name: "vault", URL: "https://mcp.example.com/mcp", Auth: "secret-token"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.McpServers[0].Auth != "" {
		t.Fatal("the create response handed the credential back to the client")
	}
	got, _, _ := svc.GetHarness(ctx, created.ID)
	if got.McpServers[0].Auth != "" {
		t.Fatal("the harness object handed the credential back to the client")
	}
	// Defaults are made explicit so a client never has to infer them.
	if got.McpServers[0].Transport != "http" || got.McpServers[0].Enabled == nil || !*got.McpServers[0].Enabled {
		t.Fatalf("MCP defaults were not normalised: %+v", got.McpServers[0])
	}
}

// A client that PUTs back the configuration it read has no credential to send,
// because the server never gave it one. Dropping the stored token there would
// silently disconnect the server on an unrelated rename.
func TestUpdateCarriesForwardAnMcpCredential(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{
		Name: "before", Base: "echo",
		McpServers: []uhp.McpServer{{Name: "vault", URL: "https://mcp.example.com/mcp", Auth: "secret-token"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := svc.UpdateHarness(ctx, created.ID, HarnessSpec{
		Name: "after", Base: "echo",
		McpServers: []uhp.McpServer{{Name: "vault", URL: "https://mcp.example.com/mcp"}},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	stored, ok, err := svc.harnesses.GetHarness(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("stored config: ok=%v err=%v", ok, err)
	}
	if stored.McpServers[0].Auth != "secret-token" {
		t.Fatalf("the credential was lost on an unrelated rename: %q", stored.McpServers[0].Auth)
	}
}

func TestCreateHarnessRefusesAnIncompleteMcpServer(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	for _, bad := range []uhp.McpServer{
		{URL: "https://mcp.example.com/mcp"},
		{Name: "vault"},
		{Name: "vault", URL: "https://mcp.example.com/mcp", Transport: "carrier-pigeon"},
	} {
		_, err := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo", McpServers: []uhp.McpServer{bad}})
		if !errors.Is(err, ErrInvalidMcpServer) {
			t.Fatalf("%+v was accepted: %v", bad, err)
		}
	}
}

// The other half of §5.3: the history survives, so a later turn in one of
// those sessions has to be refused truthfully rather than routed somewhere
// else. `harness_not_found` is the honest answer, and it is what the client
// gets.
func TestContinuingASessionOnADeletedHarnessIsRefused(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{Name: "x", Base: "echo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "hi", HarnessID: created.ID})
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := svc.DeleteHarness(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, _, err = svc.StartTask(ctx, CreateTaskRequest{
		Input: "again", HarnessID: created.ID, PreviousResponseID: task.ID})
	if !errors.Is(err, ErrHarnessNotFound) {
		t.Fatalf("expected ErrHarnessNotFound, got %v", err)
	}
}

// A harness whose base this server no longer has is still a harness: §5.2
// forbids *changing* a base, not editing a harness whose base went away. It
// must stay renameable and deletable, or removing a base from a deployment
// strands every harness built on it — readable, unrunnable, and uneditable.
func TestUpdateHarnessWhoseBaseIsGone(t *testing.T) {
	svc, path := managedService(t)
	ctx := context.Background()
	created, err := svc.CreateHarness(ctx, HarnessSpec{Name: "before", Base: "echo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Restart with the base no longer compiled in.
	hs, err := store.NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	stranded := NewTaskService(harness.NewRegistry(), store.NewMemoryStore(), slog.Default(),
		WithHarnessStore(hs))

	got, ok, err := stranded.GetHarness(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("the harness stopped resolving: ok=%v err=%v", ok, err)
	}
	if got.Status != uhpgo.HarnessUnavailable {
		t.Fatalf("expected the harness to report unavailable, got %q", got.Status)
	}

	updated, err := stranded.UpdateHarness(ctx, created.ID, HarnessSpec{Name: "after", Base: "echo"})
	if err != nil {
		t.Fatalf("renaming a harness whose base is gone: %v", err)
	}
	if updated.Name != "after" || updated.Base != "echo" {
		t.Fatalf("the rename did not take: %+v", updated)
	}
	if err := stranded.DeleteHarness(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
