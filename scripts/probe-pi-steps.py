#!/usr/bin/env python3
"""Establish whether a tool call can be counted from pi's output.

Issue #72, and the half of it #91 blocked. `probe-steps.py` answers this
question for claude, opencode and codex by asking a real model for five files;
it cannot ask pi, because pi routes through whichever provider the machine is
logged in to and the only one reachable when #72 was written capped at 8,000
tokens per minute against a 71,166-token request. That is a fact about an API
key, not about pi, and #72 was left blocked on it.

This probe removes the key from the question. pi reads a `models.json` that can
declare a provider outright, base URL and all, so the run answers from a
loopback OpenAI-compatible server of this probe's own — the same trick
`probe-pi-session.py` uses, and for the same reason: what is being measured is
pi's own layer, above whatever generated the tokens.

**What that costs, stated rather than buried.** On the other three bases a model
decided to call a tool five times. Here the probe's provider decides, by
returning five `tool_calls` deltas. So this does not show that pi narrates a
*model's* calls — it shows that pi narrates the calls it *executes*, which is
the only thing a counter reads. The evidence that they are the same calls is
where it is on every other base: on disk. pi runs its own `write` tool five
times and five files appear, so a narration of five is a narration of five side
effects that demonstrably happened, and a narration of fewer would be the
under-count that sinks the field.

What is counted is the **start** edge, matching every other base: pi announces
`assistantMessageEvent.type == "toolcall_start"` when the model asks for a tool,
carrying `toolName` and the call's id. It also announces `toolcall_end`,
`tool_execution_start` and `tool_execution_end` for the same call, and counting
any second one would double every number.

`TestPiStepProbeRunsTheShippedInvocation` pins HARNESS_ARGV to NewPi's
BuildArgs, so a probe measuring an invocation uhpd does not send fails in
`go test` rather than reporting a healthy narration for nothing. `--model` is
in the list because a run has to name a model and the declared provider's is
the only one this machine can serve; nothing else here is scaffolding.

Everything pi touches is redirected into a temporary directory
(PI_CODING_AGENT_DIR), so the machine's own pi credentials, settings and saved
sessions are neither read nor written.

Usage: probe-pi-steps.py [--timeout SEC] [--keep]

Exits non-zero if pi under-counts, or if the run took no tool call at all.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# The shipped invocation. Pinned to NewPi's BuildArgs by the Go test named
# above, with `<model>` substituted the same way probe-pi-session.py does.
HARNESS_ARGV = ["pi", "-p", "--mode", "json", "--model", "<model>"]

MODEL = "probe/probe-model"

# Five files, one tool call each — the same ground truth probe-steps.py uses, so
# the two are directly comparable and the fixtures sit in one table.
FILES = 5
PROMPT = (
    "Create exactly five files in the current directory, named step1.txt, "
    "step2.txt, step3.txt, step4.txt and step5.txt. Each file must contain only "
    "its own number, e.g. step3.txt contains 3. Create them one at a time, using "
    "a separate file-write tool call for each one. Do not use a shell loop, and "
    "do not create them in a single command. When all five exist, reply with the "
    "word DONE and nothing else."
)

# The name pi gives its own file-writing tool. Not hard-coded blindly: the
# provider checks it against the tool list pi actually sent and says so if it is
# gone, because a renamed tool would otherwise look like a narration failure.
WRITE_TOOL = "write"


class Provider:
    """A loopback OpenAI-compatible provider that drives a tool loop.

    Turn N < FILES answers with a `tool_calls` delta asking for one write; the
    turn after that answers with text, so the run ends on a turn that took no
    tool at all. That last turn is not padding — it is what shows a base does
    not narrate a step for an answering turn, which is the rule `max_step: 1`
    on a task that touches nothing depends on.
    """

    def __init__(self) -> None:
        self.turn = 0
        self.tools_offered: list[str] = []
        self._lock = threading.Lock()
        self._server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self.port = self._server.server_address[1]
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}/v1"

    def __enter__(self) -> "Provider":
        self._thread.start()
        return self

    def __exit__(self, *_) -> None:
        self._server.shutdown()
        self._server.server_close()

    def _handler(self):
        probe = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def _json(self, status: int, payload: dict) -> None:
                body = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                self._json(200, {"data": [{"id": "probe-model",
                                           "status": {"value": "loaded"}}]})

            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                try:
                    body = json.loads(raw)
                except json.JSONDecodeError:
                    body = {}
                with probe._lock:
                    turn = probe.turn
                    probe.turn += 1
                    if not probe.tools_offered:
                        probe.tools_offered = [
                            (t.get("function") or {}).get("name", "")
                            for t in (body.get("tools") or [])
                        ]
                self._stream(turn)

            def _stream(self, turn: int) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                self.end_headers()

                def chunk(delta, finish=None):
                    payload = {"id": f"probe-{turn}", "object": "chat.completion.chunk",
                               "created": 0, "model": "probe-model",
                               "choices": [{"index": 0, "delta": delta,
                                            "finish_reason": finish}]}
                    self.wfile.write(b"data: " + json.dumps(payload).encode() + b"\n\n")
                    self.wfile.flush()

                chunk({"role": "assistant", "content": ""})
                if turn < FILES:
                    args = json.dumps({"path": f"step{turn + 1}.txt",
                                       "content": str(turn + 1)})
                    chunk({"tool_calls": [{
                        "index": 0, "id": f"call_{turn}", "type": "function",
                        "function": {"name": WRITE_TOOL, "arguments": args}}]})
                    chunk({}, finish="tool_calls")
                else:
                    chunk({"content": "DONE"})
                    chunk({}, finish="stop")
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()

        return Handler


def agent_dir(work: str, base_url: str) -> str:
    """An isolated PI_CODING_AGENT_DIR pointing pi at `base_url`."""
    d = os.path.join(work, "agent")
    os.makedirs(d, exist_ok=True)
    write_json(d, "models.json", {"providers": {"probe": {
        "name": "Probe",
        "baseUrl": base_url,
        "apiKey": "local",
        "api": "openai-completions",
        "models": [{
            "id": "probe-model",
            "name": "probe-model",
            "input": ["text"],
            "contextWindow": 128000,
            "maxTokens": 1024,
            "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
            "compat": {
                "supportsStore": False,
                "supportsDeveloperRole": False,
                "supportsReasoningEffort": False,
                "supportsUsageInStreaming": True,
                "supportsStrictMode": False,
                "maxTokensField": "max_tokens",
            },
        }],
    }}})
    write_json(d, "auth.json", {"probe": {"type": "api_key", "key": "local"}})
    write_json(d, "settings.json", {"defaultProvider": "probe",
                                    "defaultModel": "probe-model"})
    return d


def write_json(directory: str, name: str, payload: dict) -> None:
    with open(os.path.join(directory, name), "w") as fh:
        json.dump(payload, fh, indent=2)


def pi_calls(events: list[dict]) -> int:
    """Tool calls pi narrated.

    `toolcall_start` is the model asking for the tool, before it runs — the same
    start edge claude's `tool_use` block and codex's `item.started` are. pi also
    emits `toolcall_delta`, `toolcall_end`, `tool_execution_start` and
    `tool_execution_end` per call, and reading any of those as well would
    multiply every number here.
    """
    return sum(
        1
        for ev in events
        if ev.get("type") == "message_update"
        and (ev.get("assistantMessageEvent") or {}).get("type") == "toolcall_start"
    )


def run_pi(work: str, env_dir: str, timeout: float) -> dict:
    """One pi run, with everything needed to judge it.

    stderr is drained on a thread rather than read after stdout closes: a run
    that filled the pipe while this side was still reading stdout would deadlock
    with neither end moving. Same failure the Go runner fixed in 995e8d3.
    """
    cwd = os.path.join(work, "ws-pi")
    os.makedirs(cwd, exist_ok=True)

    argv = [a if a != "<model>" else MODEL for a in HARNESS_ARGV]
    env = dict(os.environ, PI_CODING_AGENT_DIR=env_dir)
    proc = subprocess.Popen(argv, cwd=cwd, env=env, text=True, bufsize=1,
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE)
    assert proc.stdin and proc.stdout and proc.stderr

    errors: list[str] = []
    drain = threading.Thread(target=lambda: errors.append(proc.stderr.read()),
                             daemon=True)
    drain.start()

    timed_out = threading.Event()

    def expire():
        timed_out.set()
        proc.kill()

    watchdog = threading.Timer(timeout, expire)
    watchdog.start()

    lines: list[str] = []
    proc.stdin.write(PROMPT)
    proc.stdin.close()
    try:
        for line in proc.stdout:
            line = line.strip()
            if line:
                lines.append(line)
        proc.wait()
    finally:
        watchdog.cancel()
        if proc.poll() is None:
            proc.kill()
            proc.wait()
    drain.join(timeout=5)

    if timed_out.is_set():
        return {"skipped": f"timed out after {timeout}s"}

    capture = os.path.join(work, "pi.jsonl")
    with open(capture, "w") as fh:
        fh.write("\n".join(lines) + "\n")

    events, unparsed = [], []
    for line in lines:
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            unparsed.append(line)

    produced = len([n for n in os.listdir(cwd)
                    if n.startswith("step") and n.endswith(".txt")])

    return {
        "code": proc.returncode,
        "narrated": pi_calls(events),
        "produced": produced,
        "events": len(events),
        "unparsed": len(unparsed),
        "capture": capture,
        "cwd": cwd,
        "stderr": "".join(errors),
    }


def report(result: dict, tools_offered: list[str]) -> bool:
    if "skipped" in result:
        print(f"  SKIP  pi: {result['skipped']}")
        return True

    narrated, produced = result["narrated"], result["produced"]
    print(f"        pi: exit={result['code']} events={result['events']} "
          f"narrated={narrated} produced={produced}")

    if WRITE_TOOL not in tools_offered:
        print(f"  FAIL  pi: no `{WRITE_TOOL}` tool was offered to the provider "
              f"(saw {tools_offered or 'nothing'}). This probe asked for a tool pi "
              "no longer has, so the count below is about the probe rather than "
              "about pi's narration.")
        return False

    if produced == 0:
        print("  FAIL  pi: the run created no files, so it took no tool call to count.")
        tail = (result["stderr"] or "").strip().splitlines()[-1:]
        if tail:
            print(f"        pi said: {tail[0][:160]}")
        return False

    if narrated < produced:
        print(f"  FAIL  pi: narrated {narrated} tool calls for {produced} that "
              "happened. A budget counted from this under-counts, and a caller is "
              "told it has a ceiling it does not have.")
        return False

    if narrated > produced:
        print(f"  WARN  pi: narrated {narrated} for {produced}. Over-counting stops "
              "runs early rather than never, which is tolerable — but the extra "
              "events should be identified before this is relied on.")
        return True

    print("  OK    pi: every tool call it took, it narrated.")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--keep", action="store_true",
                        help="keep the capture and workspace instead of deleting them")
    args = parser.parse_args()

    if shutil.which("pi") is None:
        print("  SKIP  pi: pi is not installed")
        return 0

    work = tempfile.mkdtemp(prefix="uhp-probe-pi-steps-")
    print(f"Probing pi — {FILES} files, one tool call each, against a loopback provider.")
    print(f"Capture: {work}\n")

    with Provider() as provider:
        result = run_pi(work, agent_dir(work, provider.base_url), args.timeout)
        ok = report(result, provider.tools_offered)

    if args.keep:
        print(f"\nCapture kept in {work}")
    else:
        shutil.rmtree(work, ignore_errors=True)

    if not ok:
        print("\npi cannot be counted from. Under ADR-0007's rule a bound holds on "
              "every base or is not claimed at all, so this is a result about "
              "`max_step` and not only about pi.")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
