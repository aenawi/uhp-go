package service

import (
	"fmt"
	"math"
	"time"

	"github.com/aenawi/uhp-go/internal/harness"
)

// DefaultTaskBudget is how long a task may run when nothing narrows it.
//
// The value matters less than its being finite. Security §5 makes bounding
// task duration the server's obligation, and before #54 nothing in this tree
// did: a harness CLI that wedged held its run slot for ever, so N wedged agents
// took the server permanently to capacity and every later task was refused
// `503 harness_unavailable` — advice to retry, for a condition retrying never
// clears.
//
// Thirty minutes is chosen against real agent work rather than against a round
// number: the CLIs this server drives routinely think for several minutes on
// one task, occasionally for tens, and essentially never for half an hour
// without being stuck. An operator whose agents legitimately run longer raises
// UHP_TASK_TIMEOUT; there is deliberately no way to switch the bound off.
const DefaultTaskBudget = 30 * time.Minute

// reasonTimeout is what `incomplete_details.reason` says when the wall clock
// stopped the work.
//
// It is not vendor-prefixed, and it is not an error code. `incomplete_details`
// is an open object in the schema, and the protocol's own `timeout` *error*
// code is a different thing entirely — an error means the work could not be
// done, and this means it was stopped part-way. See docs/conformance.md, where
// the two are told apart.
const reasonTimeout = "timeout"

// resolveBudget picks the wall-clock budget for one run: the shortest of the
// three bounds that are set — the request's, the harness's, and the
// deployment's own.
//
// Every level is a clamp and none of them is a preference. A client may narrow
// the bound its deployment set and may not widen it, because a bound a caller
// can raise without limit is not a bound, and Security §5 asks this server for
// one rather than for a suggestion; the same holds of the harness's own budget,
// which is how an operator says "this CLI wedges, give it sixty seconds" and is
// worth nothing if the next request can say otherwise. That is also why the
// resolved value is reported back on the response
// (`metadata.timeout_seconds`): a request that asked for a day and got half an
// hour has been answered, not overruled in silence.
//
// Taking the minimum rather than the first bound set is #75. Precedence between
// the levels is only ever interesting when they disagree in the direction the
// rule already forbids, so there is none to express.
//
// A non-positive value is not a budget and is skipped rather than applied. Zero
// would mean "stop before starting", which no caller wants and which would fail
// every task on a harness that carried it.
//
// The two levels are deliberately not treated alike in where they are refused,
// and the asymmetry is the point rather than an oversight: a request naming a
// non-positive budget is refused outright at the transport, because a client is
// present to be told and silently substituting a different number is the defect
// #54 is about. Stored harness configuration has nobody to tell — it may
// already be on disk from before any of this existed — and failing every task
// on such a harness at run time is the worse of the two answers, so it is
// skipped here instead.
func resolveBudget(requested, configured *int, ceiling time.Duration) time.Duration {
	budget := ceiling
	for _, seconds := range []*int{requested, configured} {
		if seconds == nil || *seconds <= 0 {
			continue
		}
		// Refused before the multiplication rather than after it: a budget
		// naming more seconds than time.Duration can hold would wrap negative
		// and become a budget that had already expired — the widest number a
		// caller can write turning into the narrowest bound there is. It
		// cannot narrow any representable ceiling anyway, so skipping it is
		// both safe and the right answer.
		if int64(*seconds) > maxBudgetSeconds {
			continue
		}
		if named := time.Duration(*seconds) * time.Second; named < budget {
			budget = named
		}
	}
	return budget
}

// maxBudgetSeconds is the largest whole number of seconds time.Duration can
// hold — about 292 years, and so far outside any budget an operator or a client
// means that the only values above it are overflow attempts and mistakes.
const maxBudgetSeconds = int64(math.MaxInt64 / int64(time.Second))

