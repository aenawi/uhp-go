package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
)

// recordingBase stands in for a compiled-in base adapter and remembers the
// request it was handed.
type recordingBase struct {
	last RunRequest
}

func (b *recordingBase) Info() domain.Harness {
	return domain.Harness{
		ID: "chrn_base", Object: "harness", Name: "Claude Code", Base: "claude-code",
		BaseLabel: "Claude Code", DefaultModel: "m1",
		Models:       []string{"m1", "m2"},
		Capabilities: []domain.Capability{domain.CapStreaming},
		Status:       domain.HarnessReady,
	}
}
func (b *recordingBase) HealthCheck(context.Context) error { return nil }
func (b *recordingBase) Run(_ context.Context, req RunRequest) (<-chan RunUpdate, error) {
	b.last = req
	ch := make(chan RunUpdate, 1)
	ch <- RunUpdate{Type: UpdateCompleted}
	close(ch)
	return ch, nil
}
func (b *recordingBase) Cancel(context.Context, string) error { return nil }

func managedCfg() domain.HarnessConfig {
	return domain.HarnessConfig{
		ID: "chrn_managed", Name: "Research agent", Base: "claude-code",
		DefaultModel: "m2", CreatedAt: 1786403298205,
	}
}

func TestManagedInfoKeepsConfigAndComputesTheRest(t *testing.T) {
	m := NewManaged(managedCfg(), &recordingBase{})
	info := m.Info()

	if info.ID != "chrn_managed" || info.Name != "Research agent" {
		t.Fatalf("configuration was not preserved: %+v", info)
	}
	if info.Object != "harness" {
		t.Fatalf("expected object 'harness', got %q", info.Object)
	}
	if info.DefaultModel != "m2" {
		t.Fatalf("expected the configured default model, got %q", info.DefaultModel)
	}
	if info.CreatedAt != 1786403298205 {
		t.Fatalf("createdAt is not the configured one: %d", info.CreatedAt)
	}
	// Computed from the base, never from what a client stored.
	if len(info.Models) != 2 || info.Status != domain.HarnessReady {
		t.Fatalf("base-derived fields missing: %+v", info)
	}
	if info.BaseLabel != "Claude Code" {
		t.Fatalf("expected the base's label, got %q", info.BaseLabel)
	}
}

// A harness whose base is not compiled into this server must report that,
// rather than claiming a readiness it cannot check.
func TestManagedWithoutABaseIsUnavailable(t *testing.T) {
	m := NewManaged(managedCfg(), nil)
	info := m.Info()
	if info.Status != domain.HarnessUnavailable {
		t.Fatalf("expected unavailable, got %q", info.Status)
	}
	if m.Available("m1") {
		t.Fatal("a harness with no base reported a model as available")
	}
	if _, err := m.Run(context.Background(), RunRequest{TaskID: "resp_1"}); err == nil {
		t.Fatal("expected a run against a missing base to fail")
	}
}

func TestManagedAppliesDefaultModel(t *testing.T) {
	base := &recordingBase{}
	m := NewManaged(managedCfg(), base)

	if _, err := m.Run(context.Background(), RunRequest{TaskID: "resp_1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if base.last.Model != "m2" {
		t.Fatalf("expected the harness default model to be applied, got %q", base.last.Model)
	}

	// A model the task named wins over the harness default.
	if _, err := m.Run(context.Background(), RunRequest{TaskID: "resp_2", Model: "m1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if base.last.Model != "m1" {
		t.Fatalf("the request's own model was overwritten: %q", base.last.Model)
	}
}

func TestManagedConveysSystemPrompt(t *testing.T) {
	cfg := managedCfg()
	cfg.SystemPrompt = "Answer only in French."
	base := &recordingBase{}
	m := NewManaged(cfg, base)

	if _, err := m.Run(context.Background(), RunRequest{TaskID: "resp_1", Input: "hello"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(base.last.Input, "Answer only in French.") {
		t.Fatalf("the configured system prompt never reached the harness: %q", base.last.Input)
	}
	if !strings.Contains(base.last.Input, "hello") {
		t.Fatalf("the user's own input was lost: %q", base.last.Input)
	}
}

func TestManagedAvailabilityFollowsTheBase(t *testing.T) {
	m := NewManaged(managedCfg(), &recordingBase{})
	if !m.Available("m1") {
		t.Fatal("expected a model the base advertises to be available")
	}
	if m.Available("nope") {
		t.Fatal("a model the base does not advertise was reported available")
	}
}
