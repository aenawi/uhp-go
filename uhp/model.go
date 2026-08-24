package uhp

// ModelCatalog is what GET /v1/models answers: every model this server can
// reach, grouped by the backend that reaches it.
type ModelCatalog struct {
	Backends map[string]ModelCatalogBackend `json:"backends"`
}

// ModelCatalogBackend is one backend's entry in a [ModelCatalog].
//
// The schema declares this inline rather than as one of its named objects, so
// the name is this package's rather than the protocol's.
type ModelCatalogBackend struct {
	// Default is the model this backend uses when a task names none.
	Default string  `json:"default"`
	Models  []Model `json:"models"`
}

// HarnessModels is what GET /v1/harnesses/{id}/models answers: the same models
// narrowed to one harness, which is the question a client actually has before
// it sends work somewhere.
type HarnessModels struct {
	HarnessID string `json:"harness_id,omitempty"`
	Backend   string `json:"backend,omitempty"`
	// Default is what this harness runs when a task names no model.
	Default string `json:"default,omitempty"`
	// Fallback is what it substitutes when the named model cannot be served —
	// and substituting means declaring it in the response (Tasks §1.3), not
	// quietly running something else.
	Fallback string  `json:"fallback,omitempty"`
	Models   []Model `json:"models"`
}

// Model is one model a harness can run.
type Model struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Backend string `json:"backend,omitempty"`

	// Available is computed, not asserted: true means the server can serve
	// this model for this harness right now.
	//
	// The schema is unusually pointed about this one, and the reason is worth
	// repeating: listing a model as available and then failing the task is the
	// worst outcome for a client, because a user has already chosen it.
	Available bool `json:"available"`

	// Default marks the entry a task with no model named will get.
	//
	// Reported explicitly, including when false, for the reason
	// [Capabilities] reports its false values: a catalogue in which the
	// non-default entries simply lack the key leaves a client unable to tell
	// "not the default" from "this server does not say". Exactly one entry per
	// backend should carry true, and a client that finds none has learned
	// something rather than nothing.
	Default bool `json:"default"`
}
