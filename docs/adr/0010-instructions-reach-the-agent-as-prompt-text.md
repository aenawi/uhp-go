# ADR-0010: A harness's instructions reach the agent as prompt text, on every base

Date: 2026-08-29
Status: Accepted
Issue: [#79](https://github.com/aenawi/uhp-go/issues/79)

## Context

[harness.Delivery](../../internal/harness/harness.go#L149) asks an adapter three questions —
can it hard-block a tool, does it load skill folders, does it take a per-run MCP config — so
the router conveys what a runtime cannot enforce and claims only what it can.

Issue #79 observed that it does not ask a fourth: **does this runtime take a system prompt?**
It does not ask, so the answer is uniform. `service.prepareRuntime` joins the harness's system
prompt, its skill instruction and its unenforceable tool restrictions into one standing block,
and `composePrompt` prepends that block to the input. All five bases receive it as prompt
text, and three of them ship a flag that would have carried it instead:

| Base | Append to the system prompt | Replace it |
|---|---|---|
| `claude-code` | `--append-system-prompt` | `--system-prompt`, `--system-prompt-file` |
| `grok-cli` | `--rules` | `--system-prompt-override` |
| `pi` | `--append-system-prompt` | `--system-prompt` |
| `codex` | none | none |
| `opencode` | none | none |

That table is the *verified* grade this repository already defines in
[README.md](../../README.md#L943) — read from each CLI's own `--help` on a machine where it is
installed, on 2026-08-29 — and not the stronger *executed* grade. The distinction is not
pedantry here; it is one of the four reasons below.

#79 states the case for change fairly: prompt text is not wrong, Harnesses §4.3 is satisfied
by it, and the router claiming less than the runtime can do is the safe direction. The
complaint is that it was chosen by default rather than on purpose, and that nothing says which
mechanism carried the instructions.

## Decision

**Prompt text is the delivery, on every base, and it is a decision rather than a default
awaiting a better one.** `Delivery` keeps three bits. No adapter grows a system-prompt argv
branch. The three append flags above stay unused deliberately.

Four reasons, in the order they bind.

### `Delivery` is about enforcement, and a system prompt is not enforced

Each of the three bits names something with a real *no*. A runtime that cannot block a tool,
cannot load a skill folder, cannot take an MCP configuration — in each case prose is a weaker
substitute for a mechanism, and the bit exists so the router can say which of the two
happened. Harnesses §4.3 ("a restriction the runtime cannot enforce MUST still reach the
agent") and §4.1 ("a server MUST NOT advertise MCP support it cannot deliver") both need that
answer, and neither can be answered by assumption.

"Does it take a system prompt" has no such no. The words reach the model either way; only
their position in the context window changes. A fourth bit would make one struct mean two
things — what a runtime *enforces*, and where a runtime *files* text — and only the first is
something the router can over-claim. This is why the missing question looked like an omission:
the struct is shaped like a list of mechanisms, and a system prompt is a mechanism. It is not
a mechanism of the kind `Delivery` exists to track.

### ADR-0007's rule does not reach this, and naming the rule that does is the point

[ADR-0007](0007-a-declined-field-is-not-a-pending-one.md) settles that *a grant may be
per-base, and a bound must hold on all five bases or not be claimed at all*. Three of five
would fail that rule instantly, and it does not apply: the rule governs **client request
fields**, where the harm is a caller believing something about their own task that is not
true. Nothing here is a request field. This is the operator's own configuration, and
`Delivery` is already lopsided for it — per-run MCP is `claude-code` alone.

So 3-of-5 is not itself disqualifying, and this ADR does not rest on it. The rule that governs
here is §4.3, and prompt text satisfies it on all five bases. Recording which rule applies
matters as much as the outcome: a future reader who reaches for ADR-0007's all-five test would
decline this for the wrong reason and would then decline the next per-base delivery too.

### `Task.Input` is the only honest record of what a run ran under

This is the argument that decides it. The composed prompt *is* `Task.Input`
([task_service.go:497](../../internal/service/task_service.go#L497)), which is what
`GET /v1/sessions/{id}/turns` reports — the property `composePrompt`'s doc comment already
leans on to keep task instructions non-sticky without inventing session state.

Move the standing block to argv and there are three arrangements, none of them good:

- **Pass the flag and keep the block in `Input`.** The operator's instructions reach the model
  twice, once as a system prompt and once at the head of the user turn. That is a bug wearing
  a record's clothing.
- **Move it out.** The turn record stops showing it. A client reading its own session history
  sees the input without the configuration that shaped the answer, and there is no other field
  in which it survives. What a run ran under becomes unrecoverable from the wire.
- **Move it out and name it in `metadata`.** `metadata.ignored_fields`
  ([ADR-0004](0004-ignored-fields-are-declared-in-metadata.md)) exists for request fields a
  *caller sent* and this server dropped. No caller sends a harness's system prompt. A new key
  would be this server inventing a field to describe a change no client asked for and none can
  act on.

Prompt text is the one arrangement in which the record and the run agree, and it is that by
construction rather than by luck.

### The verification costs more than the change can buy

[claude.go:105-125](../../internal/harness/claude.go#L105) is the standing record of what a
delivery bit costs: two flags declared from documentation, unrun for three issues, then
executed against the real binary and watched from the far end — because *a flag the CLI
accepts and does not act on is the failure the bit exists to prevent*. `--disallowedTools`
turned out to be comma-joined rather than space-separated; `--mcp-config` turned out to need
`--strict-mcp-config` beside it to mean what it appeared to mean. Neither is visible in
`--help`.

The table above is `--help` grade. Raising it to executed grade is five probes plus a
re-run after every CLI release, to buy a change no client can observe. That is the same
arithmetic ADR-0007 ran against `include`, and it comes out the same way.

## Consequences

**Nothing changes on the wire.** No new `metadata` key, no `ignored_fields` entry — that array
is for request fields, and this is not one. Clients see exactly the responses they saw before,
which is correct, because nothing about their tasks changed.

**`CONTEXT.md` is unchanged.** It already defines **Standing instructions**; *how* they are
carried is implementation, which the glossary excludes by design.

**A test pins the mechanism, not just the ordering.**
`TestStandingInstructionsTravelAsPromptTextEvenWhereARuntimeCouldTakeThem` asserts the standing
block reaches the model inside the composed input, against an adapter that enforces everything
natively — so the day someone adds a system-prompt argv branch, the block leaves `Task.Input`
and the test fails. `instructions_test.go` pinned the ordering of the blocks and nothing pinned
where they travelled, which is how #79 became possible to file against code that was already
deliberate.

**Revisit if prompt-text delivery is measured to be weaker**, rather than assumed to be. If a
base is shown to truncate, deprioritise or otherwise degrade leading prompt text relative to
its own system prompt, that is a fact and this decision is made against the absence of one. A
model *appearing* to weight a system prompt more highly is not that fact.

## Considered options

**A fourth `Delivery` bit and a per-adapter argv branch.** What #79 proposed. Rejected on all
four grounds; the third is sufficient alone.

**Wire the replace flags rather than the append ones.** Worth recording because all three
capable bases offer both, so a reader who gets as far as the flag table will see them.
Rejected outright: `--system-prompt`, `--system-prompt-file` and `--system-prompt-override`
discard the runtime's own default system prompt — the block that tells `claude-code` how to
use its own tools and `pi` that it is a coding assistant. An operator setting
`system_prompt: "be terse"` would get a lobotomised agent and no signal that it happened. It
is also the escalation `composePrompt` already refuses, one layer down: each level of
configuration is a floor the next cannot remove, and harness configuration removing the
runtime's floor is that rule broken in the same shape.

**Report the mechanism in `metadata` and change nothing else.** Answers #79's "it is invisible"
at no risk to delivery. Rejected because the invisibility is not a client's problem — a client
cannot act on the answer — and the maintainer who does need it is served by this document and
by the README's delivery table, which now carries a system-prompt column for exactly that
reason.
