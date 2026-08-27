package service

import (
	"context"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Registry resolves a harness id to its adapter and lists what is registered.
//
// Declared here, in the package that consumes it, rather than in a shared
// "ports" package: the implementation (adapters.InMemoryRegistry) satisfies it
// structurally without importing this package, so dependency inversion holds
// and there is no third package for both sides to agree with.
type Registry interface {
	Get(harnessID string) (harness.Adapter, bool)
	List() []uhpgo.Harness
	// Resolve maps an id or a friendly alias to the canonical harness id.
	Resolve(harnessID string) (string, bool)
}

// Store persists tasks and sessions.
//
// One interface, not two. The previous split into TaskStore and SessionStore
// forced a delegating wrapper into existence for no reason other than that
// both halves wanted to be called Create/Get/Update. There is one storage
// engine and the methods are named for what they store, so the collision —
// and the wrapper — cease to exist.
//
// The two single-row reads answer with a found flag rather than folding "no
// such row" into the error, the way HarnessStore.GetHarness below already does.
//
// The flag is the whole point and it is not a style preference. These two
// results are what the transport turns into a status class, and an absent row
// and an unreadable store have to become different ones — 404, which tells a
// client its id is wrong and retrying never helps, against 500, which tells it
// the server failed and retrying is worth something. When both arrive as an
// error, the caller has nothing to tell them apart by and picks one; this
// service picked 404, so a disk that stopped answering was reported to a client
// polling a running task as a task that no longer existed.
//
// A sentinel error would separate them too, but only by convention: an engine
// that forgot to wrap it, or a caller that forgot to check, fails silently and
// in the direction that loses. Two return values cannot be conflated by
// forgetting something, and the compiler makes a new engine answer the question.
type Store interface {
	CreateTask(ctx context.Context, t *domain.Task) error
	UpdateTask(ctx context.Context, t *domain.Task) error
	GetTask(ctx context.Context, id string) (t *domain.Task, found bool, err error)
	AppendArtifact(ctx context.Context, taskID string, a domain.Artifact) error

	// DeleteTask removes a stored task. It reports found=false for an id the
	// store does not hold, rather than an error: DELETE on a task that is
	// already gone is not a storage failure.
	//
	// It has nothing to do with stopping work. Tasks §4: "A server MUST NOT let
	// this cancel a running task — cancellation and deletion are different
	// intentions." A store cannot cancel anything anyway, and that is the point
	// — deletion lives here precisely because it is not a supervisor concern.
	DeleteTask(ctx context.Context, id string) (found bool, err error)

	CreateSession(ctx context.Context, s *domain.Session) error
	GetSession(ctx context.Context, id string) (sess *domain.Session, found bool, err error)
	UpdateSession(ctx context.Context, s *domain.Session) error
	ListSessions(ctx context.Context, f domain.SessionFilter) (domain.SessionPage, error)

	// DeleteSession removes a session and every task that ran in it, reporting
	// found=false for an id the store does not hold, as DeleteTask does.
	//
	// The tasks go with it, and that is a decision rather than a convenience.
	// The turns *are* the conversation, so a session deleted with its history
	// left behind would leave every turn readable at GET /v1/responses/{id} —
	// its output, its artifact list — for a conversation whose owner has just
	// disposed of it. An engine implements this as one atomic operation: a
	// partial delete is a session that is unreadable and a history that is not.
	//
	// The session's share goes with it, and that one is a live capability
	// rather than a stale row: a share id that outlived its session would be an
	// anonymous route back to a conversation whose owner has just disposed of
	// it. See CreateShare below.
	DeleteSession(ctx context.Context, id string) (found bool, err error)

	// ListSessionTasks returns a session's tasks in the order they ran.
	ListSessionTasks(ctx context.Context, sessionID string) ([]*domain.Task, error)

	// CreateShare records a session's read-only view (Sessions §5), or reports
	// the one the session already has.
	//
	// It is get-or-create and not create, and the difference is what makes the
	// endpoint's idempotency real rather than nearly real. A session has at most
	// one share, so a caller that read "no share yet" and then wrote would race
	// a second caller doing the same: both mint, one wins, and the loser is
	// handed a 200 carrying an id that is already dead — indistinguishable, to
	// whoever it was sent to, from a link somebody deliberately revoked. Asking
	// the store to decide makes the check and the write one operation.
	//
	// `current` is the session's share afterwards: the argument when this call
	// created it, and the existing record when it did not. An engine never
	// replaces a live share, because the client that minted it was told one id
	// and revokes one id.
	//
	// `found` is the *session*, and it is checked here rather than by the caller
	// for the reason the get-or-create is here: the two tables have no foreign
	// key between them, so a caller that checked the session and then wrote
	// would race DeleteSession and leave a share row naming a conversation that
	// no longer exists — a row no endpoint can list or revoke, and a 200 handed
	// to a client carrying an id that resolves to nothing. An engine checks
	// inside the same transaction, or under the same lock, as the write.
	CreateShare(ctx context.Context, sh *domain.Share) (current *domain.Share, found bool, err error)

	// GetShare resolves a share id, reporting found=false for one this store
	// does not hold — which is the same answer a revoked id gets, deliberately.
	GetShare(ctx context.Context, shareID string) (sh *domain.Share, found bool, err error)

	// GetSessionShare finds a session's share, if it has one. This is what an
	// idempotent POST reads before minting anything.
	GetSessionShare(ctx context.Context, sessionID string) (sh *domain.Share, found bool, err error)

	// CountShares reports how many shares this store holds — how many would
	// still resolve, not how many were ever minted.
	//
	// It exists for the one question that is asked with no share id in hand:
	// whether a deployment that is not serving sharing is nonetheless sitting
	// on shares. Turning the capability off suspends them rather than revoking
	// them, so uhpd says so at startup, and saying so needs a number.
	//
	// A count and not a listing, deliberately. The id is the credential, so a
	// method that hands every live one back would be a way to read every shared
	// conversation without ever having been sent a link — and nothing needs it:
	// what an operator has to know is that there are three, not what they are.
	CountShares(ctx context.Context) (int, error)

	// DeleteSessionShare revokes a session's share, reporting found=false for a
	// session that had none.
	//
	// Revocation is required by Sessions §5 and is the only thing standing
	// between a leaked link and a permanently readable conversation, so the
	// contract is that the id stops resolving — not that it is marked, or
	// expired, or hidden from a listing.
	//
	// `shareID` is what this call actually removed, so a caller can report the
	// link it withdrew without reading first. Reading first is the obvious
	// shape and it is wrong: a revoke interleaved with a re-share would delete
	// the newly minted id while telling the client it had withdrawn the old one.
	DeleteSessionShare(ctx context.Context, sessionID string) (shareID string, found bool, err error)
}

// Uploads holds files a client sent ahead of a task (Files §1.2), until a task
// references one by id.
//
// A second, narrow interface rather than four more methods on Store: an upload
// has a different lifetime from a task or a session — it exists before either
// of them — and a deployment that wants uploads on disk while tasks stay in
// memory should be able to say so without reimplementing the rest.
type Uploads interface {
	Put(ctx context.Context, up uhpgo.Upload) error
	Get(ctx context.Context, id string) (uhpgo.Upload, error)
}

// HarnessStore persists the harnesses a client created over the API
// (Harnesses §5).
//
// It is separate from Store for the same reason Uploads is: a harness has a
// different lifetime from a task or a session — it exists before either, and
// outlives both — and a deployment that wants configuration on disk while
// tasks stay in memory should be able to say so. It is also what decides
// whether the `harness_management` capability is advertised at all: a
// configured harness that vanishes on restart is not configuration, so the
// capability is only offered when there is somewhere durable to keep one.
type HarnessStore interface {
	ListHarnesses(ctx context.Context) ([]domain.HarnessConfig, error)
	GetHarness(ctx context.Context, id string) (domain.HarnessConfig, bool, error)
	PutHarness(ctx context.Context, cfg domain.HarnessConfig) error

	// DeleteHarness removes a harness. Deleting one that is not there is not
	// an error: the caller's intent is already satisfied.
	DeleteHarness(ctx context.Context, id string) error
}
