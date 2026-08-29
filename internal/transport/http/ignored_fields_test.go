package http

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// ignoredIn reads `metadata.ignored_fields` off a created response, and reports
// whether the key was there at all — absent and empty are different answers.
func ignoredIn(t *testing.T, srv *Server, body string) ([]string, bool) {
	t.Helper()
	resp := createResponse(t, srv, body)
	raw, present := resp.Metadata["ignored_fields"]
	if !present {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("metadata.ignored_fields = %#v, want an array", raw)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out, true
}

// The client-facing half of #48: a caller that sets a field this server does
// not act on gets no signal that it was discarded.
//
// It used to be written against `max_step`, which was the sharpest example
// until #72 implemented it. `max_output_tokens` is the replacement and is the
// better one now: ADR-0007 declined it outright, so unlike `max_step` it will
// not one day make this test obsolete by being built.
func TestADroppedFieldIsNamedOnTheResponse(t *testing.T) {
	got, present := ignoredIn(t, newTestServer(),
		`{"input":"hi","max_output_tokens":5,"metadata":{"harness_id":"chrn_echo"}}`)
	if !present {
		t.Fatal("metadata.ignored_fields is absent for a request that set max_output_tokens")
	}
	if !reflect.DeepEqual(got, []string{"max_output_tokens"}) {
		t.Errorf("ignored_fields = %v, want [max_output_tokens]", got)
	}
}

// The mirror image, and the reason the test above had to change: `max_step` is
// read now, so naming it would be the defect `ignored_fields` exists to fix
// wearing the opposite face — a field the server acted on, reported as one it
// discarded. `background` has the same test above it for the same reason (#78).
func TestMaxStepIsNotReportedNowThatItIsImplemented(t *testing.T) {
	srv := newTestServer()
	for _, body := range []string{
		`{"input":"hi","max_step":5,"metadata":{"harness_id":"chrn_echo"}}`,
		`{"input":"hi","max_step":0,"metadata":{"harness_id":"chrn_echo"}}`,
	} {
		if got, present := ignoredIn(t, srv, body); present {
			t.Errorf("ignored_fields = %v for %s, want absent: the field is read", got, body)
		}
	}
}

// Absent rather than empty, so a client can test presence and a request that
// sent nothing unread looks exactly as it always did.
func TestARequestWithNothingDroppedCarriesNoIgnoredFieldsKey(t *testing.T) {
	if got, present := ignoredIn(t, newTestServer(),
		`{"input":"hi","metadata":{"harness_id":"chrn_echo"}}`); present {
		t.Errorf("metadata.ignored_fields = %v on a request that had nothing dropped", got)
	}
}

// Schema order, so the list is stable between two requests that sent the same
// fields in different orders.
func TestIgnoredFieldsAreReportedInSchemaOrder(t *testing.T) {
	srv := newTestServer()
	first, _ := ignoredIn(t, srv,
		`{"input":"hi","include":["x"],"tools":[{"type":"y"}],"max_output_tokens":2,"metadata":{"harness_id":"chrn_echo"}}`)
	second, _ := ignoredIn(t, srv,
		`{"input":"hi","max_output_tokens":2,"tools":[{"type":"y"}],"include":["x"],"metadata":{"harness_id":"chrn_echo"}}`)
	want := []string{"max_output_tokens", "tools", "include"}
	if !reflect.DeepEqual(first, want) || !reflect.DeepEqual(second, want) {
		t.Errorf("ignored_fields = %v and %v, want %v both times", first, second, want)
	}
}

// The fields this server reads are not dropped, and must not be reported as
// though they were.
func TestFieldsThisServerActsOnAreNeverReportedAsIgnored(t *testing.T) {
	got, present := ignoredIn(t, newTestServer(),
		`{"input":"hi","instructions":"be brief","store":true,"timeout_seconds":30,"max_step":4,"model":"m","metadata":{"harness_id":"chrn_echo"}}`)
	if present {
		t.Errorf("ignored_fields = %v, want absent: every field in that request is read", got)
	}
}

// Tasks §1.1's ignore-don't-reject rule exists so a newer client can talk to an
// older server. A server that named every field it did not recognise would turn
// forward compatibility into a stream of warnings about valid protocol.
func TestAnUnrecognisedFieldIsIgnoredWithoutBeingNamed(t *testing.T) {
	got, present := ignoredIn(t, newTestServer(),
		`{"input":"hi","some_future_field":"x","metadata":{"harness_id":"chrn_echo"}}`)
	if present {
		t.Errorf("ignored_fields = %v, want absent: unrecognised is not the same as unread", got)
	}
}

// A key with no value carries no instruction, so reporting it would tell a
// client its request was diminished when nothing in it was.
func TestANullValuedFieldIsNotReported(t *testing.T) {
	if got, present := ignoredIn(t, newTestServer(),
		`{"input":"hi","tools":null,"max_output_tokens":null,"metadata":{"harness_id":"chrn_echo"}}`); present {
		t.Errorf("ignored_fields = %v, want absent: null is not a value", got)
	}
}

// `background` was reported until #78 implemented it, and reporting it now
// would be the mirror image of the defect this key exists to fix: a field the
// server acted on, named as one it discarded. Neither of its two values is
// dropped any more — `false` is the POST held open that it always was, and
// `true` is answered at acceptance. See ADR-0005.
func TestBackgroundIsNotReportedNowThatItIsImplemented(t *testing.T) {
	srv := newTestServer()
	for _, body := range []string{
		`{"input":"hi","background":false,"metadata":{"harness_id":"chrn_echo"}}`,
		`{"input":"hi","background":true,"metadata":{"harness_id":"chrn_echo"}}`,
	} {
		if got, present := ignoredIn(t, srv, body); present {
			t.Errorf("ignored_fields = %v for %s, want absent: the field is read", got, body)
		}
	}
}

// A client's own metadata survives, and its own `ignored_fields` does not: this
// server's answer to the question has to be the one on the response.
func TestTheServersIgnoredFieldsReplaceAClientsOwn(t *testing.T) {
	resp := createResponse(t, newTestServer(),
		`{"input":"hi","max_output_tokens":1,"metadata":{"harness_id":"chrn_echo","ignored_fields":["nonsense"],"mine":"kept"}}`)
	if resp.Metadata["mine"] != "kept" {
		t.Errorf("metadata.mine = %v, want the client's own value", resp.Metadata["mine"])
	}
	list, _ := resp.Metadata["ignored_fields"].([]any)
	if len(list) != 1 || list[0] != "max_output_tokens" {
		t.Errorf("metadata.ignored_fields = %v, want [max_output_tokens]",
			resp.Metadata["ignored_fields"])
	}
}

// The list is hand-maintained: a `pending` field leaves it by being
// implemented, a `declined` one does not leave it at all, and the compiler
// notices neither. This is what notices.
func TestTheDroppableListIsTheFieldsThisServerDoesNotRead(t *testing.T) {
	want := []droppedField{
		{"max_output_tokens", declined},
		{"tools", declined},
		{"include", declined},
	}
	if !reflect.DeepEqual(droppableFields, want) {
		t.Errorf("droppableFields = %v, want %v — if a field was implemented, it leaves this list "+
			"and docs/conformance.md says so; if one was added, it joins it",
			droppableFields, want)
	}
	// Every name here is a property of the request schema, and a typo would
	// name a field no client can send.
	read := map[string]bool{
		"input": true, "model": true, "metadata": true, "stream": true,
		"previous_response_id": true, "instructions": true, "store": true,
		"timeout_seconds": true, "background": true, "max_step": true,
	}
	if len(read)+len(droppableFields) != 13 {
		t.Errorf("%d read + %d dropped = %d, want the schema's 13 properties",
			len(read), len(droppableFields), len(read)+len(droppableFields))
	}
	for _, field := range droppableFields {
		if read[field.name] {
			t.Errorf("%q is in both lists", field.name)
		}
	}
}

// The split was the deliverable of #48, and #72 emptied one side of it: every
// field still dropped is a decision recorded in ADR-0007, and the one entry
// that was ever `pending` left the list by being implemented — which is exactly
// what that status predicted.
//
// So this pins the absence rather than a member. A new `pending` entry is not
// forbidden and would not be wrong; it would mean somebody has decided a field
// is worth building, and it should arrive with an issue behind it rather than
// by drifting in.
func TestEveryDroppedFieldIsNowADecision(t *testing.T) {
	var stillPending []string
	for _, field := range droppableFields {
		switch field.status {
		case pending:
			stillPending = append(stillPending, field.name)
		case declined:
		default:
			t.Errorf("%q has status %q, which is neither declined nor pending",
				field.name, field.status)
		}
	}
	if len(stillPending) != 0 {
		t.Errorf("pending = %v, want none — `max_step` was the last one and ADR-0009 "+
			"implemented it; a field arriving here needs an issue saying who is building it",
			stillPending)
	}
}

// A body that is valid JSON but not an object is a client error, not a panic in
// the second decode.
func TestABodyThatIsNotAnObjectIsRefused(t *testing.T) {
	w := do(t, newTestServer(), "POST", "/v1/responses", strings.NewReader(`["not","an","object"]`))
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal is not JSON: %s", w.Body.String())
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("the refusal carries no error object: %v", body)
	}
}
