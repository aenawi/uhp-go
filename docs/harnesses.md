# Harnesses

Configuring a harness over HTTP, what actually reaches the agent, and what to check before you add a sixth one.

## Harness management

A harness is configuration: a name, a base runtime, a default model, a standing prompt, and
the skills, MCP servers and tool restrictions its agent runs with. UHP class `full` expects
that configuration to be created over HTTP rather than compiled in.

**Set `UHP_HARNESS_STORE`,** or a `UHP_WORKSPACE` that implies it. Harness management is
always offered, but only durable when one of those is set; with neither, `uhpd` keeps
created harnesses in memory and says so on startup:

```
{"level":"WARN","msg":"harness store not configured; created harnesses will not survive a restart"}
```

That warning is the whole of the notice a client gets, because nothing in the discovery
document can express "this works until the next deploy". A harness a client created,
stored the id of, and came back to after a restart is not configuration if it is gone —
so set the path anywhere you intend the ids to keep resolving. `UHP_WORKSPACE` sets this and
`UHP_DB` together, which is the reason to reach for it rather than for either alone.

```bash
curl -s http://localhost:8080/v1/harnesses   -H "Authorization: Bearer devkey" -H "Content-Type: application/json"   -d '{"name":"Research agent","base":"claude-code","default_model":"claude-sonnet-5"}'
```

A base this server cannot run is refused at configuration time:

```json
{"error":{"type":"invalid_request_error","code":"unsupported_base",
  "message":"this server cannot run harness base hermes","param":null,
  "detail":{"supported":["claude-code","codex","grok-cli","opencode","pi"]}}}
```

That refusal is the point of the endpoint, not an edge case. Accepting a base and
discovering it cannot run at the first task fails after the client has already committed
to it, and `detail.supported` is what lets a client recover without guessing.

Three more rules the endpoints enforce rather than assume:

- **`id`, `base` and `createdAt` are immutable.** `PUT` replaces the mutable configuration
  and refuses a body that names a different base with `422`, because changing a base would
  silently change the behaviour of every session already attached to the harness. The
  update verb is `PUT`, not `PATCH`: §5.2 defines a replacement and the conformance suite
  sends one, so `PATCH` answers `405` rather than quietly clearing the fields a
  merge-minded client left out.
- **A skill is a folder and must carry a `SKILL.md`,** rejected at configuration time
  rather than ignored at run time. A member whose path escapes its own folder is refused
  for the same reason, as is a `content_b64` that is not valid base64.
- **An MCP credential is never returned.** `auth` is stored and used, never serialized
  back; a `PUT` that omits it — which is all a client can do, having never been given it —
  carries the stored one forward instead of dropping it.
- **MCP is refused on a base that cannot deliver it.** §4.1 forbids advertising MCP
  support a server cannot provide, so a harness carrying MCP servers on a runtime with no
  per-run mechanism is a `422` rather than a task that quietly runs without them.

Deleting a harness leaves its sessions and responses alone: history that disappears when
configuration changes cannot be audited. The harnesses this server is started with are not
managed over the API, and trying to change or delete one is a `409` rather than a silent
no-op.

### What reaches the agent

A harness's configuration is delivered, not just stored. Before each task the router
writes the enabled skill folders to `<session>/.uhp/skills/<name>/` — the whole folder,
because materialising only `SKILL.md` breaks every skill carrying references, scripts or
data — and the enabled MCP servers to `<session>/.uhp/mcp.json`, with `auth` materialised
as the `Authorization` header that actually connects. A disabled entry of either kind is
never written at all: §4.1 requires that a disabled server not be contacted, and
"connected then hidden" still tells its operator the turn happened.

Leaving it out of the generated document is necessary and not sufficient, because a
runtime can have MCP configuration of its own. Claude Code does, so its runs also carry
`--strict-mcp-config`, which confines them to the file this server wrote — see the table
below.

How much of that the runtime enforces itself differs per base, and this server does not
overstate it:

| Base | Tool block | Skill loading | Per-run MCP | System prompt |
|---|---|---|---|---|
| `claude-code` | `--disallowedTools` (executed) | standing instruction | `--mcp-config` (executed) | prompt text — by decision |
| `grok-cli` | `--disallowed-tools` (verified) | standing instruction | none — refused at config time | prompt text — by decision |
| `pi` | `--exclude-tools` (verified) | `--skill` (verified) | none — refused at config time | prompt text — by decision |
| `codex`, `opencode` | standing instruction | standing instruction | none — refused at config time | prompt text — no flag exists |

The last column is the one that is uniform, and deliberately. `claude-code`
(`--append-system-prompt`), `grok-cli` (`--rules`) and `pi` (`--append-system-prompt`) each
ship a flag that would carry the standing block as a native system prompt; none is wired.
The composed prompt is the `Task.Input` that `GET /v1/sessions/{id}/turns` reports, so it is
also the only record of what a run actually ran under — a block moved into argv either
reaches the model twice or disappears from that record. See
[ADR-0010](adr/0010-instructions-reach-the-agent-as-prompt-text.md); issue #79 is closed
against it rather than open as a gap.

