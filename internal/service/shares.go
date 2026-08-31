package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Session sharing is UHP's Sessions §5, and it is the one chapter in this
// server that hands something out with no credential attached to it.
//
// Everything else here is behind a bearer token that names this server's single
// principal. A share is the opposite shape: an id anyone can hold, that reads
// one conversation and nothing else, for as long as the principal who minted it
// allows. That is what the chapter asks for — "shared views must be read-only",
// "servers must support revocation", "the server MUST NOT expose provider
// credentials, tokens, or another principal's data" — and each of those three is
// a property of this file rather than a note in it.
//
// # What read-only means here, mechanically
//
// It means there is no write path to refuse, rather than a write path that
// refuses. A share id is a path segment, never a credential: it is not accepted
// as a bearer token anywhere, and the only routes that read one are GETs under
// /v1/shares/. So "a share cannot start a task" is not a check someone has to
// remember to write — presenting a share id to POST /v1/responses is presenting
// an unknown token, and presenting it to POST /v1/shares/{id} is a method no
// route claims. A check would have to be got right on every future endpoint;
// an absent route is right by construction.
//
// # Why it is off unless an operator turns it on
//
// The capability defaults to off, which is not this file being timid. Turning
// it on changes what the deployment is: a server that answered nothing without
// a credential starts answering some things without one. That is a decision for
// whoever runs it, and a decision they should make on purpose rather than
// discover. With it off, discovery reports `session_sharing: false` and every
// method below refuses — which is the honest pair, and is what this server did
// before any of this existed.
//
// # Turning it back off is not revocation
//
// It suspends. The share rows outlive the flag, so a deployment restarted with
// the variable set again resolves every link it ever minted, and there is no
// way to revoke one while the capability is off — RevokeShare is behind the
// same check as everything else here.
//
// That is a decision, and it is not the one the rest of this file would
// suggest. Revocation here is absolute, which reads as "a share is a live
// credential", and a credential that a configuration change suspends rather
// than withdraws is a weaker promise than that. The alternative is revoking
// every share when the server starts without the variable, and that destroys
// state on a restart with a typo'd variable name — the silent-downgrade
// failure openHarnessStore refuses to make, in its destructive form. So the
// flag stays a flag, and uhpd says so at startup when it is off and shares are
// still stored (see warnSuspendedShares there). Withdrawing a link means
// revoking it, with sharing on.

var (
	// ErrShareNotFound is a share id that does not resolve, and a session that
	// has no share. The two are one error deliberately: on the anonymous read
	// path, "never existed" and "was revoked" must be indistinguishable, or a
	// probe learns which of its guesses were once real.
	ErrShareNotFound = errors.New("service: share not found")

	// ErrSessionSharingUnsupported is a deployment that has not enabled
	// sharing. It is the server's configuration, not the request, which is why
	// it becomes a 501 rather than a 4xx.
	ErrSessionSharingUnsupported = errors.New("service: session sharing is not enabled on this server")
)

// WithSessionSharing enables POST/GET/DELETE /v1/sessions/{id}/share and the
// anonymous read views under /v1/shares/, and makes discovery report the
// `session_sharing` capability as true.
//
// It takes no argument on purpose. The only question is whether this deployment
// serves unauthenticated read paths at all, and that is a yes or a no; a
// configurable share lifetime or an allow-list would be answering a question
// nobody has asked yet with a knob that has to be right forever.
func WithSessionSharing() Option {
	return func(s *TaskService) { s.sessionSharing = true }
}

// SessionSharingEnabled is what the discovery document reports, so the
// capability and the endpoints cannot disagree.
func (s *TaskService) SessionSharingEnabled() bool { return s.sessionSharing }

// ShareSession answers POST /v1/sessions/{session_id}/share.
//
// It is idempotent per session: a second call returns the share that already
// exists rather than minting a second one. That is not a convenience — a client
// is told about one id and revokes one id, so a second live id would be a
// capability nobody knows to withdraw. A caller that wants a fresh id revokes
// first, which is the only way to say "invalidate what I sent people".
func (s *TaskService) ShareSession(ctx context.Context, sessionID string) (uhp.SessionShare, error) {
	if !s.sessionSharing {
		return uhp.SessionShare{}, ErrSessionSharingUnsupported
	}
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return uhp.SessionShare{}, err
	}

	// Minted before the store is asked, and thrown away if the session already
	// had one. Reading first and writing second is the obvious order and is
	// wrong: two concurrent calls would both read "not shared", both mint, and
	// one client would be handed a 200 carrying an id the other's write had
	// already displaced. Store.CreateShare decides, in one operation, and a
	// wasted 32 bytes of randomness is the price.
	id, err := domain.NewShareID()
	if err != nil {
		// The one failure that is not storage: the system's randomness could
		// not be read. It is reported rather than worked around, because the
		// only thing this id has to be is unguessable — a server that cannot be
		// sure of that must mint nothing at all.
		s.log.Error("mint share id", "error", err, "session_id", sessionID)
		return uhp.SessionShare{}, fmt.Errorf("%w: mint share id: %w", ErrStorage, err)
	}
	current, found, err := s.store.CreateShare(ctx,
		&domain.Share{ID: id, SessionID: sessionID, CreatedAt: time.Now().UTC().Unix()})
	if err != nil {
		return uhp.SessionShare{}, fmt.Errorf("%w: create share for session %q: %w", ErrStorage, sessionID, err)
	}
	if !found {
		// The session went between the read above and this write — a delete of
		// the trace arriving at the same moment. Reporting it as the 404 the
		// GetSession above would have given is the honest answer, and is better
		// than the alternative the store rules out: a share row naming a
		// conversation nobody can read, and a 200 carrying an id that resolves
		// to nothing.
		return uhp.SessionShare{}, fmt.Errorf("%w: %q", ErrSessionNotFound, sessionID)
	}
	return current.Wire(s.publicBaseURL), nil
}

