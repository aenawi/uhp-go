package service

import "strings"

// composePrompt builds the text one run is driven by: the harness's standing
// instructions, then the task's own, then the input.
//
// # Appended, never substituted
//
// A task's `instructions` are additional guidance and not a replacement for the
// harness's. The standing block is where a restriction lands when the runtime
// cannot enforce it — Harnesses §4.3 requires such a restriction to reach the
// agent anyway and forbids dropping it — so a request that replaced the block
// could switch off an operator's configuration by sending one field. That is
// request-side escalation over deployment configuration, which is the same
// shape as the budget widening #75 closed and is refused for the same reason:
// each level of configuration is a floor the next one cannot remove.
//
// The ordering is standing-first for a plainer reason. The harness's block is
// the deployment's standing position; the task's is what this caller wants
// today; the input is the work. Reading them in that order is reading them from
// most general to most specific.
//
// # For this task only
//
// Nothing here persists a task's instructions for the next turn of a session.
// UHP calls them guidance "for this task only", and making them sticky would
// invent session state the wire has no field to report — a client could not ask
// what instructions its session is currently running under. What each turn
// actually ran under stays recoverable regardless, because the composed prompt
// is the Task.Input that GET /v1/sessions/{id}/turns reports.
//
// Empty parts are dropped rather than joined, so a harness with no standing
// block and a task with no instructions produce the bare input and not a prompt
// that opens with blank lines.
func composePrompt(standing, instructions, input string) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{standing, instructions, input} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}
