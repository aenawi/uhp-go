package domain

import (
	"encoding/json"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

// wireTask marshals a task and returns its raw keys, which is the only way to
// ask whether a field was emitted rather than whether it was set.
func wireTask(t *testing.T, task *Task) map[string]any {
	t.Helper()
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}

func wireError(t *testing.T, task *Task) map[string]any {
	t.Helper()
	e, _ := wireTask(t, task)["error"].(map[string]any)
	return e
}

// Issue #47: the schema makes `type`, `code` and `message` required on an error
// object, and UHP's fourth client rule — treat an unrecognised code as its type
// — has nothing to read without `type`.
//
// This guards the tag, not the value: it asserts the key is emitted even for an
// error whose Type nobody set, because that is the case omitempty silently
// dropped. That the six spellings are the schema's six is now asked directly of
// the schema, in uhp/schema_test.go, rather than against a copy of the enum
// kept here.
func TestErrorAlwaysEmitsTheRequiredKeys(t *testing.T) {
	task := &Task{Response: uhp.Response{
		ID:     "resp_1",
		Status: uhp.StatusFailed,
		Error:  &uhp.Error{Code: "uhpgo_something_failed", Message: "boom"},
	}}

	got := wireError(t, task)
	if got == nil {
		t.Fatal("a task carrying an error marshalled without an error object")
	}
	for _, k := range []string{"type", "code", "message"} {
		if _, ok := got[k]; !ok {
			t.Errorf("error object has no %q; the schema requires it", k)
		}
	}
}

// A task marshals as the response object the published type produces, because
// it does not marshal itself: the method is [uhp.Response]'s and Go promotes it.
//
// The consequence is the one ADR-0003 is about. Nothing on the outer struct
// reaches the wire, however wire-shaped it looks, so the three fields that do
// belong on the wire have to be inside Metadata by the time this runs.
func TestTaskMarshalsOnlyTheEmbeddedResponse(t *testing.T) {
	task := &Task{
		Response:  uhp.Response{ID: "resp_1", Object: "response", Status: uhp.StatusCompleted},
		SessionID: "sess_1",
		HarnessID: "chrn_1",
		Input:     "the input is internal and must never appear",
	}

	got := wireTask(t, task)
	if _, ok := got["input"]; ok {
		t.Error("the task's internal input reached the wire")
	}
	// Before SyncMetadata, the session id is nowhere — which is exactly the
	// silent drop the naive embedding produces, and the reason the projection
	// is not left to marshal time.
	meta, _ := got["metadata"].(map[string]any)
	if _, ok := meta["session_id"]; ok {
		t.Error("session_id appeared in metadata without SyncMetadata having run")
	}

	task.SyncMetadata()
	meta, _ = wireTask(t, task)["metadata"].(map[string]any)
	if meta["session_id"] != "sess_1" {
		t.Errorf("metadata.session_id = %v, want sess_1 — Tasks §3 makes it a MUST", meta["session_id"])
	}
	if meta["harness_id"] != "chrn_1" {
		t.Errorf("metadata.harness_id = %v, want chrn_1", meta["harness_id"])
	}
}

// SyncMetadata runs at two points in a task's life, so running it twice must
// mean the same thing as running it once.
func TestSyncMetadataIsIdempotent(t *testing.T) {
	task := &Task{
		Response:  uhp.Response{ID: "resp_1", Model: "m"},
		SessionID: "sess_1",
	}
	task.SyncMetadata()
	first := wireTask(t, task)["metadata"]
	task.SyncMetadata()
	if got := wireTask(t, task)["metadata"]; !jsonEqual(t, got, first) {
		t.Errorf("metadata changed on a second sync: %v then %v", first, got)
	}
}

// The client's own metadata map is not the task's to write into.
//
// It arrives on the request and the caller keeps a reference to it, so a
// projection that mutated it in place would hand the caller back a map with
// fields it never sent.
func TestSyncMetadataDoesNotMutateTheCallersMap(t *testing.T) {
	clients := map[string]any{"harness_id": "chrn_1"}
	task := &Task{
		Response:  uhp.Response{ID: "resp_1", Metadata: clients},
		SessionID: "sess_1",
	}
	task.SyncMetadata()

	if _, ok := clients["session_id"]; ok {
		t.Error("SyncMetadata wrote into the map the caller handed in")
	}
	if len(clients) != 1 {
		t.Errorf("the caller's map grew to %d keys", len(clients))
	}
}

// Tasks §1.3: model_fallback is a claim that the model asked for is not the
// model that ran. The model can change between the two sync points (#43), so a
// projection that only ever added would leave that claim behind after it had
// stopped being true.
func TestModelFallbackIsClearedWhenItStopsApplying(t *testing.T) {
	task := &Task{
		Response:       uhp.Response{ID: "resp_1", Model: "substitute"},
		RequestedModel: "asked-for",
	}
	task.SyncMetadata()

	meta, _ := wireTask(t, task)["metadata"].(map[string]any)
	if meta["model_fallback"] != true || meta["requested_model"] != "asked-for" {
		t.Fatalf("a substitution was not declared: %v", meta)
	}

	// The second sync point: the run resolved to the model that was asked for
	// after all.
	task.Model = "asked-for"
	task.SyncMetadata()

	meta, _ = wireTask(t, task)["metadata"].(map[string]any)
	if _, ok := meta["model_fallback"]; ok {
		t.Error("model_fallback survived the substitution it described")
	}
	if _, ok := meta["requested_model"]; ok {
		t.Error("requested_model survived the substitution it described")
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(x) == string(y)
}