// resolveStepBudget picks the step budget for one run: the shortest of the
// three bounds that are set — the request's, the harness's, and the
// deployment's own (#72).
//
// It is resolveBudget's shape, deliberately, because it is the same kind of
// thing: every level is a clamp, none is a preference, a client may narrow what
// its deployment set and may not widen it, and the resolved number is reported
// back on the response (`metadata.max_step`) so a caller that asked for 100
// against a harness capped at 10 reads 10 rather than discovering it by being
// stopped early.
//
// Where the two differ is the default, and the difference is the point.
// resolveBudget starts from a ceiling that always exists, because Security §5
// makes bounding a task's *duration* the server's own obligation and there is
// no spelling of "unbounded". A step ceiling is not that obligation — the wall
// clock already discharges it — so this starts from **unset**, and a task that
// asked for no step budget gets none. Inventing a default here would break
// every task that legitimately takes forty tool calls today, to bound something
// already bounded.
//
// A **negative** value is skipped at either level, and only a negative one —
// which is where this parts company with resolveBudget, whose guard is
// `<= 0`. A stored `maxStep: 0` is a coherent thing for an operator to have
// meant ("this harness may not use tools") and is applied; a stored negative is
// a value with no meaning, and reading it as the tightest possible ceiling would
// turn one typo into every task on that harness refusing to touch anything.
//
// A *request* naming a negative is refused at the transport, where there is a
// client to tell, and so is one naming `max_step` on a harness that cannot hold
// it — see requireStepBudget. Stored configuration is skipped here rather than
// refused because it may predate all of this and has nobody to tell.
//
// It returns nil for "unbounded", never zero, and that distinction is why this
// signature is not resolveBudget's: zero cannot double as the absence of a
// budget the way it does for a wall clock no caller can switch off.
func resolveStepBudget(requested, configured *int, ceiling int) *int {
	var budget *int
	if ceiling > 0 {
		budget = &ceiling
	}
	for _, steps := range []*int{requested, configured} {
		// Skipped rather than clamped to zero. A negative ceiling is not a
		// stricter budget, it is a value with no meaning, and reading one as
		// "no tool calls" would turn an operator's typo into every task on that
		// harness answering without touching anything.
		if steps == nil || *steps < 0 {
			continue
		}
		if budget == nil || *steps < *budget {
			// Copied rather than aliased: the caller owns the request body and
			// the harness config, and a pointer into either would let a later
			// mutation change a budget this run is already being held to.
			n := *steps
			budget = &n
		}
	}
	return budget
}

// enforceableStepBudget is a harness's own stored ceiling, or nil when this base
// cannot hold it (#72).
//
// Dropped rather than refused, and the asymmetry with requireStepBudget below is
// the same one resolveBudget already draws: a *request* has a client listening
// and is told no, and stored configuration has nobody to tell. `maxStep` was
// storable and inert for as long as harness management has existed, so a value
// on disk may predate the field meaning anything — `0` was a perfectly natural
// spelling of "no limit" when nothing read it.
//
// Refusing such a row instead would take the harness out of service: every task
// on it, including the overwhelming majority that never mention `max_step`,
// answered with a `422` naming a field the client did not send. Dropping it
// costs an operator a bound they may not have meant; refusing it costs every
// client the harness. The operator is told at startup — announceLiveStepBudgets
// — which is where somebody who can fix it is reading.
//
// New configuration cannot arrive this way: applySpec runs requireStepBudget at
// write time, where the operator *is* present.
func enforceableStepBudget(configured *int, edge harness.StepEdge) *int {
	if configured == nil {
		return nil
	}
	if requireStepBudget(configured, edge, "") != nil {
		return nil
	}
	return configured
}

