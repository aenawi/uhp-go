package uhp

// Discovery is what GET /v1 answers: which protocol this is, which versions it
// serves, what it claims to conform to, and what it can actually do.
//
// It is the one document a client reads before it commits to anything, which is
// why ConformanceClass and Capabilities are both here and must agree with each
// other. A class is a claim the conformance suite exists to falsify; the
// capability list is what a client branches on.
type Discovery struct {
	// Object is always "uhp.discovery" and Protocol is always "uhp". The
	// schema pins both as constants, so they are the cheapest possible check
	// that whatever answered is speaking this protocol at all.
	Object   string `json:"object"`
	Protocol string `json:"protocol"`

	// Versions is every version this server can serve, and DefaultVersion is
	// the one it serves when a client does not ask. A client picks from
	// Versions rather than assuming DefaultVersion is acceptable.
	Versions       []string `json:"versions"`
	DefaultVersion string   `json:"default_version"`

	// ConformanceClass is "core", "extended" or "full".
	ConformanceClass string `json:"conformance_class"`

	Capabilities Capabilities `json:"capabilities"`

	// Implementation names the software, not the protocol. It is optional and
	// carries nothing a client should branch on — a client that special-cases
	// an implementation name has stopped writing against the protocol.
	Implementation *Implementation `json:"implementation,omitempty"`
}

// Implementation names the server software behind a [Discovery] document.
//
// The schema declares this inline rather than as one of its named objects, so
// the name is this package's rather than the protocol's. It is
// additionalProperties: true there, and a server may report more than these
// two fields.
type Implementation struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// Capabilities is what a server can do, as named booleans.
//
// Every field is reported explicitly, including the false ones — no omitempty
// anywhere below, deliberately. The schema says so in as many words: a server
// reports false for a capability it does not implement rather than omitting it,
// so a client can tell "not supported" from "this server predates this field".
// A client reading a document written by an older server treats an absent key
// as false, which is what the zero value already does.
//
// This is the reason the type is a struct and not a map[string]bool: a map
// cannot distinguish the two cases on the way out, and a server holding one has
// to remember to write every key by hand every time.
type Capabilities struct {
	// Streaming is Server-Sent Events on POST /v1/responses with stream: true.
	Streaming bool `json:"streaming"`
	// Sessions is continuation via previous_response_id.
	Sessions bool `json:"sessions"`
	// Cancellation is POST /v1/responses/{id}/cancel.
	Cancellation bool `json:"cancellation"`
	// FilesInput is input_file and input_image on a task's input.
	FilesInput bool `json:"files_input"`
	// FilesOutput is artifact capture and the container download endpoints.
	FilesOutput bool `json:"files_output"`
	// SessionListing is GET /v1/sessions.
	SessionListing bool `json:"session_listing"`
	// HarnessManagement is creating and changing harnesses over the API.
	HarnessManagement bool `json:"harness_management"`
	// SessionSharing is exposing a session to another caller.
	SessionSharing bool `json:"session_sharing"`
	// Idempotency is honouring the Idempotency-Key header.
	Idempotency bool `json:"idempotency"`
}
