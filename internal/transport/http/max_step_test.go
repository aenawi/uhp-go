package http

import (
	"strings"
	"testing"
)

// The wire half of #72: what a client may send, what it is told back, and what
// it is refused.

// `max_step` used to be accepted, dropped, and named in
// `metadata.ignored_fields`. Now it is read — and the resolved number comes
// back, which is what makes a narrowed ceiling something the caller can act on
// rather than something it discovers by being stopped early.
func TestTheResolvedCeilingIsReportedOnTheResponse(t *testing.T) {
	resp := createResponse(t, newTestServer(),
		`{"input":"hi","max_step":4,"metadata":{"harness_id":"chrn_echo"}}`)
	if got := resp.Metadata["max_step"]; got != float64(4) {
		t.Errorf("metadata.max_step = %#v, want 4", got)
	}
}

// Absent rather than zero for an unbounded task, because the two are different
// answers: zero is a ceiling that permits no tool call, and absence is no
// ceiling at all. A server that reported one as the other would be telling
// every ordinary caller its agent had been muzzled.
func TestAnUnboundedTaskCarriesNoMaxStepKey(t *testing.T) {
	resp := createResponse(t, newTestServer(),
		`{"input":"hi","metadata":{"harness_id":"chrn_echo"}}`)
	if got, present := resp.Metadata["max_step"]; present {
		t.Errorf("metadata.max_step = %#v on a task that asked for no ceiling", got)
	}
}

// Zero is a request this server honours, so it must survive the round trip as
// zero rather than being flattened into an absence by the first thing that
// treats it as a missing value.
func TestAZeroCeilingIsReportedAsZero(t *testing.T) {
	resp := createResponse(t, newTestServer(),
		`{"input":"hi","max_step":0,"metadata":{"harness_id":"chrn_echo"}}`)
	got, present := resp.Metadata["max_step"]
	if !present {
		t.Fatal("metadata.max_step is absent for `max_step: 0` — the client is told its " +
			"tightest bound was dropped")
	}
	if got != float64(0) {
		t.Errorf("metadata.max_step = %#v, want 0", got)
	}
}

// Refused rather than ignored, and the asymmetry with `timeout_seconds` is the
// point: that one refuses zero as well, because "stop before you start" is not
// something a caller could have meant. A negative number of steps is not a
// stricter ceiling either — it is a value with no meaning — and §1.1's
// ignore-don't-reject rule covers fields a server does not understand, not
// values that say nothing.
func TestANegativeCeilingIsRefused(t *testing.T) {
	w := do(t, newTestServer(), "POST", "/v1/responses",
		strings.NewReader(`{"input":"hi","max_step":-1,"metadata":{"harness_id":"chrn_echo"}}`))
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"param":"max_step"`) {
		t.Errorf("the refusal does not name the field: %s", w.Body.String())
	}
}

// `null` is a key with no value in it, and asks for exactly what omitting it
// asks for. Refusing it would turn a client that spelled "no ceiling" the long
// way into an error.
func TestANullCeilingIsTheSameAsNone(t *testing.T) {
	resp := createResponse(t, newTestServer(),
		`{"input":"hi","max_step":null,"metadata":{"harness_id":"chrn_echo"}}`)
	if got, present := resp.Metadata["max_step"]; present {
		t.Errorf("metadata.max_step = %#v for `max_step: null`", got)
	}
}
