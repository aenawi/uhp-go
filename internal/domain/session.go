package domain

import (
	"github.com/aenawi/uhp-go/uhp"
)

// Session tracks a continued conversation across several tasks, preserving the
// conversational context, the working directory and its files, and the
// configured harness.
//
// It embeds [uhp.Session] for the same reason [Task] embeds [uhp.Response]: the
// wire object is not this package's to restate, and a second copy of it is a
// drift machine. The two fields below are the whole of what is internal.
//
// Status is a plain string here because it is a plain string in the schema. It
// carries a [uhp.ResponseStatus] value in practice — a session's status follows
// its latest task — but narrowing the Go type would reject a value a conformant
// server is allowed to send, so the conversion is explicit at the two places
// that write it.
type Session struct {
	uhp.Session

	// NativeSessionID is the harness's own session or thread id, which is what
	// makes a later resume actually resume something. LastResponseID is where
	// the next task in the chain continues from.
	//
	// Both are `json:"-"`, and that tag is load-bearing rather than tidy.
	// [uhp.Session] carries no MarshalJSON, so a Session marshals through its
	// struct tags — and an untagged exported field is emitted under its Go
	// name. Without these two tags a session response grows a
	// "NativeSessionID" key holding an identifier the harness issued and no
	// client has any business seeing. [Task] is not exposed to this: it embeds
	// a type that does marshal itself, so its internal fields cannot reach the
	// wire whatever they are tagged.
	NativeSessionID string `json:"-"`
	LastResponseID  string `json:"-"`
}

// SessionFilter selects and pages a session listing.
type SessionFilter struct {
	HarnessID string
	Limit     int
	// Cursor is opaque to the client and is the id of the last session on the
	// previous page.
	Cursor string
}

// SessionPage is one page of a session listing: the internal counterpart of
// [uhp.SessionList], and a genuinely different thing rather than a second copy
// of it.
//
// It holds pointers, because the store hands back rows it owns, and it spells
// "no more pages" as an empty string where the wire object spells it as an
// explicit null. Both spellings are right where they are — the wire one exists
// because UHP forbids making a client infer the end from a short page, a
// heuristic that is wrong whenever a page is exactly full — and the transport
// is where one becomes the other.
type SessionPage struct {
	Sessions   []*Session
	NextCursor string
}