// SessionShare answers GET /v1/sessions/{session_id}/share: the share this
// session has, or ErrShareNotFound if it has none.
//
// A session that is not there and a session that is not shared are different
// answers, unlike on the anonymous path. This one is behind a credential, so
// the caller already knows which of its own sessions exist and telling it
// nothing new — and being told "no such session" when the id was mistyped is
// the difference between fixing the id and believing the share was revoked.
func (s *TaskService) SessionShare(ctx context.Context, sessionID string) (uhp.SessionShare, error) {
	if !s.sessionSharing {
		return uhp.SessionShare{}, ErrSessionSharingUnsupported
	}
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return uhp.SessionShare{}, err
	}
	sh, found, err := s.store.GetSessionShare(ctx, sessionID)
	if err != nil {
		return uhp.SessionShare{}, fmt.Errorf("%w: read share for session %q: %w", ErrStorage, sessionID, err)
	}
	if !found {
		return uhp.SessionShare{}, fmt.Errorf("%w: session %q is not shared", ErrShareNotFound, sessionID)
	}
	return sh.Wire(s.publicBaseURL), nil
}

// RevokeShare answers DELETE /v1/sessions/{session_id}/share, returning the id
// it withdrew.
//
// Sessions §5 requires revocation and names no endpoint for it, so DELETE on
// the path the share is minted and read at is this server's reading. What the
// chapter does fix is the effect, and it is absolute: the id stops resolving.
// Not hidden, not expired, not marked — gone, so that a link already sent to
// the wrong person stops working the moment this returns.
//
// The id comes back from the delete rather than from a read before it, so the
// answer names the link this call actually withdrew. A read-then-delete would
// report the wrong one whenever a revoke and a re-share of the same session
// cross, which is precisely the moment a client most needs to be told the truth
// about which link is dead.
func (s *TaskService) RevokeShare(ctx context.Context, sessionID string) (string, error) {
	if !s.sessionSharing {
		return "", ErrSessionSharingUnsupported
	}
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return "", err
	}
	shareID, found, err := s.store.DeleteSessionShare(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("%w: revoke share for session %q: %w", ErrStorage, sessionID, err)
	}
	if !found {
		// Not idempotent-success, for the reason a second DELETE of a session
		// is a 404: a caller that revokes twice has withdrawn one capability,
		// and answering the second call as though it withdrew another would
		// tell it something untrue about what it just did.
		return "", fmt.Errorf("%w: session %q is not shared", ErrShareNotFound, sessionID)
	}
	return shareID, nil
}

// SharedSession answers GET /v1/shares/{share_id}: the conversation and the
// harness that ran it, to whoever holds the id.
func (s *TaskService) SharedSession(ctx context.Context, shareID string) (uhpgo.SharedSession, error) {
	sess, err := s.sharedSessionRecord(ctx, shareID)
	if err != nil {
		return uhpgo.SharedSession{}, err
	}
	view := uhpgo.SharedSession{Object: "session.shared", Session: sess.Session}

	h, found, err := s.GetHarness(ctx, sess.HarnessID)
	if err != nil {
		// The conversation is readable and the harness is not. Answering the
		// whole view with a storage error would take away the part that works;
		// a viewer with no credential can do nothing about either.
		s.log.Error("read harness for a shared session", "error", err, "share_session", sess.ID)
		return view, nil
	}
	if found {
		shared := sharedHarness(h)
		view.Harness = &shared
	}
	return view, nil
}

