# Step-narration captures

What each base puts on stdout when an agent calls tools, captured by execution
rather than read out of a vendor document. This is the evidence
[#72](https://github.com/aenawi/uhp-go/issues/72) turns on: `max_step` is a
budget on agent steps, and a budget can only be claimed on a base whose output a
step can be counted from.

Captured 2026-08-28. `claude` 2.1.250, `opencode` 1.18.23. Reproduce with
`make probe-steps`.

Every capture here is the **shipped invocation** — exactly `CLIHarness.BuildArgs`
with the prompt on stdin, and no permission, sandbox or approval flag, because
`uhpd` sends none. An earlier pass added `--permission-mode bypassPermissions`,
`--auto` and `--sandbox workspace-write` and passed the prompt as argv; it was
discarded. A probe that measures an invocation this server does not send
measures nothing, which is the standard `TestCodexAndGrokProbesRunTheShippedInvocation`
already holds the other probes to.

## The measurement

Ground truth is on disk, not in the stream. Each run was given one task with a
known number of verifiable side effects:

> Create exactly five files in the current directory, named step1.txt …
> step5.txt. Each file must contain only its own number. Create them one at a
> time, using a separate file-write tool call for each one. Do not use a shell
> loop, and do not create them in a single command. When all five exist, reply
> with the word DONE and nothing else.

Each run here produced exactly five files. A base narrating five tool calls
narrated every call; a base narrating fewer under-counts, which is the one
failure a step budget cannot survive — a caller told it has a ceiling of five
while the agent takes twenty.

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

## Not here

**`codex`** cannot take a tool-call round under the shipped invocation at all.
`uhpd` passes no `--sandbox` flag, so `codex` defaults to read-only and every
write is refused:

```text
ERROR codex_core::tools::router: error=patch rejected:
writing is blocked by read-only sandbox; rejected by user approval settings
```

Zero files created — and the run ended on `turn.completed`, not `turn.failed`,
so a task whose every tool call was denied is reported to the client as
`completed`. That is a defect in its own right, well outside #72, and it is why
there is no `codex` capture here.

**`grok`** enforces natively with `--max-turns` and is not counted by this
server, so it has no capture. What it still owes is a *report*: whether tripping
the flag is distinguishable from an ordinary success in its terminal event. Until
that is measured, `grok` could silently return `completed` on a truncated run.

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

Nothing the counting rule reads was touched: a call is marked by the assistant
message carrying the `tool_use` block, never by the deltas that fill it in. Every
other line is byte for byte what the CLI wrote.
