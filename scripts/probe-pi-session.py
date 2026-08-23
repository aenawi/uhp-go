#!/usr/bin/env python3
"""Run pi's streaming and session-resume behaviour against the real binary.

Issue #33. Two things in internal/harness/pi.go were declared rather than
observed, and each fails silently in its own way:

  1. `message_update` → `text_delta` was read out of pi 0.83.0's
     `pi-agent-core/dist/types.d.ts`. If the shape is wrong no line matches, the
     run completes, and the client is handed an empty answer with a success —
     the same failure #32 found in the claude path.
  2. `--session-id` was never run, so `sessions` was withheld and the session id
     pi announces was on the wire but never read. #13 is the precedent:
     opencode's resume was withheld on the same reasoning and turned out to work.

Why this probe needs no credentials, unlike capture-claude-stream.py: pi reads
a `models.json` that can declare a provider outright, base URL included, so the
probe answers from a loopback OpenAI-compatible server of its own. That is not
a weaker test of the two things above — both are pi's own layer, above whatever
generated the tokens — and it makes the second half *stronger*, because the
resumed turn's conversation history is read off the wire the provider received
rather than inferred from whether a model could recall a word.

Everything pi touches is redirected into a temporary directory
(PI_CODING_AGENT_DIR), so the machine's own pi credentials, settings and saved
sessions are neither read nor written.

TestPiProbeRunsTheShippedInvocation pins HARNESS_ARGV and RESUME_ARGV to
NewPi's BuildArgs, so a probe measuring an invocation uhpd does not send fails
in `go test` rather than reporting a healthy stream for nothing.

Four checks, six `pi` runs — the resume check needs a first turn of its own,
and so does its control:

  1. stream: the answer arrives as `message_update` → `text_delta`, during the
     run rather than in one burst at the end, and the deltas concatenate to the
     text `message_end` reports. The model's private working arrives separately
     as `thinking_delta`, which is what makes reading only the answer possible.
     Also captures the `session` event's id.
  2. resume: the same session id passed back as `--session-id`, and the
     provider receives the first turn's user message and assistant reply ahead
     of the new one.
  3. control: the same second turn without the flag, and the provider receives
     neither. Without this, check 2 proves nothing — history could have arrived
     because pi resumes by default.
  4. failure: a provider that refuses. pi reports it as `stopReason: "error"`
     on the stream and still exits 0, which is the claim harnessFailure in
     cli.go rests on and which #13 showed can flip across a version bump.

Every fixture in TestParsePiLine except the two error lines is a line this
probe produced; the header above them says which is which.

Usage: probe-pi-session.py [--timeout SEC] [--keep]

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
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# The shipped invocation, and the reason this file is not free to drift from
# the adapter: a probe that measured an argv uhpd does not send would verify
# nothing about uhpd. TestPiProbeRunsTheShippedInvocation reads both lists
# against NewPi's BuildArgs, substituting the same two placeholders.
HARNESS_ARGV = ["pi", "-p", "--mode", "json"]
RESUME_ARGV = ["pi", "-p", "--mode", "json", "--model", "<model>", "--session-id", "<session>"]

# Scaffolding, deliberately not part of either list: uhpd never sends these.
# `--model` is in RESUME_ARGV because the adapter emits it there; on the first
# turn the probe adds it separately, since a run has to name a model and
# HARNESS_ARGV is the no-model form the Go test compares against.
MODEL = "probe/probe-model"

ANSWER = "Alpha Bravo Charlie"
FIRST_PROMPT = f"Say exactly: {ANSWER}"
SECOND_PROMPT = "What was the third word I asked you to say?"

# Sent as `reasoning_content` ahead of the answer, so pi turns it into the
# `thinking_delta` the adapter must *not* read as answer text. Without it the
# probe could not tell a parser that reads every delta from one that reads only
# the answer's, and the thinking fixture in cli_test.go would have no run
# behind it.
THINKING = "weighing it up"

# Slow enough that a client streaming the run sees the words separately, fast
# enough not to pad the probe. Run 1 fails if the deltas all land at the end,
# which is what issue #14 was, so the gaps have to be real.
CHUNK_DELAY = 0.15


class Provider:
    """A loopback OpenAI-compatible provider that records what it was sent.

    Serves the two endpoints pi needs to treat this as a real provider: a model
    catalogue, and a streaming chat completion. `requests` is the evidence for
    runs 2 and 3 — the message list pi assembled, which is the only direct
    answer to "did it resume".
    """

    def __init__(self, refuse: bool = False) -> None:
        self.refuse = refuse
        self.requests: list[dict] = []
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

    def messages(self) -> list[tuple[str, str]]:
        """The last request's messages as (role, flattened text), system
        dropped: it is pi's own prompt and says nothing about resumption."""
        if not self.requests:
            return []
        out = []
        for m in self.requests[-1].get("messages", []):
            if m.get("role") == "system":
                continue
            content = m.get("content")
            if isinstance(content, list):
                content = "".join(str(p.get("text", "")) for p in content)
            out.append((m.get("role", ""), str(content)))
        return out

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
                    probe.requests.append(body)

                if probe.refuse:
                    self._json(400, {"error": {
                        "message": "probe: the provider refused this run",
                        "type": "invalid_request_error"}})
                    return
                self._stream()

            def _stream(self):
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                self.end_headers()

                def chunk(delta, finish=None, usage=None):
                    payload = {"id": "probe-1", "object": "chat.completion.chunk",
                               "created": 0, "model": "probe-model",
                               "choices": [{"index": 0, "delta": delta,
                                            "finish_reason": finish}]}
                    if usage:
                        payload["usage"] = usage
                    self.wfile.write(b"data: " + json.dumps(payload).encode() + b"\n\n")
                    self.wfile.flush()

                chunk({"role": "assistant", "content": ""})
                chunk({"reasoning_content": THINKING})
                for i, word in enumerate(ANSWER.split(" ")):
                    time.sleep(CHUNK_DELAY)
                    chunk({"content": (" " if i else "") + word})
                chunk({}, finish="stop",
                      usage={"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14})
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()

        return Handler


def agent_dir(work: str, base_url: str) -> str:
    """An isolated PI_CODING_AGENT_DIR pointing pi at `base_url`.

    `models.json` is what makes this probe credential-free: pi lets a provider
    be declared outright, base URL and all, so nothing here depends on which
    provider the machine happens to be logged in to.

    One provider, not one per behaviour: a run that must be refused gets a
    Provider(refuse=True) at this same address rather than a second entry here,
    so the argv under test is the same in every run.
    """
    d = os.path.join(work, "agent")
    os.makedirs(d, exist_ok=True)
    write(d, "models.json", {"providers": {"probe": {
        "name": "Probe",
        "baseUrl": base_url,
        "apiKey": "local",
        "api": "openai-completions",
        "models": [{
            "id": "probe-model",
            "name": "probe-model",
            # So pi renders the provider's reasoning_content as thinking rather
            # than discarding it — see THINKING.
            "reasoning": True,
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
    write(d, "auth.json", {"probe": {"type": "api_key", "key": "local"}})
    write(d, "settings.json", {"defaultProvider": "probe", "defaultModel": "probe-model"})
    return d


def write(directory: str, name: str, payload: dict) -> None:
    with open(os.path.join(directory, name), "w") as fh:
        json.dump(payload, fh, indent=2)


def substitute(argv: list[str], values: dict[str, str]) -> list[str]:
    """Fill a pinned argv's placeholders. The list is compared to the Go
    declaration verbatim, so it holds `<model>`/`<session>` rather than being
    built here."""
    return [values.get(a, a) for a in argv]


def run(argv: list[str], prompt: str, cwd: str, env_dir: str, timeout: float):
    """One pi run, returning (exit code, parsed events with arrival times, stderr).

    Arrival times are kept because the streaming check reads them. A capture
    without them cannot tell a streaming run from a buffered one, and that
    difference is the whole of issue #14 — which is also why `bufsize=1` is not
    optional here. Without it Python's own read-ahead batches whole blocks of
    stdout, and the probe would be timing its own buffer rather than pi's
    output.

    stderr is drained on a thread rather than read after stdout closes. pi
    writes warnings there — "No project session found …" is one this probe
    reads — and a run that filled the pipe while this side was still reading
    stdout would deadlock with neither end moving. That is the same failure
    995e8d3 fixed in the Go runner; a probe is not exempt from it.
    """
    env = dict(os.environ, PI_CODING_AGENT_DIR=env_dir)
    started = time.monotonic()
    proc = subprocess.Popen(argv, cwd=cwd, env=env, text=True, bufsize=1,
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE)
    assert proc.stdin and proc.stdout and proc.stderr

    errors: list[str] = []
    drain = threading.Thread(target=lambda: errors.append(proc.stderr.read()),
                             daemon=True)
    drain.start()

    # The timeout has to be a watchdog rather than an argument to proc.wait():
    # the loop below blocks on stdout, so a pi that produced output forever, or
    # produced none and never exited, would never reach the wait to be timed
    # out by it.
    timed_out = threading.Event()

    def expire():
        timed_out.set()
        proc.kill()

    watchdog = threading.Timer(timeout, expire)
    watchdog.start()

    events: list[tuple[float, dict]] = []
    proc.stdin.write(prompt)
    proc.stdin.close()
    try:
        for line in proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                events.append((time.monotonic() - started, json.loads(line)))
            except json.JSONDecodeError:
                continue
        proc.wait()
    finally:
        watchdog.cancel()
        # A run killed by the watchdog is still reaped here, so main() never
        # deletes the working directory out from under a live pi.
        if proc.poll() is None:
            proc.kill()
            proc.wait()
    drain.join(timeout=5)
    if timed_out.is_set():
        raise subprocess.TimeoutExpired(argv, timeout)
    return proc.returncode, events, "".join(errors)


def deltas(events, kind: str = "text_delta") -> list[tuple[float, str]]:
    out = []
    for at, ev in events:
        ame = ev.get("assistantMessageEvent") or {}
        if ev.get("type") == "message_update" and ame.get("type") == kind:
            out.append((at, ame.get("delta", "")))
    return out


def final_text(events) -> str:
    for _, ev in events:
        msg = ev.get("message") or {}
        if ev.get("type") == "message_end" and msg.get("role") == "assistant":
            return "".join(p.get("text", "") for p in msg.get("content", [])
                           if p.get("type") == "text")
    return ""


def session_id(events) -> str:
    for _, ev in events:
        if ev.get("type") == "session":
            return ev.get("id", "")
    return ""


def check_stream(work, env_dir, timeout, problems, notes) -> None:
    """Run 1: the answer arrives as streamed text_delta events."""
    code, events, err = run(HARNESS_ARGV + ["--model", MODEL],
                            FIRST_PROMPT, work, env_dir, timeout)
    if code != 0:
        problems.append(f"the streaming run exited {code}: {err.strip()[:300]}")
        return

    got = deltas(events)
    if not got:
        # The silent failure this probe exists for. pi did answer — the run
        # completed — but nothing the adapter reads carried the answer.
        kinds = sorted({(ev.get("assistantMessageEvent") or {}).get("type") or ev.get("type", "")
                        for _, ev in events})
        problems.append(
            "no `message_update` with an inner `text_delta` arrived, so parsePiLine would "
            f"publish an empty answer on a completed run. Event types seen: {kinds}")
        return

    joined = "".join(d for _, d in got)
    reported = final_text(events)
    if joined != reported:
        problems.append(f"the deltas concatenate to {joined!r} but message_end reports "
                        f"{reported!r} — reading the deltas alone is not safe")
    else:
        notes.append(f"{len(got)} text_delta events concatenate to the reported answer")

    # Issue #14: `--mode json` exists to make the run progressive. If every
    # delta lands with the last one, the mode is buying nothing and the
    # `streaming` capability is a claim the adapter cannot keep.
    span = got[-1][0] - got[0][0]
    if len(got) > 1 and span < CHUNK_DELAY / 2:
        problems.append(f"all {len(got)} deltas arrived within {span * 1000:.0f}ms of each "
                        f"other — the run buffered instead of streaming")
    elif len(got) > 1:
        notes.append(f"deltas spread over {span * 1000:.0f}ms, so the run streams")

    # The model's private working arrives on the same event as the answer,
    # separated only by the inner type. If pi stopped distinguishing them, a
    # parser reading every delta would publish the thinking as answer text and
    # this run would still look healthy — so the separation is checked, not
    # assumed.
    thinking = deltas(events, "thinking_delta")
    if not thinking:
        problems.append(
            f"the provider sent reasoning_content but no `thinking_delta` arrived, so this "
            f"run cannot show that thinking is distinguishable from the answer. Delta types "
            f"seen: {sorted({(ev.get('assistantMessageEvent') or {}).get('type') for _, ev in events} - {None})}")
    elif any(t in joined for _, t in thinking):
        problems.append(f"the thinking text {THINKING!r} appears in the answer deltas, so "
                        f"parsePiLine would publish the model's private working as output")
    else:
        notes.append("thinking arrives as thinking_delta, separate from the answer")

    sid = session_id(events)
    if not sid:
        problems.append("no `session` event carried an id, so nothing can be resumed")
    else:
        notes.append(f"session id announced: {sid}")


def check_resume(work, timeout, problems, notes) -> None:
    """Runs 2 and 3: the resume, and the control that gives it meaning.

    Each takes its own provider and its own first turn. A provider only holds
    the *last* request it was sent, and the question here is what a second turn
    carried, so the pair has to be isolated from run 1's.
    """
    with Provider() as provider:
        env_dir = agent_dir(work, provider.base_url)
        _, first, first_err = run(HARNESS_ARGV + ["--model", MODEL],
                                  FIRST_PROMPT, work, env_dir, timeout)
        sid = session_id(first)
        if not sid:
            problems.append(f"skipped the resume checks: the first turn announced no session "
                            f"id ({first_err.strip()[:200]})")
            return

        argv = substitute(RESUME_ARGV, {"<model>": MODEL, "<session>": sid})
        code, events, err = run(argv, SECOND_PROMPT, work, env_dir, timeout)
        if code != 0:
            problems.append(f"the resumed run exited {code}: {err.strip()[:300]}")
            return
        history = provider.messages()
        if session_id(events) != sid:
            problems.append(f"the resumed run announced {session_id(events)!r}, not the id it "
                            f"was given ({sid!r})")
        # pi warns rather than failing when the id names nothing it can find,
        # so a silent miss would otherwise read as a successful resume.
        if "No project session found" in err:
            problems.append(f"pi did not find the session it was handed: {err.strip()[:200]}")

        resumed = [(r, t) for r, t in history if t != SECOND_PROMPT]
        if not any(t == FIRST_PROMPT for _, t in resumed):
            problems.append(f"the resumed turn reached the provider without the first turn's "
                            f"prompt, so --session-id did not resume: {history}")
        elif not any(r == "assistant" for r, _ in resumed):
            problems.append(f"the resumed turn carried the first prompt but no assistant "
                            f"reply: {history}")
        else:
            notes.append(f"--session-id resumed: the provider received "
                         f"{len(resumed)} earlier message(s) ahead of the new one")

    # Run 3. Without it, run 2 is equally the result of pi resuming by default.
    with Provider() as provider:
        env_dir = agent_dir(work, provider.base_url)
        run(HARNESS_ARGV + ["--model", MODEL], FIRST_PROMPT, work, env_dir, timeout)
        code, _, err = run(HARNESS_ARGV + ["--model", MODEL],
                           SECOND_PROMPT, work, env_dir, timeout)
        if code != 0:
            problems.append(f"the control run exited {code}: {err.strip()[:300]}")
            return
        stale = [(r, t) for r, t in provider.messages() if t != SECOND_PROMPT]
        if stale:
            problems.append(f"a run *without* --session-id also carried history ({stale}), so "
                            f"the resume above was not the flag's doing")
        else:
            notes.append("without --session-id the same turn carries no history, so the "
                         "resume is the flag's")


def check_failure_exits_zero(work, timeout, problems, notes) -> None:
    """Run 4. harnessFailure in cli.go rests on this and #13 showed it can flip."""
    with Provider(refuse=True) as provider:
        env_dir = agent_dir(work, provider.base_url)
        code, events, err = run(HARNESS_ARGV + ["--model", MODEL],
                                FIRST_PROMPT, work, env_dir, timeout)

    errored = [ev for _, ev in events
               if ev.get("type") == "message_end"
               and (ev.get("message") or {}).get("stopReason") == "error"]
    if not errored:
        problems.append("a refused run printed no `message_end` with stopReason \"error\", so "
                        f"parsePiLine has nothing to fail the run on: {err.strip()[:200]}")
    elif not (errored[0].get("message") or {}).get("errorMessage"):
        problems.append("the errored message carried no `errorMessage`, so the client would be "
                        "told a run failed and not why")

    if code == 0:
        notes.append("a refused run still exits 0, so the stream is the only failure signal — "
                     "unchanged from 0.83.0")
    else:
        # Nothing breaks when this flips: harnessFailure reports the reason
        # regardless of the code, and the supervisor keeps whichever terminal
        # update arrives first. It is still a problem rather than a note,
        # because the only thing that keeps a stale comment from being read as
        # a current fact is something failing when it goes stale — and #13
        # found opencode's copy of this claim had flipped with nobody noticing.
        problems.append(f"pi now exits {code} on a refused run, not 0. Nothing breaks — "
                        f"harnessFailure does not hang on the exit code — but the comments "
                        f"in internal/harness/cli.go and internal/harness/pi.go both say 0 "
                        f"and are now wrong")


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__.strip().splitlines()[0])
    ap.add_argument("--timeout", type=float, default=120.0)
    ap.add_argument("--keep", action="store_true", help="keep the working directory")
    args = ap.parse_args(argv[1:])

    if shutil.which("pi") is None:
        print("pi is not installed, so there is nothing to probe")
        return 1
    version = subprocess.run(["pi", "--version"], capture_output=True, text=True)
    print(f"pi {version.stdout.strip() or '(version unknown)'}")
    print(f"argv: {' '.join(HARNESS_ARGV)}")
    print(f"      {' '.join(RESUME_ARGV)}")
    print()

    # A directory of its own, not the repository: pi writes session files and
    # would otherwise read this checkout's AGENTS.md into every run.
    work = tempfile.mkdtemp(prefix="uhp-pi-session-")
    started = time.monotonic()
    problems: list[str] = []
    notes: list[str] = []
    try:
        with Provider() as provider:
            env_dir = agent_dir(work, provider.base_url)
            check_stream(work, env_dir, args.timeout, problems, notes)
        check_resume(work, args.timeout, problems, notes)
        check_failure_exits_zero(work, args.timeout, problems, notes)
    except subprocess.TimeoutExpired as e:
        problems.append(f"a run exceeded --timeout {args.timeout}s: {e}")
    finally:
        if args.keep:
            notes.append(f"working directory kept at {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)

    for n in notes:
        print(f"  · {n}")
    print(f"  · {time.monotonic() - started:.0f}s")
    print()
    if problems:
        print("pi session probe FAILED")
        for p in problems:
            print(f"  - {p}")
        return 1
    print("pi session probe ok — the answer arrives as streamed text_delta events, "
          "--session-id resumes the conversation, and a refused run still says so on the stream")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
