package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
)

// reportingAdapter names the model it ran on its own output, the way claude,
// grok and pi each do. echoAdapter stands in for the other case: a runtime
// whose model can only be guessed at from the list it advertises.
type reportingAdapter struct {
	echoAdapter
	reports string
}

func (a reportingAdapter) Info() domain.Harness {
	info := a.echoAdapter.Info()
	info.ID, info.Base, info.Name = "chrn_reporting", "reporting", "Reporting"
	info.Models = []string{"echo-1", "echo-2"}
	info.DefaultModel = "echo-1"
	return info
}

func (a reportingAdapter) Run(_ context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 3)
	ch <- harness.RunUpdate{Type: harness.UpdateModel, Model: a.reports}
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "hello " + req.Input}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}

// completedTask starts a task, waits for it, and returns what the store then holds —
// which is what a client polling GET /v1/responses/{id} would read.
func completedTask(t *testing.T, svc *TaskService, req CreateTaskRequest) *domain.Task {
	t.Helper()
	ctx := context.Background()
	task, run, err := svc.StartTask(ctx, req)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return got
}

// Issue #43, and conformance T-03: "the response names the model that actually
// ran". A client that pins a model can compare the answer against what it
// asked for; one that names none has nothing of its own to compare against, so
// the response is the only place it can learn what served it — which makes it
// the case that most needs telling and the one that was silent.
func TestATaskThatNamesNoModelIsStillToldWhichOneRan(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})

	got := completedTask(t, svc, CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if got.Model != "echo-1" {
		t.Errorf("model = %q, want the harness's default %q — a client that named no model cannot tell what ran",
			got.Model, "echo-1")
	}
	// Not in scope for #43 and deliberately unchanged: the client requested
	// nothing, and `requested_model` says so by being absent.
	if got.RequestedModel != "" {
		t.Errorf("requested_model = %q, want empty — the client asked for no model", got.RequestedModel)
	}
}

// The wire object, not just the struct: T-03 reads `model` off the response and
// `model_fallback` is derived at marshal time, so both are checked where a
// client sees them.
func TestTheWireResponseNamesTheModelAndClaimsNoFallback(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})

	raw, err := json.Marshal(completedTask(t, svc, CreateTaskRequest{Input: "x", HarnessID: "echo"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Model    string         `json:"model"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.Model == "" {
		t.Error("response has no `model`, so a client cannot tell what ran")
	}
	// Nothing was requested, so nothing was fallen back from. Reporting a
	// fallback here would invent a substitution that did not happen.
	if _, ok := wire.Metadata["requested_model"]; ok {
		t.Errorf("metadata carries requested_model for a request that named none: %v", wire.Metadata)
	}
	if _, ok := wire.Metadata["model_fallback"]; ok {
		t.Errorf("metadata claims a fallback for a request that named no model: %v", wire.Metadata)
	}
}

// The default is a guess. Where the harness says what it actually ran, the
// guess is replaced by the observation — which is the whole reason the two are
// not the same thing.
func TestAHarnessThatNamesItsModelReplacesTheDefaultGuess(t *testing.T) {
	svc := newSvc(t, "reporting", reportingAdapter{reports: "echo-2"})

	got := completedTask(t, svc, CreateTaskRequest{Input: "x", HarnessID: "reporting"})
	if got.Model != "echo-2" {
		t.Errorf("model = %q, want %q — the harness said which model it ran and was not believed",
			got.Model, "echo-2")
	}
	if got.RequestedModel != "" {
		t.Errorf("requested_model = %q, want empty", got.RequestedModel)
	}
}

// silentAdapter advertises no models and names none on its output — what
// `opencode` is on a server with no UHP_OPENCODE_MODELS set and no reachable
// CLI to enumerate. It is the one shape #43's symptom survives in.
type silentAdapter struct{ echoAdapter }

func (silentAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_silent", Base: "silent", Object: "harness", Name: "Silent",
		Models: []string{}, DefaultModel: "",
		Capabilities: []domain.Capability{domain.CapStreaming, domain.CapSessions, domain.CapCancellation}}
}

// The limit of the fix, asserted rather than left to be discovered. A harness
// that advertises no models and names none on the wire has nothing true to say
// about what ran, and says nothing — it does not invent a placeholder.
//
// config.Load is where that emptiness is chosen: `pi` and `opencode` ship with
// no configured models because neither has an id that is true on someone
// else's machine, and "advertises no model rather than a wrong one" is the
// standing call. CLIHarness.validateModel makes the same call in the other
// direction, allowing any model when the catalogue is empty. This is the third
// place that reasoning lands, and reporting a guessed id here would contradict
// both of the others.
func TestAHarnessThatKnowsNoModelsNamesNoneRatherThanInventingOne(t *testing.T) {
	svc := newSvc(t, "silent", silentAdapter{})

	got := completedTask(t, svc, CreateTaskRequest{Input: "x", HarnessID: "silent"})
	if got.Model != "" {
		t.Errorf("model = %q, want empty — this server knows of no model for this harness, "+
			"and naming one would be inventing it", got.Model)
	}
}

// A pinned model is left exactly as the client spelled it. The observation is
// only allowed to fill a blank, never to overwrite an answer — see the comment
// in applyUpdate for why the stronger reading is not taken here.
func TestAReportedModelDoesNotRewriteTheModelTheClientAskedFor(t *testing.T) {
	svc := newSvc(t, "reporting", reportingAdapter{reports: "echo-2"})

	got := completedTask(t, svc, CreateTaskRequest{Input: "x", HarnessID: "reporting", Model: "echo-1"})
	if got.Model != "echo-1" {
		t.Errorf("model = %q, want the requested %q", got.Model, "echo-1")
	}
	if got.RequestedModel != "echo-1" {
		t.Errorf("requested_model = %q, want %q", got.RequestedModel, "echo-1")
	}
}