// derefOr reads a pointer, answering `fallback` for nil. It exists so the
// deployment ceiling can go through enforceableStepBudget — which speaks in
// pointers, because zero is a real ceiling — and come back as the plain int
// resolveStepBudget takes for that level, where zero means unset.
func derefOr(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

// requireStepBudget refuses a ceiling the named base cannot actually hold (#72).
//
// This is ADR-0007's rule reaching the one place it can still be broken. A grant
// may be per-base; a bound may not, because a caller who set one and was not
// told believes their work is capped when it is unbounded — and the belief costs
// money. So a ceiling that cannot be enforced is refused rather than accepted
// and left inert, which is the opposite of what Tasks §1.1 prescribes for a
// field a server does not implement, and deliberately: this server *does*
// implement `max_step`.
//
// Two refusals, and neither is reachable by an ordinary request today.
func requireStepBudget(maxStep *int, edge harness.StepEdge, harnessID string) error {
	if maxStep == nil {
		return nil
	}

	// A base that narrates no countable call and bounds nothing itself. Nothing
	// registered produces this — it is the sixth harness somebody adds without
	// probing it, and it is checked at run time as well as by the build-time
	// gate in step_capture_test.go because a harness store on disk can name a
	// base a later binary no longer counts.
	if edge == harness.StepEdgeNone {
		return fmt.Errorf("%w: harness %q narrates no countable tool call and enforces no "+
			"ceiling of its own, so max_step would be accepted and not enforced",
			ErrStepBudgetUnsupported, harnessID)
	}

	// `max_step: 0` asks for a run that answers without calling a tool, and it
	// is the one ceiling where a single overshot call is the whole of the
	// budget. Two edges overshoot it by construction:
	//
	//   - A **finish**-edge base narrates a call only once it is over, so the
	//     first call has certainly happened, side effects and all, before the
	//     router could know of it. That is `opencode`.
	//   - A **native** base is not counted here at all, so there is nothing to
	//     trip on, and its own flag is no substitute — `grok`'s `--max-turns`
	//     counts turns, and zero turns is no run at all rather than a run that
	//     calls no tools.
	//
	// A start-edge base is not exempt from the race — see stepBudgetSpent; this
	// server reads stdout and kills a process group, it cannot make a CLI wait —
	// but it acts at the earliest moment anything downstream could, which is the
	// most a router can offer. The other two cannot even do that, so the honest
	// answer there is no rather than "usually".
	//
	// Every *positive* ceiling is accepted on all four, so this is the zero case
	// alone rather than a narrowing of the field.
	if *maxStep == 0 && edge != harness.StepEdgeStart {
		return fmt.Errorf("%w: harness %q does not announce a tool call until it has "+
			"happened, so it cannot honour `max_step: 0` — the first call would run "+
			"regardless. Ask for a positive ceiling, or use a harness that announces a "+
			"call before making it", ErrStepBudgetUnsupported, harnessID)
	}
	return nil
}

// nativeMaxStep is the step ceiling to hand the adapter: the resolved number
// for a base that enforces its own, and zero for every base the supervisor
// counts.
//
// It exists so the split is stated in one place rather than inferred at the
// call site. Only [harness.StepEdgeNative] gets a number, because only that
// base has somewhere to put one; telling the other four would give the ceiling
// a second home and no second effect, and a value that is carried but not used
// is the kind that quietly stops matching the one that is.
//
// A ceiling of zero never reaches here for a native base: StartTask refuses that
// combination outright, because a turn budget cannot express "call no tools" and
// the router is not counting this base's calls to trip on instead. The guard
// stays anyway — `--max-turns 0` is not a number grok has been shown to accept,
// and a second reader of this function should not have to go and check that
// StartTask still refuses it.
func nativeMaxStep(maxStep *int, edge harness.StepEdge) int {
	if maxStep == nil || *maxStep <= 0 || edge != harness.StepEdgeNative {
		return 0
	}
	return *maxStep
}

// budgetSeconds renders a budget in the units the wire uses, never as zero.
//
// Truncating would report a sub-second budget as no budget at all —
// SyncMetadata omits `timeout_seconds` when it is zero — so a server
// configured with one would enforce a bound and tell the client nothing about
// it, which is the reporting half of #54 undone. Rounding up is the direction
// that keeps the field present; it costs at most a second of overstatement, on
// a configuration only a duration-string UHP_TASK_TIMEOUT can produce.
func budgetSeconds(d time.Duration) int {
	seconds := int(d / time.Second)
	if d%time.Second != 0 {
		seconds++
	}
	return seconds
}
