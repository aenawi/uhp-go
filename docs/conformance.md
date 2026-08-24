# Conformance

UHP conformance is defined by a runnable suite, not by self-assessment. This file records
what this server scores, how to reproduce it, and what the remaining gap is.

## Current result

```
52/52 full · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (full)
```

Measured 2026-08-23 (issue #42) against suite `2026.8.11.post1`, pinned at
harnessrouter revision `95b96d7ce473ab59d510e1690c73cc6660d0a73e`, with
`--harness-id` naming the `claude-code` harness and `--model claude-sonnet-5`. The report
reads `"highest_class_passed": "full"`.

Per class, from three separate runs of the same server: **37/37 core**, **44/45 extended**,
**52/52 full**.

**That score was conditional on `--model`, and a fourth run said so.** `make
conformance-gate` runs the same suite against the same server without naming a model, and
scored **36/37 core** — T-03 failed, because a task that named no model came back naming
none either. The run was fine; the response was silent about what served it — issue #43,
and a real defect rather than a suite artefact. **The three runs above did not find it
because all three pinned a model**, which is the configuration that never exercises the
default path.

Both numbers were true of this server at the time and neither replaced the other. The one to
quote depended on the question: 52/52 was what a client that pins its models got, and 36/37
was what one that did not. The gate defends the second, deliberately.

**#43 is fixed and the gate has now been re-run — the two configurations agree.**

```
37/37 core · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (core)
```

Measured 2026-08-24 by `make conformance-gate` against the same pinned suite and the same
`claude-code` harness, with no `--model`. T-03 passed reading `model=claude-opus-5[1m]` — a
task that named no model came back naming the one that served it. The 36/37 split above is
closed; the model-less core score is now the same 37/37 the pinned runs reported.

**What the fix does, and where it still guesses.** A task that names no model is created
carrying the harness's effective default, and three of the five adapters — `claude-code`,
`grok-cli` and `pi` — replace that default mid-run with the model their own output names, so
the answer is read rather than guessed wherever it can be read. No captured line of `codex`
or `opencode` names a model, so those two keep the guess and say so in a comment where the
parser would otherwise have read it — with a test over their fixtures that goes red if a
later capture proves them wrong.

**T-03 measures presence, not truth, and this run measured one adapter of five.** The check
asserts `model` is non-empty and that a substitution sets `metadata.model_fallback`; it has
no independent way to learn what actually ran, so a wrong-but-populated `model` passes it.
The old failure was an *absent* field, which is why fixing #43 moves the check. The gate
names `claude-code` — one of the three that read the value back — so what passed here is the
reading path. `codex` and `opencode` would pass T-03 on their guess being populated, not on
its being right, and only their fixture tests speak to that. Read this row as "the field is
now always there", not "the field is now always correct".

**The previous result — 37/37 core, 42/52 overall — predated three bodies of work**, and
this run is the first to measure them: input items, artifact capture and download (#2),
harness create/replace/delete (#3), and skills, MCP and tool restrictions (#4). All ten
checks they targeted now pass. The gap that file recorded is closed.

**The one skip is worth more than the score.** The `extended` run skipped X-07 — *"this
session produced no artifacts to download"* — and the `full` run passed it, 11 bytes of
`text/plain`. Same server, same harness, same model, ninety seconds apart. X-06 and X-07
only test something if the agent actually writes a file, and whether it does is the model's
choice on the day, so these two checks are non-deterministic by construction. **A run that
reports 52/52 with X-07 skipped is not a 52/52.** Read `skipped_not_verified` in the JSON
report, not just the summary line; the suite says so itself.

`conformance_class` in the discovery document still reads `core`, and raising it to `full`
is a deliberate follow-up rather than part of recording this. The class is what the server
*guarantees*, and one green run on one machine with one CLI installed is thinner evidence
than a guarantee wants — particularly for the F-series, which measures the API surface on a
box where `claude` happens to be logged in. Lifecycle §2 constrains the class only through
`files_input`, `files_output` and `session_listing`, all of which report `true`.

## Reproducing it

The suite lives in the protocol repository and runs against any server over HTTP.

```bash
git clone https://github.com/HarnessRouter/harnessrouter
pip install -e harnessrouter/protocol/conformance

go build -o bin/uhpd ./cmd/uhpd
UHP_API_KEYS=devkey UHP_WORKSPACE=/tmp/uhp-workspace ./bin/uhpd &

# The suite runs real agent tasks, so pick a harness whose CLI is installed and
# authenticated on this machine, and pass its canonical chrn_ id.
curl -s -H "Authorization: Bearer devkey" localhost:8080/v1/harnesses \
  | python3 -c 'import sys,json; [print(h["base"], h["id"], h["status"]) for h in json.load(sys.stdin)["harnesses"]]'

uhp-conformance --base-url http://localhost:8080 --api-key devkey \
  --class core --harness-id chrn_… --model … --plain
```

Three things to know before reading a result:

- **Pick a harness that advertises what the checks exercise.** The `capabilities` list on a
  harness object is now enforced rather than reported: a `previous_response_id` sent to a
  harness without `sessions` is refused `422`, as is a cancel sent to one without
  `cancellation`. All five bases here now advertise `sessions` — `pi` since issue #33 and
  `grok-cli` since issue #34 — so the session and continuation checks are exercisable
  against any of them. Before #34 a run pointed at `grok-cli` failed them by design.

- **The suite runs real agent tasks** — about six of them, costing real tokens and a few
  minutes. That is deliberate: a stream that never flushes, a cancellation that never
  terminates, and an artifact that cannot be downloaded are all invisible to anything that
  only inspects a schema.
- **A skip is never a pass.** The suite reports skips separately and so does this file.

## The gate

The gate runs on a maintainer's machine, not in CI, and that is a deliberate choice rather
than a missing feature.

`make conformance-gate` starts nothing by itself: it points the suite at a `uhpd` you are
already running and refuses a result worse than the one recorded above. What it refuses is
in `scripts/check-conformance.py`, which reads the JSON report rather than the suite's exit
code — because that code is zero when checks are skipped. It rejects three things: a failure
or error, *any* skip, and a pass count below `CONFORMANCE_FLOOR` in the Makefile. The last
one is what stops the denominator shrinking quietly — "0 failed" over 12 checks is not the
result that "0 failed" over 37 checks was.

**Why it is not a CI job.** It was one, briefly. The suite runs about six real agent tasks
against a real CLI, which costs real tokens and minutes, and two things follow from that:

- *GitHub withholds secrets from a fork's pull request.* A remote gate could therefore never
  have measured a contribution — it would have skipped every one of them. Running it locally
  gives up nothing that was working.
- *A gate that bills the maintainer for every push is a gate they switch off.* Free
  compilation, tests and an image build stay in CI, where they cost nothing and catch what a
  developer's own machine cannot: a Linux build, and `-race` on a different scheduler.

**What this costs, stated plainly.** A local gate is not enforcement. `git push --no-verify`
skips the hooks, nothing in GitHub can prove the suite was run, and a maintainer who forgets
leaves the score undefended without anything turning red. The score above is held up by a
person following the procedure below — not by a build. That is weaker than a CI gate, and
the honest thing is to say so here rather than let this file imply a machine is checking.

**It measures `claude-code`, and that is a decision rather than a default** (issue #14).
Three constraints pick it between them:

- *It has to advertise what the core checks exercise.* `capabilities` is enforced, so a
  harness without `sessions` fails `C-01` and `C-02` by design and the gate would be red
  from the day it was written. `pi` was in that group until issue #33 verified its resume,
  and `grok-cli` until issue #34 verified its own; on the day this was written both were
  out, which is what settled the choice.
- *It has to actually stream.* A gate for `S-09` driven by a harness that buffers measures
  nothing about streaming. `claude-code` streams token-level content blocks, but only with
  `--include-partial-messages`, which is why the invocation grew that flag.
- *It has to be installed.* The image already ships `@anthropic-ai/claude-code`, and its CLI
  reads `ANTHROPIC_API_KEY` from the environment without further configuration — which is
  the same variable your own shell already has if you use that CLI.

### Running it

Pin the suite to the revision the recorded score was taken against. It is another project's
repository, and a score that moves because somebody else pushed is a score nobody can
reproduce.

```bash
git clone --filter=blob:none https://github.com/HarnessRouter/harnessrouter suite
git -C suite checkout --quiet 95b96d7ce473ab59d510e1690c73cc6660d0a73e
pip install -e suite/protocol/conformance
```

Then start a server with a workspace — without one, `files_input`, `files_output` and
`harness_management` are honestly reported `false` and the checks behind them are refused
rather than measured — and point the gate at the `claude-code` harness:

```bash
go build -o bin/uhpd ./cmd/uhpd
UHP_API_KEYS=devkey UHP_WORKSPACE=/tmp/uhp-workspace ./bin/uhpd &

# Ask which id it serves rather than writing a derived value down by hand.
pick='import sys,json; print(next((h["id"] for h in json.load(sys.stdin)["harnesses"] if h["base"] == "claude-code"), ""))'
curl -sf -H 'Authorization: Bearer devkey' localhost:8080/v1/harnesses | python3 -c "$pick"

UHP_API_KEY=devkey UHP_HARNESS_ID=chrn_… make conformance-gate
```

`ANTHROPIC_API_KEY` is read from your environment by the `claude` CLI that `uhpd` spawns.
`process.run` never sets `cmd.Env`, so the CLI inherits the environment `uhpd` was started
in — exporting it beside the *suite* would authenticate nothing, because the suite only ever
speaks HTTP.

### When to run it

Before merging anything that could move the score: a change to the transport, the event
model, the supervisor, a harness runtime, or the discovery document. Not on every commit,
and not from a hook — six agent tasks and a few minutes is a cost you should choose to pay.

**A contribution is a maintainer's run, not the contributor's.** CI checks that a pull
request compiles, passes tests and builds an image. It does not and cannot run the suite:
GitHub gives a fork no secrets, and asking a contributor to spend their own tokens to prove
your score is not a reasonable thing to ask. So when a pull request touches anything on that
list, check the branch out locally, run the gate, and record the result in the pull request
before merging.

`CONFORMANCE_FLOOR` is the enforced copy of the score, and the score also appears in prose
here and in the README. Nothing links them mechanically, so a run that raises the score has
to raise the floor too — otherwise the number this file claims and the number the gate
defends drift apart, with the lower one silently winning.

### The free checks that did stay in CI

`.github/workflows/ci.yml` still runs `go build`, `go vet`, `make fmt-check`,
`go test ./... -race -cover` and `make docker-check` on every push and pull request, because
those cost nothing and catch what a developer's machine does not: a Linux build, and the
race detector on a different scheduler.

The same four commands run locally as a `pre-push` hook, so the round trip to a red build is
usually avoided rather than waited on:

```bash
make hooks   # git config core.hooksPath .githooks
```

The hook deliberately excludes the conformance suite. It takes about twenty seconds; a hook
that took minutes and spent tokens is one people pass `--no-verify` to, and a hook that is
routinely bypassed checks nothing.

### What S-09 does and does not defend

`S-09` is the check issue #14 was opened about, and measuring it properly changed what can
honestly be claimed for it.

The check reads the arrival times of a stream's events and fails one whose first and last
arrive within 50 ms of each other. **It cannot fail this server**, because `supervise`
publishes `response.created` the instant a run starts — so the spread it measures is the
whole duration of the task regardless of what happens in between.

Measured against `grok-cli` on 2026-08-21. Two suite runs passed it, reporting *"events
spread over 17.3s"* and *"events spread over 8.4s"*. A third stream from the same harness,
timed by hand at the socket rather than by the suite, shows what those numbers are made of
— 17 events, of which 16 arrive in the last second:

```text
 0.00s  response.created
 8.09s  response.output_item.added      ← nothing at all for eight seconds
 8.09s  response.output_text.delta      "1\n"
   …    (ten deltas over 0.3s)
 8.95s  response.completed
```

`S-09`'s rule applied to that stream gives a spread of 8.95s and a pass, on a stream that
was silent for ninety per cent of the run — the state Streaming §1 calls indistinguishable
from a hang. The suite is not wrong to measure what it measures; it is measuring the only
thing a single connection can see without knowing when the harness had something to say.

That trace is a record of an invocation this server no longer sends. Issue #34 moved
`grok-cli` onto `--output-format streaming-messages-json --include-partial-messages`, where
the answer arrives as token-level deltas during the run rather than as one block at the end,
so the same task streamed today would not look like the above. **It is kept because the
point survives the fix**: `S-09` passed that stream, and would pass the next harness that
buffers. The check's blindness is a property of what a single connection can see, not of
which CLI happened to demonstrate it.

So the gate defends the *score*, and this repository's own test defends *progressive
delivery*: `TestADeltaReachesTheClientBeforeTheRunEnds` in
`internal/transport/http/sse_test.go` opens a stream against a harness that speaks once and
then keeps working, and asserts the delta arrives while the run is still in flight. Deleting
the per-event `flusher.Flush()` turns it red, which is the property `S-09` is aiming at and
cannot reach.

## History

| Point | Score | What moved it |
|---|---|---|
| Baseline | 3/52 (16 fail, 33 skip) | — |
| Publish blockers, ceremony removal, data-driven harnesses | 3/52 | no wire change by design |
| Supervisor: task lifetime decoupled from request lifetime | 5/52 | A-02, E-03 |
| Event model and sequencer | 5/52 | correct but still unmeasured |
| Contract fixes | **38/52, core 37/37** | see below |
| Session listing, inspection and turns | **42/52** | X-01, X-02, X-03, X-04 |
| Idempotency keys | no change expected | no check sends one — see below |
| Files: input items, artifact capture, download | measured at last | X-05, X-06, X-07, and X-08 made real |
| Harness management: create, replace, delete | measured at last | F-01, F-02, F-05, F-07 |
| Skills as folders, MCP config, tool restrictions | measured at last | F-03, F-04, F-06 |
| Stream keep-alives | no change expected | no check watches a silent stream — see below |
| Harness event feed and `Last-Event-ID` resumption | no change expected | no check reconnects a stream — see below |
| Progressive streaming for `claude-code` and `pi`, CI gate | no change expected | S-09 cannot fail this server either way — see above |
| Re-measure after three unmeasured landings (#42) | **52/52, full 52/52** | the ten checks above, all at once |
| Same run, no `--model` (the gate's configuration) | 36/37 core | T-03 regressed into view — see #43 |
| A model-less task reports the model that ran (#43) | **37/37 core, model-less** | T-03, re-measured 2026-08-24 — the gate's split with the pinned runs is closed |

The #42 re-measure is the largest single jump in this table, and none of it was new work. Ten
checks moved because the code that satisfies them had been sitting there unmeasured since
issues #2, #3 and #4 — three steps that each ended with "not yet measured" in this table
and stayed that way. The lesson is the one #34 arrived at from the other direction: an
unverified claim and an unmeasured implementation fail the same way, silently, and the only
difference is which of them you find out about first.

The 33 skips in the baseline were not 33 separate defects. They cascaded from one line:
`GET /v1/harnesses` returned `{"data": […]}` where the suite reads `harnesses`, so it could
not pick a harness and every task, stream, session and cancellation check skipped untested.
Fixing that one envelope is what made three steps of prior work measurable.

## The remaining gap

No check in the suite is now unmeasured or failing. The table that stood here listed
X-05…X-07, F-01…F-07 as "implemented, unmeasured" against issues #2, #3 and #4; the
2026-08-23 run measured all ten and all ten pass. What follows is what a green suite still
does not tell you, which is the part worth reading.

**X-06 and X-07 are conditional on the agent's behaviour, not the server's.** They test
something only if the harness writes a file, which the suite arranges by asking it to and
the model is free to decline. The same server scored a skip on X-07 in one run and a pass
ninety seconds later. Neither run says anything different about this server; the JSON
report's `skipped_not_verified` is where that distinction lives, not the summary line.

**What is genuinely weaker than the checks can see** is how much of a harness's
configuration each runtime *enforces*, as opposed to being asked to honour. The suite
tests that skills, MCP servers and disabled tools round-trip through the API; it does not
test whether the agent was actually prevented from using a tool.

For `claude-code` that gap is now covered outside the suite. `--disallowedTools` and
`--mcp-config` were documented and never executed (issue #19); `make probe-claude-delivery`
executes them and watches the effect — the blocked tool is absent from the session's tool
list, the configured MCP server is contacted as the configured principal, and a server the
generated document does not name is not contacted at all. That last one needed a fix rather
than a check: `--mcp-config` adds a configuration instead of replacing the set, so runs also
carry `--strict-mcp-config`. `grok-cli` and `pi` were read from their own `--help` on a
machine where they are installed, which is the weaker claim. See the delivery table in the
README.

**Nothing in the suite watches a silent stream.** Every streaming check asks a harness to
answer, so events arrive promptly and the gap Errors §5 is about never opens. Stream
keep-alives (issue #6) are therefore held up by this repository's tests alone —
`TestStreamKeepsAliveWhileTheHarnessIsSilent` in `internal/transport/http/sse_test.go` and
the `EventsIdle` tests in `internal/service/supervisor_test.go` — and a green local test is
not a conformance result here either. A check that would catch it has to keep a stream open
across a harness's thinking time and assert bytes moved, which is exactly the kind of thing
a schema inspection cannot see.

**Nothing in the suite reconnects a stream.** Every streaming check opens one stream and
reads it to its terminal event, so neither `GET /v1/harnesses/{id}/events` nor
`Last-Event-ID` resumption (issue #8) is reachable from a run. Both are held up by this
repository's tests alone — `internal/service/feed_test.go` and
`internal/transport/http/harness_events_test.go`. The thing those assert is the thing a
single connection cannot show: that a second subscriber attaching at sequence *n* is handed
exactly the events from *n* onward and none of the ones before it.

**Nothing in the suite sends eight of the thirteen request fields.**
`CreateResponseRequest` has 13 properties in the schema; `createTaskBody` reads five. The 52
checks contain no reference to `max_step`, `timeout_seconds`, `max_output_tokens`,
`instructions` or `include`, so the 52/52 `full` result is silent about every one of them —
and about `tools` and `store` besides. Dropping them is the specified behaviour rather than a
defect: Tasks §1.1 marks all eight optional and requires a server to ignore request fields it
does not understand rather than reject them. So this is not a check that fails, and not a
check that passes; it is a column of the request surface the suite never fills in. Issue #48.

`store` is the one worth naming separately, because the field is not merely unimplemented but
inert: `Task.Store` is hardcoded `true` at `internal/service/task_service.go:358` and the
request's own `store` is never consulted. Tasks §4 says a server MAY answer `404` with
`response_not_found` for a response created with `store: false` — MAY, so retaining it anyway
is permitted, and the `store: true` echoed back is an accurate report of what this server
did. No check sends `store: false`, so nothing has ever asked.

**Nothing in the suite provokes a failed adapter start,** which is how an error object
missing a schema-required field survived to be found by reading the specification rather than
by running anything. Issue #47: `domain.TaskError.Type` carried `omitempty` against a field
the schema lists in `required`, and the adapter-start path set no type at all — so a client
following UHP's fourth client rule, treat an unrecognised `code` as its `type`, was handed
nothing to fall back on. A missing CLI binary is not a condition the 52 checks arrange, and
nothing in this repository validated a marshalled object against the published schema either.
The second half of that is what [#16](https://github.com/aenawi/uhp-go/issues/16) adds: a
local schema test costs nothing, runs on every push, and would have caught it on the first
run.

Nothing is advertised for either in the discovery document. The capability vocabulary is
the specification's and neither feature has a key in it, so a key invented here would be a
private dialect rather than a claim a client could rely on. What is on the wire instead is
an SSE `id:` line per event, which is how a client discovers resumption everywhere else
SSE is used.

**Nothing in the suite sends an `Idempotency-Key`.** The string does not appear in
`uhp_conformance/checks.py` at all, so advertising the capability moves no check and could
not be falsified by a run. Issue #7 is therefore held up by this repository's tests alone —
`internal/service/idempotency_test.go` and the three `Idempotency` tests in
`internal/transport/http/handlers_test.go` — and those assert the thing a response cannot
show: the adapter's own run count. "MUST NOT start a second execution" is invisible from the
wire, because one execution and two executions both return a plausible-looking task; only
the harness knows how many times it was asked to work.

**X-08 used to pass vacuously**: artifact ids could not traverse out of their container
because the download endpoint did not exist, so every probe 404d. The endpoint exists now,
so the check finally means something. Both of the suite's probes are answered with a 404
rather than a redirect — see `TestTraversalProbesAreRefused` — because `net/http` would
otherwise answer `../../etc/passwd` with a 301 to a cleaned path, which is neither a
refusal nor obviously safe to a caller reading status codes.

## Re-measuring the harness checks

Harness management is only offered when there is somewhere durable to keep a harness, so
the suite needs a store — `UHP_HARNESS_STORE`, or the `UHP_WORKSPACE` that implies one.
Without it, discovery reports `harness_management: false` and the endpoints answer `501`,
which is the honest failure rather than a memory-backed pretence.

```bash
UHP_API_KEYS=devkey UHP_WORKSPACE=/tmp/uhp-workspace ./bin/uhpd &
uhp-conformance --base-url http://localhost:8080 --api-key devkey \
  --class full --harness-id chrn_… --model … --plain
```

F-01 discovers a base to build on by POSTing a deliberately bogus one and reading
`error.detail.supported` off the refusal, so that field is load-bearing for the check
that follows it, not decoration on an error. It picks the first entry, which sorts to
`claude-code` — the base whose enforcement this repository has gone furthest to verify, and
therefore the one whose absence is most worth noticing. A run on a machine without that CLI
installed measures the API and not the enforcement, so name the harness explicitly rather
than letting the sort choose.

A workspace is required for more than the file checks now: skill folders are materialised
into the session working directory, so a harness carrying skills refuses to run without
one rather than starting an agent that never receives them.

## Re-measuring the file checks

The file checks need a workspace, or the server honestly reports `files_input` and
`files_output` as `false` and X-05 is refused with a 501:

```bash
UHP_API_KEYS=devkey UHP_WORKSPACE=/tmp/uhp-workspace ./bin/uhpd &
uhp-conformance --base-url http://localhost:8080 --api-key devkey \
  --class extended --harness-id chrn_… --model … --plain
```

X-06 and X-07 only test something if the harness actually writes a file, which the suite
arranges by asking it to. A harness that answers in prose without touching the filesystem
produces an empty artifact listing and a skipped download check, and a skip is not a pass.
