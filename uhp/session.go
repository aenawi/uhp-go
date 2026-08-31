package uhp

import "encoding/json"

// Session is a continued conversation across several tasks, preserving the
// conversational context, the working directory and its files, and the
// configured harness.
//
// Only ID is required. Status is a plain string here and not a
// [ResponseStatus]: the schema types it as an unconstrained string, and
// narrowing it in Go would reject a value a conformant server is allowed to
// send.
type Session struct {
	ID string `json:"id"`
	// Object is always "session".
	//
	// Omitted when unset rather than written as an empty string: the schema
	// pins it to a constant, so "" is a value that fails validation where
	// absence is merely optional. This type has no MarshalJSON to default it
	// in, which is the whole difference from [Response] and [Harness].
	Object    string `json:"object,omitempty"`
	HarnessID string `json:"harness_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	// CreatedAt and UpdatedAt are Unix seconds.
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// SessionList is one page of GET /v1/sessions.
type SessionList struct {
	// Sessions is required and is an array even when empty. See
	// [SessionList.MarshalJSON].
	Sessions []Session `json:"sessions"`

	// NextCursor is null on the last page, and is present rather than omitted
	// so that a client can read it.
	//
	// A client MUST NOT be required to detect the end by receiving fewer items
	// than it asked for. That heuristic is wrong whenever a page is exactly
	// full, and the client cannot tell the two cases apart — which is why this
	// field exists and why it carries no omitempty.
	NextCursor *string `json:"next_cursor"`
}

// MarshalJSON renders the page with sessions always present as an array, for
// the reason [Response.MarshalJSON] normalises Output: the schema declares it
// type: array and requires it, and a nil Go slice marshals as null.
func (l SessionList) MarshalJSON() ([]byte, error) {
	type sessionList SessionList
	out := sessionList(l)
	if out.Sessions == nil {
		out.Sessions = []Session{}
	}
	return json.Marshal(out)
}

// TurnItem is one task in a session's history, carrying enough to rebuild a
// transcript a client did not store.
//
// # The shape became normative on 2026-08-31
//
// It was not when this type was written. Sessions §3 required the endpoint and
// said what it was for, but declared its items
// `{ type: object, additionalProperties: true }` — a required response with no
// stated shape — so the fields below were this server's invention, published
// under the schema's naming convention and carrying a warning that a different
// conformant server might answer with different ones. That was reported
// upstream as the fourth gap of harnessrouter#44 and closed by
// harnessrouter#53: every item MUST now carry `id` and `status`, and SHOULD
// carry `user`, `assistant`, `tools` and `files`.
//
// The specification named the response id `id`. This server had named it
// `response_id`, after the thing it holds. There was never a disagreement about
// meaning, only about spelling, and the spelling is not this server's to choose.
//
// # `tools` is not answered, and that is a decision
//
// This server counts a turn's tool calls and keeps none of them. A step is
// counted so `max_step` can stop a run (ADR-0009) and is then discarded: no
// name, no arguments, no result reaches a stored task. Answering `tools` would
// mean retaining across five CLIs a record nothing here has ever kept, which is
// a feature rather than a field. §3 makes it a SHOULD, so the honest answer is
// to omit the key and say why here, rather than to answer an empty array that
// would read as "this turn called no tools".
//
// # The three deprecated fields
//
// ResponseID, Input and Output are the pre-#53 spellings of ID, User and
// Assistant, kept alongside them for one release. `uhpc` is installed
// separately from `uhpd` and the two are routinely different ages; removing the
// old keys in the release that adds the new ones would break every client not
// reinstalled that day. Read ID, User and Assistant.
//
// "One release" is a claim nothing here enforces, which is how a deprecation
// becomes a second spelling nobody ever removes. Issue #105 carries the
// condition — the next release that is not a patch — and what removal touches.
type TurnItem struct {
	// ID is the turn's response id: `GET /v1/responses/{id}` returns this turn
	// in full, where everything else here is a summary. Required by §3.
	ID string `json:"id"`
	// Status is the response's status. Required by §3.
	Status ResponseStatus `json:"status"`
	// Model is what served this turn. An extension — §3 does not ask for it —
	// kept because a session may span models, and a transcript that does not
	// say so misreports its own history.
	Model string `json:"model"`
	// User is the input that opened the turn.
	User string `json:"user"`
	// Assistant is the text the agent answered with.
	Assistant string `json:"assistant"`
	// Files are the artifacts this turn produced, as the schema's six-property
	// file object rather than the extended one `GET /v1/sessions/{id}/files`
	// answers with. That endpoint is where a file is served with its
	// `download_url` and `mime_type`; rendering the same file two ways in one
	// API would leave a client to work out which of them is authoritative, and
	// the ids here are enough to ask.
	Files []File `json:"files"`
	// CreatedAt is Unix seconds.
	CreatedAt int64 `json:"created_at"`

	// ResponseID is [TurnItem.ID] under this server's pre-#53 name.
	//
	// Deprecated: read ID, which is what Sessions §3 requires.
	ResponseID string `json:"response_id"`
	// Input is [TurnItem.User] under this server's pre-#53 name.
	//
	// Deprecated: read User.
	Input string `json:"input"`
	// Output is [TurnItem.Assistant] under this server's pre-#53 name.
	//
	// Deprecated: read Assistant.
	Output string `json:"output"`
}

// MarshalJSON renders the turn with files always present as an array, for the
// reason [SessionList.MarshalJSON] does the same with sessions: a nil Go slice
// marshals as null, and null would cost a client the difference between "this
// turn wrote nothing" and "this server does not report a turn's files". An
// empty array says the first without ambiguity.
func (t TurnItem) MarshalJSON() ([]byte, error) {
	type turnItem TurnItem
	out := turnItem(t)
	if out.Files == nil {
		out.Files = []File{}
	}
	return json.Marshal(out)
}

// SessionShare is a read-only view of a session, published under an
// unguessable id.
//
// # The shape this server invented is the shape that got codified
//
// This type used to carry the same caveat [TurnItem] did, and rather more
// strongly: Sessions §5 required POST and GET /v1/sessions/{session_id}/share
// of a `full` implementation and required the view to be read-only and
// revocable, but the schema had no share object among its twenty-three and the
// chapter named neither a revocation endpoint nor a path the view is served at.
// The fields below, DELETE on the mint path to revoke, and the view under URL
// were one server's reading of a chapter that had left three holes.
//
// Reported as harnessrouter#44 and closed by harnessrouter#53. `SessionShare`
// is now a schema object requiring `id` and `url`, `url` may be base-relative,
// and DELETE on the share endpoint is the named revocation. The reading below
// was arrived at here and, independently, by the reference implementation; that
// the two agreed is most of why it could be written down as the protocol.
//
// What was never one server's reading is the security property. A share id is a
// bearer capability — whoever holds it can read the conversation, its turns and
// its files, with no credential — so it is minted with real entropy and is the
// only secret in the object. Treat it like a password in logs, in referrers and
// in bug reports.
type SessionShare struct {
	// ID is the capability. It is opaque: nothing about a session can be
	// derived from it, and it cannot be guessed from the session's own id.
	ID string `json:"id"`
	// Object is always "session.share", and is omitted when unset for the
	// reason [Session].Object is.
	Object string `json:"object,omitempty"`
	// SessionID is the session this shares. It is meaningful to the principal
	// that minted the share and is not itself a credential.
	SessionID string `json:"session_id"`
	// URL is where the read-only view is served. It may be relative, when the
	// server has not been told the origin it is reached on.
	URL string `json:"url"`
	// CreatedAt is Unix seconds.
	CreatedAt int64 `json:"created_at"`
}

// Turn is the name this package gave [TurnItem] before Sessions §3 described
// the shape and the schema named it.
//
// Deprecated: use TurnItem. ADR-0002 settled that the schema's names win inside
// this package, and an alias is how that rule is applied to a type that shipped
// before there was a name to lose to. It is kept for one release because `uhpc`
// is installed separately from `uhpd` and clients are routinely a version
// behind. Removal is tracked in issue #105.
type Turn = TurnItem

// Share is the name this package gave [SessionShare] before Sessions §5
// described the shape and the schema named it.
//
// Deprecated: use SessionShare, for the reason [Turn] gives.
type Share = SessionShare
