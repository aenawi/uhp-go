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

	CreateSession(ctx context.Context, s *domain.Session) error
	GetSession(ctx context.Context, id string) (sess *domain.Session, found bool, err error)
	UpdateSession(ctx context.Context, s *domain.Session) error
	ListSessions(ctx context.Context, f domain.SessionFilter) (domain.SessionPage, error)

	// ListSessionTasks returns a session's tasks in the order they ran.
	ListSessionTasks(ctx context.Context, sessionID string) ([]*domain.Task, error)
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
