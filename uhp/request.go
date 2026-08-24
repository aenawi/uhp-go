package uhp

// CreateResponseRequest is the body of POST /v1/responses.
//
// All thirteen fields the schema defines are here, including the ones this
// repository's server does not act on. That is deliberate and it is the reason
// this package exists: Tasks §1.1 marks every field but Input optional and
// requires a server to *ignore* request fields it does not understand rather
// than reject them, so sending one costs nothing and omitting it from the type
// would misrepresent the protocol as being as narrow as one implementation.
//
// Which of them this server currently ignores is recorded in
// docs/conformance.md. Setting one of those gets you silence, not an error.
type CreateResponseRequest struct {
	// Input is the work, and is the only required field. It is either a string
	// or an array of input items — a bare string is shorthand for one user
	// message — which is why it is typed as any rather than as either one.
	//
	// The item forms are input_text, input_file and input_image. A Core server
	// MUST accept input_text; Extended adds the other two.
	Input any `json:"input"`

	// Model is a canonical model id, stable across providers. Omitted means
	// the harness's default.
	//
	// If the named model cannot be served, a server MUST NOT pretend it ran
	// what was asked: it either fails with model_unavailable or substitutes and
	// declares the substitution in [Response].Metadata (Tasks §1.3).
	Model string `json:"model,omitempty"`

	// Metadata is caller-supplied context, and metadata["harness_id"] is what
	// selects the configured harness.
	//
	// The harness lives in here rather than in a top-level field because the
	// task surface is deliberately Responses-compatible and metadata is the
	// extension point that surface already defines — a top-level field would be
	// a second convention for the same idea.
	Metadata map[string]any `json:"metadata,omitempty"`

	// Stream requests Server-Sent Events instead of one JSON object. Default
	// false, so the zero value is the default and omitting it is honest.
	Stream bool `json:"stream,omitempty"`

	// PreviousResponseID continues the session that produced that response.
	PreviousResponseID *string `json:"previous_response_id,omitempty"`

	// Instructions is additional system guidance for this task only.
	Instructions string `json:"instructions,omitempty"`

	// Store is whether the server retains the response for later reads.
	//
	// A pointer, unlike Stream and Background, because its default is *true*.
	// A plain bool could not express "store: false" at all — omitempty would
	// drop it and the server would apply the default, which is the opposite of
	// what was asked.
	Store *bool `json:"store,omitempty"`

	// MaxOutputTokens bounds generated tokens.
	MaxOutputTokens *int `json:"max_output_tokens,omitempty"`

	// MaxStep bounds agent steps — tool-call rounds — for this task.
	//
	// A budget, not a guarantee of precision. A server that honours it MUST
	// stop at or after the budget, MUST report StatusIncomplete, and MUST NOT
	// report StatusCompleted for work it truncated.
	MaxStep *int `json:"max_step,omitempty"`

	// TimeoutSeconds is the wall-clock budget, under the same three rules as
	// MaxStep.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`

	// Tools are additional tools offered for this task. Left open: the schema
	// says objects and does not describe them further.
	Tools []map[string]any `json:"tools,omitempty"`

	// Include names optional extra content to return in the result.
	Include []string `json:"include,omitempty"`

	// Background returns as soon as the task is accepted, to be followed with
	// the events endpoint rather than held open.
	Background bool `json:"background,omitempty"`
}
