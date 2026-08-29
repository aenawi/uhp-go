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
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
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

	// MaxStep is the resolved step budget for this run, or zero for unbounded.
	//
	// It is here for the one base that enforces its own (grok, `--max-turns`),
	// and for no other reason: on the four the router counts, the ceiling is the
	// supervisor's business and an adapter that also knew it would be a second
	// place for the number to be wrong. A negative value never reaches here —
	// the transport refuses it — so zero is the only non-positive case and it
	// means unbounded, matching the wire's `null`.
	//
	// The distinction between "unbounded" and "no tool calls permitted" is
	// therefore not expressible here, and does not need to be: the only base
	// this field reaches is one the router does not count, and `max_step: 0` on
	// such a base is refused before a run starts — see service.requireStepBudget
	// for why a turn budget cannot express it.
	MaxStep int
}

// StepEdge names which end of a tool call a base narrates, and so when a step
// budget may be tripped on it. It is a measurement, not a preference — see
// testdata/steps/README.md and `make probe-steps`, which is where each value
// below comes from.
//
// It exists because the assumption #72 was designed on did not survive being
// checked: three of the four counted bases announce a tool call when the model
// asks for it, and opencode announces one only when it has finished. Counting
// both kinds with the same comparison would spend one call more or fewer than
// the client asked for, in a way nothing in the code said out loud.
type StepEdge string

const (
	// StepEdgeNone is a base whose output no tool call can be counted from.
	// It is the zero value on purpose: a new adapter that says nothing is
	// assumed to be uncountable rather than assumed safe, and the registry gate
	// in step_capture_test.go fails the build rather than letting a task on it
	// carry a ceiling nobody enforces.
	StepEdgeNone StepEdge = ""

	// StepEdgeStart is a base that announces a tool call before it runs —
	// claude's `tool_use` block, codex's `item.started`, pi's `toolcall_start`.
	// The budget is tripped when the *next* call after the ceiling is asked
	// for, which is the earliest moment anything downstream could act on it.
	//
	// Not the same as stopping the call: the router reads stdout and kills a
	// process group, and the agent has already dispatched the tool by the time
	// the line naming it has been parsed. See service.stepBudgetSpent for what a
	// ceiling therefore does and does not promise.
	StepEdgeStart StepEdge = "start"

	// StepEdgeFinish is a base that announces a tool call only once it is over.
	// That is opencode, and it was established by execution rather than
	// assumed: `--format json` emits exactly one `tool_use` per call, carrying
	// `state.status == "completed"`, even for a call that took twelve seconds.
	//
	// The budget is therefore tripped by the completion of a call one past the
	// ceiling — which has, by construction, already run. So opencode overshoots
	// a ceiling by at least one where a start-edge base overshoots only by
	// whatever the teardown races. It is also why opencode cannot take
	// `max_step: 0`: there, one overshot call is the whole budget.
	StepEdgeFinish StepEdge = "finish"

	// StepEdgeNative is a base that bounds its own steps and is not counted
	// here. That is grok: it takes `--max-turns`, and — measured on 1.0.13,
	// `make probe-grok-max-turns` — reports the stop as
	// `result.subtype == "error_max_turns"`, which is what lets a truncated run
	// reach a client as `incomplete` rather than as a success.
	//
	// It is not an exemption from being bounded. It is an exemption from being
	// bounded *by the router*, and a base may only claim it by also reporting
	// its own stop; a flag that stops a run while looking like an ordinary
	// success is worse than no flag, because nothing downstream can repair it.
	StepEdgeNative StepEdge = "native"
)

// StepCounter is implemented by adapters that can say which edge of a tool call
// they narrate. An adapter that does not implement it counts as StepEdgeNone —
// the safe direction, and the one the registry gate refuses to ship.
type StepCounter interface {
	StepEdge() StepEdge
}

// ReasonMaxStep is what `incomplete_details.reason` says when a step budget
// stopped the work.
//
// It lives here rather than beside the wall clock's `reasonTimeout` in the
// service package because both sides need it: the supervisor writes it for the
// four bases it counts, and grok's adapter writes it for itself. A second copy
// of the string in the adapter is how a client ends up reading two different
// words for one outcome depending on which base ran.
//
// Not an error code, and not vendor-prefixed. `incomplete_details` is an open
// object in the schema, and `incomplete` means the work was stopped part-way —
// a different claim from an error, which means it could not be done at all.
const ReasonMaxStep = "max_step"

