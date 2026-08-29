# ADR-0009: A step is one tool call, and each base is counted on the edge it narrates

Date: 2026-08-29
Status: Accepted
Issue: [#72](https://github.com/aenawi/uhp-go/issues/72)

## Context

`max_step` was the only `pending` entry on `droppableFields`: the one request field
[ADR-0007](0007-a-declined-field-is-not-a-pending-one.md) declined to decline. That ADR's
rule is what made it hard, and the rule is worth restating because everything below is an
application of it:

> A grant may be per-base, and **a bound must hold on all five bases or not be claimed at
> all.**

`max_step` is a bound. `max_output_tokens` failed the same rule and was declined because
token accounting arrives only after the tokens exist, so nothing could be stopped. A tool
call is different: it can be counted as it is requested, and the run cancelled before it
runs. So the field was reachable in principle and blocked in practice on two bases whose
behaviour nobody had measured.

The schema is no help on what to count. It says `"Agent step (tool-call round) budget"` and
defines a round no further.

## Decision

### A step is one tool call, not one round

Measured, not read off the schema. `opencode` was captured twice against the same prompt and
the same five writes: five `step_start`/`step_finish` pairs the first time, **one** pair the
second. Under a round rule, `max_step: 5` on `opencode` is satisfied by a single step
containing twenty writes and the ceiling never fires.

Counting calls gives five on every base and does not depend on a grouping the runtime is free
to change between runs.

The cost is stated rather than hidden: a step is finer-grained than the schema's phrasing
suggests, so `max_step: 5` buys fewer than five model turns. That over-counts rather than
under-counts — a run stops early rather than never — which is the one direction a budget may
err in.

### Each base is counted on the edge it narrates, and there are two

This is the part the issue got wrong, and it was wrong in the direction that costs a client
money rather than the one that costs it work.

The design assumed every base announces a tool call *before* it runs, so the ceiling could
always be tripped by the request after the last one allowed. Three bases do. `opencode` does
not: every `tool_use` event it emits carries `state.status == "completed"`, and a deliberate
run of a twelve-second shell command produced exactly one such event, twelve seconds in, and
nothing while the command was running. There is no earlier moment to count.

| base | edge | what marks it |
| --- | --- | --- |
| `claude` | start | `tool_use` blocks in an `assistant` message |
| `codex` | start | `item.started` whose item is a tool |
| `pi` | start | `assistantMessageEvent.type == "toolcall_start"` |
| `opencode` | **finish** | `tool_use` event |
| `grok` | — | not counted; see below |

**The comparison is the same everywhere** — `service.stepBudgetSpent` trips when the count
*exceeds* the ceiling — and what the edge changes is what that costs:

- On a **start** edge the tripping event is a *request*, so the stop is issued at the earliest
  moment anything downstream could know of the call.
- On a **finish** edge it is a *completion*, so that call has certainly run. `opencode`
  overshoots by at least one, always.

**A ceiling stops a run, not a call.** This is worth stating plainly because the obvious
reading of "trip on the request" is too strong: this server reads stdout and kills a process
group, and has no way to make a CLI wait. By the time a `tool_use` line is read, parsed and
counted, the agent has dispatched that tool. `max_step` therefore bounds how far a run
proceeds; it does not guarantee a number of tool calls, and no arrangement of a router that
observes a CLI could. `claude` sharpens the point: it puts a whole parallel batch on one
`assistant` line, so `max_step: 1` against a three-call batch counts one, counts two, trips —
and all three were dispatched before the first was counted.

Overshooting is the tolerable direction, which is the same reasoning that made a call rather
than a round the unit: a run stops early rather than never. It is documented per base in the
README rather than implied away.

The alternative for the finish edge — trip when the count *reaches* the ceiling, on the
ceiling'th call's own completion — keeps the number exact and was rejected, because it is
wrong in the way that matters far more: **it kills a run that complied.** An agent given five
calls that uses exactly five is torn down at the moment the fifth finishes, before it can
write its answer, and the client is handed `incomplete` with nothing in it — while the
identical request on `claude` completes.

So the declared edge (`harness.StepEdge`) does not select a comparison. It decides two things
a client can read: that `opencode` may exceed its ceiling by one call, and that `opencode` and
`grok` cannot honour `max_step: 0` at all.

### Only the start edge, and only one edge

Every base narrates both ends of a call — `user` tool-result, `toolcall_end`,
`item.completed`, `tool_execution_end`. A counter reading two of them halves every ceiling a
client set, and does it silently. Each adapter emits `UpdateToolCall` on exactly one edge and
stays silent on the other.

A turn with no tool call in it is **not** a step. Otherwise `max_step: 1` breaks a task that
answers without touching anything. An agent that reasons in circles calling nothing is the
wall clock's problem, and `timeout_seconds` is enforced (ADR's precedent: #54).

### `grok` enforces its own, and may only do so because it reports it

`grok` takes `--max-turns` and is not counted here. A flag that stops a run is half of a
budget; the other half is that the stopped run *says* it stopped, because a truncated run
that looks like an ordinary success reaches a client as `completed` and the router cannot
repair it — it did not do the stopping and has nothing to relabel.

`grok` has both halves. Measured on 1.0.13 (`make probe-grok-max-turns`), the terminal
`result` event carries `"subtype":"error_max_turns"` alongside `"stop_reason":"cancelled"`
and `"errors":["Reached the maximum number of turns"]`.

Two things follow, and both are load-bearing:

- **`subtype` is read before `is_error`.** The same line carries `is_error: true`, and `grok`
  exits 1. Read in the other order a budget stop reaches a client as `failed`, and Lifecycle
  §3 requires `incomplete` for a budget and forbids it for an error.
- **`stop_reason` is not the discriminator.** It says `"cancelled"` — the same word a
  client's own cancel produces — so reading it would make a budget stop and a cancellation
  indistinguishable, which is the exact confusion `incomplete` exists to avoid.

The unit is `grok`'s and it is not this server's. `--max-turns` counts *turns* where the
other four are counted in calls, so the same `max_step: 5` buys a coarser ceiling there. The
schema's own phrase is "tool-call round", so neither reading is the wrong one; only silence
about which is in use would be. It goes in the README, per base.

### Zero is a ceiling; negative is a `400`; unset is unbounded

`max_step: 0` asks for a run that answers without calling a tool. That is coherent and it is
the tightest bound the field can express — so zero cannot double as the absence of a budget,
which is why every ceiling in this change is a `*int` rather than an `int`.

A negative value is refused with `400`. §1.1's ignore-don't-reject rule covers fields a
server does not understand, not values with no meaning, and reading a negative as a stricter
ceiling would turn a client's typo into a task that silently refused to use a tool.

**Zero is refused on the two bases that cannot stop a call in time**, rather than accepted
and half-honoured. Only a start-edge base can deliver "call no tools": the request is seen
and the run cancelled before anything happens. The other two cannot, for different reasons
and with the same consequence — one tool call, side effects and all, against a client that
asked for none:

- `opencode` narrates a call only once it is **over**, so the first one has certainly happened
  by the moment the router could act. This is the same overshoot as above, at the one ceiling
  where a single call is the whole of the budget.
- `grok` is not counted here at all, so there is nothing to trip on, and its own flag is no
  substitute: `--max-turns 0` asks for no run rather than a run that calls no tools.

Both get `422 uhpgo_step_budget_unsupported`, by the same rule as the uncountable base below.
Every *positive* ceiling is held on all four, so this is the zero case alone and not a
narrowing of the field.

It is worth being clear that this is a real cost of both decisions rather than an oversight
in either. Counting `grok` like the other four would close half of it; nothing would close
`opencode`'s except upstream giving it a start edge. The alternative — accepting zero and
letting one call through — is the shape of defect this whole field exists to remove, so the
refusal is the cheaper of the two.

**There is no default.** Unset is unbounded, and this is the one place `max_step` deliberately
does *not* follow `timeout_seconds`. Security §5 makes bounding a task's duration this
server's obligation, so the wall clock has no spelling of "unbounded"; it says nothing about
tool calls, the wall clock already stops a runaway agent, and a surprise step ceiling would
break every task that legitimately takes forty calls today.

### The supervisor counts, and the capability check is skipped

Counting is `service.supervise`'s. It is already the task's only writer, already holds the
resolved budget, and already owns the cancel-and-relabel path #54 built; four adapter-local
counters would be four chances to disagree about what a step is.

The stop is the #54 path unchanged: `run.cancel()` through the adapter's own `Cancel`,
relabel whatever terminal update comes back, `UpdateIncomplete` with reason `max_step`.

No `cancellation` capability check, as the wall clock skips it — but on different grounds,
and the difference needs writing down. The wall clock's justification is Security §5:
bounding task duration is the *server's* obligation and a harness cannot opt out of it. That
does not transfer to a client's requested bound. What does: once this server accepts
`max_step` and reports the applied number back, honouring it is the server's own statement,
and reporting a ceiling it declines to enforce is ADR-0004's lie in new clothes.

### Per Task, resolved as the shortest of three, reported as the number applied

Per Task and never per Session: a session-wide count would silently give the fourth task
fewer steps than the first.

Resolved as the shortest of the three that are set — the request's, the harness's, and
`UHP_TASK_MAX_STEP`. Each is a clamp and none is a preference, so a client may narrow what its
operator set and may not widen it.

Reported as `metadata.max_step`, carrying the number actually applied, so a caller that asked
for 100 against a harness capped at 10 reads 10 rather than discovering it by being stopped
early. The *unit* stays off the wire: the schema has no word for round-versus-call, and
inventing one is the mistake that got `include` declined. No consumed count either — the
ceiling and `incomplete_details.reason` are everything a client can act on.

**Retries are not capped.** A client that gets `incomplete` can retry in the same session for
another N. That is exactly how `timeout_seconds` already behaves, per-Task is the only thing
the wire lets a client express, and it is said in the README rather than fixed. `max_step`
stops a runaway agent; it does not cap a client's spend.

### A base that cannot hold the bound refuses the task

ADR-0007's rule reaching the one place it can still be broken. An adapter that narrates no
countable call and bounds nothing itself gets `422 uhpgo_step_budget_unsupported` for a task
carrying a ceiling — not the `ignore-don't-reject` treatment §1.1 prescribes for an
unimplemented field, because this server *does* implement `max_step`, and a bound accepted on
a base that cannot hold it is the silence ADR-0004 removed wearing the appearance of success.

It is refused where the number is *stored* as well as where it is used: `POST`/`PUT`/`PATCH
/v1/harnesses` applies the same check to a harness's own `maxStep`, and refuses a negative
one outright. An operator is present at configuration time and is not present at run time, and
a harness saved with an unenforceable bound is one whose every task is about to be refused
for a field its clients never sent.

No base fails the uncountable check today. It is for the sixth, and it is the run-time half
of a gate whose other half is build-time: `TestEveryRegisteredBaseCanBeBounded` enumerates
the constructors `uhpd` compiles in, with no allowlist, and fails any base that declares no
edge or has no capture behind the one it declares. An allowlist is how a gate rots.

## Consequences

**The `pending` bucket is empty, and `droppableFields` is now entirely decisions.** Every
field still dropped is one ADR-0007 declined. That is worth reading as a fact rather than as
a to-do list: the obstacle is the same one three times over, and none of the three is waiting
on somebody's afternoon. The status is kept rather than deleted with its last member, because
a list of nothing but declines would make "declined" look like the only answer a dropped
field can get.

**Implementing `max_step` does not move the conformance score.** 52/52 before, 52/52 after,
because no check in the suite sends the field. Said out loud because the next reader will
otherwise assume a column got filled in.

**Stored `maxStep` values became live bounds retroactively.** `POST /v1/harnesses` has
accepted and stored the field since harness management existed, and nothing read it. An
operator who set 12 meant 12, and grandfathering their setting to `null` would destroy a
stated intent to avoid a surprise. It announces itself with a startup log naming every
harness carrying one, rather than a data migration — nothing about the values needs changing,
only somebody needs telling.

A value already on disk that *cannot* be honoured is the exception, and is named as ignored
rather than announced as enforced: a stored negative is skipped by `resolveStepBudget` and
logged at `Warn`. Announcing it as a live bound would be this ADR's own defect, in the one
place an operator is most likely to believe it.

**#91 stopped being a blocker without being fixed.** `pi` had no provider with a usable token
budget on the capture machine, which is a fact about an API key rather than about `pi`.
`make probe-pi-steps` answers from a loopback OpenAI-compatible provider declared in `pi`'s
own `models.json` — the trick `probe-pi-session.py` already uses for #33 — so `pi`'s narration
is established without credentials. What that does *not* establish is stated in the probe and
in the fixture README: a model did not choose those calls, the provider did. `pi` narrating
the calls it executes is the only thing a counter reads, and five files on disk is the same
ground truth every other base was held to.

**The `grok` result parser now has an ordering nothing else enforces.** `subtype` before
`is_error`, and only `TestGrokIsNotCountedAndReportsItsOwnStop` and the fixture behind it say
why. A `grok` release that collapses the two subtypes fails that test rather than silently
reporting truncated runs as failures.

**`opencode`'s edge is a standing obligation, not a settled fact.** It is the one base where
an upstream change — adding a `running` state to `tool_use`, say — would make a strictly
better answer available. `make probe-steps` after an `opencode` upgrade is what would find it.
