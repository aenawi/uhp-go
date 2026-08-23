#!/usr/bin/env python3
"""Re-run the codex probes against the real binary, and check the parser.

Issue #34. Nothing in internal/harness/codex.go was ever marked UNVERIFIED —
every claim in it carried "verified by execution". What none of them carried
was a version, and #13 is why that is not the same thing: two of opencode's
execution-verified claims were true when written and false by 1.18.21, and
nothing in the tests noticed. Verification has a shelf life. This is the
command that renews it.

The four probes, in the order the issue lists them:

  1. delivery: the prompt goes over stdin in the declared mode (PromptStdin)
     and is answered.
  2. injection: the same string in argv is *not* a prompt. This is the
     counterfactual PromptStdin exists for, and without it "the prompt was
     answered" is not evidence of anything.
  3. separator: `--` does protect codex, unlike grok — recorded, and still not
     used, because stdin keeps the prompt out of argv altogether.
  4. session: `thread.started` announces an id, `codex exec resume <id>`
     resumes that conversation, and a control without the flag does not.

Plus the two things that are not in the issue and turned out to matter more
than the four that are:

  5. messages: a run with a tool call between two sentences sends two
     `agent_message` items with no separator in either. Concatenated as-is they
     answer "Alpha.Gamma.".
  6. failure: a run that cannot proceed prints its reason on stdout as
     `error` and again as `turn.failed`, and exits 1. Before #34 neither line
     was read, so the client got "exit status 1" and none of the words. The
     adapter reads only `turn.failed`; this checks both, because "the same
     sentence is in both" is the observation that makes reading one of them
     safe.

And one claim that is load-bearing enough to check on its own:

  7. gitcheck: without `--skip-git-repo-check` codex refuses to start outside a
     trusted directory. The router gives every session a working directory that
     is not a git repo, so if this ever stops being needed the flag is merely
     redundant — but if it is still needed and were dropped, every codex task
     would fail.

Unlike probe-pi-session.py this cannot answer from a loopback provider: codex
authenticates to ChatGPT and has no per-run base URL to point elsewhere. It
therefore needs a logged-in `codex` on PATH, and it spends real tokens — which
is why the prompts are as short as they are.

TestCodexAndGrokProbesRunTheShippedInvocation pins HARNESS_ARGV and
RESUME_ARGV to NewCodex's BuildArgs, so a probe measuring an invocation uhpd
does not send fails in `go test` rather than reporting a healthy CLI for
nothing.

Every codex*Event fixture in cli_test.go is a line this probe produced.

Usage: probe-codex-session.py [--timeout SEC] [--keep]

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
# the adapter: a probe that measured an argv uhpd does not send would verify
# nothing about uhpd. TestCodexAndGrokProbesRunTheShippedInvocation reads both
# lists against NewCodex's BuildArgs, substituting the same two placeholders.
HARNESS_ARGV = ["codex", "exec", "--json", "--skip-git-repo-check"]
RESUME_ARGV = ["codex", "exec", "resume", "--json", "--skip-git-repo-check", "--model", "<model>", "<session>"]

WORD = "ZEPHYR"
FIRST_PROMPT = f"Remember this word: {WORD}. Just say OK."
RECALL_PROMPT = "What was the word I asked you to remember? Answer with the single word only."

# Two sentences with a shell call between them, which is what makes codex send
# two `agent_message` items in one run. The command is the cheapest one that
# proves a tool ran.
SPLIT_PROMPT = "First say exactly: Alpha. Then run the shell command 'echo HELLO_PROBE'. Then say exactly: Gamma."

# Not a model, and not a plausible one either, so a version that starts
# validating slugs locally still fails the run rather than quietly picking a
# neighbour.
BAD_MODEL = "bogus-model-xyz"


class Probe:
    """One codex run, and what came back from it.

    Captures are written outside the working directory codex is given. That is
    not tidiness: codex has a shell and a file reader, and a control turn asked
    to recall a word it was never told will find it by reading the probe's own
    transcript off disk. The grok probe learned this the expensive way — see
    its docstring.
    """

    def __init__(self, home: str, label: str) -> None:
        self.label = label
        # A fresh directory per run, and deliberately not a git repo: that is
        # the state the router puts every session in, and the state check 7 is
        # about.
        self.cwd = os.path.join(home, "runs", label)
        os.makedirs(self.cwd, exist_ok=True)
        self.captures = os.path.join(home, "captures")
        os.makedirs(self.captures, exist_ok=True)

        self.argv: list[str] = []
        self.code: int | None = None
        self.stderr = ""
        self.lines: list[dict] = []
        self.unparsed: list[str] = []
        # (seconds since launch, event) for every line, so "arrived during the
        # run" can be told from "arrived in one burst at the end".
        self.timeline: list[tuple[float, dict]] = []

    def run(self, argv: list[str], stdin: str | None, timeout: float) -> "Probe":
        self.argv = argv
        out_path = os.path.join(self.captures, f"{self.label}.jsonl")
        started = time.monotonic()
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
        self.stderr = proc.stderr
        elapsed = time.monotonic() - started
        with open(out_path, "w") as fh:
            fh.write(proc.stdout)

        for line in proc.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                self.unparsed.append(line)
                continue
            self.lines.append(event)
            self.timeline.append((elapsed, event))
        return self

    def types(self) -> list[str]:
        return [e.get("type", "") for e in self.lines]

    def of_type(self, kind: str) -> list[dict]:
        return [e for e in self.lines if e.get("type") == kind]

    def items(self, item_type: str) -> list[dict]:
        return [
            e["item"]
            for e in self.lines
            if e.get("type") == "item.completed"
            and isinstance(e.get("item"), dict)
            and e["item"].get("type") == item_type
        ]

    def thread_id(self) -> str:
        for e in self.of_type("thread.started"):
            if e.get("thread_id"):
                return e["thread_id"]
        return ""

    def answer(self) -> str:
        """What the adapter would publish as output_text.

        Deliberately the adapter's reading rather than a convenient one: this
        is parseCodexLine's rule — completed `agent_message` items, in order,
        newline-separated — so a check on this is a check on what a client sees.
        """
        return "".join(item.get("text", "") + "\n" for item in self.items("agent_message"))


class Report:
    def __init__(self) -> None:
        self.failures: list[str] = []
        self.notes: list[str] = []

    def check(self, ok: bool, name: str, detail: str) -> bool:
        mark = "ok  " if ok else "FAIL"
        print(f"  [{mark}] {name}: {detail}")
        if not ok:
            self.failures.append(f"{name}: {detail}")
        return ok

    def note(self, text: str) -> None:
        print(f"  [note] {text}")
        self.notes.append(text)


def probe_delivery(home: str, timeout: float, report: Report) -> Probe:
    """1. The prompt goes over stdin and is answered, progressively."""
    print("\n1. delivery — the prompt on stdin, in the declared mode")
    run = Probe(home, "delivery").run(HARNESS_ARGV, FIRST_PROMPT, timeout)

    report.check(run.code == 0, "exit", f"code={run.code}")
    report.check(
        "Reading prompt from stdin" in run.stderr,
        "stdin",
        "codex says it read the prompt from stdin"
        if "Reading prompt from stdin" in run.stderr
        else f"codex did not report reading stdin: {run.stderr.strip()[:200]!r}",
    )
    report.check(
        bool(run.thread_id()),
        "session id",
        f"thread.started announced {run.thread_id() or '(nothing)'}",
    )
    report.check(
        "OK" in run.answer().upper(),
        "answered",
        f"answer={run.answer()!r}",
    )
    report.check(
        run.unparsed == [],
        "stdout is all JSON",
        "every line parsed" if not run.unparsed else f"{len(run.unparsed)} lines did not: {run.unparsed[:2]}",
    )
    return run


def probe_injection(home: str, timeout: float, report: Report) -> None:
    """2 and 3. argv is unsafe; `--` protects; stdin needs neither."""
    print("\n2. injection — the same string in argv is not a prompt")
    bare = Probe(home, "injection-bare").run(HARNESS_ARGV + ["--help"], "", timeout)
    printed_usage = "Usage: codex exec" in "".join(l for l in bare.unparsed)
    report.check(
        printed_usage and not bare.items("agent_message"),
        "argv is injectable",
        "codex exec \"--help\" printed usage and ran nothing, so PromptStdin is not merely the conservative choice"
        if printed_usage
        else f"argv prompt was not parsed as an option: types={bare.types()}",
    )

    print("\n3. separator — what `--` does")
    guarded = Probe(home, "injection-guarded").run(
        HARNESS_ARGV + ["--", "--help"], "", timeout
    )
    protected = bool(guarded.items("agent_message"))
    report.check(
        protected,
        "`--` protects",
        "codex exec -- \"--help\" treated it as a prompt",
    )
    if protected:
        report.note(
            "`--` does protect codex, unlike grok, and is still not used: stdin "
            "keeps the prompt out of argv entirely, which is stronger than "
            "escaping it, and codex read stdin as an additional `<stdin>` block "
            "even on this run."
        )


def probe_session(home: str, timeout: float, report: Report) -> None:
    """4. The announced id resumes that conversation, and only with the flag."""
    print("\n4. session — discovery and resume")
    first = Probe(home, "session-first").run(HARNESS_ARGV, FIRST_PROMPT, timeout)
    sid = first.thread_id()
    if not report.check(bool(sid), "first turn", f"thread_id={sid or '(none)'}"):
        return

    resume_argv = [a for a in RESUME_ARGV if a not in ("--model", "<model>")]
    resume_argv = [sid if a == "<session>" else a for a in resume_argv]
    resumed = Probe(home, "session-resume").run(resume_argv, RECALL_PROMPT, timeout)

    report.check(
        WORD in resumed.answer().upper(),
        "resumed",
        f"the second turn recalled a word given only in the first: {resumed.answer().strip()!r}",
    )
    report.check(
        resumed.thread_id() == sid,
        "same thread",
        f"resumed thread_id={resumed.thread_id()} first={sid}",
    )

    # Without this the resume check proves nothing: codex could be continuing
    # the most recent session for this directory by default, which is exactly
    # what `--last` exists to ask for.
    control = Probe(home, "session-control").run(HARNESS_ARGV, RECALL_PROMPT, timeout)
    report.check(
        WORD not in control.answer().upper(),
        "control",
        f"the same turn without resume did not recall it: {control.answer().strip()!r}",
    )
    report.check(
        control.thread_id() != sid,
        "control is a new thread",
        f"control thread_id={control.thread_id()}",
    )


def probe_messages(home: str, timeout: float, report: Report) -> None:
    """5. Two assistant messages in one run, and nothing between them."""
    print("\n5. messages — two agent messages do not separate themselves")
    run = Probe(home, "messages").run(
        HARNESS_ARGV + ["--sandbox", "workspace-write"], SPLIT_PROMPT, timeout
    )
    texts = [item.get("text", "") for item in run.items("agent_message")]
    report.check(
        len(texts) >= 2,
        "split answer",
        f"{len(texts)} agent_message items in one run: {texts}",
    )
    report.check(
        bool(run.items("command_execution")),
        "tool ran",
        "a command_execution item is present, which is what splits the answer",
    )
    # The defect this is really about: the tool call's own stdout must not be
    # reachable as answer text.
    leaked = [t for t in texts if "HELLO_PROBE\n" == t]
    report.check(
        not any(item.get("text") for item in run.items("command_execution")),
        "tool output is not answer text",
        "command_execution carries its stdout in aggregated_output, not text",
    )
    if len(texts) >= 2:
        unseparated = "".join(texts)
        report.note(
            f"unseparated, a client would read {unseparated!r}; parseCodexLine "
            f"appends a newline per message, giving {run.answer()!r}"
        )
    if leaked:
        report.check(False, "leak", f"a shell's stdout arrived as an agent message: {leaked}")


def probe_failure(home: str, timeout: float, report: Report) -> None:
    """6. A failed run says why, on stdout, in words."""
    print("\n6. failure — the reason is on stdout, not only in the exit code")
    run = Probe(home, "failure").run(
        HARNESS_ARGV + ["--model", BAD_MODEL], "say hi", timeout
    )
    report.check(run.code not in (0, None), "exit", f"code={run.code}")

    top_level = [e["message"] for e in run.of_type("error") if e.get("message")]
    terminal = [
        (e.get("error") or {}).get("message", "")
        for e in run.of_type("turn.failed")
        if (e.get("error") or {}).get("message")
    ]

    report.check(
        bool(terminal),
        "turn.failed carries the reason",
        f"{terminal[0][:160]!r}" if terminal else "turn.failed carried no message, or did not arrive",
    )
    report.check(
        any(BAD_MODEL in r for r in terminal),
        "reason names the cause",
        "the model that could not be used is named in the message the adapter reads",
    )
    # The adapter reads `turn.failed` alone, because UpdateFailed is terminal
    # and a top-level `error` codex recovers from would kill a healthy run. That
    # choice costs nothing only while the two carry the same sentence, so the
    # sameness is checked rather than assumed.
    report.check(
        bool(top_level) and set(top_level) == set(terminal),
        "the two lines say the same thing",
        f"top-level error={len(top_level)}, turn.failed={len(terminal)}, identical={set(top_level) == set(terminal)}",
    )
    report.check(
        not run.items("agent_message"),
        "no answer",
        "a failed run published no agent_message, so the failure is the only thing to report",
    )


def probe_gitcheck(home: str, timeout: float, report: Report) -> None:
    """7. `--skip-git-repo-check` is still required outside a git repo."""
    print("\n7. gitcheck — the flag the router cannot do without")
    argv = [a for a in HARNESS_ARGV if a != "--skip-git-repo-check"]
    run = Probe(home, "gitcheck").run(argv, "say hi", timeout)
    refused = "--skip-git-repo-check was not specified" in run.stderr
    report.check(
        refused and run.code not in (0, None),
        "still required",
        "codex refuses to start outside a trusted directory without it"
        if refused
        else f"codex ran without it (code={run.code}); the flag may now be redundant: {run.stderr.strip()[:200]!r}",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=float, default=300.0, help="seconds per codex run")
    parser.add_argument("--keep", action="store_true", help="keep the captures directory")
    args = parser.parse_args()

    if not shutil.which("codex"):
        print("codex is not on PATH; nothing to probe", file=sys.stderr)
        return 2

    version = subprocess.run(
        ["codex", "--version"], capture_output=True, text=True
    ).stdout.strip()
    print(f"probing {version or 'codex (version unknown)'}")

    home = tempfile.mkdtemp(prefix="uhp-codex-probe-")
    report = Report()
    try:
        probe_delivery(home, args.timeout, report)
        probe_injection(home, args.timeout, report)
        probe_session(home, args.timeout, report)
        probe_messages(home, args.timeout, report)
        probe_failure(home, args.timeout, report)
        probe_gitcheck(home, args.timeout, report)
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
