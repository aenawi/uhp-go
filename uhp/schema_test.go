package uhp_test

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/aenawi/uhp-go/uhp"
)

// This file is the gate ADR-0002 asks for: every public type is marshalled and
// validated against the vendored normative schema.
//
// It is not the conformance suite and does not overlap with it. The suite
// proves *this server* conformant end to end, costs real tokens, and runs on a
// maintainer's machine; it never touches the published Go types, so nothing in
// it would notice uhp.Response drifting from the object it claims to be. This
// test is free, runs on every push, and catches exactly that — a renamed JSON
// tag, an omitempty on a field the schema requires, a nil slice marshalling as
// null where an array is declared.
//
// It also fails when a schema object has no Go type at all. See
// TestEveryDefinitionIsCovered.

// compileDef compiles one object out of the vendored schema.
func compileDef(t *testing.T, def string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("uhp.json", strings.NewReader(uhp.SchemaJSON)); err != nil {
		t.Fatalf("add vendored schema: %v", err)
	}
	sch, err := c.Compile("uhp.json#/$defs/" + def)
	if err != nil {
		t.Fatalf("compile #/$defs/%s: %v", def, err)
	}
	return sch
}

// validate marshals v through the public API — exactly as a consumer would —
// and validates the bytes.
//
// Marshalling first is the whole point. Validating a hand-written JSON literal
// would prove the literal correct and say nothing about the Go type, which is
// the thing that drifts.
func validate(t *testing.T, def string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", def, err)
	}

	// UseNumber, because the validator checks `minimum` and integer-ness. Left
	// to the default, every number decodes as float64 and a constraint on an
	// integer field is tested against something that is no longer one.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%s: decode marshalled bytes: %v", def, err)
	}

	if err := compileDef(t, def).Validate(doc); err != nil {
		t.Errorf("%s does not validate against #/$defs/%s:\n%v\n\nmarshalled as: %s",
			defGoName(def), def, err, data)
	}
}

// defGoName is only for the failure message: the schema name and the Go name
// are the same by design, and saying so in the one place a human reads is
// cheaper than explaining it again.
func defGoName(def string) string { return "uhp." + def }

func ptr[T any](v T) *T { return &v }

