package http

import (
	"context"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

func echoHarness() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "chrn_echo",
		Object:       "harness",
		Name:         "Echo",
		Base:         "echo",
		DefaultModel: "fast"},
		Models: []string{"fast", "slow"},
		Status: uhpgo.HarnessReady}
}

// modelEntries pulls one backend's model list out of GET /v1/models.
func modelEntries(t *testing.T, body map[string]any, backend string) []any {
	t.Helper()
	backends, ok := body["backends"].(map[string]any)
	if !ok {
		t.Fatalf("no backends object: %v", body)
	}
	entry, ok := backends[backend].(map[string]any)
	if !ok {
		t.Fatalf("no backend %q in %v", backend, backends)
	}
	return jsonArray(t, entry, "models")
}

// `available` is computed per model rather than asserted, because it reflects
// whether the CLI is reachable right now. A stand-in is the only way to hold
// that answer still: against a real registry it depends on what is installed on
// the machine running the test.
func TestListModelsComputesAvailability(t *testing.T) {
	asked := map[string]bool{}
	srv := newFakeServer(&fakeService{
		listHarnesses: func(context.Context) ([]uhpgo.Harness, error) {
			return []uhpgo.Harness{echoHarness()}, nil
		},
		modelAvailable: func(_ context.Context, harnessID, model string) bool {
			asked[harnessID+"/"+model] = true
			return model == "fast"
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/models", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}

	models := modelEntries(t, body, "echo")
	if len(models) != 2 {
		t.Fatalf("expected two models, got %d: %v", len(models), models)
	}
	for _, m := range models {
		entry, _ := m.(map[string]any)
		wantAvailable := entry["id"] == "fast"
		if entry["available"] != wantAvailable {
			t.Fatalf("model %v: available %v, want %v", entry["id"], entry["available"], wantAvailable)
		}
		if entry["default"] != (entry["id"] == "fast") {
			t.Fatalf("model %v: default %v", entry["id"], entry["default"])
		}
		if entry["backend"] != "echo" {
			t.Fatalf("model %v: backend %v, want echo", entry["id"], entry["backend"])
		}
	}
	if !asked["chrn_echo/fast"] || !asked["chrn_echo/slow"] {
		t.Fatalf("availability was not asked per model: %v", asked)
	}
}

// A harness with an empty catalogue still gets an array, not null: the field is
// what a client iterates to render a model picker.
func TestListModelsEmptyCatalogueIsAnArray(t *testing.T) {
	h := echoHarness()
	h.Models = nil
	h.DefaultModel = ""
	srv := newFakeServer(&fakeService{
		listHarnesses: func(context.Context) ([]uhpgo.Harness, error) {
			return []uhpgo.Harness{h}, nil
		},
		modelAvailable: func(context.Context, string, string) bool { return true },
	})

	status, body := callJSON(t, srv, "GET", "/v1/models", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if got := modelEntries(t, body, "echo"); len(got) != 0 {
		t.Fatalf("expected no models, got %v", got)
	}
}

func TestHarnessModels(t *testing.T) {
	srv := newFakeServer(&fakeService{
		getHarness: func(_ context.Context, id string) (uhpgo.Harness, bool, error) {
			if id != "echo" {
				t.Fatalf("handler asked for %q", id)
			}
			return echoHarness(), true, nil
		},
		modelAvailable: func(_ context.Context, _, model string) bool { return model == "fast" },
	})

	status, body := callJSON(t, srv, "GET", "/v1/harnesses/echo/models", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if body["harness_id"] != "chrn_echo" || body["backend"] != "echo" {
		t.Fatalf("unexpected identity: %v", body)
	}
	// `fallback` mirrors `default` and both are present: a client that reads
	// either one gets the same answer rather than a missing field.
	if body["default"] != "fast" || body["fallback"] != "fast" {
		t.Fatalf("unexpected default: %v", body)
	}
	models := jsonArray(t, body, "models")
	if len(models) != 2 {
		t.Fatalf("expected two models, got %v", models)
	}
	first, _ := models[0].(map[string]any)
	if first["id"] != "fast" || first["available"] != true || first["default"] != true {
		t.Fatalf("unexpected first model: %#v", models[0])
	}
}

func TestHarnessModelsUnknownHarness(t *testing.T) {
	srv := newFakeServer(&fakeService{
		getHarness: func(context.Context, string) (uhpgo.Harness, bool, error) {
			return uhpgo.Harness{}, false, nil
		},
	})

	status, body := callJSON(t, srv, "GET", "/v1/harnesses/nope/models", "")
	if status != 404 {
		t.Fatalf("expected 404, got %d: %v", status, body)
	}
	if code := errorCode(t, body); code != "harness_not_found" {
		t.Fatalf("expected harness_not_found, got %s", code)
	}
}

// A harness store that cannot be read is distinct from a server with no
// harnesses, and the listing must not flatten the two into an empty catalogue:
// a client would conclude there is nothing to run and stop asking. The refusal
// itself is in the storage-failure table in errors_test.go; this is the case it
// must not be confused with.
func TestListHarnessesEmptyIsAnArrayNotNull(t *testing.T) {
	srv := newFakeServer(&fakeService{
		listHarnesses: func(context.Context) ([]uhpgo.Harness, error) { return nil, nil },
	})

	status, body := callJSON(t, srv, "GET", "/v1/harnesses", "")
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, body)
	}
	if got := jsonArray(t, body, "harnesses"); len(got) != 0 {
		t.Fatalf("expected no harnesses, got %v", got)
	}
}
