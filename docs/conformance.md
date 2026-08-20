# Conformance

UHP conformance is defined by a runnable suite, not by self-assessment. This file records
what this server scores, how to reproduce it, and what the remaining gap is.

## Current result

```
37/37 core · 0 failed · 0 skipped · 0 errored
CONFORMANT — UHP 2026-08-11 (core)
```

Across all three classes: **38/52**. The remaining 14 are all `extended` and `full`.

This server does not claim `extended` or `full`. Its discovery document reports the
capabilities it does not implement as `false` rather than omitting them, because
`conformance_class` must agree with the capability list.

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

The 33 skips in the baseline were not 33 separate defects. They cascaded from one line:
`GET /v1/harnesses` returned `{"data": […]}` where the suite reads `harnesses`, so it could
not pick a harness and every task, stream, session and cancellation check skipped untested.
Fixing that one envelope is what made three steps of prior work measurable.

## The remaining 14

| Checks | Needs | Issue |
|---|---|---|
| X-01…X-04 | session listing, inspection, turns, pagination | #1 |
| X-05…X-07 | file input, artifact capture and download | #2 |
| F-01, F-02, F-05, F-07 | harness create/update/delete | #3 |
| F-03, F-04, F-06 | skills as folders, MCP config | #4 |

Note that **X-08 currently passes vacuously**: artifact ids cannot traverse out of their
container because the download endpoint does not exist, so every probe 404s. It becomes a
real check only once #2 lands.
