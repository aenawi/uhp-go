package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

func testConfig(id, name string) domain.HarnessConfig {
	return domain.HarnessConfig{
		ID: id, Name: name, Base: "claude-code", CreatedAt: 1786403298205,
		Skills: []uhp.Skill{{Name: "manual", Files: []uhp.SkillFile{
			{Path: "SKILL.md", Content: "---\nname: manual\n---\n"},
			{Path: "assets/blob.bin", ContentB64: "AAECAwQF"},
		}}},
	}
}

func TestFileHarnessesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harnesses.json")
	hs, err := NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()

	if err := hs.PutHarness(ctx, testConfig("chrn_a", "A")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := hs.GetHarness(ctx, "chrn_a")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Name != "A" || got.Base != "claude-code" {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
	if len(got.Skills) != 1 || len(got.Skills[0].Files) != 2 {
		t.Fatalf("skill folder did not round-trip: %+v", got.Skills)
	}
	if got.Skills[0].Files[1].ContentB64 != "AAECAwQF" {
		t.Fatalf("binary member was not preserved: %+v", got.Skills[0].Files[1])
	}
}

// A harness created over the API is configuration, and configuration that
// vanishes on restart is not configuration.
func TestFileHarnessesSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "harnesses.json")
	hs, err := NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := hs.PutHarness(ctx, testConfig("chrn_a", "A")); err != nil {
		t.Fatalf("put: %v", err)
	}

	reopened, err := NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok, err := reopened.GetHarness(ctx, "chrn_a")
	if err != nil || !ok {
		t.Fatalf("harness did not survive a reopen: ok=%v err=%v", ok, err)
	}
	if got.Name != "A" {
		t.Fatalf("expected name A, got %q", got.Name)
	}
}

func TestFileHarnessesListIsOrdered(t *testing.T) {
	hs, err := NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"chrn_c", "chrn_a", "chrn_b"} {
		if err := hs.PutHarness(ctx, testConfig(id, id)); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	list, err := hs.ListHarnesses(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var ids []string
	for _, h := range list {
		ids = append(ids, h.ID)
	}
	if len(ids) != 3 || ids[0] != "chrn_a" || ids[1] != "chrn_b" || ids[2] != "chrn_c" {
		t.Fatalf("expected ids sorted, got %v", ids)
	}
}

func TestFileHarnessesDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harnesses.json")
	hs, err := NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := hs.PutHarness(ctx, testConfig("chrn_a", "A")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := hs.DeleteHarness(ctx, "chrn_a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := hs.GetHarness(ctx, "chrn_a"); ok {
		t.Fatal("a deleted harness still resolves")
	}
	// Deleting again is not an error: the caller's intent is already satisfied.
	if err := hs.DeleteHarness(ctx, "chrn_a"); err != nil {
		t.Fatalf("second delete: %v", err)
	}

	reopened, err := NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok, _ := reopened.GetHarness(ctx, "chrn_a"); ok {
		t.Fatal("a deleted harness came back after a reopen")
	}
}

// The returned config must not alias the stored one, or a caller that appends
// to a slice it read edits storage without going through Put.
func TestFileHarnessesReturnsCopies(t *testing.T) {
	hs, err := NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := hs.PutHarness(ctx, testConfig("chrn_a", "A")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, _, _ := hs.GetHarness(ctx, "chrn_a")
	got.Skills[0].Name = "mutated"
	got.Skills[0].Files[0].Content = "mutated"

	again, _, _ := hs.GetHarness(ctx, "chrn_a")
	if again.Skills[0].Name == "mutated" || again.Skills[0].Files[0].Content == "mutated" {
		t.Fatal("mutating a returned config changed what the store holds")
	}
}

// A crash between truncate and write must not leave an unreadable document,
// so the write goes through a temporary file and a rename.
func TestFileHarnessesWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "harnesses.json")
	hs, err := NewFileHarnesses(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := hs.PutHarness(context.Background(), testConfig("chrn_a", "A")); err != nil {
		t.Fatalf("put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "harnesses.json" {
			t.Fatalf("a temporary file was left behind: %s", e.Name())
		}
	}
}
