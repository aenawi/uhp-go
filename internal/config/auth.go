package config

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

// CheckAuthPosture refuses a configuration that would serve every authenticated
// endpoint to anyone who can reach the port, and reports the one that is merely
// permissive.
//
// UHP Security §1 is unconditional — "A server MUST authenticate every endpoint
// except `GET /v1/uhp`" — so a server started with no keys is not a conformant
// one, whatever it is bound to. That is a defensible posture for a tool running
// on loopback and an open relay anywhere else, and the difference is invisible
// on the wire: discovery reports the same document either way, so a client
// cannot tell the two apart. This is where the distinction gets made instead.
//
// Refusing at boot rather than per request is the point. An open server is a
// deployment mistake, and by the time a request arrives to be answered or
// refused, the mistake has already been live for as long as the process has.
// The warning exists for the other half — an operator who meant loopback and
// got it, who should still find "this server authenticates nothing" in their
// log rather than in someone else's scan.
//
// See issue #55, and the `UHP_API_KEYS` section of the README for the
// conformance obligation this leaves as a documented one.
func (c Config) CheckAuthPosture(log *slog.Logger) error {
	if len(c.APIKeys) > 0 {
		if len(c.APIKeys) > 1 {
			// `UHP_API_KEYS` is plural, and the obvious reading of a plural is
			// wrong here: the keys are equivalent credentials for one principal,
			// not one principal each. An operator who hands three keys to three
			// people has given all three the same sessions, transcripts and
			// artifacts. The README says so, but the moment to say it is the
			// moment they configured the second key rather than whenever they
			// next read the README. See ADR-0006 and issue #56.
			log.Info("several API keys are configured; they are equivalent credentials for one principal, not one tenant each",
				"keys", len(c.APIKeys),
				"hint", "run one uhpd per tenant if they must not share sessions, transcripts or artifacts")
		}
		if isLoopback(c.Addr) {
			// The default bind is loopback, and an operator upgrading from a
			// build whose default was `:8080` has a keyed, correctly
			// configured server that nothing off the machine can reach. That
			// is the one failure mode of this change which is otherwise
			// silent — the check above passes on the keys and never looks at
			// the address — so it is said out loud rather than left to be
			// diagnosed from the far end of a connection that never arrives.
			log.Info("listening on loopback only; nothing off this machine can reach this server",
				"addr", c.Addr, "hint", "set UHP_ADDR to widen the bind")
		}
		return nil
	}
	if !isLoopback(c.Addr) {
		return fmt.Errorf(
			"UHP_API_KEYS is unset and UHP_ADDR %q is not a loopback address: "+
				"with no keys every endpoint except GET /v1/uhp answers anyone who can reach the port. "+
				"Set UHP_API_KEYS, or bind UHP_ADDR to 127.0.0.1 to run this as a local tool",
			c.Addr)
	}
	log.Warn("running unauthenticated; every endpoint except GET /v1/uhp answers without a credential, which UHP Security §1 forbids",
		"addr", c.Addr, "hint", "set UHP_API_KEYS")
	return nil
}

// lookupHost resolves a name to the addresses a listener would bind. A var so a
// test can answer for a resolver rather than for whichever machine it runs on —
// the thing under test is what this check does with an answer, not what this
// machine's `/etc/hosts` happens to say.
var lookupHost = net.DefaultResolver.LookupHost

// resolveTimeout bounds the one lookup this check can make. Exceeding it is
// treated as "not loopback", so a resolver that will not answer costs an
// unkeyed server its boot rather than costing it its refusal.
const resolveTimeout = 2 * time.Second

// isLoopback reports whether addr binds a listener the rest of the network
// cannot reach.
//
// Anything it cannot prove is loopback is treated as reachable, which is the
// only safe direction for a check whose false negative is an open server. That
// makes a bare ":8080" — and the empty address net/http reads as ":http" —
// non-loopback: a port with no host is every interface.
//
// A literal IP is decided without asking anything, which is every address an
// operator is likely to type. A name is resolved, and every address it resolves
// to must be loopback; a name that resolves to nothing, or that the resolver
// will not answer for, is not loopback.
//
// Resolving is safe on both counts it looks unsafe on. It is not an outbound
// connection this server would not otherwise make — `net.Listen` resolves the
// same name moments later, so refusing to ask here would only mean binding
// without knowing what was bound, and the README's "opens no outbound network
// connections of its own" is untouched. And it is not a stale answer: the
// question is what *this* bind, happening now, will reach, not what the name
// means in general. `localhost` is the case that matters — it is conventionally
// loopback and is ultimately whatever the resolver says, and an unkeyed server
// on a `localhost` that someone has pointed elsewhere is exactly the open
// server this check exists to refuse.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Not a host:port pair. Either it is a bare host — which net/http will
		// refuse for the missing port before anything is served — or it is
		// malformed, and neither is worth guessing loopback for.
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	resolved, err := lookupHost(ctx, host)
	if err != nil || len(resolved) == 0 {
		return false
	}
	for _, a := range resolved {
		// A name that is loopback on one of its addresses and reachable on
		// another is reachable; the listener gets whichever the stack picks.
		ip := net.ParseIP(a)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}
