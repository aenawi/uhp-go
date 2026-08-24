package domain

import (
	"encoding/json"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

// A session marshals as the wire object and nothing more.
//
// This asserts the whole key set rather than the presence of the fields it
// wants, because the failure it exists to catch is an *extra* key. Embedding
// [uhp.Session] means the tags do the marshalling, and an exported field with
// no tag is emitted under its Go name — so forgetting one publishes the
// harness's own session id under "NativeSessionID", with every existing test
// still green because every one of them asks whether a key is present rather
// than whether it should be.
func TestSessionMarshalsAsTheWireObjectAndNothingElse(t *testing.T) {
	sess := &Session{
		Session: uhp.Session{
			ID: "sess_1", Object: "session", HarnessID: "chrn_1",
			Title: "a session", Status: string(uhp.StatusCompleted),
			CreatedAt: 1786400000, UpdatedAt: 1786400240,
		},
		NativeSessionID: "the-harness-thread-id",
		LastResponseID:  "resp_1",
	}

	b, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]any{
		"id": "sess_1", "object": "session", "harness_id": "chrn_1",
		"title": "a session", "status": "completed",
		"created_at": float64(1786400000), "updated_at": float64(1786400240),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("session published %q, which is not part of the wire object", k)
		}
	}
}