"Verified" means the flag was read from that CLI's own `--help` on a machine where it is
installed — re-read for `grok-cli` on 1.0.5 (2026-08-23, issue #34), where it is still
spelled `--disallowed-tools`. The nearby `--deny <RULE>` now carries `--disallowedTools`
as a compat alias, which is a different flag with a different grammar rather than a rename
of this one, so reading only the alias list would have moved the harness onto the wrong
one.

"Executed" is the stronger claim and the only one that settles issue #19: the flag was not
read but run, and the run was watched from the far end. `make probe-claude-delivery` does
that, and a flag the CLI accepts and ignores fails it:

- `--disallowedTools Bash` removes `Bash` from the session's tool list, so the model is
  never offered it. The same run without the flag used `Bash` and returned its output.
  The list is comma-joined rather than space-separated — `--help` allows either, but the
  flag is variadic, so a space-separated list would spread across argv elements.
- `--mcp-config` reaches the server: the generated document's `Authorization` header
  arrives on every request, `tools/call` is served, and the model returns a secret only
  that server knows.
- A server the generated document does *not* name is never contacted — but only because
  `--strict-mcp-config` is also sent, unconditionally, on every run. `--mcp-config` adds
  a configuration rather than replacing the set: without the second flag, Claude Code
  also connects the host's own MCP servers, and a server the operator disabled is
  contacted anyway. The probe demonstrates both directions.

All of that is a maintainer's command rather than a settled fact. `go test` cannot reach a
logged-in CLI, so nothing in CI re-runs it, and a Claude Code release is free to change any
of it — which is the whole reason these two claims went three issues without being checked.
Run the probe after every upgrade.

Where a runtime cannot hard-block a tool, the restriction is conveyed as a standing
instruction and described to the model as unenforced — never dropped. §4.3 is explicit
that dropping it is the worst outcome: the operator believes a tool is off, and it is not.

## Extending with a new harness

1. Create `internal/harness/<name>.go` returning a `*CLIHarness` literal: id, binary,
   models, capabilities, `Prompt` mode, `BuildArgs`, `ParseLine`.
2. Add it to the slice in `cmd/uhpd/main.go`.
3. Add a `BuildArgs` case to the table in `internal/harness/cli_test.go`.

**Verify the CLI by running it.** `Prompt: PromptStdin` is correct only if the CLI
actually reads a prompt from stdin — grok does not, and a `--` terminator that works for
claude and codex does not work for grok or pi. Every one of those facts was established
by executing the CLI, and none of them is guessable from its `--help`.

**Check that the agent can write.** Ask a new harness to create a file and look on disk,
because "it defaults to something sensible" is not a default any two CLIs share. `codex`
defaults to a read-only workspace and refused every write for as long as nobody looked, and
it did so while reporting the run `completed` — issue #89, and
[ADR-0008](adr/0008-an-agent-may-write-in-the-directory-it-was-given.md) is the policy
a sixth harness inherits: write access to the session's working directory, granted by
whatever argument that runtime needs, or none if it needs none.

**The `Capabilities` list is enforced, so declare only what the harness delivers.** Listing
`sessions` on a harness that cannot resume turns every continuation into a silent fresh
conversation. `cancellation` needs no declaring: `Build` adds it, because the shared runner
delivers it for every harness.

`TestAdvertisedSessionsReachArgv` catches half of the `sessions` mistake — it fails if a
harness advertising `sessions` builds the same argv with and without a native session id.
The other half is not mechanically checkable: the id has to be *discovered* from the CLI's
own output by `ParseLine` before there is anything to pass back, and a `passthroughParseLine`
harness can never produce one. `opencode` is the worked example, and it is worked in both
directions: it once carried the flag without the parser, so every continuation silently
started a new conversation, and issue #13 restored the two halves together — `--format json`
so the CLI prints its `sessionID`, `parseOpenCodeLine` to read it, then the capability.
Check both halves by hand before you claim `sessions`.

`pi` is the other direction of the same mistake, and the more tempting one: it withheld
`sessions` it could deliver. The id was on the wire but never read, because nobody had run
`--session-id` against the binary — so the capability was declined on the strength of not
having checked. Issue #33 checked, with `scripts/probe-pi-session.py`, and it resumes.
**Withholding a capability is not the safe default it looks like.** It is a wrong answer in
the other direction, and unlike an over-claim nothing fails to make it visible.

**`streaming` has the same two halves, and the second one is easy to miss.** Several of
these CLIs default to an output mode that prints nothing until the run is over, so an
invocation can be perfectly correct and still buffer: `pi -p` writes the finished answer
after its own `session.prompt()` resolves, `claude -p --output-format stream-json` emits
one event per *completed* assistant message, and `grok --output-format
streaming-messages-json` does the same until `--include-partial-messages` is added — the
same prompt without it produced three lines and no delta at all. All three needed a flag
before the capability they already advertised was true. Read the CLI's own streaming mode,
not just its exit code, and note that whichever text the incremental mode gives you is
usually repeated whole at the end, so `ParseLine` must read one or the other and never
both.

**Two messages in one run need a separator, and no CLI supplies one.** A run that
interleaves prose with tool calls answers in several pieces — opencode as text parts, codex
as `agent_message` items, grok as messages between `message_stop` events — and every delta
is appended into a single `output_text`, so passing the pieces through unchanged runs
"Alpha" and "Gamma" together as "AlphaGamma". Three adapters add a newline at their own
boundary: `opencode` and `codex` on the text, because their pieces are whole messages, and
`grok-cli` on `message_stop`, because its deltas are token-level and a newline per delta
would break every word apart.

`claude-code` and `pi` are the outstanding cases, and this is the honest state rather than
a claim they are fine. Both stream token-level deltas with no separator at any boundary, so
both will run two messages together the same way — `pi` says so where it declines one, and
`claude-code` does not. Neither was probed for it in issue #34, whose scope was codex and
grok, and neither should be assumed correct because it is not listed above.

**Usage is a run total, and several CLIs also publish a per-message one under the same
field names.** opencode's `step_finish`, grok's `message_delta` and codex's `turn.completed`
all carry `input_tokens`; only the last is the whole run's. Usage is applied
last-write-wins, so reading the per-message event publishes the final message's accounting
as the task's — grok's captured tool run reported 166 input tokens on its second message
against a real total of 19,838. UHP permits usage to be null; it does not permit it to be
wrong.
