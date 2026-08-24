package uhp

import "encoding/json"

// ResponseStatus is a response's lifecycle state: one non-terminal value and
// four terminal ones.
//
// The three terminal failures are not interchangeable and the distinction is
// the client's, not the server's convenience. StatusIncomplete means a budget
// stopped the work and continuing is usually worth trying; StatusFailed means
// it could not be done; StatusCancelled means the client asked for a stop and
// MUST NOT be reported as failed, because a client that got what it asked for
// has not hit an error.
type ResponseStatus string

const (
	StatusInProgress ResponseStatus = "in_progress"
	StatusCompleted  ResponseStatus = "completed"
	StatusFailed     ResponseStatus = "failed"
	StatusIncomplete ResponseStatus = "incomplete"
	StatusCancelled  ResponseStatus = "cancelled"
)

// Response is what a unit of work is reported as: UHP's response object, and
// the thing GET /v1/responses/{id} returns.
//
// Six fields are required by the schema — ID, Object, CreatedAt, Status, Output
// and Model — and four more are required to be *present* by Tasks §3 even when
// they carry nothing: Error, IncompleteDetails, PreviousResponseID and Usage
// are explicit nulls rather than omitted keys. None of them carries omitempty
// for that reason. A client that cannot tell "no value" from "this server is
// older than the field" has to guess, and the specification declines to make it
// guess.
type Response struct {
	// ID is "resp_"-prefixed.
	ID string `json:"id"`
	// Object is always "response".
	Object string `json:"object"`
	// CreatedAt is Unix seconds, not milliseconds. [Harness].CreatedAt is
	// milliseconds; the two chapters disagree and the schema records both.
	CreatedAt int64 `json:"created_at"`

	Status ResponseStatus `json:"status"`

	// Error is non-null only when Status is StatusFailed.
	Error *Error `json:"error"`

	// IncompleteDetails says why a StatusIncomplete response stopped.
	IncompleteDetails map[string]any `json:"incomplete_details"`

	// PreviousResponseID is the response this one continues, or null.
	PreviousResponseID *string `json:"previous_response_id"`

	// Model is the model that actually ran, which is not necessarily the one
	// that was asked for — compare it with Metadata["requested_model"].
	Model string `json:"model"`

	// Output is ordered and may be empty, but is never absent and never null.
	// See [Response.MarshalJSON].
	Output []OutputItem `json:"output"`

	// Store is whether this response was retained for later reads.
	Store bool `json:"store"`

	// Usage is null when the server cannot account for tokens. A fabricated
	// zero would be worse than an honest absence, because a client cannot tell
	// it from a task that genuinely cost nothing.
	Usage *Usage `json:"usage"`

	// Metadata MUST carry session_id (Tasks §3), and carries the model
	// substitution fields from Tasks §1.3 — requested_model, model_fallback,
	// model_fallback_reason — whenever the model that ran is not the model that
	// was asked for.
	//
	// It is a map rather than a struct because the schema leaves it open: it is
	// the extension point the task surface already defines for caller-supplied
	// context, and harness_id travels through it on the way in. Typing it would
	// throw away every key a caller put there.
	Metadata map[string]any `json:"metadata"`
}

// MarshalJSON renders the response, normalising the three fields whose unset
// case the schema does not allow.
//
// Output is declared type: array and Metadata type: object, so a nil Go slice
// or map — which encoding/json writes as null — produces a document that fails
// validation against the very schema this type mirrors. Normalising here rather
// than asking every caller to remember is what keeps a zero-valued Response
// marshalable into something conformant.
//
// It lives on this type and not on any type that embeds it. A server whose
// internal response type overrides this method emits JSON that a client
// marshalling a [Response] cannot reproduce, and a published type that does not
// round-trip to the wire format defeats the reason for publishing it. See
// docs/adr/0003-internal-types-embed-the-wire-types.md.
func (r Response) MarshalJSON() ([]byte, error) {
	// A distinct type, so that marshalling the copy does not call this method
	// again. It carries no methods of its own and Response embeds nothing, so
	// nothing else is promoted onto it.
	type response Response
	out := response(r)
	// The schema requires `object` and pins it to a constant, so an unset one
	// is not merely empty — it is a document that fails validation. Defaulting
	// it here beats asking every construction site to remember a value that has
	// exactly one legal spelling.
	if out.Object == "" {
		out.Object = "response"
	}
	if out.Output == nil {
		out.Output = []OutputItem{}
	}
	if out.Metadata == nil {
		out.Metadata = map[string]any{}
	}
	return json.Marshal(out)
}

