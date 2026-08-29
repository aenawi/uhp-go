# Step-narration captures

What each base puts on stdout when an agent calls tools, captured by execution
rather than read out of a vendor document. This is the evidence
[#72](https://github.com/aenawi/uhp-go/issues/72) turns on: `max_step` is a
budget on agent steps, and a budget can only be claimed on a base whose output a
step can be counted from.

Captured 2026-08-28. `claude` 2.1.250, `opencode` 1.18.23. Reproduce with
`make probe-steps`.

`codex.jsonl` is dated separately: captured 2026-08-29 on `codex` **0.150.1**, because
until that day `codex` could not take a tool call under the shipped invocation
at all. `codex-read-only.jsonl` and its `.stderr` are the run that could not, kept
as the fixture for [#89](https://github.com/aenawi/uhp-go/issues/89) and read
below under [The run that could not act](#the-run-that-could-not-act).

`make probe-steps` runs every base. One at a time is the script directly, which
is how the `codex` pair was taken:

```bash
python3 scripts/probe-steps.py --base codex --keep
```

`grok-max-turns.jsonl` is the odd one out and is dated separately: captured
2026-08-29 on `grok` **1.0.13**, a version ahead of the 1.0.5 every other `grok`
capture in this repository was taken on. It answers a different question, from a
different probe — `make probe-grok-max-turns` — and is read below under
[What grok's own stop looks like](#what-groks-own-stop-looks-like).

Every capture here is the **shipped invocation** — exactly `CLIHarness.BuildArgs`,
with the prompt wherever that adapter puts it. An earlier pass added
`--permission-mode bypassPermissions`, `--auto` and `--sandbox workspace-write`
and passed the prompt as argv, none of which `uhpd` sent; it was discarded. A
probe that measures an invocation this server does not send measures nothing,
which is the standard `TestCodexAndGrokProbesRunTheShippedInvocation` already
holds the other probes to and `TestStepProbeRunsTheShippedInvocation` holds
these.

One sandbox argument is now shipped, and only on one base: `codex` carries
`-c sandbox_mode=workspace-write`, decided in
[ADR-0008](../../../../docs/adr/0008-an-agent-may-write-in-the-directory-it-was-given.md).
That is the reverse of the discarded pass rather than a return to it — the flag
is here because the adapter sends it, and the pin is what makes the difference
checkable rather than a matter of trust.

The prompt is on stdin for `claude`, `opencode` and `codex`, and in argv for
`grok` alone — `-p=<prompt>`, the only shape that carries an arbitrary prompt
safely there. The `grok` run adds `--max-turns`, and that one flag is the sole
departure from what `uhpd` sends: it is the subject of the measurement, nothing
sends it today, and it is kept in a list of its own in the probe so the pin
cannot come to assert that this server does.

## The measurement

Ground truth is on disk, not in the stream. Each **counted** base — `claude`,
`opencode` and `codex` — was given one task with a known number of verifiable
side effects:

> Create exactly five files in the current directory, named step1.txt …
> step5.txt. Each file must contain only its own number. Create them one at a
> time, using a separate file-write tool call for each one. Do not use a shell
> loop, and do not create them in a single command. When all five exist, reply
> with the word DONE and nothing else.

Each of those runs produced exactly five files. A base narrating five tool calls
narrated every call; a base narrating fewer under-counts, which is the one
failure a step budget cannot survive — a caller told it has a ceiling of five
while the agent takes twenty.

`grok` is measured against ground truth on disk the same way, for a different
question and with a different task. Its own is below.

## What is counted

The **start** of a tool call: the model asking for the tool, before its work
happens. Each base also narrates a finish — `user` tool-result, `step_finish`,
`item.completed` — and counting both doubles every round. Starting is also what
makes "allow N, stop before the N+1th" exact: the run is stopped when the next
call is requested, not after it has run.

| base | start edge | narrated | files |
| --- | --- | --- | --- |
| `claude` | `tool_use` blocks in an `assistant` message | 5 | 5 |
| `opencode` | `tool_use` event | 5 | 5 |
| `codex` | `item.started` whose item is a tool | 5 | 5 |

## A step is a tool call, not a round

The schema calls `max_step` a "tool-call round" budget. No CLI here delimits a
round, and the one that comes closest does not do it the same way twice.

`opencode` was captured twice against this same prompt. The first run bracketed
each write in its own `step_start`/`step_finish` pair — five steps. The second
put all five writes inside **one** pair. Same CLI, same prompt, same five files.

Counting rounds would therefore have made `max_step: 5` on `opencode` satisfiable
by a single step containing twenty writes, and the ceiling would never fire.
Counting calls gives five on every base and does not depend on a grouping the
runtime is free to change. So the tests here assert the invariant — narrated
calls equal calls that happened — and assert nothing at all about grouping.

The cost is stated rather than hidden: a step is finer-grained than the schema's
word suggests, so `max_step: 5` buys fewer than five model turns, and `grok`'s
native `--max-turns` — which really does count turns — is further from the other
four than the phrasing implies. That over-counts rather than under-counts, so a
run stops early rather than never, which is the tolerable direction.

## What grok's own stop looks like

`grok` is asked a different question from the other four, because this server
does not count it. It bounds its own steps with `--max-turns`, so what it owes is
not a *narration* but a *report* — a flag that stops a run is half of a budget,
and the other half is that the stopped run says it stopped. A truncated run that
looks like an ordinary success would reach a client as `completed`, and the
router could not repair that: it did not do the stopping and has nothing to
relabel.

| base | narrates every call | reports its own stop |
| --- | --- | --- |
| `grok` | not applicable — not counted | yes, `result.subtype == "error_max_turns"` |

Measured by `make probe-grok-max-turns`: the shipped invocation plus
`--max-turns 1`, against a *chained* task rather than the parallelisable one
above. A model free to batch would satisfy that one in a single turn, never reach
the ceiling, and end on a success saying nothing about `--max-turns`:

> Create step1.txt in the current directory containing only the number 1. Then
> read step1.txt and create step2.txt containing that number plus one. Then read
> step2.txt and create step3.txt containing that number plus one. Continue this
> way through step5.txt. You must read the previous file before creating the next
> one. When step5.txt exists, reply with the word DONE and nothing else.

Ground truth is on disk as always: **one file of five**, so the run was genuinely
truncated and not a task that happened to finish early.

The terminal event, `modelUsage` elided:

```json
{"type":"result","subtype":"error_max_turns","is_error":true,"duration_ms":5233,
 "duration_api_ms":4937,"num_turns":1,"stop_reason":"cancelled",
 "total_cost_usd":0.00744192,
 "usage":{"input_tokens":19956,"output_tokens":164,"cache_read_input_tokens":5760,
 "cache_creation_input_tokens":0,"server_tool_use":{"web_search_requests":0}},
 "errors":["Reached the maximum number of turns"],
 "session_id":"01a04bc9-5df9-7752-9038-386346ab4b04",
 "uuid":"0749d172-e862-401f-afd5-f80f8022bc7f"}
```

Exit code 1. So the exemption in [#72](https://github.com/aenawi/uhp-go/issues/72)
survives: `grok` self-enforces *and* reports, in a dedicated field rather than in
prose that would have to be pattern-matched.

**It reports as an error, and that is the load-bearing detail.**
`parseGrokLine` reads `is_error` off this line and hands it to `harnessFailure`,
so a `--max-turns` stop would today reach a client as `failed`. Lifecycle §3
requires `incomplete` for a budget and forbids it for an error. Whatever lands
`max_step` must read `subtype` **before** `is_error`. Nothing sends `--max-turns`
yet, so this is a design input for #72 rather than a live defect, and the mapping
belongs there along with the rest of the field.

Two smaller facts, both from lines the parser ignores today:

- **`num_turns` is on the `result` line** — `1` here, matching the flag. The
  ceiling actually applied is readable from the stream, if `metadata.max_step`
  ever wants to report the number enforced rather than the number requested.
- **`stop_reason: "cancelled"`** — the same word the cancel path uses. It is not
  a discriminator: reading it instead of `subtype` would make a client cancel and
  a budget stop indistinguishable, which is the exact confusion `UpdateIncomplete`
  exists to avoid.

`TestGrokReportsItsOwnMaxTurnsStop` pins the subtype against the success fixture
in `cli_test.go`, so a future `grok` release that collapses the two fails the
build rather than silently reintroducing the risk.

## The run that could not act

`codex-read-only.jsonl` is the same prompt on the same `codex` 0.150.1, minutes
earlier, under the invocation `uhpd` sent before
[ADR-0008](../../../../docs/adr/0008-an-agent-may-write-in-the-directory-it-was-given.md).
No `--sandbox` argument, so `codex` defaulted to a read-only workspace and
refused every write. **Zero files created**, against the five the counted capture
produced from the identical prompt.

It is kept because a detector proven only against a refused run is a detector
nobody has shown to be safe. The pair is the fixture for
[#89](https://github.com/aenawi/uhp-go/issues/89): one run that must be reported
`failed` and one that must not, differing in the argument this server now sends.

The whole of what the refusal said is in `codex-read-only.stderr`:

```text
ERROR codex_core::tools::router: error=patch rejected:
writing is blocked by read-only sandbox; rejected by user approval settings
```

**And that is the whole of it.** The capture's stdout carries no item for the
rejected patch — not an `item.started`, not a failed `item.completed`, nothing —
ends on `turn.completed` rather than `turn.failed`, and the process exits `0`.
So a task in which nothing could be written was indistinguishable on stdout from
one that succeeded, and reached the client as `completed` with the agent's
apology as its answer. `codexWatch` in `codex.go` is what reads the stderr line;
`codex_refusal_test.go` replays both captures through it.

The two captures also disagree about tool narration, which is why this section is
not simply the old "Not here" note with a newer date. The refused run narrates
**one** tool call — a shell command that listed the missing files and succeeded —
so the run was not one in which nothing worked, only one in which nothing was
written. Any rule reading "some tool succeeded" as recovery would clear it.

### Two more measurements, and the limits they set

Both taken 2026-08-29 on the same 0.150.1, because the detector's shape depends on
them and neither is guessable.

**A refused *call* does not read like a refused *run*.** A `workspace-write` run
asked to write one file outside its directory and one inside was refused the first
and wrote the second, and finished:

```text
ERROR codex_core::tools::router: error=patch rejected:
writing outside of the project; rejected by user approval settings
```

Same target, same level, same six opening words as the read-only refusal, and the
opposite meaning. That pair is why the match is on the span where they diverge and
not on the target or on the word "rejected" — and it is pinned by
`TestCodexPerCallRefusalIsNotARunFailure`.

**A read-only run that only tries the shell leaves no trace at all.** Asked to
create a file with `printf` rather than an editing tool, `codex` had both attempts
blocked and created nothing — while emitting **no `ERROR` line and no
`command_execution` item**. The refusal survives only in the agent's own prose
("Both shell commands were blocked because the folder is read-only"), which is not
a signal this server will match. So the reporting fix covers the write route that
logs and not this one; what removes the cause for both is
[ADR-0008](../../../../docs/adr/0008-an-agent-may-write-in-the-directory-it-was-given.md).

## Not here

**`pi`** could not be captured. The only provider with credentials on the capture
machine was `groq`, whose on-demand tier caps at 8,000 tokens per minute against
a request of 71,166. The run never reached a tool call, so these captures say
nothing about `pi` either way — a missing API key, not a fact about the base.

## Redactions

`claude`'s `system` events carry the machine the capture was taken on: absolute
home paths, the installed plugin/skill/agent inventory, memory and socket
locations. Those keys are replaced with a placeholder. `partial_json` is also
replaced, because it streams a tool's arguments a fragment at a time and a path
inside it survives any whole-string substitution. The run's working directory is
rewritten to `/workspace`.

`grok`'s `system`/`init` line carries the same kind of thing under different
names — `tools`, `slash_commands`, `skills` and `mcp_servers` are the capture
machine's inventory, not facts about `grok` — and those four keys are replaced
with the same placeholder. Its `cwd` and every absolute path in a `thinking` or
`tool_result` body are rewritten to `/workspace`, and its `partial_json` is
replaced for the reason above.

`codex`'s two captures carry the probe's own temporary directory in every
`file_change` path, which is a fact about the capture machine and not about
`codex`. That prefix is rewritten to `/workspace`; nothing else in either file
was changed, and the `.stderr` is byte for byte what `codex` wrote.

Nothing any reading depends on was touched: a call is marked by the assistant
message carrying the `tool_use` block, never by the deltas that fill it in, and
the terminal `result` event is verbatim. The `system` line is the only one
re-serialised, because it is the only one with a key removed; every other line is
byte for byte what the CLI wrote apart from the substituted spans.
