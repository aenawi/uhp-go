# ADR-0008: An agent may write in the directory it was given

Date: 2026-08-29
Status: Accepted
Issue: [#89](https://github.com/aenawi/uhp-go/issues/89)

## Context

`uhpd` passed no sandbox, approval or permission flag to any of the five CLIs. That was
never a decision. It is the absence of one, and until it was measured nobody knew what it
meant, because it does not mean the same thing twice.

The measurement is `make probe-steps`, run on 2026-08-28 and 2026-08-29 against the shipped
invocation of each base — exactly `CLIHarness.BuildArgs`, prompt where that adapter puts it,
nothing added. One prompt, asking for five files with a separate write for each, and ground
truth counted on disk afterwards:

| base | files asked for | files created |
| --- | --- | --- |
| `claude` 2.1.250 | 5 | 5 |
| `opencode` 1.18.23 | 5 | 5 |
| `codex` 0.150.1 | 5 | **0** |

`codex` refused every write, because with no flag its workspace is read-only:

```text
ERROR codex_core::tools::router: error=patch rejected:
writing is blocked by read-only sandbox; rejected by user approval settings
```

So this server was already granting an agent write access on some bases and withholding it
on another, and the difference was a vendor default rather than anything anyone here chose.
An operator picking a harness could not predict from that choice whether their agent could
do the work — and `codex` advertises `tools` in its own capability list while being unable
to complete the most ordinary tool call there is.

Two further facts frame the decision rather than follow from it.

**The boundary already exists.** The router gives each Session its own working directory and
runs the child in it; the Files chapter calls that a Container, and `CONTEXT.md` says a
Session and its Container are the same thing named from two places. Whatever is decided
here, the unit it is decided about is not "the machine" — it is that directory.

**The refusal was invisible.** The run that created nothing ended on `turn.completed`,
exited `0`, and reached the client as `completed` with the agent's apology as its answer.
That is a defect in reporting rather than in policy, it is fixed under the same issue, and
it is deliberately *not* what this ADR decides — adding a flag without fixing the reporting
would leave the same hole with a smaller entrance.

## Decision

**`uhpd` grants every harness write access to the working directory it gave the Session.
Where a runtime can be confined to that directory by an argument, it is. Where it cannot,
the grant is the same and the confinement is not claimed.**

The policy is uniform across the five bases. The mechanism is per-base, and only `codex`
needs one.

**Granted, rather than withheld, because withholding cannot be delivered and would break
the server's own promises.** An agent that cannot write cannot use the Container, cannot
produce an Artifact, and cannot answer the file-shaped half of what this server exists to
route. Four of the five bases would also ignore a decision to withhold: none of `claude`,
`opencode`, `grok` or `pi` takes an argument that makes it read-only, so "read-only by
default" would be a claim this server could keep on one base and not the other four —
exactly the shape ADR-0007 rules out for anything a client is entitled to rely on.

**`codex` is confined with `-c sandbox_mode=workspace-write`, on both the fresh and the
resumed invocation.** Which form carries it is measured, not chosen for looks:

- `--sandbox workspace-write` is the documented flag and `codex exec resume` rejects it
  outright — *"error: unexpected argument `--sandbox` found"*. A grant that only the first
  turn can express is a Session whose second turn is read-only.
- `-c sandbox_mode=workspace-write` is the same setting as a config override, and `-c` is
  accepted by both forms. Verified 2026-08-29 on 0.150.1: a fresh turn wrote its file, and a
  resume of that thread wrote another.

A resume was also measured *without* it and wrote anyway — the thread remembers the mode it
started in. It is still passed on both, because that inheritance is undocumented, is not
something `codex` promises, and costs nothing to stop depending on, while depending on it
puts every continued task one release away from silently going read-only.

**What is not claimed.** `codex` is the only base this server confines. The other four are
granted the same access and are bounded by nothing `uhpd` passes: they write where the agent
points them, and the working directory is where they happen to start rather than a wall.
That is stated here because it cannot be discovered by asking the server, and it is recorded
as inherited work in the Consequences rather than presented as a property this decision
established.

## Considered options

**Leave it alone.** The status quo, and the reason this ADR exists. It is not neutral: it is
a policy that differs per base, that nobody selected, and that on one base makes every
writing task fail — silently, until #89.

**Pass `--sandbox workspace-write` on the fresh invocation only.** The obvious reading of
the issue, and it was measured before it was rejected. `codex exec resume` does not accept
the flag, so a Session would be granted write access on its first turn and quietly lose it
on every turn after — the same defect this decision removes, moved somewhere harder to see.

**Pass `--dangerously-bypass-approvals-and-sandbox`, or `--sandbox danger-full-access`.**
Both would have worked and both grant the machine. The router runs an agent in a directory
on the operator's own computer; the directory is the boundary it already maintains, and
there is no requirement here that needs a wider one. `workspace-write` is the narrowest
setting that lets the work happen, which is the one to pick.

**Make it an operator setting — `UHP_SANDBOX=read-only`.** Rejected for now on the same
ground as the decision itself. A knob that is honoured on one of five bases and silently
does nothing on the other four tells an operator they have turned something off when they
have not, which Harnesses §4.3 calls the worst outcome. If a second base ever gains a
read-only argument, this is the ADR to supersede.

**Grant write access and say nothing about confinement.** Tempting, because it is one
sentence shorter and describes the same argv. It would also let "the agent is sandboxed to
its session directory" become something people believe about all five bases on the strength
of a flag that applies to one.

## Consequences

**`codex` can do the work it advertises.** It declares `tools` in its capability list, and
under the previous invocation a write — the most ordinary tool call there is — could not
complete. Every writing task on `codex` failed, and until #89 it failed as a success.

**Four of five bases are granted write access and confined by nothing.** `claude`,
`opencode`, `grok` and `pi` write wherever the agent points them. No test here can assert
otherwise, because there is no argument to assert about. A future ADR that finds one
inherits this as work rather than as a passing check — the same shape as ADR-0006's
untestable `403`.

**The shipped invocation changed, so every probe measuring it changed with it.**
`CODEX_ARGV` in `probe-steps.py` and `HARNESS_ARGV`/`RESUME_ARGV` in `probe-codex-session.py`
carry the override, pinned to `BuildArgs` by `TestStepProbeRunsTheShippedInvocation` and
`TestCodexAndGrokProbesRunTheShippedInvocation`. A probe measuring an invocation this server
does not send measures nothing, which is the standard those tests already existed to hold.

**`codex` can now be measured, which is not the same as counted.** Under the old invocation
it could take no tool call at all, so the step-narration README carried it under "Not here".
The re-run narrates five calls for five files, and `TestCapturedToolCallsMatchGroundTruth`
has a `codex` row asserting that equality. Nothing here counts a step: `max_step` is #72's,
and what it gains is a base whose evidence exists rather than a line of its implementation.

**This decision does not close #89, and the reporting fix does not depend on it.** A
wholly-refused `codex` run now terminates as `failed` carrying the runtime's own words, by a
mechanism that reads the child's stderr and is described in `codex.go`. It is kept because
the hole swallows every reason a tool is refused and not only this one — including the case
where a later `codex` renames `sandbox_mode` and this ADR's grant stops arriving. The two
halves guard each other: the flag stops the refusal happening, and the reporting stops it
happening silently.

**One refusal remains undetectable, and this decision is what answers it.** Measured
2026-08-29: a read-only run told to write with `printf` rather than an editing tool had both
attempts blocked, created nothing, and emitted no `ERROR` line and no `command_execution`
item — the refusal survives only in the agent's prose. There is no signal there to read, so
the reporting fix covers the write route that logs and not that one. It is recorded here
because it makes the grant load-bearing rather than convenient: without it, a class of
`codex` run fails silently and nothing this server could add would notice.
