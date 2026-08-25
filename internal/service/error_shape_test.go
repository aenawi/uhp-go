package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Issue #47: this service puts an `error` object on a task that failed, and the
// schema makes `type`, `code` and `message` required on one, with `type`
// constrained to a six-value enum. A failed adapter start used to emit no
// `type` at all and an unprefixed vendor code, and nothing noticed: the only
// schema checking that happened was inside the conformance suite on a
// maintainer's machine, and none of its checks provoke an adapter that will not
// start.
//
// uhp/schema_test.go closed half of that — every public type is marshalled and
// validated — but it validates values that file writes itself, so it proves
// [uhp.Error] *can* be marshalled into a valid object and says nothing about
// the ones this package builds. The three construction sites are the half that
// drifts, and one of them is how the defect shipped. So this drives each path
// to its terminal state and validates what it actually left behind.
//
// It replaces an earlier single-path guard that checked the failed start alone.

// failingAdapter reports UpdateFailed, the way a CLI that started and then
// could not finish the work does.
type failingAdapter struct{ echoAdapter }

func (failingAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "chrn_failing", Base: "failing", Object: "harness", Name: "Failing"}}
}

var errHarnessGaveUp = errors.New("the harness could not complete the work")

func (failingAdapter) Run(context.Context, harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "half an answer"}
	ch <- harness.RunUpdate{Type: harness.UpdateFailed, Err: errHarnessGaveUp}
	close(ch)
	return ch, nil
}

// errorSchema compiles #/$defs/Error out of the vendored normative schema.
//
// Both this and specCodes read [uhp.SchemaJSON] rather than the file on disk,
// for the reason that field is a string: the copy the published package
// validates against is the copy this test must use, or the two can disagree.
func errorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("uhp.json", strings.NewReader(uhp.SchemaJSON)); err != nil {
		t.Fatalf("add vendored schema: %v", err)
	}
	sch, err := c.Compile("uhp.json#/$defs/Error")
	if err != nil {
		t.Fatalf("compile #/$defs/Error: %v", err)
	}
	return sch
}

// specCodes is the set of codes Errors §3 defines, read off the schema rather
// than copied.
//
// The schema lists them as `examples` and not as an `enum`, deliberately: a
// server MAY define codes for conditions the specification does not cover. That
// is why this cannot be a schema assertion and has to be one here — the
// namespacing rule attached to that permission is the half a validator cannot
// check.
func specCodes(t *testing.T) map[string]bool {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(uhp.SchemaJSON), &doc); err != nil {
		t.Fatalf("decode vendored schema: %v", err)
	}
	defs, _ := doc["$defs"].(map[string]any)
	errDef, _ := defs["Error"].(map[string]any)
	props, _ := errDef["properties"].(map[string]any)
	code, _ := props["code"].(map[string]any)
	examples, _ := code["examples"].([]any)
	if len(examples) == 0 {
		t.Fatal("the schema's Error.code carries no examples; this test reads the specification's codes from there")
	}
	set := make(map[string]bool, len(examples))
	for _, e := range examples {
		s, _ := e.(string)
		set[s] = true
	}
	return set
}

func validateError(t *testing.T, sch *jsonschema.Schema, e *uhp.Error) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal error object: %v", err)
	}
	// UseNumber for the same reason uhp/schema_test.go uses it: the default
	// decodes every number as float64, and an integer constraint would then be
	// tested against something that is no longer one.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode marshalled bytes: %v", err)
	}
	if err := sch.Validate(doc); err != nil {
		t.Errorf("the error this path built does not validate against #/$defs/Error:\n%v\n\nmarshalled as: %s", err, data)
	}
}

func TestEveryFailurePathBuildsASchemaValidError(t *testing.T) {
	sch := errorSchema(t)
	spec := specCodes(t)

	cases := []struct {
		name     string
		harness  string
		adapter  harness.Adapter
		wantType string
		wantCode string
		// startFails is true where StartTask itself returns the error and
		// hands back no run to wait on.
		startFails bool
	}{
		{
			// The path the defect shipped on. server_error is the honest
			// class: an adapter that would not start is this server's problem,
			// not the caller's request being wrong.
			name:       "the adapter refuses to start",
			harness:    "refusing",
			adapter:    refusingAdapter{},
			wantType:   uhp.ErrorTypeServerError,
			wantCode:   uhpgo.CodeAdapterStartFailed,
			startFails: true,
		},
		{
			name:     "the harness reports failure",
			harness:  "failing",
			adapter:  failingAdapter{},
			wantType: uhp.ErrorTypeHarness,
			wantCode: uhp.CodeHarnessError,
		},
		{
			name:     "the adapter closes without a terminal update",
			harness:  "bad",
			adapter:  neverTerminalAdapter{},
			wantType: uhp.ErrorTypeHarness,
			wantCode: uhp.CodeHarnessError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewTaskService(newRegistryWith(tc.adapter), newMemStore(), testLogger())
			ctx := context.Background()

			task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: tc.harness})
			if tc.startFails {
				if err == nil {
					t.Fatal("the adapter refused to start and StartTask reported success")
				}
			} else {
				if err != nil {
					t.Fatalf("StartTask: %v", err)
				}
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if err := run.Wait(wctx); err != nil {
					t.Fatalf("Wait: %v", err)
				}
				if task, err = svc.GetTask(ctx, task.ID); err != nil {
					t.Fatalf("GetTask: %v", err)
				}
			}

			if task == nil || task.Error == nil {
				t.Fatal("a failed task carries no error object")
			}
			if task.Status != uhp.StatusFailed {
				t.Errorf("status = %q, want %q", task.Status, uhp.StatusFailed)
			}

			// Asserted separately from the schema check even though the enum
			// would also reject "": an enum failure names six values a reader
			// has to map back to a missing field, and the omission is the
			// defect this exists to catch. UHP's fourth client rule — treat an
			// unrecognised code as its type — is what has nothing to read.
			if task.Error.Type == "" {
				t.Error("error.type is empty; the schema requires it, and a client that meets an unfamiliar code has nothing to fall back on")
			}
			if task.Error.Type != tc.wantType {
				t.Errorf("error.type = %q, want %q", task.Error.Type, tc.wantType)
			}
			if task.Error.Code != tc.wantCode {
				t.Errorf("error.code = %q, want %q", task.Error.Code, tc.wantCode)
			}
			if !spec[task.Error.Code] && !strings.HasPrefix(task.Error.Code, uhpgo.CodePrefix) {
				t.Errorf("error.code = %q is neither one of the specification's codes nor vendor-prefixed; an additional code MUST be namespaced so a future version cannot collide with it", task.Error.Code)
			}

			validateError(t, sch, task.Error)
		})
	}
}
