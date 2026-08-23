#!/usr/bin/env python3
"""A one-tool MCP server over streamable HTTP that records every contact.

It exists for `probe-claude-delivery.py`, and the recording is the whole point.
Harnesses §4.1 says a disabled MCP entry "MUST NOT be contacted at all — not
connected and then hidden, which would still leak the turn's existence to
whoever operates that endpoint". "Not hidden but connected" is invisible from
the agent's side: the model is simply never shown the tool. The only place the
difference is legible is at the endpoint, so this writes one JSON line per
request and the probe reads that file.

The transport matches what uhpd actually generates. `writeMcpConfig` in
internal/service/harness_runtime.go writes `{"type":"http"|"sse","url":…,
"headers":{…}}`, materialising a server's `auth` as an `Authorization` header —
so this speaks streamable HTTP and logs the header it was sent, which is what
turns "the CLI connected" into "the CLI connected as the configured
principal".

Usage: mcp-probe-server.py <name> <port> <logfile> <secret>

The secret is the tool's entire output. A model can describe a tool it was
merely shown; it can only repeat this if the call actually happened.
"""
from __future__ import annotations

import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# The MCP revision this server claims. Claude Code negotiates down to what the
# server offers, so a fixed value here keeps the probe from drifting with the
# CLI — and a version it refuses outright would show up as a failed connection
# rather than a silently wrong verdict.
PROTOCOL_VERSION = "2025-06-18"

TOOL_NAME = "uhp_probe_secret"


class Recorder:
    """One append-only log of contacts, shared by the handler threads."""

    def __init__(self, name: str, path: str) -> None:
        self.name = name
        self.path = path
        self.lock = threading.Lock()

    def write(self, **fields) -> None:
        # Flushed per line, not buffered: the probe reads this file while the
        # server is still running, and a contact still sitting in a buffer
        # reads exactly like a contact that never happened — which is the one
        # conclusion this file exists to support.
        with self.lock, open(self.path, "a") as f:
            f.write(json.dumps({"server": self.name, **fields}) + "\n")
            f.flush()


def make_handler(rec: Recorder, secret: str):
    class Handler(BaseHTTPRequestHandler):
        # Keep-alive, so the CLI's connection reuse behaves as it would against
        # a real server rather than forcing a reconnect per request.
        protocol_version = "HTTP/1.1"

        def log_message(self, *args):
            """Silence the default stderr access log; the recorder is the log."""

        def _respond(self, code: int, payload: dict | None, session: bool = False) -> None:
            body = json.dumps(payload).encode() if payload is not None else b""
            self.send_response(code)
            if body:
                self.send_header("Content-Type", "application/json")
            if session:
                self.send_header("Mcp-Session-Id", f"{rec.name}-session")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if body:
                self.wfile.write(body)

        # Every verb is recorded, not just POST. A client that only opened the
        # GET stream still contacted the endpoint, and §4.1 counts that.
        def do_GET(self):
            rec.write(method="GET", path=self.path)
            self._respond(405, None)

        def do_DELETE(self):
            rec.write(method="DELETE", path=self.path)
            self._respond(200, None)

        def do_POST(self):
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length).decode() if length else ""
            try:
                req = json.loads(raw)
            except ValueError:
                rec.write(method="<unparsed>", body=raw[:200])
                self._respond(400, {})
                return

            method = req.get("method")
            rec.write(method=method, authorization=self.headers.get("Authorization"))

            if method and method.startswith("notifications/"):
                self._respond(202, None)
                return

            handlers = {
                "initialize": lambda: {
                    "protocolVersion": PROTOCOL_VERSION,
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": rec.name, "version": "0.0.1"},
                },
                "tools/list": lambda: {"tools": [{
                    "name": TOOL_NAME,
                    "description": "Return this probe server's secret string.",
                    "inputSchema": {"type": "object", "properties": {}},
                }]},
                "tools/call": lambda: {
                    "content": [{"type": "text", "text": secret}],
                    "isError": False,
                },
                "ping": dict,
            }
            build = handlers.get(method)
            if build is None:
                self._respond(200, {"jsonrpc": "2.0", "id": req.get("id"),
                                    "error": {"code": -32601, "message": f"no {method}"}})
                return
            self._respond(200, {"jsonrpc": "2.0", "id": req.get("id"), "result": build()},
                          session=True)

    return Handler


def start(name: str, logfile: str, secret: str) -> tuple[ThreadingHTTPServer, int]:
    """Serve on a free loopback port in a background thread, returning the port.

    Port 0 rather than a chosen number, and returned rather than guessed: the
    probe runs several of these at once and a port collision would look exactly
    like the failure it is trying to measure — a server that was never
    contacted.
    """
    server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(Recorder(name, logfile), secret))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, server.server_address[1]


def main(argv: list[str]) -> int:
    if len(argv) != 5:
        print(__doc__, file=sys.stderr)
        return 2
    _, name, port, logfile, secret = argv
    rec = Recorder(name, logfile)
    server = ThreadingHTTPServer(("127.0.0.1", int(port)), make_handler(rec, secret))
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
