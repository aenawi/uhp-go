package uhp_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
// It also fails when a schema object has no Go type at all, and — since #63 —
// when a Go type has no schema object and no reason on record. See
// TestEveryDefinitionIsCovered and TestEveryPublicTypeIsAccountedFor.

// compileRef compiles one subschema out of the vendored schema, named by JSON
// pointer.
//
// A pointer rather than a $defs name, because two of the shapes this package
// publishes are described inside a parent object and have no name to compile.
// See TestInlineSchemaObjectsValidate.
func compileRef(t *testing.T, ref string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("uhp.json", strings.NewReader(uhp.SchemaJSON)); err != nil {
		t.Fatalf("add vendored schema: %v", err)
	}
	sch, err := c.Compile("uhp.json" + ref)
	if err != nil {
		t.Fatalf("compile %s: %v", ref, err)
	}
	return sch
}

// compileDef compiles one named object out of the vendored schema.
func compileDef(t *testing.T, def string) *jsonschema.Schema {
	t.Helper()
	return compileRef(t, "#/$defs/"+def)
}

// validate marshals v through the public API — exactly as a consumer would —
// and validates the bytes.
//
// Marshalling first is the whole point. Validating a hand-written JSON literal
// would prove the literal correct and say nothing about the Go type, which is
// the thing that drifts.
func validate(t *testing.T, def string, v any) {
	t.Helper()
	validateRef(t, "#/$defs/"+def, defGoName(def), v)
}

