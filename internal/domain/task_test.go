package domain

import (
	"encoding/json"
	"testing"
	"time"
)

// wireError marshals a task and returns its `error` object as raw keys, which is
// the only way to ask whether a field was emitted rather than whether it was
// set. The two were different questions while `type` carried omitempty.
func wireError(t *testing.T, task *Task) map[string]any {
	t.Helper()
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return wire.Error
}

// Issue #47: the schema makes `type`, `code` and `message` required on an error
// object, and UHP's fourth client rule — treat an unrecognised code as its type
// — has nothing to read without `type`.
//
// This guards the tag, not the value: it asserts the key is emitted even for an
// error whose Type nobody set, because that is the case omitempty silently
// dropped. That a construction site sets a *valid* type is a separate question,
// asked where the error is built.
func TestErrorAlwaysEmitsTheRequiredKeys(t *testing.T) {
	task := &Task{
		ID:        "resp_1",
		Status:    StatusFailed,
		CreatedAt: time.Unix(0, 0).UTC(),
		Error:     &TaskError{Code: "uhpgo_something_failed", Message: "boom"},
	}

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

// The error types this server sets must be drawn from the schema's six-value
// enum. A typo in a string literal is invisible until a client branches on it,
// which is why the four in use are constants rather than spellings.
func TestErrorTypesAreSchemaValues(t *testing.T) {
	allowed := map[string]bool{
		"invalid_request_error": true,
		"authentication_error":  true,
		"permission_error":      true,
		"rate_limit_error":      true,
		"harness_error":         true,
		"server_error":          true,
	}
	for _, v := range []string{
		ErrorTypeInvalidRequest,
		ErrorTypeAuthentication,
		ErrorTypeHarness,
		ErrorTypeServerError,
	} {
		if !allowed[v] {
			t.Errorf("error type %q is not one of the schema's six values", v)
		}
	}
}
