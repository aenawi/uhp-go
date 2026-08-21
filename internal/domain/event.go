package domain

// Event is one streamed Server-Sent Event (UHP "Streaming" chapter).
//
// It was previously a loose four-field union — type, task, delta, seq — which
// could not express the Responses event vocabulary at all: no item_id, no
// output_index, no content_index, no per-type payloads. A client could not
// tell which item a delta belonged to.
//
// Every field beyond type and sequence_number is optional and omitted when
// absent, because the vocabulary is genuinely heterogeneous: a delta event has
// no `response`, and a terminal event has no `delta`.
type Event struct {
	Type string `json:"type"`
	Seq  int    `json:"sequence_number"`

	Response *Task        `json:"response,omitempty"`
	Item     *OutputItem  `json:"item,omitempty"`
	Part     *ContentPart `json:"part,omitempty"`

	Delta string `json:"delta,omitempty"`
	Text  string `json:"text,omitempty"`

	ItemID       string `json:"item_id,omitempty"`
	OutputIndex  *int   `json:"output_index,omitempty"`
	ContentIndex *int   `json:"content_index,omitempty"`

	// ResponseID and SessionID say which task an event came from, and are set
	// only on a harness feed.
	//
	// A task's own stream carries one task, so naming it on every event would
	// be noise a client already has. A feed multiplexes every task on a
	// harness, and without these two an event cannot be attributed to
	// anything: a `response.output_text.delta` names an item, not a response,
	// so two tasks writing at once are one interleaved text with no way to
	// separate them again.
	ResponseID string `json:"response_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`

	// Set only on `error` events, which report a problem that did not end the
	// task and MUST be followed by a terminal event.
	Code    string  `json:"code,omitempty"`
	Message string  `json:"message,omitempty"`
	Param   *string `json:"param,omitempty"`
}

// IsTerminal reports whether this event type ends a stream. Exactly one
// terminal event must be the last event of any stream.
func (e Event) IsTerminal() bool {
	switch e.Type {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}