// populated returns one fully-populated value per schema object, keyed by the
// object's name in the schema.
//
// Fully populated, not minimally valid: a minimal value exercises only the
// required fields, and every optional field is one whose tag could be wrong
// without anything noticing. Where a field is constrained — a const, an enum, a
// pattern, a minimum — the value here is one that satisfies it, so a Go
// constant that drifts from the schema's spelling shows up as a validation
// failure rather than as a passing test of the wrong string.
func populated() map[string]any {
	enabled := true

	return map[string]any{
		"Discovery": uhp.Discovery{
			Object:           "uhp.discovery",
			Protocol:         "uhp",
			Versions:         []string{uhp.Version},
			DefaultVersion:   uhp.Version,
			ConformanceClass: "full",
			Capabilities:     uhp.Capabilities{Streaming: true, Sessions: true},
			Implementation:   &uhp.Implementation{Name: "uhp-go", Version: "1.2.3"},
		},

		"Capabilities": uhp.Capabilities{
			Streaming: true, Sessions: true, Cancellation: true,
			FilesInput: true, FilesOutput: true, SessionListing: true,
			HarnessManagement: true, SessionSharing: false, Idempotency: true,
		},

		"Harness": uhp.Harness{
			ID: "chrn_08dae611630d467ab3e67ed792570ae5", Object: "harness",
			Name: "reviewer", Base: "claude-code", BaseLabel: "Claude Code",
			DefaultModel: "claude-opus-5", SystemPrompt: "be terse",
			McpServers:    []uhp.McpServer{{Name: "docs", URL: "https://mcp.example/docs"}},
			Skills:        []uhp.Skill{{Name: "review", Content: "# SKILL"}},
			DisabledTools: []string{"WebSearch"},
			MaxStep:       ptr(40), TimeoutSeconds: ptr(600),
			CreatedAt: 1786400000000, // milliseconds, unlike Response.CreatedAt
		},

		"McpServer": uhp.McpServer{
			Name: "docs", URL: "https://mcp.example/docs", Transport: "sse",
			Enabled: &enabled,
			Headers: map[string]string{"X-Tenant": "acme"},
			Auth:    "ref:secret/mcp-docs",
		},

		"Skill": uhp.Skill{
			Name: "review", Enabled: &enabled,
			Files:   []uhp.SkillFile{{Path: "SKILL.md", Content: "# Review"}},
			Content: "# Review",
			Blob:    "blob_9f2c",
		},

		"SkillFile": uhp.SkillFile{
			Path: "assets/logo.png", Content: "", ContentB64: "aGVsbG8=",
		},

		"HarnessCreate": uhp.HarnessCreate{
			Name: "reviewer", Base: "codex",
			DefaultModel: "gpt-5.4", SystemPrompt: "be terse",
			McpServers:    []uhp.McpServer{{Name: "docs", URL: "https://mcp.example/docs"}},
			Skills:        []uhp.Skill{{Name: "review"}},
			DisabledTools: []string{"WebSearch"},
			MaxStep:       ptr(40), TimeoutSeconds: ptr(600),
		},

		"ModelCatalog": uhp.ModelCatalog{
			Backends: map[string]uhp.ModelCatalogBackend{
				"claude-code": {
					Default: "claude-opus-5",
					Models:  []uhp.Model{{ID: "claude-opus-5", Available: true, Default: true}},
				},
			},
		},

		"HarnessModels": uhp.HarnessModels{
			HarnessID: "chrn_08dae611630d467ab3e67ed792570ae5",
			Backend:   "claude-code",
			Default:   "claude-opus-5",
			Fallback:  "claude-opus-5",
			Models: []uhp.Model{{
				ID: "claude-opus-5", Label: "Claude Opus 5",
				Backend: "claude-code", Available: true, Default: true,
			}},
		},

		"Model": uhp.Model{
			ID: "claude-opus-5", Label: "Claude Opus 5", Backend: "claude-code",
			Available: true, Default: true,
		},

		"CreateResponseRequest": uhp.CreateResponseRequest{
			Input: "Summarise README.md in three bullets.",
			Model: "claude-opus-5",
			Metadata: map[string]any{
				"harness_id": "chrn_08dae611630d467ab3e67ed792570ae5",
			},
			Stream:             true,
			PreviousResponseID: ptr("resp_a1b2c3"),
			Instructions:       "cite line numbers",
			Store:              ptr(false),
			MaxOutputTokens:    ptr(4096),
			MaxStep:            ptr(40),
			TimeoutSeconds:     ptr(600),
			Tools:              []map[string]any{{"type": "function", "name": "read_file"}},
			Include:            []string{"reasoning.encrypted_content"},
			Background:         true,
		},

		"Response": uhp.Response{
			ID: "resp_a1b2c3", Object: "response",
			CreatedAt: 1786400000, // seconds, unlike Harness.CreatedAt
			Status:    uhp.StatusCompleted,
			Error:     nil,
			IncompleteDetails: map[string]any{
				"reason": "max_step",
			},
			PreviousResponseID: ptr("resp_000000"),
			Model:              "claude-opus-5",
			Output: []uhp.OutputItem{{
				ID: "msg_1", Type: "message", Role: "assistant", Status: "completed",
				Content: []uhp.ContentPart{{Type: "output_text", Text: "- a\n- b\n- c"}},
			}},
			Store: true,
			Usage: &uhp.Usage{InputTokens: 5120, OutputTokens: 240, TotalTokens: 5360},
			Metadata: map[string]any{
				"session_id":            "sess_7e78",
				"requested_model":       "claude-opus-5",
				"model_fallback":        false,
				"model_fallback_reason": "",
			},
		},

		"OutputItem": uhp.OutputItem{
			ID: "fc_1", Type: "function_call", Status: "completed", Role: "assistant",
			Content: []uhp.ContentPart{{Type: "output_text", Text: "hi"}},
			Summary: []map[string]any{{"type": "summary_text", "text": "thought"}},
			CallID:  "call_1", Name: "read_file",
			Arguments: `{"path":"README.md"}`,
			Output:    "…",
		},

		"ContentPart": uhp.ContentPart{
			Type: "output_text", Text: "hello",
			Annotations: []uhp.Annotation{{
				Type: uhp.AnnotationTypeFileCitation, FileID: "file_1",
			}},
		},

		"Annotation": uhp.Annotation{
			Type:        uhp.AnnotationTypeFileCitation,
			ContainerID: "cntr_7e78", FileID: "file_1", Filename: "report.md",
			DownloadURL: "https://api.example/v1/containers/cntr_7e78/files/file_1/content",
			// Zero on purpose: it is the offset a plain int with omitempty
			// would have dropped, and the pointer is why it survives.
			StartIndex: ptr(0), EndIndex: ptr(12),
		},

		"Usage": uhp.Usage{
			InputTokens: 5120, OutputTokens: 240, TotalTokens: 5360,
			CacheReadTokens: 4096, CacheWriteTokens: 1024,
		},

		"Session": uhp.Session{
			ID: "sess_7e78", Object: "session",
			HarnessID: "chrn_08dae611630d467ab3e67ed792570ae5",
			Title:     "Summarise README.md", Status: "completed",
			CreatedAt: 1786400000, UpdatedAt: 1786400240,
		},

		"SessionList": uhp.SessionList{
			Sessions:   []uhp.Session{{ID: "sess_7e78", Object: "session"}},
			NextCursor: ptr("sess_7e78"),
		},

		"File": uhp.File{
			ID: "file_1", Object: "file", ContainerID: "cntr_7e78",
			Filename: "reports/q3.md", Bytes: 4096, CreatedAt: 1786400240,
		},

		"ErrorEnvelope": uhp.ErrorEnvelope{
			Error: uhp.Error{
				Type: uhp.ErrorTypeInvalidRequest, Code: uhp.CodeHarnessNotFound,
				Message: "No harness with id 'chrn_deadbeef'.",
				Param:   ptr("metadata.harness_id"),
				Detail:  map[string]any{"supported": []string{"codex"}},
			},
			Detail: "No harness with id 'chrn_deadbeef'.",
		},

		"Error": uhp.Error{
			Type: uhp.ErrorTypeServerError, Code: uhp.CodeHarnessUnavailable,
			Message: "No capacity to run this harness right now.",
			Param:   nil,
			Detail:  map[string]any{"max_concurrent_runs": 4},
		},

		"Event": uhp.Event{
			Type: uhp.EventOutputTextDelta, SequenceNumber: 7,
			Response:   &uhp.Response{ID: "resp_a1b2c3", Object: "response", Status: uhp.StatusInProgress, Model: "m"},
			Item:       &uhp.OutputItem{Type: "message"},
			Part:       &uhp.ContentPart{Type: "output_text"},
			Annotation: &uhp.Annotation{Type: uhp.AnnotationTypeFileCitation},
			Delta:      "Sum", Text: "Summary", Arguments: `{"path":"README.md"}`,
			ItemID:          "msg_1",
			OutputIndex:     ptr(0),
			ContentIndex:    ptr(0),
			SummaryIndex:    ptr(0),
			AnnotationIndex: ptr(0),
			Code:            uhp.CodeHarnessError, Message: "the harness stopped",
			Param: ptr("input"),
		},

		// ResponseStatus is a bare string in the schema, not an object. Every
		// constant is checked rather than one of them: the enum is the whole
		// content of this definition, so a typo in any single constant is the
		// only way this type can be wrong.
		"ResponseStatus": uhp.StatusCompleted,
	}
}

