package http

import (
	"context"
	"os"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// TaskService is the application core as this transport needs it: every method
// the handlers call, and nothing else.
//
// Declared here, in the package that consumes it, rather than exported from
// internal/service — the same reason Registry and Store are declared in
// service/deps.go. *service.TaskService satisfies it structurally without
// knowing this package exists, so no handler is written against a concretion
// (issue #10). The one place the concrete type is still named is the assertion
// at the foot of this file, which exists precisely to check the claim; before
// this, it was the signature of NewServer.
//
// The practical half of the argument is that a handler can then be tested
// against a stand-in. Half the endpoints here have no run behind them at all —
// sessions, models, retrieve, cancel — and testing them was previously a matter
// of standing up a registry, a store and a real service to ask a question about
// JSON shape. The storage-failure paths were not merely awkward but unreachable:
// the in-memory store never fails. See errors_test.go.
//
// service.Run, service.Feed, service.CreateTaskRequest, service.HarnessSpec and
// service.HarnessPatch stay concrete in these signatures. They are values that
// cross the boundary, not collaborators to be substituted, and this package
// already speaks in them.
type TaskService interface {
	// Discovery: what this server is and what it can run (Lifecycle §2,
	// Harnesses §1-3). FilesEnabled and HarnessManagementEnabled are what the
	// discovery document reports, so a capability is never advertised by a
	// deployment that cannot honour it.
	ListHarnesses(ctx context.Context) ([]uhpgo.Harness, error)
	GetHarness(ctx context.Context, id string) (uhpgo.Harness, bool, error)
	ModelAvailable(ctx context.Context, harnessID, model string) bool
	FilesEnabled() bool
	HarnessManagementEnabled() bool

	// Harness management (Harnesses §4-5).
	CreateHarness(ctx context.Context, spec service.HarnessSpec) (uhpgo.Harness, error)
	UpdateHarness(ctx context.Context, id string, spec service.HarnessSpec) (uhpgo.Harness, error)
	PatchHarness(ctx context.Context, id string, p service.HarnessPatch) (uhpgo.Harness, error)
	DeleteHarness(ctx context.Context, id string) error
	HarnessSkillFiles(ctx context.Context, harnessID, skillID string) ([]uhp.SkillFile, bool, error)
	HarnessFeed(ctx context.Context, id string) (*service.Feed, bool, error)

	// Tasks (Tasks §1-6, Streaming). ResumableStream is what decides whether a
	// Last-Event-ID sent with a POST resumes anything or starts a fresh stream
	// whose opening events would be skipped.
	StartTask(ctx context.Context, req service.CreateTaskRequest) (*domain.Task, *service.Run, error)
	GetTask(ctx context.Context, id string) (*domain.Task, error)
	CancelTask(ctx context.Context, taskID string) error
	ResumableStream(key string) bool

	// Sessions (Sessions §1-4).
	ListSessions(ctx context.Context, f domain.SessionFilter) (domain.SessionPage, error)
	GetSession(ctx context.Context, id string) (*domain.Session, error)
	SessionTurns(ctx context.Context, id string) ([]uhp.Turn, error)
	CancelSession(ctx context.Context, id string) error

	// Files (Files §1-5).
	StoreUpload(ctx context.Context, filename, mimeType string, data []byte) (uhpgo.Upload, error)
	SessionFiles(ctx context.Context, sessionID string) ([]domain.Artifact, error)
	OpenArtifact(ctx context.Context, containerID, fileID string) (domain.Artifact, *os.File, error)
}

// The concretion is asserted here rather than discovered at the composition
// root, so a signature that drifts in internal/service fails the build in the
// package that made the claim.
var _ TaskService = (*service.TaskService)(nil)
