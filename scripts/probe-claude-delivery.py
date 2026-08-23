#!/usr/bin/env python3
"""Run Claude Code's tool-block and MCP delivery mechanisms against the real
binary, on a machine where the CLI is installed and logged in.

Issue #19. `--disallowedTools` and `--mcp-config` were declared from Claude
Code's documentation and never executed, while the comparable flags for grok
and pi were read from their own `--help` on a machine where they are installed.
That gap mattered more than it looks: F-01 of the conformance suite discovers a
base by taking the first entry of `error.detail.supported`, which sorts to
`claude-code` — so the base most likely to be exercised is the one whose
enforcement was least verified.

This is a probe rather than a `go test` for the reason `capture-claude-
stream.py` is: it needs a logged-in Claude Code, plus a listening MCP endpoint,
and `go test` has neither. TestClaudeDeliveryProbeRunsTheShippedFlags pins the
flag spellings here to the adapter's, so a probe measuring flags uhpd does not
send fails in CI rather than reporting a healthy block for nothing.

Five runs, each answering something the previous one cannot:

  1. control: the named tool is used when nothing blocks it. Without this, "the
     model did not call Bash" is not evidence of a block — it is equally the
     answer of a model that did not feel like it.
  2. blocked: with the block, the tool is gone from the session's tool list, so
     the model is never offered it.
  3. mcp: the configured server is contacted, as the configured principal, and
     its tool is actually called — not merely advertised.
  4. isolation: a server uhpd's document does not name is not contacted at all
     (§4.1), even though the working directory configures one.
  5. isolation control: drop `--strict-mcp-config` and that same server *is*
     contacted — which is what makes 4 a property of the invocation rather than
     an accident of the machine.

Usage: probe-claude-delivery.py [--timeout SEC] [--keep]

Exits non-zero if any check fails.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

# The shipped invocation, and the three lists below are the reason this file is
# not free to drift from the adapter: a probe that measured flags uhpd does not
# send would verify nothing about uhpd.
# TestClaudeProbeRunsTheShippedInvocation reads HARNESS_ARGV and
# TestClaudeDeliveryProbeRunsTheShippedFlags reads the other two, each against
# the hook it mirrors in internal/harness/claude.go.
HARNESS_ARGV = [
    "claude",
    "-p",
    "--output-format",
    "stream-json",
    "--verbose",
    "--include-partial-messages",
    "--strict-mcp-config",
]

# CLIHarness.MCPArgs and CLIHarness.DisallowArgs, with the run-time values left
# as placeholders. The Go test substitutes the same two strings.
MCP_ARGV = ["--mcp-config", "<config>"]
DISALLOW_ARGV = ["--disallowedTools", "<tools>"]

# Scaffolding, and deliberately not part of any list above: uhpd never sends
# `--allowedTools`. A tool call in `-p` mode needs a permission decision and
# there is no one to ask, so without this the control run fails for a reason
# that has nothing to do with the flag under test — and a control that cannot
# succeed cannot support the conclusion the blocked run draws from it.
ALLOW_FLAG = "--allowedTools"

BLOCKED_TOOL = "Bash"
NONCE = "UHP-PROBE-D19"
BASH_PROMPT = (f"Use the {BLOCKED_TOOL} tool to run exactly: echo {NONCE}\n"
               "Then reply with only that command's output.")

# Named as uhpd would name them: sanitizeSkillName strips anything outside
# [A-Za-z0-9._-], and the tool id the model sees is mcp__<server>__<tool>.
CONFIGURED_SERVER = "uhpconfigured"
CONFIGURED_SECRET = "CONFIGURED-SECRET-4711"
HOST_SERVER = "uhphostonly"
HOST_SECRET = "HOST-SECRET-9922"


def load_probe_server():
    """Import the sibling MCP server by path; its name is not an identifier."""
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mcp-probe-server.py")
    spec = importlib.util.spec_from_file_location("uhp_mcp_probe_server", path)
    if spec is None or spec.loader is None:
        raise SystemExit(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Run:
    """One `claude` invocation, parsed into the handful of facts under test."""

    def __init__(self, label: str, extra: list[str], prompt: str, cwd: str, timeout: float,
                 base: list[str] | None = None):
        self.label = label
        self.argv = (HARNESS_ARGV if base is None else base) + extra
        proc = subprocess.run(self.argv, input=prompt, cwd=cwd, text=True,
                              capture_output=True, timeout=timeout)
        self.returncode = proc.returncode
        self.stderr = proc.stderr
        self.tools: list[str] = []
        self.mcp_servers: list[dict] = []
        self.tool_calls: list[str] = []
        self.tool_results: list[str] = []
        self.answer = ""
        self.is_error = None
        self.failed_to_start = False

        for line in proc.stdout.splitlines():
            if not line.strip():
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            if not isinstance(ev, dict):
                continue
            typ = ev.get("type")
            if typ == "system" and ev.get("subtype") == "init":
                self.tools = ev.get("tools") or []
                self.mcp_servers = ev.get("mcp_servers") or []
            elif typ == "assistant":
                for block in ev.get("message", {}).get("content", []):
                    if block.get("type") == "tool_use":
                        self.tool_calls.append(block.get("name", "?"))
            elif typ == "user":
                # What a tool actually returned, which is not the same question
                # as what the model then said. A refused run quotes the command
                # it was asked to run — nonce and all — while explaining that it
                # cannot, so the answer text cannot tell "it ran" from "it
                # repeated the prompt". A tool result can.
                content = ev.get("message", {}).get("content")
                for block in content if isinstance(content, list) else []:
                    if block.get("type") == "tool_result":
                        self.tool_results.append(json.dumps(block.get("content", "")))
            elif typ == "result":
                self.is_error = ev.get("is_error")
                self.answer = ev.get("result") or ""

        # A rejected flag never reaches the model: claude prints a usage error
        # and exits without emitting a single stream line. Distinguished from a
        # failed run because the two want different fixes, and because "the CLI
        # does not know this flag" is the failure #19 was written to rule out.
        self.failed_to_start = self.is_error is None and not self.tools

    def ran(self, nonce: str) -> bool:
        """Did a tool actually produce this string?"""
        return any(nonce in r for r in self.tool_results)

    def note(self) -> str:
        return (f"{self.label}: exit {self.returncode} · is_error={self.is_error} · "
                f"{BLOCKED_TOOL} offered={BLOCKED_TOOL in self.tools} · "
                f"tool calls {self.tool_calls or '[]'} · answer {self.answer[:60]!r}")


def contacts(logfile: str) -> list[dict]:
    """Every request the probe server recorded, or none if it was never hit."""
    if not os.path.exists(logfile):
        return []
    with open(logfile) as f:
        return [json.loads(line) for line in f if line.strip()]


def mcp_document(server: str, port: int, token: str) -> dict:
    """The document uhpd generates, in the shape writeMcpConfig writes it.

    Kept identical to internal/service/harness_runtime.go on purpose: a probe
    that handed the CLI a differently-shaped file would prove the flag works on
    a document uhpd never produces.
    """
    return {"mcpServers": {server: {
        "type": "http",
        "url": f"http://127.0.0.1:{port}/",
        "headers": {"Authorization": f"Bearer {token}"},
    }}}


def check_tool_block(work: str, timeout: float, problems: list[str], notes: list[str]) -> None:
    control = Run("tool control", [ALLOW_FLAG, BLOCKED_TOOL], BASH_PROMPT, work, timeout)
    notes.append(control.note())
    if control.failed_to_start:
        problems.append(f"the control run never started — claude rejected "
                        f"{control.argv}: {control.stderr.strip()[:200]}")
        return
    if not control.ran(NONCE) or BLOCKED_TOOL not in control.tool_calls:
        problems.append(
            f"the control run did not use {BLOCKED_TOOL}, so the blocked run below proves "
            f"nothing: a tool that goes unused when permitted is not evidence of a block. "
            f"calls={control.tool_calls} results={control.tool_results[:2]}")
        return

    blocked = Run("tool blocked", [ALLOW_FLAG, BLOCKED_TOOL] +
                  substitute(DISALLOW_ARGV, {"<tools>": BLOCKED_TOOL}),
                  BASH_PROMPT, work, timeout)
    notes.append(blocked.note())
    if blocked.failed_to_start:
        problems.append(f"claude rejected {DISALLOW_ARGV[0]}: the spelling is wrong and every "
                        f"task with disabled_tools fails to start: {blocked.stderr.strip()[:200]}")
        return
    if BLOCKED_TOOL in blocked.tools:
        problems.append(f"{DISALLOW_ARGV[0]} was accepted but {BLOCKED_TOOL} is still in the "
                        f"session's tool list — the model is offered a tool the operator "
                        f"switched off")
    if BLOCKED_TOOL in blocked.tool_calls:
        problems.append(f"{BLOCKED_TOOL} was called despite the block: {blocked.tool_calls}")
    if blocked.ran(NONCE):
        problems.append(f"a tool returned {NONCE!r} in the blocked run, so the command ran "
                        f"anyway: {blocked.tool_results[:2]}")
    if BLOCKED_TOOL not in blocked.tools and BLOCKED_TOOL not in blocked.tool_calls:
        notes.append(f"  → {BLOCKED_TOOL} is not offered at all under the block, and the "
                     f"model says so rather than being refused mid-call")


def check_mcp(work: str, mod, timeout: float, problems: list[str], notes: list[str]) -> None:
    configured_log = os.path.join(work, "configured-contacts.jsonl")
    host_log = os.path.join(work, "host-contacts.jsonl")
    _, configured_port = mod.start(CONFIGURED_SERVER, configured_log, CONFIGURED_SECRET)
    _, host_port = mod.start(HOST_SERVER, host_log, HOST_SECRET)

    config_path = os.path.join(work, "mcp.json")
    with open(config_path, "w") as f:
        json.dump(mcp_document(CONFIGURED_SERVER, configured_port, "probe-token"), f, indent=2)

    # Stands in for a configuration uhpd did not write and did not authorise:
    # the operator's own user-scope servers, a plugin's, or — as here — the
    # working directory's. Whatever the harness disabled looks exactly like
    # this from the CLI's side, which is why §4.1 is not satisfied by filtering
    # the generated document alone.
    with open(os.path.join(work, ".mcp.json"), "w") as f:
        json.dump(mcp_document(HOST_SERVER, host_port, "host-token"), f, indent=2)

    # Both the prompt and the permission come from the server module rather
    # than from a string here: a renamed tool would otherwise fail check 3 as
    # though `--mcp-config` had stopped working.
    prompt = f"Call the tool {mod.TOOL_NAME} and reply with only the text it returns."
    mcp_args = substitute(MCP_ARGV, {"<config>": config_path})
    allow = [ALLOW_FLAG, f"mcp__{CONFIGURED_SERVER}__{mod.TOOL_NAME}"]

    run = Run("mcp configured", mcp_args + allow, prompt, work, timeout)
    notes.append(run.note())
    if run.failed_to_start:
        problems.append(f"claude rejected {MCP_ARGV[0]}: the spelling is wrong and every task "
                        f"with mcp_servers fails to start: {run.stderr.strip()[:200]}")
        return

    seen = contacts(configured_log)
    methods = [c.get("method") for c in seen]
    if "tools/call" not in methods:
        problems.append(f"the configured server was not called: it saw {methods or 'nothing'}. "
                        f"An advertised tool is not a reached one.")
    elif CONFIGURED_SECRET not in run.answer:
        problems.append(f"the server was called but its answer did not reach the model: "
                        f"{run.answer[:120]!r}")
    else:
        notes.append(f"  → configured server reached: {', '.join(dict.fromkeys(methods))}")

    # The credential is the difference between "connected" and "connected as
    # the configured principal", and it is the field writeMcpConfig exists to
    # place. A server that answers anonymous requests would hide its loss.
    if seen and not any(c.get("authorization") == "Bearer probe-token" for c in seen):
        problems.append("the generated Authorization header never arrived: the `auth` field "
                        "of an MCP server is written to the document and dropped on the way")

    isolated = contacts(host_log)
    if isolated:
        problems.append(
            f"§4.1: a server the generated document does not name was contacted anyway — "
            f"{[c.get('method') for c in isolated]}. Anything the harness disabled is "
            f"reachable the same way, and its operator learns the turn happened.")
    elif any(s.get("name") == HOST_SERVER for s in run.mcp_servers):
        problems.append(f"{HOST_SERVER} is connected in the init event without having been "
                        f"contacted — read the probe server, one of these is lying")
    else:
        notes.append(f"  → unnamed server never contacted; init lists only "
                     f"{[s.get('name') for s in run.mcp_servers]}")

    # And the same run without the isolation flag, so a pass above is a
    # property of the invocation rather than of a machine that happened to have
    # no other MCP configuration.
    open(host_log, "w").close()
    loose = Run("mcp without --strict-mcp-config", mcp_args + allow, prompt, work, timeout,
                base=[a for a in HARNESS_ARGV if a != "--strict-mcp-config"])
    notes.append(loose.note())
    leaked = contacts(host_log)
    if loose.failed_to_start:
        # An empty contact log means "not contacted" only for a run that
        # happened. A run that never started — an expired login, a usage limit,
        # a rejected flag — leaves the same empty file, and reading it as
        # isolation would turn an outage into a conclusion about §4.1.
        problems.append(f"the isolation control never started, so the empty log below says "
                        f"nothing: {loose.stderr.strip()[:200]}")
    elif leaked:
        notes.append(f"  → without --strict-mcp-config the same server is contacted "
                     f"({', '.join(dict.fromkeys(c.get('method') for c in leaked))}), which is "
                     f"what the flag is holding back")
    else:
        problems.append(
            "dropping --strict-mcp-config changed nothing, so the isolation above was not "
            "this invocation's doing. Either the working directory's .mcp.json is no longer "
            "read in -p mode — in which case this probe has stopped measuring §4.1 and needs "
            "a different stand-in for a host configuration — or the flag is now the default. "
            "The contact log is empty after a run that did reach the model.")


def substitute(argv: list[str], values: dict[str, str]) -> list[str]:
    """Fill a pinned flag list's placeholders. The list is compared to the Go
    declaration verbatim, so it holds `<config>`/`<tools>` rather than being
    built here."""
    return [values.get(a, a) for a in argv]


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__.strip().splitlines()[0])
    ap.add_argument("--timeout", type=float, default=180.0)
    ap.add_argument("--keep", action="store_true",
                    help="keep the working directory, contact logs included")
    args = ap.parse_args(argv[1:])

    mod = load_probe_server()
    version = subprocess.run(["claude", "--version"], capture_output=True, text=True)
    print(f"claude {version.stdout.strip() or '(version unknown)'}")
    print(f"argv: {' '.join(HARNESS_ARGV)} [+ {' '.join(MCP_ARGV)}] [+ {' '.join(DISALLOW_ARGV)}]")
    print()

    # A directory of its own, not the repository: run 4 writes a `.mcp.json`
    # that a later `claude` in this checkout would pick up, and the contact
    # logs are evidence for one run only.
    work = tempfile.mkdtemp(prefix="uhp-claude-delivery-")
    started = time.monotonic()
    problems: list[str] = []
    notes: list[str] = []
    try:
        check_tool_block(work, args.timeout, problems, notes)
        check_mcp(work, mod, args.timeout, problems, notes)
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
        print("claude delivery probe FAILED")
        for p in problems:
            print(f"  - {p}")
        return 1
    print("claude delivery probe ok — the tool block removes the tool, the configured MCP "
          "server is reached, and nothing else is")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
