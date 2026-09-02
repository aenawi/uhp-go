# Conformance

UHP conformance is defined by a runnable suite, not by self-assessment. This file records
what this server scores, how to reproduce it, and what the remaining gap is.

## Current result

```
63/63 full · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (full)
```

Measured 2026-09-01 (issue #107) against suite `2026.8.11.post1`, pinned at harnessrouter
`08d61ea145d6b78c433f6910547c1e7ee293c948`, at class `full` with `--harness-id` naming a
`claude-code` harness and **no `--model`**. Per class: **40/40 core**, **8/8 extended**,
**15/15 full**.

**The pin is a commit rather than a tag on purpose.** Every entry below names a revision, and
a reader will otherwise ask why this one is not a release. `v0.12.1` was cut nine minutes
before harnessrouter#53 merged and a day before harnessrouter#55, so the newest tag contains
neither the specification and schema this run measures against nor the check that moved its
denominator. It becomes a tag when upstream cuts one.

### Why the denominator moved, which is the part worth reading

The suite went from 52 checks to 63, and eight of the eleven are here because this repository
filed the gap:

| Upstream | | Class |
| --- | --- | --- |
| harnessrouter#45 | `R-01`…`R-07`, session sharing — from issue #66, filed upstream as #44 | Full |
| harnessrouter#46 | `T-08`…`T-10`, reserved fields — from issue #86, filed upstream as #42 | Core |
| harnessrouter#53 | `R-01`/`R-02`/`R-06` and `X-04` stop probing and start asserting | — |
| harnessrouter#55 | `R-08`, a bodyless `POST` publishes — the sentence #53 named as unenforced, from issue #103 | Full |

Until the 2026-08-31 run the headline above said `52/52`, which had become the failure mode
this document exists to prevent: the denominator moved and the numerator was copied. A
complete score against a suite that no longer existed read as a complete score against the one
that did — and the copied number flattered us, because seven of the ten checks it was missing
were checks this repository had argued should exist.

**It then happened again in twelve hours, which is the part worth acting on.** `R-08` merged
upstream at 2026-08-31T21:36Z, the same day the `62/62` above it was recorded, and for a day
this file reported a complete score against a suite one check larger — the check being the one
written here, in the pull request this repository opened. Neither miss was a lapse of
attention; both are the same missing mechanism. Nothing in this tree reads the suite's
revision, so the interval between the denominator moving and anyone noticing is however long
it takes a person to look. Issue #107.

### The run before the fix, which is the half that proves the checks work

`X-04` used to stop at "the endpoint answered 200". harnessrouter#53 gave Sessions §3's turn
items a shape, and this server was answering `response_id` where §3 requires `id`. Measured
on this repository's `main` at `cd7ac83`, same suite, same harness, same machine:

```
61/62 passed · 1 failed · 0 skipped · 0 errored
NOT CONFORMANT at class 'full' · highest class fully passed: core

X-04  FAIL  A session's turn history is available, in the specified shape
            does not match schema TurnItem: at (root): 'id' is a required property
```

Then on the branch that fixed it (#101), 62/62 with the same command. Both runs are real:
one server, one CLI, ninety seconds apart, and the only difference between them is the
field name.

That is worth stating plainly because a green run is weak evidence on its own — D-05 passed
vacuously on this server for months, which is what started #66. A check that has been
watched failing on the defect it names, and then passing once the defect was fixed, is a
check that has demonstrated it can tell the two apart.

### One prediction, recorded before either run

Issue #102 predicted `61/62` with `X-04` as the sole failure, reading the merged suite
against this server's source rather than running anything. It was right to the check. That
is not a boast about the prediction — it is the reason the prediction was written down: a
repository that can say in advance what a suite will find has understood the suite, and one
that cannot has only ever read its own score.

## Earlier results

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

**The gate has since been run at class `full`, with session sharing enabled (#57).**

```
52/52 full · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (full)
```

Measured 2026-08-25 by `make conformance-gate CONFORMANCE_FLOOR=52` with `UHP_CLASS=full`,
against the same pinned suite revision and the same `claude-code` harness, on a server
started with `UHP_WORKSPACE`, a harness store and `UHP_SESSION_SHARING=1`. Still no
`--model`: T-03 read `claude-opus-5[1m]` off a task that named none. **Zero skips**, which
is the part worth reading — the 2026-08-23 `extended` run skipped X-07 because the agent
happened not to write a file, and this run passed X-06 and X-07 on a real artifact.

**The gate has since been re-run against a server that answers `full` (#65).**

```
52/52 full · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (full)
```

Measured 2026-08-26 by `make conformance-gate CONFORMANCE_FLOOR=52` with `UHP_CLASS=full`,
against suite `2026.8.11.post1` at harnessrouter revision `856d62c`, on the `claude-code`
harness with no `--model`. What is new is not the number — it is the same 52/52 — but what
the server said about itself while being scored. Before #65 the discovery document answered
`core` no matter how `uhpd` was started, so D-05 graded the claim against the three
unconditional `core` capabilities and could not fail; the check was vacuous on this server.
This run is the first where it graded the claim that was actually made:

```
D-01  pass  conformance_class=full versions=['2026-08-11']
D-05  pass  class=full
```

Two notes for anyone reproducing it. The suite revision is `856d62c` rather than the
`95b96d7` the runs above pin — it was re-read at that revision to confirm the D-05 source,
and the check is unchanged between them. And `claude-code` reported `unavailable` until the
CLI was put on `PATH`: on a machine where `claude` is a shell alias rather than a binary,
`uhpd`'s health check (`exec.LookPath`) cannot find it, and the harness is honestly reported
as unavailable. Start the server with the real binary's directory on `PATH`, or the gate
measures a harness that cannot run.

That was the strongest number this server had recorded until 2026-08-31, and it is worth
being exact about what it did not cover. **None of the 52 checks touches session sharing.** `/share` appears
nowhere in the suite at the pinned revision, so the feature #57 added was carried through
this run without a single check looking at it. What the run proves is that adding it broke
nothing else — a real question, since it changed the `Store` interface, the discovery
document and the routing table — and nothing more than that.

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

**The recorded runs were made with `UHP_API_KEYS=devkey`, which the default configuration
does not produce.** With the variable unset, `withAuth` returns early and every endpoint is
open — Security §1 says "A server MUST authenticate every endpoint except `GET /v1/uhp`",
so the default configuration is not a conformant one and no check can see the difference,
because the suite always passes a key. Issue #55.

**#55 is fixed, and it did not close that gap so much as bound it.** Every number on this
page still describes a keyed server, because that is still the only server the suite knows
how to measure — nothing in the capability vocabulary covers "this server is open", so
inventing a key for it would be a private dialect, and this file plus the README's
[Authentication](../README.md#authentication) section are the obligation instead. What
changed is where the unkeyed configuration can get to: `UHP_ADDR` now defaults to
`127.0.0.1:8080`, an unkeyed server on any other address refuses to start, and one on
loopback logs a `WARN` that it authenticates nothing. So the configuration these runs do
not describe is now one that only the local machine can reach, and its operator has been
told. **Read every score below as "measured with `UHP_API_KEYS=devkey`" — it still is.**

Related, and equally invisible to a run: every configured key is the same principal, so the
scoping MUSTs in Architecture and Files §5 are satisfied vacuously rather than enforced
(#56).

`conformance_class` in the discovery document read `core` when this run was recorded, and
raising it was a deliberate follow-up rather than part of recording it. That follow-up is
#65, and it did not raise the class so much as stop it being a constant: the document now
computes it from the capabilities it publishes. A server started the way this run's was —
workspace, harness store, sharing on — answers `full`; the same binary started bare answers
`core`. See "The class is not a constant" below.

**That has since been measured rather than reasoned about.** The 2026-08-26 run below is the
first recorded score from a server that *claimed* `full` while being graded, and D-05 —
which reads the class off the discovery document, not off `--class` — reported `class=full`
rather than the `class=core` every previous run recorded. Every number above it was taken
from a server whose class was the constant.

The caution that held the class down is still worth reading, because it is about evidence
rather than about code: one green run on one
machine with one CLI installed is thinner evidence than a guarantee wants, particularly for
the F-series, which measures the API surface on a box where `claude` happens to be logged
in.

**The run this page led with until 2026-09-01, kept because it is what a stale denominator
looks like from the inside (#107).**

```
62/62 full · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (full)
```

Measured 2026-08-31 (issue #102) against suite `2026.8.11.post1`, pinned at harnessrouter
`1176d9a5175fa0938d4c2e43ccbbdaab621d8030`, on the same `claude-code` harness with the same
configuration and no `--model`. Per class: **40/40 core**, **8/8 extended**, **14/14 full**.

It was a complete score against the suite as it stood that afternoon, and it was complete for
about twelve hours: `R-08` merged upstream at 21:36Z the same day. **Nothing about this server
changed between that run and the current one** — it passed `R-08` on the first run that
contained it, without a line being edited. What changed was the denominator, which is the
whole reason this file records a suite revision beside every number.

## Reproducing it

The suite lives in the protocol repository and runs against any server over HTTP.

```bash
git clone https://github.com/HarnessRouter/harnessrouter
pip install -e harnessrouter/protocol/conformance

make build   # bin/uhpd and bin/uhpc
UHP_API_KEYS=devkey UHP_WORKSPACE=/tmp/uhp-workspace ./bin/uhpd &

# The suite runs real agent tasks, so pick a harness whose CLI is installed and
# authenticated on this machine, and pass its canonical chrn_ id. The status
# column is the one to read: `unavailable` is a CLI that is not installed or not
# logged in, and every check pointed at it will fail for that reason alone.
UHP_API_KEY=devkey ./bin/uhpc harnesses

uhp-conformance --base-url http://localhost:8080 --api-key devkey \
  --class core --harness-id chrn_… --model … --plain
```

Three things to know before reading a result:

- **Pick a harness that advertises what the checks exercise.** The `capabilities` list on a
  harness object is now enforced rather than reported: a `previous_response_id` sent to a
  harness without `sessions` is refused `422`, as is a cancel sent to one without
  `cancellation`. Every base here now advertises `sessions` — `pi` since issue #33 and
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
git -C suite checkout --quiet 08d61ea145d6b78c433f6910547c1e7ee293c948
pip install -e suite/protocol/conformance
```

Then start a server with a workspace — without one, `files_input`, `files_output` and
`harness_management` are honestly reported `false` and the checks behind them are refused
rather than measured — and point the gate at the `claude-code` harness:

```bash
make build   # bin/uhpd and bin/uhpc
UHP_API_KEYS=devkey UHP_WORKSPACE=/tmp/uhp-workspace ./bin/uhpd &

# Ask which id it serves rather than writing a derived value down by hand, and
# check it is ready before spending six agent tasks finding out it was not.
UHP_API_KEY=devkey ./bin/uhpc harnesses | awk '$2 == "claude-code" { print $1, $3 }'

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
| Audit of the published surface against `routes()` | no change expected | four endpoints found missing, two of them core — #51, #52, #57, #58; no check sends any of them |
| A default harness for a task that names none (#53) | no change expected | no check omits `harness_id` — see below |
| A client, and the first tests to use a socket (#62) | no change expected | the suite never touches the Go types; these never touch the suite |
| Turn items carry `id` (#101), and the score re-measured against a suite grown to 62 (#102) | **62/62, full 62/62** | X-04 stopped probing and started asserting |
| `R-08` lands upstream (harnessrouter#55), from this repository (#107) | **63/63, full 63/63** | the one sentence of §5 the R-series' retry was hiding |

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

**The score below has not been re-measured since #51, #52, #53, #59 and #62 landed.** Those
changes add two endpoints, change the answer to a request the suite always makes (a task
now falls back to a default harness rather than being refused when `harness_id` is absent —
though the suite always sends one, so no check should move), and change
`DELETE /v1/harnesses/{id}` from 204 to 200 with a body, which F-05 exercises. Run
`make conformance-gate` before quoting the number again.

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

**Nothing in the suite runs two credentials.** Every recorded run was made with
`UHP_API_KEYS=devkey` — one key, one caller — so no check has ever presented a second
credential and asked whether it can see the first one's sessions, transcripts or artifacts.
A green run therefore cannot distinguish this server, where every configured key is an
equivalent credential for one principal, from one that enforces scope per credential.

That is a gap in what the suite can see rather than a defect, because this server serves one
principal deliberately: the Architecture requirements to scope every object to its creating
principal and to answer `404` rather than `403` outside a caller's scope are satisfied the
way a rule about "every element" is satisfied by an empty set. The decision, its rejected
alternative, and what it costs are in
[ADR-0006](adr/0006-one-principal-per-server.md); issue #56 is where it was argued. What no
run can tell you is the part worth writing down here — and the converse holds too: a server
that *did* enforce scope would score exactly the same 52/52, so this number is no evidence
either way.

**Four endpoints of the published surface were missing, and no check asked for
any of them.** The 52 checks contain no request to `GET /v1/responses/{id}/input_items`
or `DELETE /v1/responses/{id}` — both Tasks-chapter endpoints of class *core* in
`uhp-2026-08-11.openapi.yaml` — so 37/37 core was measured against a server that had
no route for either. Both landed in issues #51 and #52. `DELETE /v1/traces/{id}` (#58) was
the third and has now landed too — the suite asks for none of the three, so all three were
found by reading. `POST`/`GET /v1/sessions/{id}/share` (#57) was the fourth and has now
landed as well, which closes this list: every endpoint of the published surface has a route.

**Session sharing was the largest entry on this list, and it is no longer on it.** What this
paragraph used to say was that nothing in the 52 checks opened a share, that nothing could
usefully check the part that mattered even if it did — the requirement is that the view be
**read-only** and **revocable**, and both are properties of what the server *refuses* — and
that the refusals were held up by this repository's tests and nothing else.

The middle claim was wrong, and being wrong about it was the useful part. Those refusals are
checkable; nobody had written the checks. They are `R-01`…`R-07`, filed as issue #66, taken
upstream as harnessrouter#44, and merged as harnessrouter#45, and this server passes all
seven. `R-08` makes it eight: the one sentence of §5 that the R-series' own retry was
hiding, named as a known gap by harnessrouter#53, taken up here as issue #103 and merged as
harnessrouter#55. `R-04` alone sends seven write probes at a published link. Sessions §5 is now the
best-measured chapter of this server rather than the least, and the local tests that carried
it alone — `internal/transport/http/share_handlers_test.go` and
`internal/store/share_contract_test.go` — are corroboration rather than the whole case.

Worth keeping from the old paragraph: a suite is not a fact of nature. A green run says
nothing about a property nobody has written a check for, and the distance between "the suite
cannot see this" and "nobody has written it yet" is one issue. Every remaining entry on this
list should be read as a claim of the first kind that might turn out to be the second.

The capability is off by default (`UHP_SESSION_SHARING`), which has a consequence for how
this file's numbers should be read: **a suite run that does not set it is measuring a server
that reports `session_sharing: false`**, and the class question is then open again for the
same reason it was before #57 landed. A `full` claim has to be measured with sharing on.
This is the shape of #43 and #53 a third time — the configuration that exercises the
feature is the one nothing runs by default — and it is written down here rather than
discovered later. The 2026-08-25 run was taken with it on, so the 52/52 it recorded was a
number from a server that was serving shares and simply not a number *about* them. The
2026-09-01 run is a number about them: eight of its sixty-three checks are `R-01`…`R-08`, and
a run made without `UHP_SESSION_SHARING=1` skips every one of them —
which `scripts/check-conformance.py` refuses outright, so the misconfiguration now fails
loudly instead of scoring quietly.

One further thing that run settled, by reading the check rather than by scoring it. D-05
("conformance_class agrees with capabilities") tests the class against a fixed list, and
that list is `streaming, sessions, cancellation, files_input, files_output,
session_listing, harness_management` — **`session_sharing` is not in it.** So the suite
would accept a `full` claim from a server with sharing switched off, and would refuse one
from a server started without `UHP_WORKSPACE`, because that server honestly reports
`files_input: false`.

That is why raising `ConformanceClass` was not a matter of editing a constant. Three of this
server's capabilities are computed from configuration, so a hardcoded `full` would be a
claim `uhpd` contradicts the moment it is started without a workspace — and D-05 would
catch it. A class this server can defend has to be computed from the same booleans the
capability list is.

### The class is not a constant

That is what #65 did. `conformanceClass` in `internal/transport/http/discovery.go` takes the
capability struct — the same value `handleDiscovery` puts on the wire, not the three
configuration booleans behind it — and grades it:

| Files | Session listing | Harness management | Session sharing | Class |
| --- | --- | --- | --- | --- |
| ✓ | ✓ | ✓ | ✓ | `full` |
| ✓ | ✓ | — | any | `extended` |
| — | any | any | any | `core` |

In terms of how `uhpd` is started, that is: nothing → `core`, `UHP_WORKSPACE` → `extended`,
`UHP_WORKSPACE` and `UHP_SESSION_SHARING=1` → `full`. The harness-management column never
moves the class for this binary, because `openHarnessStore` always returns a store — an
in-memory one when no path is configured — so `harness_management` is `true` on every
`uhpd`. It is read rather than assumed anyway: the service is usable without a store, and a
class that assumed the binary's wiring would be describing `cmd/uhpd` rather than the
document.

Two consequences are worth stating outright.

**Session sharing gates `full` here, and D-05 does not require it.** That is the one real
decision in the change, taken towards the stricter reading: Sessions §5 is what makes
sharing a `full` feature — it is the premise #57 was filed on — while the suite's list stops
at `harness_management` (#66). A server that satisfies the stricter rule satisfies the
suite's as well, so requiring sharing cannot be wrong; not requiring it would let a
deployment with sharing switched off claim the class the specification reserves for one that
has it. Anyone reading the class and expecting the suite's answer should read
`conformanceClass`'s comment, which says the same thing at the point of the decision.

**`core` is the floor rather than a branch.** There is no class below it, and its three
requirements — streaming, sessions, cancellation — are unconditionally true on this server.
That is exactly why the old constant was safe: not because it was checked, but because the
only class it claimed happened to need nothing configurable.

The table above is asserted, not described:
`TestConformanceClassIsComputedFromTheCapabilitiesItClaims` in
`internal/transport/http/discovery_test.go` runs all eight configurations through
`GET /v1/uhp` and holds each document to D-05's own rule — every capability the class it
claims requires is `true` in that same document. The case that matters is the bare one,
which must report `core` and must not report `full` merely because the binary contains the
code for all of it.

The trace deletion is worth a note of its own, because nothing in the suite would catch
getting it backwards. `DELETE /v1/traces/{id}` cancels first and `DELETE /v1/responses/{id}`
MUST NOT, and an implementation with the two the wrong way round answers 200 to both. What
separates them is asserted in this repository's tests and nowhere else:
`TestDeletingAResponseDoesNotStopTheRun` and
`TestDeleteSessionCancelsTheRunAndReapsAfterwards`. The second also pins the part that is
invisible from the wire entirely — that the session's working directory is removed once the
run is dead, so "deleted" describes the disk and not only the database.

This is the same lesson as #42 one level out. There, the code existed and no check
measured it; here, the check does not exist either, so a green suite said nothing at
all. Both were found by reading rather than by running — the second by comparing the
OpenAPI's paths against `Server.routes()`, which is a diff a person can do in a minute
and nothing in this repository does automatically.

**A task that named no harness was refused.** Tasks §1.2 requires the opposite — "If
`harness_id` is absent, the server MUST use a default harness and MUST report which one
it used in the response `metadata`" — and this server answered `400 invalid_input`, so
`{"input":"hi"}`, the smallest body the schema permits, did not work. The suite always
passes `--harness-id` and puts it in `metadata` on every task, so no check has ever sent
a request without one. Fixed in #53. This is exactly the shape of #43: the configuration
that would have found the defect is the one nothing ran.

**Seven codes of the published vocabulary cannot be produced by this server.**
`uhp/error.go` publishes the specification's full list because the package models the
protocol; which of those a *given server* can emit is a property of that server, and
this is the table that says which:

| Code | Why it is unreachable here | Reachable if |
|---|---|---|
| `session_expired` | no retention or expiry exists; a session lives until the database is deleted | a retention policy is implemented |
| `insufficient_scope` | one principal, so nothing is ever out of scope | [ADR-0006](adr/0006-one-principal-per-server.md) is superseded and a credential carries a principal |
| `rate_limited` | no rate limiting | a limiter is added |
| `quota_exhausted` | no quota accounting | quotas exist |
| `timeout` | a wall-clock budget is enforced since #54, but a budget is not an error: it produces `status: "incomplete"` with `incomplete_details.reason: "timeout"`, and `error` stays null | an adapter reports an *upstream* timeout it can tell apart from a harness failure |
| `provider_error` | adapter failures are all reported as `harness_error` | adapters distinguish an upstream refusal |
| `preview_failed` | `/pdf` always answers `501 preview_unavailable`; no conversion is attempted | conversion is implemented |

Most of these are not defects — `rate_limited` and `quota_exhausted` are not MUSTs, and
`preview_failed` is the honest answer for a server that does not convert. What was wrong
was that nothing said so, which left a client author writing a `switch` over `uhp.Code*`
with seven arms that never fire and no way to learn it from either the package or these
docs. Issue #61.

**Nothing in the suite sends three of the thirteen request fields** — a count that has since
come down to one, from the other direction.
`CreateResponseRequest` has 13 properties in the schema; `createTaskBody` reads ten. The 52
checks contained no reference to `max_step`, `timeout_seconds`, `max_output_tokens`,
`instructions` or `include`, so the 52/52 `full` result was silent about every one of them —
and about `tools`, `store` and `background` besides. Two of those have since been filled in
by the suite rather than by this server: `T-08`…`T-10` (harnessrouter#46, from issue #86)
send both `tools` and `include` and assert that they are accepted, ignored, and *named* in
`metadata.ignored_fields`. A declined field is now measured as declined, which is the one
outcome this paragraph assumed no check could ever produce. `max_output_tokens` is the last
one nothing sends. Dropping the ones this server does not
read is the specified behaviour rather than a defect: Tasks §1.1 marks all of them optional
and requires a server to ignore request fields it does not understand rather than reject
them. So this is not a check that fails, and not a check that passes; it is a column of the
request surface the suite never fills in. Issue #48.

The count has moved five times and is worth stating precisely, because "eight dropped
fields" is the sentence #48 was filed under and not one of the five movements shows up in the
score:

- **`timeout_seconds` is read and enforced** (#54, #75, #77). See below.
- **`instructions` and `store` are read** (#80). See below.
- **`background` was the eighth field #48's own list never named**, and was split out as #78
  — a correction to the count rather than a field moving.
- **`background` is now read** (#78): `true` answers the POST as soon as the task is
  accepted rather than holding it open. See below.
- **`max_step` is read and enforced** (#72). See below, and note the heading it is under:
  this is the one movement most likely to be mistaken for a score change.

What is left is three: `max_output_tokens`, `tools` and `include` — and none of the three is
undecided. All three are **declined**, which is a decision rather than a gap: no base takes a
sampling parameter and token accounting arrives only once a run is over, the schema describes
the `tools` objects no further than "object" so any meaning would be invented, and `include`
has neither an agreed vocabulary nor any extra content to name.

Two of the three have since stopped being this repository's reading of a silence. Tasks §1.4
marks `tools` and `include` **reserved and ignored** in the specification itself
(harnessrouter#46, from issue #86), for a reason stronger than the one argued here: a UHP
harness executes tools itself and reports them in `output`, so there is no input path for a
tool result and the client-executed loop the field implies cannot be completed by any
conformant server. ADR-0007 reached the right answer from the weaker premise of what five
CLIs happen to support.
[ADR-0007](adr/0007-a-declined-field-is-not-a-pending-one.md)
records all three and the rule they were measured against; the `tools` question went upstream
to the protocol rather than being answered here, as
[HarnessRouter/harnessrouter#42](https://github.com/HarnessRouter/harnessrouter/issues/42).
Nothing is `pending` any more: `max_step` was the last one, and
[ADR-0009](adr/0009-a-step-is-one-tool-call.md) implemented it.

All thirteen are *published*, on `uhp.CreateResponseRequest`, which widens the distance
rather than closing it: a caller can set `MaxStep` in Go, against a server that will ignore
it, and get no error from either side. That is the deliberate consequence recorded in
ADR-0002 — the package models the protocol, not this server — and it is why the gap is
written down here instead of being hidden by a narrower type.

**The three that are still dropped are no longer dropped in silence.** A response now names
them, in `metadata.ignored_fields`, when the request actually sent one:

```json
{ "metadata": { "session_id": "sess_…", "ignored_fields": ["max_output_tokens", "tools"] } }
```

Absent entirely when there is nothing to report. Only fields this server knows and does not
act on — an unrecognised field is ignored without being named, because §1.1's
ignore-don't-reject rule exists so a newer client can talk to an older server and naming
every unknown field would turn that into a stream of warnings about valid protocol. And only
values that ask for something: `null` is a key with no instruction in it. This is an
extension, not protocol — a different conformant server will not emit the key, and a client
must not read its absence as "nothing was dropped". ADR-0004 records the decision; issue #80
records the work. No check in the suite sends any of the three, so none of this shows up in
the score either.

**`background` was the last field that needed no machinery,** and the reason it took its own
issue is that it is not a parameter of the run at all. The other four asked for the work to be
done differently — a bound, a tool list, extra content in the result — and `background` asks
only when this request stops waiting for it. Everything it needed was already here: the run
is detached from the request with `context.WithoutCancel`, a run retains its whole event log,
`GET /v1/responses/{id}` already answers mid-run, and a repeated `Idempotency-Key` already
hands a retry the first request's own run.

So the work was three decisions, recorded in ADR-0005. `background: true` is answered `200`
at acceptance with the response object as it stands — `in_progress`, empty `output`, session
id and resolved budget already in `metadata`. `background` with `stream` streams exactly as
it always did, and the field is honoured rather than dropped, because a stream is a held-open
POST by construction and everything else `background` asks for is already true of one.
`background: true` with `store: false` and no stream is refused `400 invalid_input`: the
record is dropped when the run ends and this request will not be there to receive it, so the
answer would be delivered nowhere. No check in the suite sends the field either way.

**`timeout_seconds` was the first to move,** and it is worth being exact about what moved
and what did not. Two of the thirteen carry a MUST *if honoured* — a server that acts on
`max_step` or `timeout_seconds` must stop at or after the budget, report `incomplete`, and
never report `completed` for truncated work — and #54 took that on for the wall clock only:

- **`timeout_seconds` is read and enforced.** Every task runs under a budget resolved as the
  shortest of the bounds that are set — the request's, the harness's, and `UHP_TASK_TIMEOUT`
  — so none of the three can be widened by another (#75); the resolved value comes back as
  `metadata.timeout_seconds`; a run that outlives it is stopped, reported
  `incomplete` with `incomplete_details.reason: "timeout"`, terminated on a stream with
  `response.incomplete`, and keeps whatever it had produced. `error` stays null, because
  Lifecycle §3 forbids `incomplete` for errors and reserves `failed` for work that could not
  be done. See [Task budgets](../README.md#task-budgets).
- **`max_step` is now read and enforced too** (#72), on the same terms: the resolved ceiling
  is the shortest of the request's, the harness's and `UHP_TASK_MAX_STEP`; it comes back as
  `metadata.max_step`; a run that exceeds it is stopped, reported `incomplete` with
  `incomplete_details.reason: "max_step"`, terminated on a stream with `response.incomplete`,
  and keeps whatever it had produced. `error` stays null, for the same Lifecycle §3 reason.
  See [Step budgets](../README.md#step-budgets).

  The split that used to be here — one budget honoured, the other dropped — is closed, and
  the reason it took its own issue is that a step had to be *defined* before it could be
  counted. The schema says "tool-call round" and stops there; what a round is turned out to
  vary between two runs of the same CLI, so a step is one tool call, established per base by
  capture. [ADR-0009](adr/0009-a-step-is-one-tool-call.md) records that and the two bases
  that made it awkward: `opencode`, which narrates a call only once it is over, and `grok`,
  which bounds its own turns and reports its own stop.

**Implementing `max_step` does not move the score.** 52/52 before, 52/52 after. This is worth
stating loudly because it is exactly the kind of change a reader assumes filled in a column:
no check in the suite sends the field, so the suite is as silent about it now as it was when
the field was dropped on the floor. The same is true of `timeout_seconds`. Both are measured
by `internal/service/budget_test.go`, `internal/service/step_budget_test.go`,
`internal/transport/http/budget_test.go` and `internal/transport/http/max_step_test.go`
instead — which is the honest place for them, and not a substitute for checks that do not
exist.

**`instructions` and `store` were the second and third to move** (#80), and neither needed a
decision the protocol had not already made:

- **`instructions` is read and prepended.** A task's own system guidance is appended to the
  harness's standing block and never replaces it, because the standing block is where a tool
  restriction lands when the runtime cannot enforce it (Harnesses §4.3) — so a request able
  to replace it would be a request able to switch off an operator's configuration. It applies
  to the task that sent it and does not carry into the next turn of a session, which is what
  UHP's "for this task only" says. `uhpc run --instructions` had offered the flag since the
  CLI shipped and had done nothing with it.
- **`store` is read and honoured.** It was the field #48 singled out as inert rather than
  merely unimplemented: `domain.Task.Store` existed, was hardcoded `true`, and the request's
  own value was never consulted. `store: false` now means the record is kept while the run
  needs it and dropped once the run is terminal — the client is answered in full exactly
  once, in the POST body or the terminal stream event, and every later read of the response,
  its input items, its place in the session's turns and its use as a `previous_response_id`
  is gone. Tasks §4 makes the resulting `404 response_not_found` a MAY, which is what permits
  it. The Session survives, because it owns the working directory and the harness binding and
  carries the harness's own session id; the run's artifacts survive on disk, because `store`
  is about response retention and erasing a run's files is a different thing. The one
  exception is an `Idempotency-Key` retry, which Tasks §6 requires be given the first
  request's answer — so it is, from the run's own copy, because a `404` there would make the
  replay differ from the original.

No check sends `store: false` or an `instructions` string, so nothing in the suite has ever
asked. `internal/service/store_test.go`, `internal/service/instructions_test.go` and their
transport-layer counterparts are what ask.

**The suite was not re-run for #80, and the measurement above still stands.** Saying so is
the point: a reader finding this section rewritten under an older date is owed the reason
rather than left to wonder whether the ledger is stale. Nothing in the 52 checks touches any
of these fields, the class is unchanged, and the suite runs about six real agent tasks at a
real cost — so a re-run would produce the same 52/52 and falsify nothing. That is the same
argument this file makes everywhere else about what a measurement is worth.

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

**The schema test and the suite measure different things, and neither substitutes.**
`uhp/schema_test.go` marshals every public type and validates it against a vendored copy of
`uhp-2026-08-11.schema.json` — the twenty-three objects, plus a check that no object in the
schema is missing a Go type. It is free, deterministic, and part of `go test ./...`, so it
runs on every push where the suite runs on a maintainer's machine and costs real tokens.

What it proves is that the *published types* are the protocol. What it cannot prove is that
this server emits them: it never starts a task, never opens a stream, and would stay green
against a server that returned the right shapes with the wrong contents. The suite is the
only thing that answers that, and it in turn never touches the Go types — nothing in its 52
checks would notice `uhp.Response` drifting from what the handlers write. The overlap between
the two is empty by construction, which is why both exist.

One check does bridge them:
`TestServerStreamDecodesWithThePublishedDecoder` in `internal/transport/http/sse_test.go`
feeds this server's own SSE output to `uhp.EventDecoder`. Framing is the one place a server
and a client written in the same repository can drift silently — each half tested against
bytes its own package wrote — and a suite green on both halves would say nothing about the
join.

**One field left the wire.** A failed task's `error` used to carry `retryable: true`, which
is not in the schema and never appeared on the HTTP error envelope that models the same
object. Collapsing both renderings onto `uhp.Error` removed it (ADR-0002; the shape survives
as `uhpgo.Error` for anyone holding older responses). Nothing measured it — the suite does
not mention it and nothing in this repository read it back — and Errors §4 already makes the
error *type* the retry signal, which that failure sets to `harness_error`. It is recorded
here because "no check noticed" is the reason it was safe to remove, not a reason it did not
happen.

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

## How the score is measured and defended

UHP conformance is defined by a runnable suite, not by self-assessment. The suite lives
in the protocol repository and anyone can run it against this server:

```bash
pip install -e protocol/conformance
uhp-conformance --base-url http://localhost:8080 --api-key "$UHP_API_KEY" --class full
```

**Last measured score: `full` 52/52 — CONFORMANT (UHP 2026-08-11, class full), 0 skipped.**
Details, reproduction steps and what a green suite still cannot see:
[docs/conformance.md](conformance.md).
Re-measured 2026-08-26 by `make conformance-gate` at `UHP_CLASS=full` on a server with a
workspace and `UHP_SESSION_SHARING=1`, with no `--model` pinned. Zero skips, which the runs
before 2026-08-25 could not say — and the first run scored against a server that answered
`conformance_class: full` while being graded, so `D-05` reported `class=full` rather than
the `class=core` every earlier run recorded (#65).

File support (issue #2) and harness management with skills, MCP and tool restrictions
(issues #3 and #4) had all landed after the previous run and were carried as "implemented,
unmeasured" until issue #42. The ten checks they target — `X-05`…`X-08` and
`F-01`…`F-07` — now pass against the suite rather than against this repository's own tests
alone.

The `extended` run's 44/45 is a *skip*, not a failure: `X-07` downloads an artifact, so it
only tests anything if the agent chose to write a file, and the `full` run passed the same
check ninety seconds later on the same server. Read `skipped_not_verified` in the JSON
report rather than the summary line.

**All three of those runs pinned a model, and that was doing work.** `make conformance-gate`
runs the same suite against the same server *without* `--model`, and on 2026-08-23 it scored
**36/37 core**: a task that named no model came back naming none either, so a client that did
not pin one could not tell what served it (issue #43). That is fixed, and the gate was
re-run on 2026-08-24 — **37/37 core, 0 skipped**, `T-03` reading `claude-opus-5[1m]` off a
task that named no model. The two configurations now agree. The gate stays the one place the
suite runs unpinned, because that is the configuration that found the defect.

**`conformance_class` is no longer a constant, and what it reads depends on how `uhpd` was
started** (issue #65). Three capabilities here are computed from configuration —
`files_input`, `files_output` and `harness_management` — so a hardcoded class would be a
claim the same document contradicts two fields later, which is what the suite's D-05 check
("conformance_class agrees with capabilities") exists to catch. `conformanceClass` in
`internal/transport/http/discovery.go` therefore reads the class off the very capability
struct the document publishes:

| `uhpd` started with | Answers |
| --- | --- |
| nothing | `core` |
| `UHP_WORKSPACE` | `extended` |
| `UHP_WORKSPACE` + `UHP_SESSION_SHARING=1` | `full` |

Only two variables appear because `uhpd` always hands the service a harness store — a
durable one where a path is configured, an in-memory one otherwise — so
`harness_management` is `true` on every deployment of this binary and never the reason a
class is held down. The capability is still read rather than assumed, because the service
is usable without one.

**Session sharing gates `full` here even though D-05 does not require it.** The suite's
list stops at `harness_management`; Sessions §5 is what makes sharing a `full` feature, and
the stricter reading is the one that cannot be wrong. So a server with a workspace and
sharing switched off answers `extended`, not `full`.

The default `uhpd` — no workspace, sharing off — still answers `core`, and that is now a
property of the configuration rather than a line someone remembered not to edit.
Capabilities a deployment does not offer stay reported as `false` rather than omitted — see
[UHP surface implemented](api.md).

A skip is counted as a failure here, not as a pass.

That score is defended by a maintainer running `make conformance-gate` before a merge, not
by CI. The gate points the suite at a running `uhpd` and refuses a failure, *any* skip, or a
pass count below the recorded one — but it runs on a person's machine, because the suite
starts real agent tasks against a real CLI and costs real tokens. GitHub withholds secrets
from a fork's pull request anyway, so a remote job could never have measured a contribution.
CI keeps the free checks: compilation, tests, and an image build.

That is weaker than a machine-enforced gate and is stated as such: what holds the score up
is a procedure, and a maintainer who skips it leaves nothing red behind. Why `claude-code`
is the harness measured, when to run the gate, and what `S-09` can and cannot see are all in
[docs/conformance.md](conformance.md).