func TestPublicTypesValidateAgainstSchema(t *testing.T) {
	for def, value := range populated() {
		t.Run(def, func(t *testing.T) {
			validate(t, def, value)
		})
	}
}

func TestEveryResponseStatusValidates(t *testing.T) {
	for _, s := range []uhp.ResponseStatus{
		uhp.StatusInProgress, uhp.StatusCompleted, uhp.StatusFailed,
		uhp.StatusIncomplete, uhp.StatusCancelled,
	} {
		t.Run(string(s), func(t *testing.T) { validate(t, "ResponseStatus", s) })
	}
}

// TestEveryErrorTypeValidates checks the six constants against the schema's
// enum.
//
// Error.type is the field UHP's fourth client rule falls back to when a code is
// unrecognised, so a constant misspelled here is not a cosmetic defect: it
// produces a response that a client following the specification cannot classify
// at all.
func TestEveryErrorTypeValidates(t *testing.T) {
	for _, typ := range []string{
		uhp.ErrorTypeInvalidRequest, uhp.ErrorTypeAuthentication,
		uhp.ErrorTypePermission, uhp.ErrorTypeRateLimit,
		uhp.ErrorTypeHarness, uhp.ErrorTypeServerError,
	} {
		t.Run(typ, func(t *testing.T) {
			validate(t, "Error", uhp.Error{Type: typ, Code: "x", Message: "y"})
		})
	}
}