// validateRef is validate against any subschema, with the Go name to report
// passed in because the pointer no longer implies it.
func validateRef(t *testing.T, ref, goName string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", goName, err)
	}

	// UseNumber, because the validator checks `minimum` and integer-ness. Left
	// to the default, every number decodes as float64 and a constraint on an
	// integer field is tested against something that is no longer one.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%s: decode marshalled bytes: %v", goName, err)
	}

	if err := compileRef(t, ref).Validate(doc); err != nil {
		t.Errorf("%s does not validate against %s:\n%v\n\nmarshalled as: %s",
			goName, ref, err, data)
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

		// TurnItem and SessionShare are the two objects `2026-08-11` gained
		// after it was published, both closing gaps this repository reported
		// (harnessrouter#44). Until then these were the whole of notNormative;
		// they are here now for the same reason the other twenty-three are.
		"TurnItem": uhp.TurnItem{
			ID: "resp_a1b2c3", Status: uhp.StatusCompleted, Model: "claude-sonnet-5",
			User: "summarise README.md", Assistant: "Three bullets follow.",
			Files: []uhp.File{{
				ID: "file_c0ffee", Object: "file", ContainerID: "cntr_a1b2c3",
				Filename: "notes.md", Bytes: 128, CreatedAt: 1786400240,
			}},
			CreatedAt: 1786400240,
			// The deprecated trio is populated too: they are still on the wire
			// for one release, and `additionalProperties: true` means a wrong
			// tag on one of them would otherwise be invisible here.
			ResponseID: "resp_a1b2c3", Input: "summarise README.md",
			Output: "Three bullets follow.",
		},

		"SessionShare": uhp.SessionShare{
			ID: "shr_e5467da8", Object: "session.share", SessionID: "sess_a1b2c3",
			URL: "/v1/shares/shr_e5467da8", CreatedAt: 1786400240,
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
	const wantDefs = 25
	if len(doc.Defs) != wantDefs {
		t.Errorf("vendored schema defines %d objects, want %d", len(doc.Defs), wantDefs)
	}
}

// The rest of this file is TestEveryDefinitionIsCovered read backwards.
//
// That test asks whether every schema object has a Go type. It cannot ask
// whether every Go type is a schema object, and the difference is not
// theoretical: what is now [uhp.TurnItem] shipped as a twenty-fourth type in a
// package whose premise is that it invented nothing, and no test went red. A
// client author reading godoc had no way to tell it apart from the
// twenty-three, which was the defect — not the type's existence, which was
// defensible and has since been vindicated by the schema adopting the shape,
// but its silence.
//
// So every exported type in the package is sorted into exactly one of five
// buckets below, and a type in none of them fails. Adding a twenty-sixth shape
// is then a line a contributor writes on purpose, in a list a reviewer reads,
// rather than a struct that slips in beside the protocol.

// inlineObject is one shape the schema describes without naming.
type inlineObject struct {
	// ref is the JSON pointer at which the schema describes the shape.
	ref string
	// value is a fully-populated instance, validated against ref for exactly
	// the reason populated() gives.
	value any
	// invalid is a document the subschema at ref must reject, written as raw
	// JSON because the point is to say something the Go type cannot.
	//
	// Compiling a pointer proves it resolves. It does not prove it resolves to
	// a *schema*: a pointer one segment short — #/$defs/Discovery/properties —
	// lands on a JSON object with no keyword the compiler recognises, which
	// accepts everything, value above included. So each entry also carries
	// something its own subschema forbids, and a ref that stops rejecting it
	// has stopped pointing at the shape.
	invalid string
}

// inlineSchemaObjects are the types the schema describes inline, inside a
// parent object, rather than lifting into $defs.
//
// These are protocol shapes — a conformant server must produce them, and the
// fields are the schema's — so they belong in this package as much as the
// twenty-three do. Only the Go name is this repository's, because the schema
// gave the shape no name to borrow. Each type's godoc says so.
var inlineSchemaObjects = map[string]inlineObject{
	"Implementation": {
		ref:   "#/$defs/Discovery/properties/implementation",
		value: uhp.Implementation{Name: "uhp-go", Version: "1.2.3"},
		// This subschema requires nothing and permits anything extra, so the
		// only claim it makes is the type of the two fields it names — and
		// that is all this check can prove. A renamed json tag here is caught
		// by the Discovery case in populated(), not by this one.
		invalid: `{"name": 1}`,
	},
	"ModelCatalogBackend": {
		ref: "#/$defs/ModelCatalog/properties/backends/additionalProperties",
		value: uhp.ModelCatalogBackend{
			Default: "claude-opus-5",
			Models:  []uhp.Model{{ID: "claude-opus-5", Available: true, Default: true}},
		},
		// Both fields are required here, so dropping either json tag is a
		// document this rejects.
		invalid: `{"default": "claude-opus-5"}`,
	},
}

// notNormative are the shapes the schema does not describe at all, published
// here anyway because the endpoint that returns them is specified.
//
// This list is the one that must stay short. Every entry is a shape this
// repository invented and published under the schema's naming convention, so
// every entry is a way for a client author to mistake one implementation's
// reading for the protocol. The mitigation is the godoc heading
// TestNotNormativeTypesSaySo requires; the list is what makes it unmissable
// how many of these there are and what they cost.
//
// # It is empty, and how it emptied is the point
//
// It held two: `Turn` and `Share`. Both are now schema objects — `TurnItem` and
// `SessionShare` — and both got there because the caveats in this list were
// written out as harnessrouter#42 and #44 rather than kept as local notes. The
// list is not a backlog and emptying it was never the goal; that it emptied by
// the specification growing, rather than by this package quietly dropping the
// warning, is the outcome worth keeping the machinery for.
//
// It stays because the next endpoint specified ahead of its response shape puts
// an entry here, and an empty list a contributor has to add the first line back
// to is a worse prompt than an empty one they can simply extend.
var notNormative = map[string]string{}

// deprecatedAliases are names this package used before the schema had one to
// borrow, kept as aliases to the type that now carries the schema's name.
//
// An alias is a declared type as far as declaredTypes is concerned, so without
// this list every deprecation would fail TestEveryPublicTypeIsAccountedFor for
// having no home — and the fix a contributor would reach for is deleting the
// alias, which is exactly the breaking change the alias exists to defer.
//
// An entry is a commitment to remove it, not to keep it. The release it goes in
// belongs in the type's godoc, where the client author reading the deprecation
// notice will see it.
var deprecatedAliases = map[string]string{
	"Turn":  "renamed TurnItem when harnessrouter#53 gave Sessions §3's item a schema object.",
	"Share": "renamed SessionShare when harnessrouter#53 gave Sessions §5's share a schema object.",
}

// clientMachinery is the part of this package that is not wire vocabulary at
// all.
//
// A type here never appears in a document. It is the apparatus for sending and
// receiving them, and the schema has nothing to say about it — no more than it
// has to say about net/http.Client. Listing them is not an admission; it is the
// only way the check above can be about the shapes it is meant to be about.
var clientMachinery = map[string]string{
	"Client":               "the HTTP client itself.",
	"SessionFilter":        "arguments to a listing call, sent as query parameters rather than as a body.",
	"Stream":               "an open SSE response, not a thing on the wire.",
	"EventDecoder":         "the SSE framing reader; [uhp.Event] is the shape it yields.",
	"VersionMismatchError": "a client-side diagnosis, raised from a UHP-Version header.",
	"GapError":             "a client-side diagnosis, raised from a sequence_number gap.",
	"FrameError":           "a client-side diagnosis, raised from a malformed SSE frame.",
}

// declaredTypes returns every exported type this package declares, mapped to
// the doc comment attached to it.
//
// Parsing the source is the only way to ask this question. Reflection reaches a
// type only from a value of it, so a type nobody wrote a test for — precisely
// the case this file is here to catch — is invisible to it. The package's own
// directory is the test's working directory, so the files are simply there.
func declaredTypes(t *testing.T) map[string]string {
	t.Helper()

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}

	fset := token.NewFileSet()
	out := make(map[string]string)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				// A one-spec declaration carries its comment on the GenDecl and
				// a grouped one carries it on the spec; godoc reads both, so
				// this does too.
				doc := ts.Doc
				if doc == nil && len(gen.Specs) == 1 {
					doc = gen.Doc
				}
				out[ts.Name.Name] = doc.Text()
			}
		}
	}

	// A glob that matched nothing, or a package that moved out from under this
	// test, would otherwise report every type as accounted for.
	if len(out) == 0 {
		t.Fatal("no exported type declarations found in the package directory; this test found nothing to check rather than nothing wrong")
	}
	return out
}

func keysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryPublicTypeIsAccountedFor fails when this package publishes a type
// the schema does not define and no list above claims.
//
// The failure message is written for the contributor who hits it, because the
// answer is a judgement rather than a lookup: the type is either a schema shape
// spelled wrong, a server extension that belongs in uhpgo, an alias left behind
// by a rename, or a genuine twenty-sixth shape that must be documented as one.
func TestEveryPublicTypeIsAccountedFor(t *testing.T) {
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(uhp.SchemaJSON), &doc); err != nil {
		t.Fatalf("parse vendored schema: %v", err)
	}

	declared := declaredTypes(t)
	for _, name := range keysOf(declared) {
		var homes []string
		if _, ok := doc.Defs[name]; ok {
			homes = append(homes, "a schema object")
		}
		if _, ok := inlineSchemaObjects[name]; ok {
			homes = append(homes, "inlineSchemaObjects")
		}
		if _, ok := notNormative[name]; ok {
			homes = append(homes, "notNormative")
		}
		if _, ok := deprecatedAliases[name]; ok {
			homes = append(homes, "deprecatedAliases")
		}
		if _, ok := clientMachinery[name]; ok {
			homes = append(homes, "clientMachinery")
		}

		switch len(homes) {
		case 1:
		case 0:
			t.Errorf("uhp.%s is published beside the schema's objects without being one of them.\n"+
				"Either it mirrors a $defs entry and is misnamed, or it is this server's own "+
				"addition and belongs in uhp/uhpgo, or it is the old name of a type the schema "+
				"has since renamed — in which case add it to deprecatedAliases — or it is a "+
				"shape the schema leaves untyped, in which case add it to notNormative and say "+
				"so in its godoc.", name)
		default:
			t.Errorf("uhp.%s is claimed by more than one list: %v", name, homes)
		}
	}

	// The lists are kept honest in the other direction too: an entry naming a
	// type that no longer exists is a caveat still being advertised for a
	// shape nobody ships, and it lengthens the list a reviewer is meant to be
	// able to read at a glance.
	for _, list := range []struct {
		name  string
		types []string
	}{
		{"inlineSchemaObjects", keysOf(inlineSchemaObjects)},
		{"notNormative", keysOf(notNormative)},
		{"deprecatedAliases", keysOf(deprecatedAliases)},
		{"clientMachinery", keysOf(clientMachinery)},
	} {
		for _, typ := range list.types {
			if _, ok := declared[typ]; !ok {
				t.Errorf("%s names uhp.%s, which this package no longer declares", list.name, typ)
			}
		}
	}
}

// TestInlineSchemaObjectsValidate is TestPublicTypesValidateAgainstSchema for
// the two shapes that have no $defs entry to name.
//
// Being described inline makes a shape no less normative, so the same check
// applies: marshal through the public API and validate the bytes, here against
// the JSON pointer the parent object describes it at. This also resolves the
// pointer, which is what stops an entry in inlineSchemaObjects from being a
// claim rather than a check.
func TestInlineSchemaObjectsValidate(t *testing.T) {
	for _, name := range keysOf(inlineSchemaObjects) {
		obj := inlineSchemaObjects[name]
		t.Run(name, func(t *testing.T) {
			validateRef(t, obj.ref, "uhp."+name, obj.value)

			// The negative half, for the reason inlineObject.invalid gives: a
			// subschema that accepts everything passes the line above while
			// proving nothing.
			var doc any
			dec := json.NewDecoder(strings.NewReader(obj.invalid))
			dec.UseNumber()
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("decode invalid document for uhp.%s: %v", name, err)
			}
			if err := compileRef(t, obj.ref).Validate(doc); err == nil {
				t.Errorf("%s accepts %s, so it constrains nothing this test can see; "+
					"the pointer resolves but does not name the shape uhp.%s mirrors",
					obj.ref, obj.invalid, name)
			}
		})
	}
}

// TestNotNormativeTypesSaySo requires each invented shape to carry the godoc
// heading that warns a reader it is one.
//
// The list above is invisible to a client author; godoc is what they read. A
// type may be on the list only if the warning is where the reader is, which is
// what makes "documented" a fact this suite checks rather than a step someone
// remembered.
func TestNotNormativeTypesSaySo(t *testing.T) {
	const heading = "# This shape is not normative"

	declared := declaredTypes(t)
	for _, name := range keysOf(notNormative) {
		doc, ok := declared[name]
		if !ok {
			continue // TestEveryPublicTypeIsAccountedFor reports the stale entry.
		}
		if !strings.Contains(doc, heading) {
			t.Errorf("uhp.%s is on the notNormative list and its godoc does not contain %q, "+
				"so a client author reading it cannot tell the shape from the protocol", name, heading)
		}
	}
}
