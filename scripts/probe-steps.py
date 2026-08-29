#!/usr/bin/env python3
"""Establish, per base, whether a tool call can be counted from its output.

Issue #72. `max_step` is a budget on agent steps, and ADR-0007's rule is that a
bound must hold on every base or not be claimed at all. So the question this
answers is not "does the CLI have a flag" but "does the CLI *say* every tool
call it makes" — because a counter fed by a partial narration produces a ceiling
that never fires, and a caller believing in a ceiling it does not have is the
defect #48 and #54 both exist to remove.

The measurement is a count against ground truth, never a reading of a vendor
document. Each base is asked for five files, one tool call each, and what landed
on disk is the number its narration has to match:

  - narrated == produced  -> countable, and the fixture is kept
  - narrated <  produced  -> under-counts, and the base sinks the field
  - narrated >  produced  -> over-counts, which stops runs early rather than
                             never; tolerable, and recorded

Every run uses the shipped invocation — exactly `CLIHarness.BuildArgs` with the
prompt on stdin. An earlier pass of this probe added `--permission-mode
bypassPermissions`, `--auto` and `--sandbox workspace-write` and passed the
prompt as argv, none of which uhpd sent. It measured a server that does not
exist. `TestStepProbeRunsTheShippedInvocation` pins CLAUDE_ARGV, OPENCODE_ARGV
and CODEX_ARGV below to each adapter's BuildArgs so that cannot happen quietly
again.

That pin is also why CODEX_ARGV now carries `-c sandbox_mode=workspace-write`
and the other two carry nothing of the kind: ADR-0008 decided uhpd grants each
agent write access to the directory it gave the session, and codex is the one
base of the five that needs an argument to get there. The flag is here because
the adapter sends it, not because the probe wanted it — which is the same rule
as before, applied to an invocation that changed.

Not covered, and both deliberate:

  - `grok` enforces a step budget natively with `--max-turns`, so nothing here
    counts it. What it owes instead is a *report* — whether tripping the flag is
    distinguishable from an ordinary success — which is a different probe, and
    is `probe-grok-max-turns.py`. It answers yes: `result.subtype ==
    "error_max_turns"`.
  - `pi` routes through whichever provider the machine is logged in to, and the
    only one authed on the capture machine capped at 8,000 tokens per minute
    against a 71,166-token request. That is a fact about an API key rather than
    about `pi`, so its narration is established by a probe of its own —
    `probe-pi-steps.py`, which answers from a loopback provider declared in
    `pi`'s own models.json and needs no credentials at all.

Usage: probe-steps.py [--timeout SEC] [--keep] [--base NAME]

Exits non-zero if any base under-counts, or if a base cannot run at all.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

# Each base's shipped argv, excluding the prompt: four of the five adapters
# declare PromptStdin, so the prompt reaches the child on stdin and never
# appears here. Pinned to BuildArgs by the Go test named above.
CLAUDE_ARGV = [
    "claude", "-p", "--output-format", "stream-json", "--verbose",
    "--include-partial-messages", "--strict-mcp-config",
]
OPENCODE_ARGV = ["opencode", "run", "--format", "json"]
CODEX_ARGV = ["codex", "exec", "--json", "--skip-git-repo-check", "-c", "sandbox_mode=workspace-write"]

HARNESS_ARGV = {
    "claude": CLAUDE_ARGV,
    "opencode": OPENCODE_ARGV,
    "codex": CODEX_ARGV,
}

# Five files, one tool call each, and an answer that needs no tool. The last
# sentence matters: it produces a turn with no tool call in it, which is what
# shows whether a base brackets an answering turn the same way it brackets a
# working one.
FILES = 5
PROMPT = (
    "Create exactly five files in the current directory, named step1.txt, "
    "step2.txt, step3.txt, step4.txt and step5.txt. Each file must contain only "
    "its own number, e.g. step3.txt contains 3. Create them one at a time, using "
    "a separate file-write tool call for each one. Do not use a shell loop, and "
    "do not create them in a single command. When all five exist, reply with the "
    "word DONE and nothing else."
)


def claude_calls(events: list[dict]) -> int:
    """Tool calls claude narrated.

    The `assistant` message carrying `tool_use` blocks is the model asking for
    those tools, before any of them runs. One message can carry several, so the
    blocks are counted rather than the messages. The matching finish is the
    `user` message carrying `tool_result`, and counting both would double every
    number this probe reports.
    """
    n = 0
    for ev in events:
        if ev.get("type") != "assistant":
            continue
        for block in ev.get("message", {}).get("content", []) or []:
            if block.get("type") == "tool_use":
                n += 1
    return n


def opencode_calls(events: list[dict]) -> int:
    """Tool calls opencode narrated.

    One event per tool part, whatever step opencode decided to group that part
    into. The grouping is not counted and must not be: the same five writes
    arrived as five steps in one capture and one step in the next, so a counter
    reading `step_start` would have made `max_step` unfireable on the second
    run. See internal/harness/testdata/steps/README.md.
    """
    return sum(1 for ev in events if ev.get("type") == "tool_use")


# Item types that are a tool doing something, as opposed to the model talking.
# `agent_message` and `reasoning` are the answer being written, which is not a
# tool call.
CODEX_TOOL_ITEMS = {"file_change", "command_execution", "mcp_tool_call", "web_search"}


def codex_calls(events: list[dict]) -> int:
    """Tool calls codex narrated.

    Counted on `item.started` — the request — not `item.completed`.
    """
    return sum(
        1
        for ev in events
        if ev.get("type") == "item.started"
        and (ev.get("item") or {}).get("type") in CODEX_TOOL_ITEMS
    )


COUNTERS = {"claude": claude_calls, "opencode": opencode_calls, "codex": codex_calls}


def run_base(base: str, out_dir: str, timeout: float) -> dict:
    """One base's run, and everything needed to judge it."""
    workspace = os.path.join(out_dir, f"ws-{base}")
    os.makedirs(workspace, exist_ok=True)
    capture = os.path.join(out_dir, f"{base}.jsonl")

    argv = HARNESS_ARGV[base]
    if shutil.which(argv[0]) is None:
        return {"base": base, "skipped": f"{argv[0]} is not installed"}

    try:
        proc = subprocess.run(
            argv,
            cwd=workspace,
            input=PROMPT,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        code, stdout, stderr = proc.returncode, proc.stdout, proc.stderr
    except subprocess.TimeoutExpired:
        return {"base": base, "skipped": f"timed out after {timeout}s"}

    with open(capture, "w") as f:
        f.write(stdout)
    with open(capture.replace(".jsonl", ".stderr"), "w") as f:
        f.write(stderr)

    events, unparsed = [], []
    for line in stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            unparsed.append(line)

    produced = len(
        [
            name
            for name in os.listdir(workspace)
            if name.startswith("step") and name.endswith(".txt")
        ]
    )

    return {
        "base": base,
        "code": code,
        "narrated": COUNTERS[base](events),
        "produced": produced,
        "events": len(events),
        "unparsed": len(unparsed),
        "capture": capture,
        "stderr": stderr,
    }


def report(result: dict) -> bool:
    """Print one base's verdict. Returns False if the base failed the bar."""
    base = result["base"]
    if "skipped" in result:
        print(f"  SKIP  {base}: {result['skipped']}")
        return True

    narrated, produced = result["narrated"], result["produced"]
    print(
        f"        {base}: exit={result['code']} events={result['events']} "
        f"narrated={narrated} produced={produced}"
    )

    if produced == 0:
        print(f"  FAIL  {base}: the run created no files, so it took no tool call to count.")
        tail = (result["stderr"] or "").strip().splitlines()[-1:]
        if tail:
            print(f"        {base} said: {tail[0][:160]}")
        print(
            f"        This is not a fact about {base}'s narration. Establish why it could "
            "not act before reading anything into the count."
        )
        return False

    if narrated < produced:
        print(
            f"  FAIL  {base}: narrated {narrated} tool calls for {produced} that happened. "
            "A budget counted from this under-counts, and a caller is told it has a "
            "ceiling it does not have."
        )
        return False

    if narrated > produced:
        print(
            f"  WARN  {base}: narrated {narrated} for {produced}. Over-counting stops runs "
            "early rather than never, which is tolerable — but the extra events should be "
            "identified before this is relied on."
        )
        return True

    print(f"  OK    {base}: every tool call it took, it narrated.")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=float, default=300.0)
    parser.add_argument(
        "--keep",
        action="store_true",
        help="keep the captures and workspaces instead of deleting them",
    )
    parser.add_argument(
        "--base",
        action="append",
        choices=sorted(HARNESS_ARGV),
        help="probe only this base (repeatable); default is all of them",
    )
    args = parser.parse_args()

    bases = args.base or sorted(HARNESS_ARGV)
    out_dir = tempfile.mkdtemp(prefix="uhp-probe-steps-")

    print(f"Probing {', '.join(bases)} — {FILES} files, one tool call each.")
    print(f"Captures: {out_dir}\n")

    ok = True
    for base in bases:
        ok &= report(run_base(base, out_dir, args.timeout))

    if args.keep:
        print(f"\nCaptures kept in {out_dir}")
    else:
        shutil.rmtree(out_dir, ignore_errors=True)

    if not ok:
        print(
            "\nAt least one base cannot be counted from. Under ADR-0007's rule a bound "
            "holds on every base or is not claimed at all, so this is a result about "
            "`max_step` and not only about the base that failed."
        )
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