// TestZeroValuesValidate is the half that catches nil slices and nil maps.
//
// Go marshals a nil slice as null and a nil map as null, and the schema
// declares Response.output as an array, Response.metadata as an object,
// ContentPart.annotations as an array and SessionList.sessions as an array —
// so a zero value of any of the four is a document that fails validation unless
// the type normalises on the way out. Three MarshalJSON methods exist for this
// reason and nothing else; without a test they are the kind of code that gets
// deleted as redundant.
func TestZeroValuesValidate(t *testing.T) {
	validate(t, "Response", uhp.Response{
		ID: "resp_zero", Object: "response", CreatedAt: 1,
		Status: uhp.StatusInProgress, Model: "m",
	})
	validate(t, "ContentPart", uhp.ContentPart{Type: "output_text"})
	validate(t, "SessionList", uhp.SessionList{})
	validate(t, "Harness", uhp.Harness{ID: "chrn_zero", Name: "zero", Base: "b"})

	// Five objects pin `object` to a constant, so an unset one must never
	// marshal as an empty string. Response and Harness default it in their
	// marshallers; Session and File have none and omit it instead. Both routes
	// are checked, because the wrong one produces a document that fails on a
	// field nobody set on purpose.
	validate(t, "Session", uhp.Session{ID: "sess_zero"})
	validate(t, "File", uhp.File{ID: "file_zero", Filename: "a.txt"})
	validate(t, "Response", uhp.Response{
		ID: "resp_defaulted", CreatedAt: 1, Status: uhp.StatusInProgress, Model: "m",
	})
}

// TestRequiredNullsArePresent pins the fields Tasks §3 and Errors §1 require to
// be present even when they carry nothing.
//
// The schema cannot express this: it lists them as optional, so a Response that
// omitted `error` and `usage` entirely would validate. The specification asks
// for explicit nulls anyway, because a client that cannot tell "no value" from
// "this server is older than the field" has to guess — so this is checked
// against the marshalled keys rather than against the schema.
func TestRequiredNullsArePresent(t *testing.T) {
	data, err := json.Marshal(uhp.Response{
		ID: "resp_zero", Object: "response", CreatedAt: 1,
		Status: uhp.StatusInProgress, Model: "m",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"error", "incomplete_details", "previous_response_id", "usage"} {
		raw, ok := got[key]
		if !ok {
			t.Errorf("uhp.Response omitted %q; Tasks §3 requires it present as an explicit null", key)
			continue
		}
		if string(raw) != "null" {
			t.Errorf("uhp.Response %q = %s, want null", key, raw)
		}
	}

	envelope, err := json.Marshal(uhp.ErrorEnvelope{Error: uhp.Error{
		Type: uhp.ErrorTypeServerError, Code: "x", Message: "y",
	}})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if !bytes.Contains(envelope, []byte(`"param":null`)) ||
		!bytes.Contains(envelope, []byte(`"detail":null`)) {
		t.Errorf("uhp.Error omitted param or detail; Errors §1 requires both present: %s", envelope)
	}
}

// TestEveryDefinitionIsCovered fails when the schema defines an object this
// package does not.
//
// This is the check that makes the rest of the file mean something. Without it
// the suite proves only that the types which happen to be listed above are
// correct, and a schema object nobody wrote a Go type for is indistinguishable
// from one nobody thought to test — which is precisely the gap ADR-0002 found:
// six of the twenty-three objects had no counterpart in this repository at all.
func TestEveryDefinitionIsCovered(t *testing.T) {
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(uhp.SchemaJSON), &doc); err != nil {
		t.Fatalf("parse vendored schema: %v", err)
	}

	covered := populated()
	var missing []string
	for def := range doc.Defs {
		if _, ok := covered[def]; !ok {
			missing = append(missing, def)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("schema objects with no Go type in package uhp: %v", missing)
	}

	// The other direction: an entry here that names nothing in the schema is a
	// test that validates against a definition the compiler resolves to
	// nothing useful, and it would pass silently.
	var unknown []string
	for def := range covered {
		if _, ok := doc.Defs[def]; !ok {
			unknown = append(unknown, def)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("covered names that are not schema objects: %v", unknown)
	}

	// The count is asserted outright, so that a schema swapped for one with a
	// different object set is a failure rather than a quiet change of scope.
	const wantDefs = 23
	if len(doc.Defs) != wantDefs {
		t.Errorf("vendored schema defines %d objects, want %d", len(doc.Defs), wantDefs)
	}
}
