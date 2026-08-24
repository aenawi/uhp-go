package uhpgo_test

import (
	"encoding/json"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

func marshalMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return got
}

// The extensions must survive marshalling, and this is not a formality.
//
// uhp.Harness carries a MarshalJSON and Go promotes it to anything that embeds
// it, so without uhpgo.Harness declaring its own, these three keys vanish from
// every harness the server serves — silently, with the code compiling and the
// protocol half of the object still perfectly correct.
func TestHarnessKeepsItsExtensionsWhenMarshalled(t *testing.T) {
	h := uhpgo.Harness{
		Harness: uhp.Harness{
			ID: "chrn_1", Object: "harness", Name: "Reviewer", Base: "claude-code",
		},
		Models:       []string{"claude-opus-5"},
		Capabilities: []uhpgo.Capability{uhpgo.CapStreaming, uhpgo.CapSessions},
		Status:       uhpgo.HarnessReady,
	}

	got := marshalMap(t, h)

	// The protocol half.
	if got["id"] != "chrn_1" || got["base"] != "claude-code" {
		t.Fatalf("protocol fields lost: %v", got)
	}
	// The extension half — the one a promoted marshaller would drop.
	models, ok := got["models"].([]any)
	if !ok || len(models) != 1 || models[0] != "claude-opus-5" {
		t.Errorf("models = %v, want the one model set", got["models"])
	}
	caps, ok := got["capabilities"].([]any)
	if !ok || len(caps) != 2 {
		t.Errorf("capabilities = %v, want two", got["capabilities"])
	}
	if got["status"] != uhpgo.HarnessReady {
		t.Errorf("status = %v, want %q", got["status"], uhpgo.HarnessReady)
	}
}

// The empty-array normalisation belongs to uhp.Harness and must still happen
// through the override, or the splice would have quietly replaced it with
// nothing.
func TestHarnessStillNormalisesTheProtocolLists(t *testing.T) {
	got := marshalMap(t, uhpgo.Harness{Harness: uhp.Harness{ID: "chrn_1", Base: "b"}})

	for _, key := range []string{"mcpServers", "skills", "disabledTools"} {
		v, ok := got[key]
		if !ok {
			t.Errorf("%s is absent; an empty list and no list are different claims", key)
			continue
		}
		if _, isArray := v.([]any); !isArray {
			t.Errorf("%s = %v, want an array", key, v)
		}
	}
}

// Event, Error and Upload embed types that carry no marshaller, so struct tags
// already flatten them. This is the test that says so rather than leaving it to
// be rediscovered — and that would fail if a marshaller were ever added to
// uhp.Event or uhp.Error without an override following it.
func TestEmbeddedExtensionsFlatten(t *testing.T) {
	ev := marshalMap(t, uhpgo.Event{
		Event:      uhp.Event{Type: uhp.EventOutputTextDelta, SequenceNumber: 3},
		ResponseID: "resp_1",
		SessionID:  "sess_1",
	})
	if ev["type"] != uhp.EventOutputTextDelta || ev["response_id"] != "resp_1" || ev["session_id"] != "sess_1" {
		t.Errorf("event did not flatten: %v", ev)
	}

	e := marshalMap(t, uhpgo.Error{
		Error:     uhp.Error{Type: uhp.ErrorTypeServerError, Code: "x", Message: "y"},
		Retryable: true,
	})
	if e["type"] != uhp.ErrorTypeServerError || e["retryable"] != true {
		t.Errorf("error did not flatten: %v", e)
	}
}

// An upload reports the size of the bytes it holds, and never the bytes.
func TestUploadReportsSizeAndWithholdsContent(t *testing.T) {
	got := marshalMap(t, uhpgo.Upload{
		File:     uhp.File{ID: "file_1", Filename: "q3.pdf", CreatedAt: 1786400240},
		MimeType: "application/pdf",
		Data:     []byte("hello"),
	})

	if got["bytes"] != float64(5) {
		t.Errorf("bytes = %v, want 5 — derived from Data, not from whoever set the field", got["bytes"])
	}
	if _, ok := got["data"]; ok {
		t.Error("the upload's bytes reached a client")
	}
	if got["object"] != "file" {
		t.Errorf("object = %v, want file", got["object"])
	}
}
