package http

import (
	"bytes"
	"encoding/json"
)

// droppedStatus says why a field is on the droppable list. The two are not the
// same fact and were read as one for as long as the list was only names:
// `pending` is work nobody has done, and `declined` is a decision somebody made
// — see ADR-0007.
//
// Nothing on the wire distinguishes them. `metadata.ignored_fields` names both
// kinds identically, because the difference is about this server's intentions
// and the caller asked about their request.
type droppedStatus string

const (
	// declined: this server will not implement the field. The reason is in
	// ADR-0007, per field, and it is the same obstacle each time — this server
	// drives five CLIs rather than talking to a model, so a sampling parameter
	// has nowhere to go, an undescribed object cannot be honoured without
	// inventing protocol, and there is no extra content to return.
	declined droppedStatus = "declined"

	// pending: no decision against the field, just not built yet. Expect this
	// one to move; that is the whole difference from declined.
	pending droppedStatus = "pending"
)

// droppedField is one entry: the schema property name, and whether it is on the
// list by decision or by omission.
type droppedField struct {
	name   string
	status droppedStatus
}

// droppableFields are the schema's request properties this server accepts and
// does not act on, in the order the schema declares them.
//
// Dropping them is the specified behaviour rather than a defect: Tasks §1.1
// marks every field but `input` optional and requires a server to *ignore* a
// field it does not understand rather than reject it. What was missing was any
// way for the caller to find out — a request setting `max_step: 5` got
// unbounded work and silence, which is the complaint #48 was written about.
//
// The list is deliberately short and hand-maintained rather than derived from
// the schema. A `pending` field leaves it by being implemented, a `declined`
// one does not leave it at all, and the compiler notices neither; a test that
// names all four and both statuses is what does. `background` left the list by
// being implemented — see ADR-0005 and issue #78.
var droppableFields = []droppedField{
	{"max_output_tokens", declined}, // ADR-0007: no base takes it, and nothing
	// can count tokens before the run that generated them is over.
	{"max_step", pending}, // #72: needs a step counter no adapter offers.
	{"tools", declined},   // ADR-0007: the schema does not describe these
	// objects, and guessing what they are would be inventing protocol.
	{"include", declined}, // ADR-0007: no agreed vocabulary, and no extra
	// content to return even if there were one.
}

// ignoredFields names which of the above this request actually sent, for
// `metadata.ignored_fields`. It returns nil when there is nothing to report,
// because the key is absent rather than empty in that case: a client can then
// test presence, and a request that asked for nothing unread looks exactly as
// it always did.
//
// Only fields this server knows and does not act on. Unrecognised fields are
// deliberately not reported: §1.1's ignore-don't-reject rule exists so a newer
// client can talk to an older server, and a server that named every field it
// did not recognise would turn forward compatibility into a stream of warnings
// about perfectly valid protocol.
func ignoredFields(present map[string]json.RawMessage) []string {
	var out []string
	for _, field := range droppableFields {
		raw, sent := present[field.name]
		if !sent || !carriesInstruction(raw) {
			continue
		}
		out = append(out, field.name)
	}
	return out
}

// carriesInstruction reports whether a field's value asks for something this
// server did not do. `null` is a key with no value in it, so reporting one
// would tell a client its request was diminished when nothing in it was.
//
// There used to be a second case here, `"background": false`, which named the
// behaviour this server already provided and so was honoured rather than
// dropped. It went when `background` did (#78): a field this server implements
// is not on the droppable list at all, and neither of its two values is
// reported.
//
// Nothing replaced it, and the four that remain are reported on presence alone.
// That is not quite exact — `"tools": []` and `"include": []` ask for nothing
// and are still named — but an empty list is a client saying something it could
// have said by omission, where `background: false` was a client saying the one
// thing this server actually did. Naming an empty list overstates by a word;
// naming `background: false` would have been false.
func carriesInstruction(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
