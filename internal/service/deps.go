package service

import (
	"context"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
)

// Registry resolves a harness id to its adapter and lists what is registered.
//
// Declared here, in the package that consumes it, rather than in a shared
// "ports" package: the implementation (adapters.InMemoryRegistry) satisfies it
// structurally without importing this package, so dependency inversion holds
// and there is no third package for both sides to agree with.
type Registry interface {
	Get(harnessID string) (harness.Adapter, bool)
	List() []domain.Harness
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
type Store interface {
	CreateTask(ctx context.Context, t *domain.Task) error
	UpdateTask(ctx context.Context, t *domain.Task) error
	GetTask(ctx context.Context, id string) (*domain.Task, error)
	AppendArtifact(ctx context.Context, taskID string, a domain.Artifact) error

	CreateSession(ctx context.Context, s *domain.Session) error
	GetSession(ctx context.Context, id string) (*domain.Session, error)
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
	Put(ctx context.Context, up domain.Upload) error
	Get(ctx context.Context, id string) (domain.Upload, error)
}
