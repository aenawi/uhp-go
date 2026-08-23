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

	// WorkDir is the working directory the harness process runs in. A task's
	// input files are already written there when the run starts, and the
	// prompt names them: that is the whole of the file-input mechanism, because
	// none of the five CLIs has a generic "attach this file" flag and inventing
	// per-harness plumbing for something the filesystem already does would be
	// five ways to get it wrong. So there is deliberately no separate list of
	// input paths here — a field every adapter is handed and none can act on
	// reads as if attaching were something an adapter chooses to support.
	WorkDir string

	// SkillDirs are the skill folders already written to disk, one per
	// enabled skill, in configuration order. A runtime that can load a skill
	// folder natively is given these; every runtime gets them materialized
	// either way, because a folder the agent can read is the floor.
	SkillDirs []string

	// McpConfigPath is a generated MCP configuration file holding only the
	// enabled servers, or empty when the harness has none. A disabled entry
	// never reaches this file: Harnesses §4.1 requires it not be contacted at
	// all, and "connected then hidden" still tells its operator the turn
	// happened.
	McpConfigPath string

	// DisabledTools are the tools withheld from the agent. The runner passes
	// them to a runtime that can block them; where it cannot, the router has
	// already conveyed them as a standing instruction instead.
	DisabledTools []string
}

// Delivery reports which parts of a harness's configuration a runtime enforces
// itself, as opposed to what the router has to convey as a standing
// instruction.
//
// It exists so the router can tell the difference honestly. Harnesses §4.3 is
// explicit that a restriction the runtime cannot enforce MUST still reach the
// agent and MUST NOT be dropped, and §4.1 that a server MUST NOT advertise MCP
// support it cannot deliver — both of which need an answer to "can this
// runtime actually do it", not an assumption.
type Delivery struct {
	// MCPServers is true when the runtime accepts a per-run MCP configuration.
	// False means a harness carrying MCP servers is refused at configuration
	// time rather than accepted and quietly run without them.
	MCPServers bool

	// ToolBlock is true when the runtime can hard-block named tools. False
	// falls back to a standing instruction, which is weaker and is described
	// as such rather than reported as a block.
	ToolBlock bool

	// Skills is true when the runtime loads a skill folder natively. False
	// still gets the folder on disk and a standing instruction naming it.
	Skills bool
}

// Deliverer is implemented by adapters that can report what they enforce. An
// adapter that does not implement it is taken to enforce nothing, which is the
// safe direction: the router conveys more, and claims less.
type Deliverer interface {
	Delivery() Delivery
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

	// UpdateModel carries the model the harness says it is running, read off
	// its own output. It exists for the task that named no model: the router
	// then invokes the CLI with no `--model` at all, the CLI picks its own
	// default, and the router's idea of what that default is — the first entry
	// of the list `/v1/models` advertises — is a guess rather than an
	// observation. claude, grok and pi each name the model on the wire, so for
	// those three the guess can be replaced with the answer. See issue #43.
	UpdateModel UpdateType = "model"

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
	Model     string
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
