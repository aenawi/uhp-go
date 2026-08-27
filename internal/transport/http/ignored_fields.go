package http

import (
	"bytes"
	"encoding/json"
)

// droppableFields are the schema's request properties this server accepts and
// does not act on, in the order the schema declares them.
//
// Dropping them is the specified behaviour rather than a defect: Tasks §1.1
// marks every field but `input` optional and requires a server to *ignore* a
// field it does not understand rather than reject it. What was missing was any
// way for the caller to find out — a request setting `max_step: 5` got
// unbounded work and silence, which is the complaint #48 is written about.
//
// The list is deliberately short and hand-maintained rather than derived from
// the schema. A field leaves it by being implemented, and the compiler cannot
// notice that; a test that names all four is what does. Each has an issue:
// `max_step` is #72, and the remaining three are #48. `background` left the
// list by being implemented — see ADR-0005 and issue #78.
var droppableFields = []string{
	"max_output_tokens",
	"max_step",
	"tools",
	"include",
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
	for _, name := range droppableFields {
		raw, sent := present[name]
		if !sent || !carriesInstruction(raw) {
			continue
		}
		out = append(out, name)
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
