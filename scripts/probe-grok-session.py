#!/usr/bin/env python3
"""Re-run the grok probes against the real binary, and check the parser.

Issue #34, the same renewal as probe-codex-session.py and for the same reason:
every claim in internal/harness/grok.go said "verified by execution" and none
of them said against what. #13 is what that costs — two of opencode's
execution-verified claims were true when written and false one minor version
later, with nothing in the tests to notice.

The four probes, in the order the issue lists them:

  1. delivery: the prompt must go in argv. grok does not read one from stdin,
     and `-p` with a piped prompt still reports "a value is required".
  2. injection: `-p "--help"` is an argument error, not a prompt, and the
     attached form `-p=--help` is not. This is why the adapter builds
     `-p=<prompt>` rather than the two-element form.
  3. separator: `--` does *not* protect, because the prompt is the value of an
     option rather than a positional. grok's notes have always said so; this
     is the check that they are still right.
  4. session: `session_id` is on every line of the stream and `--resume <id>`
     continues that conversation.

Plus the two the old adapter could not have had, because it ran grok in its
default `plain` mode and passed stdout through line by line:

  5. streaming: `--output-format streaming-messages-json` emits NDJSON in the
     Anthropic Messages API wire format, and `--include-partial-messages` is
     what makes it progressive. Without that flag the same prompt produces
     three lines and no delta at all, which is issue #14 in its third harness.
  6. failure: a run that cannot proceed reports `is_error` with an `errors`
     array and exits 1.

On check 4 and the shape of the control. The obvious control — ask the second
turn *without* `--resume` and expect it not to know the word — passes for the
wrong reason and then fails. grok has a shell and a file reader, and a control
run in the same directory as the probe's own captures found the word by reading
them off disk; it reported the right answer having genuinely resumed nothing.
So the evidence here is `grok export <id>`, which prints the session's own
transcript: after a resumed turn that transcript holds both turns, and the
control's holds one. Every capture this probe writes is kept outside the
directory grok is given, so a future control cannot read them either.

Needs a logged-in `grok` on PATH. Unlike probe-pi-session.py it cannot answer
from a loopback provider — grok authenticates to grok.com and takes no per-run
base URL — so it spends real tokens, which is why the prompts are short.

TestCodexAndGrokProbesRunTheShippedInvocation pins HARNESS_ARGV and
RESUME_ARGV to NewGrok's BuildArgs, so a probe measuring an invocation uhpd
does not send fails in `go test` rather than reporting a healthy CLI for
nothing.

Every grok*Event fixture in cli_test.go is a line this probe produced.

Usage: probe-grok-session.py [--timeout SEC] [--keep]

Exits non-zero if any check fails.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

# The shipped invocation, and the reason this file is not free to drift from
# the adapter. TestCodexAndGrokProbesRunTheShippedInvocation reads both lists
# against NewGrok's BuildArgs, substituting the same three placeholders.
# `<prompt>` is
# one of them because grok is the harness whose prompt is *in* argv — that is
# the thing check 1 is about, so the pin has to cover it.
HARNESS_ARGV = ["grok", "-p=<prompt>", "--output-format", "streaming-messages-json", "--include-partial-messages"]
RESUME_ARGV = ["grok", "-p=<prompt>", "--output-format", "streaming-messages-json", "--include-partial-messages", "--model", "<model>", "--resume", "<session>"]

WORD = "ZEPHYR"
FIRST_PROMPT = f"Remember this word: {WORD}. Just say OK."
RECALL_PROMPT = "What was the word I asked you to remember? Answer with the single word only."
ANSWER = "ALPHA BRAVO CHARLIE"
SAY_PROMPT = f"Say exactly: {ANSWER}"

# A tool call between two stretches of prose, which is what makes grok send two
# assistant messages in one run.
SPLIT_PROMPT = "Run the shell command 'echo HELLO_PROBE' and tell me what it printed."

BAD_MODEL = "bogus-model-xyz"


class Probe:
    """One grok run, and what came back from it."""

    def __init__(self, home: str, label: str) -> None:
        self.label = label
        # A fresh, empty directory per run. Empty is the load-bearing part:
        # grok can read files, and a directory holding this probe's own output
        # is a directory in which "the model knew the word" means nothing.
        self.cwd = os.path.join(home, "runs", label)
        os.makedirs(self.cwd, exist_ok=True)
        # Kept in a sibling of `runs`, never inside a run's own cwd.
        self.captures = os.path.join(home, "captures")
        os.makedirs(self.captures, exist_ok=True)

        self.argv: list[str] = []
        self.code: int | None = None
        self.stdout = ""
        self.stderr = ""
        self.lines: list[dict] = []
        self.unparsed: list[str] = []

    def run(self, argv: list[str], stdin: str | None, timeout: float) -> "Probe":
        self.argv = argv
        try:
            proc = subprocess.run(
                argv,
                cwd=self.cwd,
                input=stdin,
                capture_output=True,
                text=True,
                timeout=timeout,
            )
        except subprocess.TimeoutExpired:
            self.code = None
            self.stderr = f"timed out after {timeout}s"
            return self

        self.code = proc.returncode
        self.stdout = proc.stdout
        self.stderr = proc.stderr
        with open(os.path.join(self.captures, f"{self.label}.jsonl"), "w") as fh:
            fh.write(proc.stdout)

        for line in proc.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                self.lines.append(json.loads(line))
            except json.JSONDecodeError:
                self.unparsed.append(line)
        return self

    def of_type(self, kind: str) -> list[dict]:
        return [e for e in self.lines if e.get("type") == kind]

    def stream_events(self, event_type: str) -> list[dict]:
        return [
            e["event"]
            for e in self.lines
            if e.get("type") == "stream_event"
            and isinstance(e.get("event"), dict)
            and e["event"].get("type") == event_type
        ]

    def deltas(self, delta_type: str) -> list[str]:
        out = []
        for ev in self.stream_events("content_block_delta"):
            delta = ev.get("delta") or {}
            if delta.get("type") == delta_type:
                out.append(delta.get("text") or delta.get("thinking") or delta.get("partial_json") or "")
        return out

    def session_id(self) -> str:
        for e in self.lines:
            if e.get("type") == "system" and e.get("subtype") == "init" and e.get("session_id"):
                return e["session_id"]
        return ""

    def answer(self) -> str:
        """What the adapter would publish as output_text.

        parseGrokLine's rule, so a check on this is a check on what a client
        sees: text deltas passed through unpadded, plus a newline at every
        `message_stop`.
        """
        out = []
        for e in self.lines:
            if e.get("type") != "stream_event":
                continue
            ev = e.get("event") or {}
            if ev.get("type") == "content_block_delta" and (ev.get("delta") or {}).get("type") == "text_delta":
                out.append(ev["delta"].get("text", ""))
            elif ev.get("type") == "message_stop":
                out.append("\n")
        return "".join(out)

    def result(self) -> dict:
        results = self.of_type("result")
        return results[-1] if results else {}


class Report:
    def __init__(self) -> None:
        self.failures: list[str] = []
        self.notes: list[str] = []

    def check(self, ok: bool, name: str, detail: str) -> bool:
        print(f"  [{'ok  ' if ok else 'FAIL'}] {name}: {detail}")
        if not ok:
            self.failures.append(f"{name}: {detail}")
        return ok

    def note(self, text: str) -> None:
        print(f"  [note] {text}")
        self.notes.append(text)


def with_prompt(argv: list[str], prompt: str) -> list[str]:
    return [a.replace("<prompt>", prompt) for a in argv]


def probe_delivery(home: str, timeout: float, report: Report) -> None:
    """1. argv carries the prompt; stdin is not read."""
    print("\n1. delivery — the prompt in argv, in the declared mode")
    run = Probe(home, "delivery").run(with_prompt(HARNESS_ARGV, SAY_PROMPT), None, timeout)
    report.check(run.code == 0, "exit", f"code={run.code}")
    report.check(
        ANSWER in run.answer(),
        "answered",
        f"answer={run.answer().strip()!r}",
    )
    report.check(
        bool(run.session_id()),
        "session id",
        f"the init event announced {run.session_id() or '(nothing)'}",
    )

    # The other half of "must go in argv": grok genuinely cannot take it any
    # other way, so PromptArgs is a constraint rather than a preference.
    piped = Probe(home, "delivery-stdin").run(
        ["grok", "-p", "--output-format", "streaming-messages-json"], SAY_PROMPT, timeout
    )
    refused = "a value is required for '--single <PROMPT>'" in (piped.stderr + piped.stdout)
    report.check(
        refused,
        "stdin refused",
        "a piped prompt with a bare -p is still an argument error, so PromptArgs is forced"
        if refused
        else f"grok accepted a prompt on stdin (code={piped.code}); PromptStdin may now be available",
    )


def probe_injection(home: str, timeout: float, report: Report) -> None:
    """2 and 3. The attached form is the only safe one, and `--` does not help."""
    print("\n2. injection — a hyphen-leading prompt")
    bare = Probe(home, "injection-bare").run(
        ["grok", "-p", "--help", "--output-format", "streaming-messages-json"], None, timeout
    )
    bare_failed = "a value is required for '--single <PROMPT>'" in (bare.stderr + bare.stdout)
    report.check(
        bare_failed,
        "bare -p is unsafe",
        "grok -p \"--help\" is still an argument error, not a prompt",
    )

    print("\n3. separator — what `--` does")
    guarded = Probe(home, "injection-guarded").run(
        ["grok", "-p", "--", "--help", "--output-format", "streaming-messages-json"], None, timeout
    )
    guarded_failed = "a value is required for '--single <PROMPT>'" in (guarded.stderr + guarded.stdout)
    report.check(
        guarded_failed,
        "`--` does not protect",
        "grok -p -- \"--help\" fails identically, because the prompt is an option's value and not a positional"
        if guarded_failed
        else f"`--` now protects grok (code={guarded.code}); the adapter's note is stale",
    )

    attached = Probe(home, "injection-attached").run(
        with_prompt(HARNESS_ARGV, "--help"), None, timeout
    )
    report.check(
        attached.code == 0 and bool(attached.answer().strip()),
        "attached -p= is safe",
        f"-p=--help was answered as a prompt: {attached.answer().strip()[:80]!r}",
    )


def probe_session(home: str, timeout: float, report: Report) -> tuple[str, str]:
    """4. Discovery and resume, evidenced by the session's own transcript."""
    print("\n4. session — discovery and resume")
    first = Probe(home, "session-first").run(with_prompt(HARNESS_ARGV, FIRST_PROMPT), None, timeout)
    sid = first.session_id()
    if not report.check(bool(sid), "first turn", f"session_id={sid or '(none)'}"):
        return "", ""

    resume_argv = [a for a in RESUME_ARGV if a not in ("--model", "<model>")]
    resume_argv = [sid if a == "<session>" else a for a in resume_argv]
    resumed = Probe(home, "session-resume").run(with_prompt(resume_argv, RECALL_PROMPT), None, timeout)

    report.check(
        resumed.session_id() == sid,
        "same session",
        f"resumed session_id={resumed.session_id()}",
    )

    # The evidence. The model's answer is not it — see the module docstring.
    transcript = export(sid, timeout)
    report.check(
        transcript.count("## User") >= 2 and WORD in transcript,
        "transcript holds both turns",
        f"grok export {sid} shows {transcript.count('## User')} user turn(s)",
    )

    control = Probe(home, "session-control").run(with_prompt(HARNESS_ARGV, RECALL_PROMPT), None, timeout)
    control_sid = control.session_id()
    report.check(
        control_sid != sid,
        "control is a new session",
        f"control session_id={control_sid}",
    )
    control_transcript = export(control_sid, timeout) if control_sid else ""
    report.check(
        control_transcript.count("## User") == 1,
        "control transcript holds one turn",
        f"grok export {control_sid} shows {control_transcript.count('## User')} user turn(s)",
    )
    if WORD in control_transcript:
        report.note(
            "the control still answered with the word, without having resumed "
            "anything — grok found it another way. This is why the transcript "
            "is the evidence and the answer is not."
        )
    return sid, control_sid


