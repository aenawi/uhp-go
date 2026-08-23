#!/usr/bin/env python3
"""Capture Claude Code's stream-json output and check it against what the
adapter assumes, on a machine where the CLI is logged in.

This exists because `parseClaudeLine` is the one path in this repository that
fails *silently*. If the delta shape is wrong, no line matches, the run
completes, and the client is handed an empty answer with a success. Issue #32:
that shape was read out of the 2.1.238 binary rather than off the wire, and the
CI conformance gate that was supposed to catch it never ran once.

It is a probe rather than a test because it needs something `go test` cannot
have: a logged-in Claude Code. `make conformance-gate` is on a maintainer's
machine for the same reason (60135aa), so this follows it there.

Run it after every Claude Code upgrade. What it asserts:

  1. the run opens with a `system`/`init` line carrying a session_id
  2. the answer arrives as `stream_event` → `content_block_delta` → `text_delta`
  3. those deltas arrive *during* the run, not in one burst at the end — which
     is the whole of issue #14, and is invisible in a capture without
     arrival times
  4. the deltas concatenate to the same text the `result` line reports, which
     is what makes "read the deltas, ignore the finished message" safe
  5. the run closes with a `result` line carrying usage in the four fields the
     adapter reads

Usage: capture-claude-stream.py [--prompt TEXT] [--out PATH] [--timeout SEC]

Writes the stdout of the run verbatim to --out (default claude-capture.jsonl),
so the lines can be lifted straight into the fixtures in cli_test.go, and
prints each line's arrival time in milliseconds since launch.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time

# The invocation under test, and the reason this file is not free to drift from
# it: a probe that measured a different argv than the one uhpd ships would
# verify nothing about uhpd. TestClaudeProbeRunsTheShippedInvocation reads this
# list and fails if it stops matching NewClaude's BuildArgs.
HARNESS_ARGV = [
    "claude",
    "-p",
    "--output-format",
    "stream-json",
    "--verbose",
    "--include-partial-messages",
]

# This list used to carry `--bare`, and the probe used to refuse to run inside a
# Claude Code session, on the theory that a nested claude cannot authenticate.
# Both are gone, and the second was a wrong conclusion drawn from the first.
#
# `--bare` says it skips hooks, LSP and plugins. It also says "Anthropic auth is
# strictly ANTHROPIC_API_KEY or apiKeyHelper via --settings (OAuth and keychain
# are never read)" — so on a Pro or Max subscription it discards the login and
# the run comes back "Not logged in · Please run /login" with no stream_event in
# it. That is the four-line capture the nesting guard was built to explain. The
# nesting was a coincidence; the flag was the cause, and it failed the same way
# from Terminal.app.
#
# Verified 2026-08-23 against Claude Code 2.1.240, from inside a Claude Code
# session, one shell and one minute apart: with `--bare`, is_error=true and "Not
# logged in"; without it, is_error=false, "OK", two text deltas. So this probe
# runs anywhere the CLI is logged in, nested or not.

# Long enough that the model has to produce the answer in pieces. A one-word
# answer is one delta, and one delta cannot show whether a stream is
# progressive or buffered.
DEFAULT_PROMPT = (
    "Count from 1 to 40. Put each number on its own line. "
    "Do not use any tools and do not write anything else."
)


def run(prompt: str, timeout: float) -> tuple[list[tuple[int, str]], int, str]:
    """Run the harness invocation, returning (arrival_ms, line) pairs.

    The prompt goes over stdin because that is how the adapter sends it
    (PromptStdin) — `claude -p "--help"` prints usage instead of answering,
    so argv is not an option and a probe that used it would be testing a path
    uhpd never takes.
    """
    t0 = time.monotonic()
    proc = subprocess.Popen(
        HARNESS_ARGV,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )
    assert proc.stdin and proc.stdout and proc.stderr
    proc.stdin.write(prompt)
    proc.stdin.close()

    lines: list[tuple[int, str]] = []
    try:
        for line in proc.stdout:
            lines.append((int((time.monotonic() - t0) * 1000), line.rstrip("\n")))
    finally:
        try:
            proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    return lines, proc.returncode, proc.stderr.read()


def classify(lines: list[tuple[int, str]]) -> dict:
    """Pick out the lines the adapter reads. Mirrors parseClaudeLine, so a
    shape the adapter would miss is a shape this misses too — the point is to
    find out whether the assumption holds, not to be more forgiving than the
    code under test."""
    found = {
        "init": None,
        "deltas": [],          # (ms, text)
        "other_deltas": [],    # (ms, delta type) — thinking, tool arguments
        "result": None,
        "unparsed": [],
        "envelopes": {},       # type -> count, so a renamed envelope is visible
    }
    for ms, line in lines:
        if not line.strip():
            continue
        try:
            ev = json.loads(line)
        except ValueError:
            found["unparsed"].append((ms, line))
            continue
        if not isinstance(ev, dict):
            found["unparsed"].append((ms, line))
            continue

        typ = ev.get("type", "<no type>")
        key = f"{typ}/{ev['subtype']}" if "subtype" in ev and typ == "system" else typ
        found["envelopes"][key] = found["envelopes"].get(key, 0) + 1

        if typ == "system" and ev.get("subtype") == "init" and ev.get("session_id"):
            found["init"] = (ms, ev)
        elif typ == "stream_event":
            inner = ev.get("event") or {}
            delta = inner.get("delta") or {}
            if inner.get("type") == "content_block_delta":
                if delta.get("type") == "text_delta" and delta.get("text"):
                    found["deltas"].append((ms, delta["text"]))
                else:
                    found["other_deltas"].append((ms, delta.get("type", "?")))
        elif typ == "result":
            found["result"] = (ms, ev)
    return found


def report(found: dict, rc: int, stderr: str) -> int:
    problems: list[str] = []
    notes: list[str] = []

    envelopes = ", ".join(f"{k}×{v}" for k, v in sorted(found["envelopes"].items()))
    notes.append(f"envelopes seen: {envelopes or '(none)'}")

    if found["init"]:
        ms, ev = found["init"]
        notes.append(f"init at {ms}ms · session_id {ev['session_id']} · model {ev.get('model', '?')}")
    else:
        problems.append("no system/init line carrying a session_id — "
                        "UpdateSessionID never fires and --resume cannot work")

    # A run that failed has no deltas *because* it failed, and saying "this is
    # issue #32" over the top of "Not logged in" is how the --bare auth bug got
    # read as a broken delta shape in the first place. Report the failure and
    # stop: the delta shape is simply not under test in a run that never
    # reached the model.
    if found["result"] and found["result"][1].get("is_error"):
        ms, ev = found["result"]
        problems.append(f"the run failed: {ev.get('result', '')!r} — a failed run "
                        "says nothing about the delta shape, so fix this first")
        if "not logged in" in str(ev.get("result", "")).lower():
            problems.append("that is an auth failure, not a stream failure: check "
                            "`claude` is logged in, and that no flag in HARNESS_ARGV "
                            "suppresses OAuth (`--bare` did, which was issue #32's "
                            "second door)")
        notes.append(f"result at {ms}ms · is_error=True · subtype={ev.get('subtype')}")
        return finish(problems, notes, rc, stderr)

    deltas = found["deltas"]
    if not deltas:
        # The failure this whole probe exists for. Say what was there instead,
        # because "no text_delta" plus the envelope census is the diagnosis.
        problems.append(
            "NO stream_event/content_block_delta/text_delta line arrived — this is the "
            "silent-empty-answer path in issue #32. parseClaudeLine matches nothing, "
            "the run succeeds, and the client gets \"\"")
    else:
        first, last = deltas[0][0], deltas[-1][0]
        notes.append(f"{len(deltas)} text deltas, first at {first}ms, last at {last}ms "
                     f"(spread {last - first}ms)")

    if found["other_deltas"]:
        kinds = sorted({k for _, k in found["other_deltas"]})
        notes.append(f"non-answer deltas correctly ignored: {', '.join(kinds)}")

    if not found["result"]:
        problems.append("no result line — usage is never reported and a failed run "
                        "loses the CLI's own words")
        return finish(problems, notes, rc, stderr)

    result_ms, result_ev = found["result"]
    notes.append(f"result at {result_ms}ms · is_error={result_ev.get('is_error')} "
                 f"· subtype={result_ev.get('subtype')}")

    # is_error was handled above, before the delta verdict.

    if deltas:
        # Issue #14. A stream whose every delta lands in the same instant as the
        # result line is a buffered stream wearing delta clothing, and is
        # indistinguishable at the socket from the bug #14 fixed.
        if deltas[0][0] >= result_ms:
            problems.append(f"deltas did not arrive during the run: first delta at "
                            f"{deltas[0][0]}ms, result at {result_ms}ms")
        elif result_ms - deltas[0][0] < 50:
            problems.append(f"the whole answer landed {result_ms - deltas[0][0]}ms before the "
                            "result line — that is a buffered stream, which is issue #14")

        # The adapter reads the deltas and deliberately drops the finished
        # `assistant` message. That is only safe if the two say the same thing.
        joined = "".join(t for _, t in deltas)
        reported = result_ev.get("result", "")
        if joined.strip() != reported.strip():
            problems.append(
                "the concatenated deltas are not the answer the result line reports, so "
                "reading deltas and dropping the finished message loses text:\n"
                f"      deltas: {joined[:200]!r}\n"
                f"      result: {reported[:200]!r}")
        else:
            notes.append(f"deltas concatenate to the reported answer ({len(joined)} chars)")

    usage = result_ev.get("usage")
    if not isinstance(usage, dict):
        problems.append("the result line carries no usage object")
    else:
        missing = [f for f in ("input_tokens", "output_tokens",
                               "cache_read_input_tokens", "cache_creation_input_tokens")
                   if f not in usage]
        if missing:
            problems.append(f"usage is missing the fields the adapter reads: {', '.join(missing)}")
        else:
            notes.append(f"usage in={usage['input_tokens']} out={usage['output_tokens']} "
                         f"cache_read={usage['cache_read_input_tokens']} "
                         f"cache_write={usage['cache_creation_input_tokens']}")

    return finish(problems, notes, rc, stderr)


def finish(problems: list[str], notes: list[str], rc: int, stderr: str) -> int:
    for n in notes:
        print(f"  · {n}")
    if stderr.strip():
        print(f"  · stderr: {stderr.strip()[:400]}")
    print(f"  · exit {rc}")
    print()
    if problems:
        print("claude stream probe FAILED")
        for p in problems:
            print(f"  - {p}")
        return 1
    print("claude stream probe ok — the shape parseClaudeLine assumes is the shape on the wire")
    return 0


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__.strip().splitlines()[0])
    ap.add_argument("--prompt", default=DEFAULT_PROMPT)
    ap.add_argument("--out", default="claude-capture.jsonl")
    ap.add_argument("--timeout", type=float, default=180.0)
    args = ap.parse_args(argv[1:])

    # Removed before the run rather than after it, and before the guard below
    # rather than after: a probe that refuses to launch would otherwise leave
    # the previous attempt's capture in place to be read as this one's. The
    # conformance gate learned the same lesson (see `conformance-gate` in the
    # Makefile), and it costs more here — a stale capture is a fixture source.
    try:
        os.remove(args.out)
    except FileNotFoundError:
        pass

    version = subprocess.run(["claude", "--version"], capture_output=True, text=True)
    print(f"claude {version.stdout.strip() or '(version unknown)'}")
    print(f"argv: {' '.join(HARNESS_ARGV)}")
    print(f"prompt on stdin: {args.prompt!r}")
    print()

    lines, rc, stderr = run(args.prompt, args.timeout)

    with open(args.out, "w") as f:
        for _, line in lines:
            f.write(line + "\n")
    print(f"captured {len(lines)} lines verbatim to {args.out}")
    print()

    return report(classify(lines), rc, stderr)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
