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
// notice that; a test that names all five is what does. Each has an issue:
// `max_step` is #72, `background` is #78, and the remaining three are #48.
var droppableFields = []string{
	"max_output_tokens",
	"max_step",
	"tools",
	"include",
	"background",
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
		if !sent || !carriesInstruction(name, raw) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// carriesInstruction reports whether a field's value asks for something this
// server did not do. Two values are present on the wire and ask for nothing.
//
// `null` is the first: a key with no value carries no instruction, so
// reporting it would tell a client its request was diminished when nothing in
// it was.
//
// `"background": false` is the second, and is the one worth spelling out. It
// names the behaviour this server actually provides — a POST held open until
// the task is done — so listing it would claim a request was ignored that was
// in fact honoured exactly. `background: true` is the one that is dropped, and
// is reported. No other droppable field has a value that means "the default",
// which is why this is a case on one name rather than a general rule.
func carriesInstruction(name string, raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	if name == "background" && bytes.Equal(trimmed, []byte("false")) {
		return false
	}
	return true
}
