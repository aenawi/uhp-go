package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/aenawi/uhp-go/uhp"
)

// shareIDBytes is how much entropy a share id carries.
//
// It is a security parameter, not a formatting choice. A share id is the whole
// credential for a read-only view served without authentication, so the only
// defence against someone reading a stranger's conversation is that the id
// cannot be found by guessing. 32 bytes is 256 bits, which is the same order as
// an API key and far past anything an online attacker can enumerate against a
// server that answers one request at a time.
//
// Nothing about a session is derivable from it, deliberately. Deriving the id
// from the session id — a hash, an HMAC, anything — would mean a share that
// cannot be revoked without also invalidating a future one, and would put the
// server's own secret in the position of being the only thing between a session
// id and its share.
const shareIDBytes = 32

// sharePrefix marks the identifier space. Session ids are `sess_`, containers
// `cntr_`, artifacts `file_`; a share is its own space so that an id handed to
// the wrong endpoint is refused by shape rather than looked up.
const sharePrefix = "shr_"

// NewShareID mints an unguessable share id.
//
// A failure to read the system's randomness is returned rather than papered
// over with a weaker source. There is exactly one thing this id has to be, and
// a server that cannot be sure of it must decline to mint one — a predictable
// share id is a public conversation that reads as a private one.
func NewShareID() (string, error) {
	buf := make([]byte, shareIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("domain: mint share id: %w", err)
	}
	return sharePrefix + hex.EncodeToString(buf), nil
}

// ValidShareID allows only the shape [NewShareID] mints: the prefix plus
// exactly the right number of lower-case hex characters.
//
// It is checked before a share id reaches storage, for the reason
// [ValidSessionID] is: an id that arrives from a URL is client input until
// something has said otherwise, and refusing the wrong shape early means a
// hostile one never reaches a lookup. The exact-length rule also means a
// truncated id — the kind a chat client produces by wrapping a long URL — is
// refused rather than looked up and reported as a revoked share.
func ValidShareID(id string) bool {
	if !strings.HasPrefix(id, sharePrefix) {
		return false
	}
	rest := id[len(sharePrefix):]
	if len(rest) != shareIDBytes*2 {
		return false
	}
	for _, r := range rest {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Share is a read-only view of a session, published under an unguessable id.
//
// It does not embed [uhp.Share] the way [Session] embeds [uhp.Session], because
// the two are genuinely different objects rather than one object seen twice.
// The wire share carries a URL, and a URL is not a property of the share: it is
// a property of the origin this server happens to be reached on, which the
// store has no business holding and which changes when a deployment moves. The
// stored record is the three facts below and the wire object is rendered from
// them — see [Share.Wire], and [Artifact.Cite], which resolves the same
// question the same way.
//
// There is deliberately no expiry field. Sessions §5 requires revocation and
// says nothing about expiry, and a stored expiry nothing enforces would be a
// worse promise than none: a viewer would read a timestamp that had passed and
// still be served the conversation.
type Share struct {
	// ID is the capability, and the only secret here.
	ID string
	// SessionID is what the share views. A session has at most one share, so
	// this is unique across the shares a server holds.
	SessionID string
	// CreatedAt is Unix seconds.
	CreatedAt int64
}

// SharePath is where a share id's read-only view is served.
//
// Sessions §5 does not name this path — it requires that the view be read-only
// and revocable and says nothing about where it lives — so this is this
// server's choice. It is under its own prefix rather than beneath
// /v1/sessions/ so that no unauthenticated route shares a subtree with an
// authenticated one: the whole of /v1/shares/ is anonymous, and every other
// /v1 path needs a credential, which is a rule a reader can check against the
// routing table in one pass.
func SharePath(shareID string) string {
	return "/v1/shares/" + shareID
}

// Wire renders the share as the object a client is answered with. baseURL is
// the server's externally reachable origin; empty means a relative URL, which
// is correct whenever the client and the API share an origin and is the only
// honest answer when the operator has not told this server its own address.
func (s Share) Wire(baseURL string) uhp.Share {
	return uhp.Share{
		ID:        s.ID,
		Object:    "session.share",
		SessionID: s.SessionID,
		URL:       strings.TrimSuffix(baseURL, "/") + SharePath(s.ID),
		CreatedAt: s.CreatedAt,
	}
}