def export(session_id: str, timeout: float) -> str:
    """`grok export <id>` — the session's own transcript, as grok records it."""
    try:
        proc = subprocess.run(
            ["grok", "export", session_id],
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        return ""
    return proc.stdout


def probe_streaming(home: str, timeout: float, report: Report) -> None:
    """5. The deltas exist, and the flag is what produces them."""
    print("\n5. streaming — the flag that makes it progressive")
    run = Probe(home, "streaming").run(with_prompt(HARNESS_ARGV, SPLIT_PROMPT), None, timeout)

    text = run.deltas("text_delta")
    report.check(len(text) > 1, "text deltas", f"{len(text)} text_delta events")
    report.check(
        bool(run.deltas("thinking_delta")),
        "thinking is separable",
        f"{len(run.deltas('thinking_delta'))} thinking_delta events, which the adapter must not publish",
    )
    report.check(
        bool(run.deltas("input_json_delta")) or not run.stream_events("content_block_start"),
        "tool arguments are separable",
        f"{len(run.deltas('input_json_delta'))} input_json_delta events, which the adapter must not publish",
    )

    stops = len(run.stream_events("message_stop"))
    report.check(
        stops >= 2,
        "two messages in one run",
        f"{stops} message_stop events, so the answer arrives in {stops} pieces with no separator between them",
    )
    if stops >= 2:
        report.note(f"unseparated a client would read {''.join(text)!r}")
        report.note(f"parseGrokLine's reading is {run.answer()!r}")

    # message_delta's usage is per message; result's is the run's. Reading the
    # wrong one is wrong in the silent direction, so the difference is checked
    # rather than assumed.
    per_message = [
        (ev.get("usage") or {}).get("input_tokens", 0) for ev in run.stream_events("message_delta")
    ]
    total = (run.result().get("usage") or {}).get("input_tokens", 0)
    report.check(
        total == sum(per_message) if per_message else total > 0,
        "usage totals",
        f"result input_tokens={total}, message_delta input_tokens={per_message}",
    )

    # The counterfactual: the same prompt without the flag.
    plain = Probe(home, "streaming-whole").run(
        with_prompt([a for a in HARNESS_ARGV if a != "--include-partial-messages"], SAY_PROMPT),
        None,
        timeout,
    )
    report.check(
        not plain.deltas("text_delta"),
        "the flag is load-bearing",
        f"without it the run emitted no text_delta at all, only {sorted({e.get('type', '') for e in plain.lines})}",
    )


def probe_failure(home: str, timeout: float, report: Report) -> None:
    """6. A failed run says why, on stdout, in words."""
    print("\n6. failure — the reason is on stdout, not only in the exit code")
    run = Probe(home, "failure").run(
        with_prompt(HARNESS_ARGV, "hi") + ["--model", BAD_MODEL], None, timeout
    )
    report.check(run.code not in (0, None), "exit", f"code={run.code}")

    result = run.result()
    report.check(bool(result), "a result event", f"subtype={result.get('subtype')!r}")
    report.check(result.get("is_error") is True, "is_error", f"is_error={result.get('is_error')}")
    errors = result.get("errors") or []
    report.check(
        bool(errors) and any(BAD_MODEL in e for e in errors),
        "reason names the cause",
        f"errors={errors[:1]}",
    )
    report.check(
        "result" not in result,
        "no `result` field",
        "grok leaves it absent on a failure, unlike claude, which is why the adapter reads `errors`"
        if "result" not in result
        else f"grok now also fills `result`: {result.get('result')!r}",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=float, default=300.0, help="seconds per grok run")
    parser.add_argument("--keep", action="store_true", help="keep the captures directory")
    args = parser.parse_args()

    if not shutil.which("grok"):
        print("grok is not on PATH; nothing to probe", file=sys.stderr)
        return 2

    version = subprocess.run(
        ["grok", "--version"], capture_output=True, text=True
    ).stdout.strip()
    print(f"probing {version or 'grok (version unknown)'}")

    home = tempfile.mkdtemp(prefix="uhp-grok-probe-")
    report = Report()
    try:
        probe_delivery(home, args.timeout, report)
        probe_injection(home, args.timeout, report)
        probe_session(home, args.timeout, report)
        probe_streaming(home, args.timeout, report)
        probe_failure(home, args.timeout, report)
    finally:
        if args.keep:
            print(f"\ncaptures kept in {home}")
        else:
            shutil.rmtree(home, ignore_errors=True)

    print()
    if report.failures:
        print(f"{len(report.failures)} check(s) failed against {version}:")
        for f in report.failures:
            print(f"  - {f}")
        return 1
    print(f"all checks passed against {version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
