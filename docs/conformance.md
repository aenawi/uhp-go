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
capture and artifact download (issue #2) and harness create/replace/delete (issue #3)
landed afterwards and have not been re-measured against the published suite. They are
covered by this repository's own tests — see `internal/transport/http/file_handlers_test.go`,
`internal/service/artifacts_test.go`, `internal/transport/http/harness_handlers_test.go`
and `internal/service/harnesses_test.go` — but a passing local test is not a conformance
result, and the number above stays until someone runs the suite again.

This server does not claim `extended` or `full`. Its discovery document reports the
capabilities it does not implement as `false` rather than omitting them, because
`conformance_class` must agree with the capability list. `harness_management` now reports
`true` where it is configured, which is a capability above the claimed class rather than a
contradiction of it: the class is what this server guarantees, not a ceiling on what it
offers, and `full` stays unclaimed while F-03, F-04 and F-06 fail.

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

Two things to know before reading a result:

- **The suite runs real agent tasks** — about six of them, costing real tokens and a few
  minutes. That is deliberate: a stream that never flushes, a cancellation that never
  terminates, and an artifact that cannot be downloaded are all invisible to anything that
  only inspects a schema.
- **A skip is never a pass.** The suite reports skips separately and so does this file.

## History

| Point | Score | What moved it |
|---|---|---|
| Baseline | 3/52 (16 fail, 33 skip) | — |
| Publish blockers, ceremony removal, data-driven harnesses | 3/52 | no wire change by design |
| Supervisor: task lifetime decoupled from request lifetime | 5/52 | A-02, E-03 |
| Event model and sequencer | 5/52 | correct but still unmeasured |
| Contract fixes | **38/52, core 37/37** | see below |
| Session listing, inspection and turns | **42/52** | X-01, X-02, X-03, X-04 |
| Files: input items, artifact capture, download | not yet measured | targets X-05, X-06, X-07, and makes X-08 real |
| Harness management: create, replace, delete | not yet measured | targets F-01, F-02, F-05, F-07 |

The 33 skips in the baseline were not 33 separate defects. They cascaded from one line:
`GET /v1/harnesses` returned `{"data": […]}` where the suite reads `harnesses`, so it could
not pick a harness and every task, stream, session and cancellation check skipped untested.
Fixing that one envelope is what made three steps of prior work measurable.

## The remaining gap

| Checks | Needs | Issue |
|---|---|---|
| X-05…X-07 | file input, artifact capture and download | #2 — implemented, unmeasured |
| F-01, F-02, F-05, F-07 | harness create/replace/delete | #3 — implemented, unmeasured |
| F-03, F-04, F-06 | skills materialised for the agent, MCP wired into the run | #4 |

**F-03, F-04 and F-06 are storage checks that this server now half-satisfies, and the
half it does not is the half that matters.** A skill folder, an MCP server and a
disabled-tool list all survive create, `GET` and a replacing `PUT` byte-for-byte —
`TestSkillFolderSurvivesCreateAndRename` covers exactly the round trip F-04 describes —
but none of them reaches the agent, and `GET /v1/harnesses/{id}/skills/{skill_id}/files`
does not exist, so F-03 reads the endpoint and fails. Configuration that is stored and
never delivered is the failure mode Harnesses §4.3 calls the worst outcome: the operator
believes a tool is off, and it is not. The README says so, this table says so, and the
capability stays honest until #4 lands.

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
that follows it, not decoration on an error.

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