// sharedHarness is the harness as a link holder may see it.
//
// The projection is a copy of the fields that are kept, never a blanking of the
// fields that are not, and the target is [uhpgo.SharedHarness] rather than a
// narrowed [uhpgo.Harness]. Both of those are deliberate and they defend
// different things.
//
// The allow-list defends against the next field. A deny-list is correct only
// until somebody adds one to the harness object, and the cost of forgetting is
// a configuration secret served to whoever holds a URL — MCP `headers` is
// exactly that shape, a free-form map that carries a working credential with
// `auth` already empty. The separate type defends against the fields that are
// gone: several of a Harness's mean something when empty, so a stripped one
// would tell a viewer this harness has no MCP servers and no step budget rather
// than telling them nothing. See [uhpgo.SharedHarness] for the full argument
// and for what is left out.
func sharedHarness(h uhpgo.Harness) uhpgo.SharedHarness {
	return uhpgo.SharedHarness{
		Object:       "session.shared.harness",
		ID:           h.ID,
		Name:         h.Name,
		Base:         h.Base,
		BaseLabel:    h.BaseLabel,
		DefaultModel: h.DefaultModel,
		Models:       h.Models,
		Capabilities: h.Capabilities,
		Status:       h.Status,
		CreatedAt:    h.CreatedAt,
	}
}

// SharedTurns answers GET /v1/shares/{share_id}/turns.
func (s *TaskService) SharedTurns(ctx context.Context, shareID string) ([]uhp.TurnItem, error) {
	sess, err := s.sharedSessionRecord(ctx, shareID)
	if err != nil {
		return nil, err
	}
	turns, err := s.SessionTurns(ctx, sess.ID)
	return turns, asShareMiss(err)
}

// SharedFiles answers GET /v1/shares/{share_id}/files.
func (s *TaskService) SharedFiles(ctx context.Context, shareID string) ([]domain.Artifact, error) {
	sess, err := s.sharedSessionRecord(ctx, shareID)
	if err != nil {
		return nil, err
	}
	files, err := s.SessionFiles(ctx, sess.ID)
	return files, asShareMiss(err)
}

// asShareMiss rewrites "no such session" into "no such share" on the anonymous
// paths.
//
// Both of the reads above resolve the share and then ask a session method,
// which looks the session up a second time — so a DELETE /v1/traces/{id}
// landing between the two would answer an anonymous caller `session_not_found`
// where every other miss on this surface is `uhpgo_share_not_found`. That is
// two problems in one. A client switching on the code sees an error it was told
// could not happen here, and the code itself is a statement about a session the
// caller never named and is not entitled to hear about.
func asShareMiss(err error) error {
	if errors.Is(err, ErrSessionNotFound) {
		return fmt.Errorf("%w: the shared session no longer exists", ErrShareNotFound)
	}
	return err
}

// OpenSharedArtifact answers GET /v1/shares/{share_id}/files/{file_id}/content.
//
// There is no container id in the signature, and that absence is the scoping.
// [TaskService.OpenArtifact] takes one from the client and trusts the id space
// to keep it honest; here the container is derived from the session the share
// names, so a file id belonging to a different session resolves to nothing at
// all rather than to a check that has to be remembered. A share is a view over
// one conversation, and this is the shape that makes it impossible for it to be
// a view over two.
func (s *TaskService) OpenSharedArtifact(
	ctx context.Context, shareID, fileID string,
) (domain.Artifact, *os.File, error) {
	sess, err := s.sharedSessionRecord(ctx, shareID)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	return s.OpenArtifact(ctx, domain.ContainerIDFor(sess.ID), fileID)
}

// sharedSessionRecord resolves a share id to the session it views.
//
// Every anonymous read goes through here, so this is the whole of what an
// unauthenticated caller has to get past, and it is deliberately short: the
// feature is off, the id is not one this server mints, the id does not resolve,
// or the session behind it is gone. Each of those is the same answer.
//
// The shape check ahead of the lookup is not an optimisation. A share id
// reaches this from a URL, so it is client input; refusing anything this server
// could not have minted means a hostile id never becomes a storage query, and
// means a truncated id — the kind a chat client produces by wrapping a long
// link — is refused rather than looked up and reported as revoked.
func (s *TaskService) sharedSessionRecord(ctx context.Context, shareID string) (*domain.Session, error) {
	if !s.sessionSharing {
		return nil, ErrSessionSharingUnsupported
	}
	if !domain.ValidShareID(shareID) {
		return nil, fmt.Errorf("%w: not a share id this server minted", ErrShareNotFound)
	}
	sh, found, err := s.store.GetShare(ctx, shareID)
	if err != nil {
		return nil, fmt.Errorf("%w: read share: %w", ErrStorage, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: no such share", ErrShareNotFound)
	}
	sess, err := s.GetSession(ctx, sh.SessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			// A share whose session has gone. The store revokes a share with
			// its session, so this is a race or a repair rather than the
			// ordinary path — and the answer is the one a revoked share gets,
			// because that is what it now is.
			return nil, fmt.Errorf("%w: the shared session no longer exists", ErrShareNotFound)
		}
		return nil, err
	}
	return sess, nil
}
