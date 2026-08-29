#!/usr/bin/env python3
"""Issue #90: when grok stops on `--max-turns`, does the run *say* so?

`grok` is the one base that bounds its own steps natively, so #72 exempts it
from the counting `probe-steps.py` establishes for the others. That exemption
rests on half a fact. A flag that *stops* a run is one half; the other is that
the stopped run is distinguishable from one that finished. If a truncated run
looks like an ordinary success, this server reports `completed` for work that
was cut short — and unlike every counting risk in #72 the router cannot repair
it, because the router did not do the stopping and has nothing to relabel.

So this probe asks one question and answers it from execution:

    Is grok's terminal `result` event, on a run stopped by --max-turns,
    distinguishable from the same event on a run that finished?

The measurement, like `probe-steps.py`'s, is against ground truth on disk. The
task is *chained* — each file's contents depend on reading the previous one — so
it cannot be collapsed into a single turn and batched away. Fewer files on disk
than the task required is what makes the run a genuine truncation rather than a
task that happened to finish early, and it is checked before anything is read
into the terminal event.

The invocation is the shipped one exactly — GROK_ARGV below is `NewGrok`'s
BuildArgs, pinned by `TestStepProbeRunsTheShippedInvocation` — plus
MAX_TURNS_ARGV, which is deliberately a separate list because `--max-turns` is
the one flag uhpd does *not* send today. Nothing sends it until #72 honours
`max_step`; this probe sends it to find out what would come back if it did.

Needs a logged-in `grok` on PATH. It spends real tokens — grok authenticates to
grok.com and takes no per-run base URL — which is why the ceiling is 1 turn.
The measurement it replaces cost $0.0086.

Usage: probe-grok-max-turns.py [--timeout SEC] [--keep] [--turns N]

Exits non-zero unless the run was genuinely truncated — some files, but not all
of them — *and* ended on the subtype this repository has pinned as the
`--max-turns` stop. Neither half alone is the answer: a run that took no tool
call never reached the ceiling, and "some subtype other than success" is also
what a run that died on a bad flag reports.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

# The shipped invocation, and the reason this file is not free to drift from the
# adapter. `<prompt>` is a placeholder here only: grok is the one harness whose
# prompt is in argv, so the pin has to cover the flag that carries it.
GROK_ARGV = [
    "grok", "-p=<prompt>", "--output-format", "streaming-messages-json",
    "--include-partial-messages",
]

# Kept out of GROK_ARGV on purpose. This is the flag uhpd does not send, and the
# whole subject of the probe; folding it into the pinned list would make the pin
# assert that uhpd sends it.
MAX_TURNS_ARGV = ["--max-turns", "1"]

# Five files, and every one of them behind a read of the one before it. A
# parallelisable task is no measurement: a model free to batch would satisfy it
# in one turn and never reach the ceiling, and the run would end on success for
# a reason that says nothing about --max-turns.
FILES = 5
PROMPT = (
    "Create step1.txt in the current directory containing only the number 1. "
    "Then read step1.txt and create step2.txt containing that number plus one. "
    "Then read step2.txt and create step3.txt containing that number plus one. "
    "Continue this way through step5.txt. You must read the previous file "
    "before creating the next one. When step5.txt exists, reply with the word "
    "DONE and nothing else."
)

# What an untruncated grok run puts on its `result` line, and what a stopped one
# put on it when this was measured. Both are named, because "not success" is not
# the bar: grok has at least one other failure subtype — `error_during_execution`,
# the one a bad `--model` produces — and a run that died before it ever reached
# the ceiling would satisfy a bare inequality while measuring nothing.
# TestGrokReportsItsOwnMaxTurnsStop pins the same pair in Go.
SUCCESS_SUBTYPE = "success"
MAX_TURNS_SUBTYPE = "error_max_turns"


def run(workspace: str, capture: str, turns: int, timeout: float) -> dict:
    """One grok run under a turn ceiling, and everything needed to judge it."""
    argv = [a.replace("<prompt>", PROMPT) for a in GROK_ARGV]
    argv += [MAX_TURNS_ARGV[0], str(turns)]

    proc = subprocess.run(
        argv, cwd=workspace, capture_output=True, text=True, timeout=timeout
    )

    with open(capture, "w") as f:
        f.write(proc.stdout)
    with open(capture.replace(".jsonl", ".stderr"), "w") as f:
        f.write(proc.stderr)

    events, unparsed = [], []
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            unparsed.append(line)

    terminal = None
    for ev in events:
        if ev.get("type") == "result":
            terminal = ev

    produced = sorted(
        name
        for name in os.listdir(workspace)
        if name.startswith("step") and name.endswith(".txt")
    )

    return {
        "code": proc.returncode,
        "events": len(events),
        "unparsed": len(unparsed),
        "produced": produced,
        "terminal": terminal,
        "stderr": proc.stderr,
        "capture": capture,
    }


def report(result: dict) -> bool:
    """Print the verdict. Returns False if the probe did not answer."""
    produced, terminal = result["produced"], result["terminal"]
    print(
        f"        exit={result['code']} events={result['events']} "
        f"produced={len(produced)}/{FILES} {produced}"
    )

    if len(produced) >= FILES:
        print(
            f"  FAIL  the run produced all {FILES} files, so it was never truncated. "
            "Whatever its terminal event says is a fact about a completed run and "
            "says nothing about --max-turns. Lower --turns, or make the task longer."
        )
        return False

    if not produced:
        print(
            "  FAIL  the run produced no files at all, so it never took a tool call and "
            "never reached the ceiling. This is not a fact about --max-turns; establish "
            "why grok could not act before reading anything into the terminal event."
        )
        tail = (result["stderr"] or "").strip().splitlines()[-2:]
        for line in tail:
            print(f"        grok said: {line[:160]}")
        return False

    if terminal is None:
        print("  FAIL  the run printed no `result` event at all.")
        tail = (result["stderr"] or "").strip().splitlines()[-2:]
        for line in tail:
            print(f"        grok said: {line[:160]}")
        return False

    keep = {
        k: terminal.get(k)
        for k in ("subtype", "is_error", "num_turns", "stop_reason", "result", "errors")
    }
    print(f"        result: {json.dumps(keep)[:1000]}")

    subtype = terminal.get("subtype")
    if subtype == SUCCESS_SUBTYPE:
        print(
            f"  FAIL  the stop is reported as {SUCCESS_SUBTYPE!r}, the same subtype an "
            "untruncated run carries. A truncated run would reach a client as "
            "`completed`, and #72's exemption for grok does not survive: it would have "
            "to be counted like the other four."
        )
        return False

    if subtype != MAX_TURNS_SUBTYPE:
        print(
            f"  FAIL  the run ended on subtype={subtype!r}, not {MAX_TURNS_SUBTYPE!r}. It is "
            "distinguishable from a success, but it is not established that --max-turns is "
            "what ended it — grok reports other failures here too. Either grok has renamed "
            "the subtype, in which case the fixture, the README and "
            "TestGrokReportsItsOwnMaxTurnsStop all need re-taking, or this run failed for "
            "an unrelated reason and measured nothing."
        )
        return False

    print(f"  OK    the stop is distinguishable: subtype={subtype!r}, distinct from "
          f"{SUCCESS_SUBTYPE!r}.")
    if terminal.get("is_error"):
        print(
            "  NOTE  it arrives labelled as an error. Lifecycle §3 requires `incomplete` "
            "for a budget and forbids it for an error, so the adapter must read "
            "`subtype` before `is_error` — see #72."
        )
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=float, default=600.0)
    parser.add_argument("--turns", type=int, default=int(MAX_TURNS_ARGV[1]))
    parser.add_argument(
        "--keep",
        action="store_true",
        help="keep the capture and workspace instead of deleting them",
    )
    args = parser.parse_args()

    if shutil.which("grok") is None:
        print("SKIP  grok is not installed")
        return 0

    out_dir = tempfile.mkdtemp(prefix="uhp-probe-grok-max-turns-")
    # The capture is kept beside the workspace, never inside it: grok can read
    # files, and a run whose own transcript is on its desk is a run whose files
    # prove nothing.
    workspace = os.path.join(out_dir, "ws")
    os.makedirs(workspace, exist_ok=True)
    capture = os.path.join(out_dir, "grok-max-turns.jsonl")

    print(f"Probing grok — {FILES} chained files under --max-turns {args.turns}.")
    print(f"Capture: {capture}\n")

    try:
        ok = report(run(workspace, capture, args.turns, args.timeout))
    except subprocess.TimeoutExpired:
        print(f"  FAIL  grok did not finish within {args.timeout}s")
        ok = False

    if args.keep:
        print(f"\nCapture kept in {out_dir}")
    else:
        shutil.rmtree(out_dir, ignore_errors=True)

    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
