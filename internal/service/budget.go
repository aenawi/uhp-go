package service

import (
	"math"
	"time"
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
