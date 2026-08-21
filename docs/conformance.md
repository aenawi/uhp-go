# Conformance

UHP conformance is defined by a runnable suite, not by self-assessment. This file records
what this server scores, how to reproduce it, and what the remaining gap is.

## Current result

```
37/37 core · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (core)
```

Across all three classes: **42/52**. `extended` stood at 42/45 when that run was taken;
the three outstanding checks there were all file-related.

**This result predates file support and harness management.** Input items, artifact
capture and artifact download (issue #2), harness create/replace/delete (issue #3) and
skills, MCP and tool restrictions (issue #4) all landed afterwards and have not been
re-measured against the published suite. They are covered by this repository's own
tests — see `internal/transport/http/file_handlers_test.go`,
`internal/service/artifacts_test.go`, `internal/transport/http/harness_handlers_test.go`,
`internal/service/harnesses_test.go` and `internal/service/harness_runtime_test.go` — but
a passing local test is not a conformance result, and the number above stays until someone
runs the suite again.

`conformance_class` still reads `core`. It is not raised on the strength of local tests:
the class is a claim the suite exists to falsify, and raising it before a run would make
this file the thing asserting conformance rather than the thing recording it.
`harness_management` and `idempotency` report `true`, which are capabilities above the
claimed class rather than contradictions of it — the class is what this server guarantees,
not a ceiling on what it offers. Lifecycle §2 constrains the class only through
`files_input`, `files_output` and `session_listing`.

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
  `cancellation`. Of the five bases here only `claude-code`, `codex` and `opencode`
  advertise `sessions`, so a run pointed at `grok-cli` or `pi` fails the session and
  continuation checks by design — the refusal is the honest answer, but it is not a
  conformance measurement of this server's session support.

- **The suite runs real agent tasks** — about six of them, costing real tokens and a few
  minutes. That is deliberate: a stream that never flushes, a cancellation that never
  terminates, and an artifact that cannot be downloaded are all invisible to anything that
  only inspects a schema.
- **A skip is never a pass.** The suite reports skips separately and so does this file.

## The CI gate

The score above is now defended by a build rather than remembered from a run. The
`conformance` job in `.github/workflows/ci.yml` starts `uhpd`, points the suite at it and
refuses a result worse than the one recorded here.

**It measures `claude-code`, and that is a decision rather than a default** (issue #14).
Three constraints pick it between them:

- *It has to advertise what the core checks exercise.* `capabilities` is enforced, so a run
  against `grok-cli` or `pi` fails `C-01` and `C-02` by design and the gate would be red
  from the day it was written.
- *It has to actually stream.* A gate for `S-09` driven by a harness that buffers measures
  nothing about streaming. `claude-code` streams token-level content blocks, but only with
  `--include-partial-messages`, which is why the invocation grew that flag.
- *It has to be installed.* The image already ships `@anthropic-ai/claude-code`, and its CLI
  reads `ANTHROPIC_API_KEY` from the environment without further configuration.

The job skips itself, loudly, when no `ANTHROPIC_API_KEY` secret is visible — which is the
case for every pull request from a fork. The alternative is a job that is red for reasons
nobody can fix, and a red build everyone has learned to ignore defends less than no gate.

**It is skipping on every run until that secret is set**, which is the state this repository
is in as of the commit that added the job. Nothing here can set it; a maintainer must:

```bash
gh secret set ANTHROPIC_API_KEY --repo aenawi/uhp-go
```

Until then the gate is wiring rather than a gate, and the score above is still a number
someone measured by hand.

The suite's own exit code is not the whole verdict, because it is zero when checks are
skipped. `scripts/check-conformance.py` reads the JSON report and refuses three things: a
failure or error, *any* skip, and a pass count below `CONFORMANCE_FLOOR` in the Makefile.
The last one is what stops the denominator shrinking quietly — "0 failed" over 12 checks is
not the result that "0 failed" over 37 checks was.

`CONFORMANCE_FLOOR` is the enforced copy of the score, and the score also appears in prose
here and in the README. Nothing links them mechanically, so a run that raises the score has
to raise the floor too — otherwise the number this file claims and the number CI defends
drift apart, with the lower one silently winning.

The suite is pinned to a commit, not to a branch. It is another project's repository, and a
gate that can go red because somebody else pushed is a gate people stop believing.

Run the same thing by hand:

```bash
UHP_API_KEY=devkey UHP_HARNESS_ID=chrn_… make conformance-gate
```

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
| Files: input items, artifact capture, download | not yet measured | targets X-05, X-06, X-07, and makes X-08 real |
| Harness management: create, replace, delete | not yet measured | targets F-01, F-02, F-05, F-07 |
| Skills as folders, MCP config, tool restrictions | not yet measured | targets F-03, F-04, F-06 |
| Stream keep-alives | no change expected | no check watches a silent stream — see below |
| Harness event feed and `Last-Event-ID` resumption | no change expected | no check reconnects a stream — see below |
| Progressive streaming for `claude-code` and `pi`, CI gate | no change expected | S-09 cannot fail this server either way — see above |

The 33 skips in the baseline were not 33 separate defects. They cascaded from one line:
`GET /v1/harnesses` returned `{"data": […]}` where the suite reads `harnesses`, so it could
not pick a harness and every task, stream, session and cancellation check skipped untested.
Fixing that one envelope is what made three steps of prior work measurable.

## The remaining gap

| Checks | Needs | Issue |
|---|---|---|
| X-05…X-07 | file input, artifact capture and download | #2 — implemented, unmeasured |
| F-01, F-02, F-05, F-07 | harness create/replace/delete | #3 — implemented, unmeasured |
| F-03, F-04, F-06 | skills as folders, MCP config, disabled tools | #4 — implemented, unmeasured |

Every F check now has an implementation behind it, and each was walked through its exact
HTTP sequence against a running server by hand. That is not the same as a suite run and is
not counted as one.

**What is genuinely weaker than the checks can see** is how much of a harness's
configuration each runtime *enforces*, as opposed to being asked to honour. The suite
tests that skills, MCP servers and disabled tools round-trip through the API; it does not
test whether the agent was actually prevented from using a tool. Two of the three
mechanisms for `claude-code` — `--disallowedTools` and `--mcp-config` — are documented but
have not been run against the real binary, and are marked UNVERIFIED in
`internal/harness/claude.go`. They are now the only such claim left: issue #13 settled
opencode's prompt delivery by execution. `grok-cli` and `pi` were read from their own
`--help` on a machine where they are installed. See the delivery table in the README.

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
`claude-code` — the one base whose delivery mechanisms are unverified, so a run on a
machine without that CLI installed measures the API and not the enforcement.

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
