# Testing

What runs for free on every push, and what costs real tokens and has to be run on purpose.

```bash
make test   # go test ./... -race -cover
make vet
make fmt
```

Run those four before a push automatically:

```bash
make hooks   # git config core.hooksPath .githooks
```

`.githooks/pre-push` builds, vets, checks formatting and runs the tests — about twenty
seconds, no tokens. It is the same set CI runs, so a failure here is a red build you did not
have to wait for. Bypass a single push with `git push --no-verify`.

`go test ./...` also includes the end-to-end tests in
`internal/transport/http/client_end_to_end_test.go`, which are the only ones here that speak
UHP **over a socket**. Everything else calls a handler directly, so both halves of the wire
format are otherwise only ever checked against bytes their own package wrote —
`docs/conformance.md` names that gap for SSE framing, and it was just as real for headers,
status codes, the error envelope and the version handshake. These drive a real listener with
the published `uhp.Client`: the same code an external consumer imports, not a test-local HTTP
client that would prove nothing about what ships.

`go test ./...` includes `uhp/schema_test.go`, which marshals every published type and
validates it against the vendored copy of `uhp-2026-08-11.schema.json` — and fails if the
schema defines an object no Go type mirrors. It is the only thing in the repository that
checks the types against the specification rather than against each other, and it is free.
It does not overlap with the conformance gate below: that one proves *this server*
conformant end to end and never looks at the Go types, so neither would catch what the other
does. See [docs/conformance.md](conformance.md).

The conformance gate is separate and is *not* in that hook, because it spends real tokens on
real agent tasks. It needs the suite installed and a running server, and it is a thing you
run on purpose before merging something that could move the score — see
[docs/conformance.md](conformance.md) for when, and for the pinned suite revision.

```bash
UHP_API_KEY=devkey UHP_HARNESS_ID=chrn_… make conformance-gate
```

Two Claude Code probes sit beside it, for the same reason and with the same schedule —
run them after every Claude Code upgrade. Both need a logged-in `claude` and neither can
be a `go test`, which is exactly how the claims they check went unverified for as long as
they did.

```bash
make capture-claude          # what the CLI streams back  (#32)
make probe-claude-delivery   # what it does with the configuration (#19)
```

The first runs the shipped invocation and checks the stream against what `parseClaudeLine`
assumes — the failure it exists for is silent, an empty answer reported as a success. The
second checks enforcement: that a blocked tool is really gone, that a configured MCP server
is really reached as the configured principal, and that nothing else is. It starts its own
MCP endpoints on loopback and needs no network. Both spend a few tokens; neither is in the
pre-push hook.

A third probe covers `pi`, and this one costs nothing at all — run it after every pi
upgrade:

```bash
make probe-pi                # streaming, --session-id resume, exit-0 on failure (#33)
```

`pi` reads a `models.json` that can declare a provider outright, base URL included, so the
probe answers from a loopback server of its own: no credentials, no network, no tokens, and
it finishes in seconds. It checks the same silent failure `capture-claude` does — that the
answer really arrives as `message_update`/`text_delta` — and then the half a capture cannot
reach, that `--session-id` really resumes, by reading the conversation history off the
request the resumed turn sent. Everything it touches lives in a temporary
`PI_CODING_AGENT_DIR`, so the machine's own pi sessions and credentials are untouched.
Being free of credentials, it is the one probe here that could reasonably move into CI
alongside a `pi` install.

Two more cover `codex` and `grok` (#34), and these are back to costing real tokens: neither
CLI takes a per-run base URL, so neither can be pointed at a loopback provider the way `pi`
can.

```bash
make probe-codex             # stdin delivery, argv injectability, `--`, resume (#34)
make probe-grok              # argv delivery, `--`, resume, the streaming format (#34)
make probes                  # every probe above, in one command
```

Nothing in `codex.go` or `grok.go` was ever marked UNVERIFIED — every claim in both said
"verified by execution", and none of them said against what. Issue #13 is why that is not
the same thing: two of opencode's execution-verified claims were true when written and
false by 1.18.21, and nothing in the tests noticed. **Verification has a shelf life.** Run
both probes after every `codex` or `grok` upgrade; each prints the version it ran against.

`probe-grok` is also the worked example of a control that passes for the wrong reason. The
obvious test of resume — ask a second turn without `--resume` and expect it not to know the
word — reported success while proving nothing: grok has a shell and a file reader, and the
control found the word by reading the probe's own captures off disk. The evidence is now
`grok export <id>`, the session's own transcript, and every capture is written outside the
directory the CLI is given.
