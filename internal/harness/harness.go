// Package harness defines the contract every harness backend satisfies, plus
// the two data types that cross that boundary. It is deliberately the only
// thing an adapter and the router both import: the adapter contract is a real
// seam — five implementations exist and "add a harness without touching client
// code" is the point of the project — so it earns a shared package. The
// interfaces the router consumes for storage and lookup do not, and are
// declared in the router itself.
package harness

import (
	"context"

	"github.com/aenawi/uhp-go/internal/domain"
)

// RunRequest is what the router hands to an adapter to start work.
// It is intentionally adapter-agnostic: no CLI flags, no SDK types leak in.
type RunRequest struct {
	TaskID          string
	Input           string
	Model           string
	NativeSessionID string // resume/continue if the harness supports it
	Metadata        map[string]any
	WorkDir         string   // working directory the harness process runs in
	InputFiles      []string // absolute paths already materialized on disk
}

// UpdateType enumerates the kinds of incremental update an adapter can emit.
// It is a defined type rather than a bare string so that a typo is a compile
// error and the set of cases is discoverable from one place.
type UpdateType string

const (
	UpdateDelta    UpdateType = "delta"
	UpdateToolCall UpdateType = "tool_call"
	UpdateArtifact UpdateType = "artifact"

	// UpdateSessionID carries the harness's own session/thread id, discovered
	// from its output. Without it every --resume/--session branch in every
	// adapter is unreachable and continuing a conversation silently starts a
	// new one.
	UpdateSessionID UpdateType = "session_id"

	// UpdateUsage carries token accounting the harness reported. UHP requires
	// `usage` to be an object or explicitly null, never a fabricated zero.
	UpdateUsage UpdateType = "usage"

	UpdateCompleted UpdateType = "completed"
	UpdateFailed    UpdateType = "failed"
	UpdateCancelled UpdateType = "cancelled"
)

// Terminal reports whether this update ends a run. Every terminal state must
// answer true here, or a consumer blocks until the adapter closes the channel.
func (t UpdateType) Terminal() bool {
	switch t {
	case UpdateCompleted, UpdateFailed, UpdateCancelled:
		return true
	}
	return false
}

// RunUpdate is a single incremental event an adapter pushes back while a
// task executes.
type RunUpdate struct {
	Type      UpdateType
	Delta     string
	Artifact  *domain.Artifact
	SessionID string
	Usage     *domain.Usage
	Err       error
}

// Adapter is the single interface every harness backend must satisfy.
type Adapter interface {
	// Info returns static capability/model metadata for discovery.
	Info() domain.Harness

	// HealthCheck reports whether the underlying CLI/SDK/binary is reachable.
	HealthCheck(ctx context.Context) error

	// Run starts a task and streams updates on the returned channel until
	// it closes. Implementations MUST close the channel exactly once and
	// MUST respect ctx cancellation (maps to UHP task cancellation).
	Run(ctx context.Context, req RunRequest) (<-chan RunUpdate, error)

	// Cancel stops an in-flight run identified by taskID, if supported.
	Cancel(ctx context.Context, taskID string) error
}
