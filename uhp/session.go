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

// Turn is one task in a session's history, carrying enough to rebuild a
// transcript a client did not store.
//
// # This shape is not normative
//
// GET /v1/sessions/{session_id}/turns is specified — Sessions §3 requires the
// endpoint and says what it is for — but its response shape is not among the
// twenty-three objects in uhp-2026-08-11.schema.json. There is nothing to
// mirror, so this is *this server's* rendering published under the schema's
// naming convention, and a different conformant server may answer that endpoint
// with different fields.
//
// It is published anyway because the alternative is worse: a client calling a
// specified endpoint would have nothing at all to unmarshal into. Treat it as a
// convenience with a caveat rather than as protocol, and read ResponseID
// against GET /v1/responses/{id} — which *is* normative — when the distinction
// matters. The gap is worth reporting upstream.
type Turn struct {
	// ResponseID identifies the turn's response, so a client can fetch any
	// turn in full rather than relying on the summary below.
	ResponseID string         `json:"response_id"`
	Status     ResponseStatus `json:"status"`
	Model      string         `json:"model"`
	Input      string         `json:"input"`
	Output     string         `json:"output"`
	// CreatedAt is Unix seconds.
	CreatedAt int64 `json:"created_at"`
}

// Share is a read-only view of a session, published under an unguessable id.
//
// # This shape is not normative
//
// The caveat on [Turn] applies here for the same reason and rather more
// strongly. Sessions §5 requires POST and GET /v1/sessions/{session_id}/share
// of a `full` implementation, and requires that the view be read-only and
// revocable — but the schema has no share object among its twenty-three, and
// the chapter names no revocation endpoint and no path the view is served at.
// So this is one server's reading: the fields below, DELETE on the same path
// to revoke, and the view itself under URL.
//
// What is not one server's reading is the security property. A share id is a
// bearer capability — whoever holds it can read the conversation, its turns and
// its files, with no credential — so it is minted with real entropy and is the
// only secret in the object. Treat it like a password in logs, in referrers and
// in bug reports.
type Share struct {
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
