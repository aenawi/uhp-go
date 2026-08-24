package uhp

// The event types the Streaming chapter defines.
//
// The list is open and these constants are a convenience, not an enum. A server
// MAY add event types within a published version, and a client MUST ignore ones
// it does not recognise — skip and continue reading, never error. Switching on
// [Event].Type with a default that does nothing is the whole of that rule; see
// [EventDecoder] for why the decoder cannot enforce it on your behalf.
const (
	// Response lifecycle.
	EventResponseCreated    = "response.created"
	EventResponseInProgress = "response.in_progress"
	EventResponseCompleted  = "response.completed"
	EventResponseIncomplete = "response.incomplete"
	EventResponseFailed     = "response.failed"

	// Output items.
	EventOutputItemAdded = "response.output_item.added"
	EventOutputItemDone  = "response.output_item.done"

	// Assistant text.
	EventContentPartAdded   = "response.content_part.added"
	EventContentPartDone    = "response.content_part.done"
	EventOutputTextDelta    = "response.output_text.delta"
	EventOutputTextDone     = "response.output_text.done"
	EventOutputTextAnnAdded = "response.output_text.annotation.added"

	// Reasoning. A summary, never a verbatim chain of thought: a server is
	// never required to expose raw model reasoning, and many harness and model
	// combinations produce none, so a client that requires these will break.
	EventReasoningSummaryPartAdded = "response.reasoning_summary_part.added"
	EventReasoningSummaryTextDelta = "response.reasoning_summary_text.delta"
	EventReasoningSummaryPartDone  = "response.reasoning_summary_part.done"

	// Tool use. The call itself arrives as an output item of type
	// function_call and its result as function_call_output, matched by call_id.
	EventFunctionCallArgumentsDelta = "response.function_call_arguments.delta"
	EventFunctionCallArgumentsDone  = "response.function_call_arguments.done"

	// EventError reports a problem that did not end the task, and MUST be
	// followed by a terminal event. A stream that emits this and then stops is
	// malformed: the client cannot tell whether the task died or the connection
	// did.
	EventError = "error"
)

// Event is one streamed Server-Sent Event.
//
// The vocabulary is genuinely heterogeneous — a delta event carries no
// Response, a terminal event carries no Delta — so everything past Type and
// SequenceNumber is optional and omitted when absent. Reading a field that this
// event type does not carry gets you a zero value, not a lie: check Type first.
type Event struct {
	// Type is open; the Event constants above name the ones this version
	// defines.
	Type string `json:"type"`

	// SequenceNumber starts at 0 and increases by exactly 1 per event within a
	// stream.
	//
	// That guarantee is what lets a client detect a dropped event rather than
	// silently rendering a gap, and it is the only reason to read this field.
	// [EventDecoder] deliberately does not check it — see the note there.
	SequenceNumber int `json:"sequence_number"`

	// Response is carried by the lifecycle events. Every terminal event
	// carries the complete final response, which is deliberate redundancy: a
	// client that missed every intermediate event can render the result from
	// the last one alone, so a dropped connection mid-stream costs latency
	// rather than correctness.
	Response *Response `json:"response,omitempty"`

	Item       *OutputItem  `json:"item,omitempty"`
	Part       *ContentPart `json:"part,omitempty"`
	Annotation *Annotation  `json:"annotation,omitempty"`

	// Delta is the next fragment of text or of a JSON argument string.
	//
	// Fragments are not lines and not tokens. A client MUST concatenate them in
	// SequenceNumber order and MUST NOT assume any particular chunking.
	Delta string `json:"delta,omitempty"`
	// Text is the complete text of a part, on a *.done event.
	Text string `json:"text,omitempty"`
	// Arguments is the complete JSON argument string, on a
	// function_call_arguments.done event.
	Arguments string `json:"arguments,omitempty"`

	ItemID string `json:"item_id,omitempty"`

	// The four index fields are pointers because zero is a real index: the
	// first output item is index 0, and a plain int with omitempty would drop
	// the one value most likely to be correct.
	OutputIndex     *int `json:"output_index,omitempty"`
	ContentIndex    *int `json:"content_index,omitempty"`
	SummaryIndex    *int `json:"summary_index,omitempty"`
	AnnotationIndex *int `json:"annotation_index,omitempty"`

	// Set on an EventError, which reports a problem that did not end the task.
	Code    string  `json:"code,omitempty"`
	Message string  `json:"message,omitempty"`
	Param   *string `json:"param,omitempty"`
}

// IsTerminal reports whether this event type ends a stream. Exactly one
// terminal event is the last event of any stream (Streaming §3).
//
// A cancelled task terminates with response.failed carrying StatusCancelled in
// the response object: the status field is authoritative, not the event name,
// so a client deciding whether a task failed reads Response.Status rather than
// inferring it from here.
func (e Event) IsTerminal() bool {
	switch e.Type {
	case EventResponseCompleted, EventResponseIncomplete, EventResponseFailed:
		return true
	}
	return false
}