// OutputItem is one element of a response's output array.
//
// UHP's output is an ordered array of typed items — message, reasoning,
// function_call, function_call_output — and the list is open. A client MUST
// tolerate item types it does not recognise, and one that renders only message
// items and ignores everything else is a valid client.
type OutputItem struct {
	ID string `json:"id,omitempty"`
	// Type is open. The schema gives examples, not an enum.
	Type   string `json:"type"`
	Status string `json:"status,omitempty"`
	Role   string `json:"role,omitempty"`

	Content []ContentPart `json:"content,omitempty"`

	// Summary carries a reasoning item's summary parts. It is a summary and
	// not a verbatim chain of thought: a server is never required to expose raw
	// model reasoning, and many harness and model combinations produce none, so
	// a client that requires this field will break on those.
	Summary []map[string]any `json:"summary,omitempty"`

	// CallID matches a function_call to its function_call_output.
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
	// Arguments is JSON as a string, not a nested object.
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

// ContentPart is one part of an output item's content.
type ContentPart struct {
	// Type is open; "output_text" is the one the schema gives as an example.
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// Annotations is always present and never null. See
	// [ContentPart.MarshalJSON].
	Annotations []Annotation `json:"annotations"`
}

// MarshalJSON renders the part with annotations always present as an array.
//
// A nil slice would marshal as null, which fails the schema's type: array — and
// omitting the key instead would cost a client the difference between "this
// part cites nothing" and "this server predates annotations". An empty array
// says the first without ambiguity, so that is what a zero-valued part emits.
func (c ContentPart) MarshalJSON() ([]byte, error) {
	type contentPart ContentPart
	out := contentPart(c)
	if out.Annotations == nil {
		out.Annotations = []Annotation{}
	}
	return json.Marshal(out)
}

// AnnotationTypeFileCitation is the only annotation type this version of the
// protocol defines. The schema pins Annotation.Type to it as a constant.
const AnnotationTypeFileCitation = "container_file_citation"

// Annotation cites a file from within assistant text (Files §2.1).
//
// It exists to point a client at bytes it can then fetch, which is why it
// carries the container and file ids and the URL that serves them rather than
// only an offset into the text.
type Annotation struct {
	// Type is always [AnnotationTypeFileCitation].
	Type        string `json:"type"`
	ContainerID string `json:"container_id,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	Filename    string `json:"filename,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`

	// StartIndex and EndIndex locate the citation within the part's text.
	//
	// Pointers because zero is a real offset: the first character of a part is
	// index 0, and a plain int with omitempty would silently drop a citation
	// that starts at the beginning of the text — the single most likely place
	// for one to start.
	StartIndex *int `json:"start_index,omitempty"`
	EndIndex   *int `json:"end_index,omitempty"`
}

// Usage accounts for the tokens a task consumed.
//
// Every field is optional in the schema, and a server that cannot account for
// usage reports [Response].Usage as null rather than reporting a Usage of
// zeroes — see the note on that field.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	// The cache fields are omitted when zero rather than reported as zero.
	// Unlike the three above they are genuinely optional in practice — a
	// harness that does no prompt caching has nothing to report, and writing
	// zero would claim a cache that was read and missed.
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}