// Delivery reports which parts of a harness's configuration a runtime enforces
// itself, as opposed to what the router has to convey as a standing
// instruction.
//
// It exists so the router can tell the difference honestly. Harnesses §4.3 is
// explicit that a restriction the runtime cannot enforce MUST still reach the
// agent and MUST NOT be dropped, and §4.1 that a server MUST NOT advertise MCP
// support it cannot deliver — both of which need an answer to "can this
// runtime actually do it", not an assumption.
//
// # Enforcement, not placement
//
// Every bit below names something with a real *no*, where prose is a weaker
// substitute for a mechanism and the router would otherwise be over-claiming.
// Where a runtime merely files the same text somewhere else, there is nothing
// to over-claim and no bit belongs here.
//
// This is why there is no fourth bit for "does this runtime take a system
// prompt", though three of the five bases have a flag for one. The standing
// block reaches the agent as prompt text on every base, deliberately: the
// composed prompt is the Task.Input a session's turns report, so it is also
// the only record of what a run actually ran under. See
// [ADR-0010](../../docs/adr/0010-instructions-reach-the-agent-as-prompt-text.md),
// which is a decision and not a gap — issue #79 is closed against it.
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
	UpdateDelta UpdateType = "delta"

	// UpdateToolCall says the agent took one tool call — one *step*, in the
	// wire's word. It is emitted once per call, on the edge [StepEdge] names
	// for that base, and never on the other edge: every base narrates both, and
	// reading both would halve every step budget silently.
	//
	// It carries no name, no arguments and no id, and that is a decision rather
	// than an omission (#72). The supervisor counts these and nothing else
	// reads them, because naming a step on the wire would mean inventing the
	// vocabulary the schema lacks — the mistake that got `tools` and `include`
	// declined in ADR-0007. A client learns its budget stopped the run from
	// `incomplete_details.reason`, which is the field for it.
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

	// UpdateIncomplete ends a run because a budget stopped it, which is a
	// different outcome from all three above and is why it is a fourth member
	// rather than a flag on one of them: Lifecycle §3 requires `incomplete` for
	// a budget and forbids it for an error, and a client reads the distinction
	// as "this work is worth continuing".
	//
	// The router synthesizes it today. A wall-clock budget is enforced by the
	// supervisor, which cancels the run through the adapter's own Cancel and
	// relabels the `cancelled` that comes back — see service.supervise. It is
	// declared here rather than kept internal because the *other* budget is
	// not the router's to enforce: a step budget is counted by whatever runs
	// the tool-call loop, so an adapter that bounds its own steps has this
	// vocabulary waiting for it and needs no second mechanism.
	UpdateIncomplete UpdateType = "incomplete"
)

// Terminal reports whether this update ends a run. Every terminal state must
// answer true here, or a consumer blocks until the adapter closes the channel.
func (t UpdateType) Terminal() bool {
	switch t {
	case UpdateCompleted, UpdateFailed, UpdateCancelled, UpdateIncomplete:
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
	Usage     *uhp.Usage
	Err       error

	// Reason says which budget stopped an [UpdateIncomplete] run, and becomes
	// `incomplete_details.reason` on the response. It is separate from Err
	// because the two are opposites: Err says the work could not be done, and
	// this says it was stopped part-way and could be continued.
	Reason string
}

// Adapter is the single interface every harness backend must satisfy.
type Adapter interface {
	// Info returns static capability/model metadata for discovery.
	Info() uhpgo.Harness

	// HealthCheck reports whether the underlying CLI/SDK/binary is reachable.
	HealthCheck(ctx context.Context) error

	// Run starts a task and streams updates on the returned channel until
	// it closes. Implementations MUST close the channel exactly once and
	// MUST respect ctx cancellation (maps to UHP task cancellation).
	Run(ctx context.Context, req RunRequest) (<-chan RunUpdate, error)

	// Cancel stops an in-flight run identified by taskID, if supported.
	Cancel(ctx context.Context, taskID string) error
}
