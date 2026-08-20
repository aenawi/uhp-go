# uhp-go

A Go implementation of a **UHP (Unified Harness Protocol)** server: one HTTP
contract to drive Claude Code, Codex CLI, Grok CLI, OpenCode, and Pi (or any
future CLI/SDK agent harness) through the same task/session/streaming/files
API — modeled on the OpenAI Responses API and specified at
https://unifiedharnessprotocol.org.

## Why

Every product that embeds an agent harness re-implements: start a task,
follow progress, continue the conversation, cancel it, get produced files,
and understand failures — once per harness. This router answers those
questions once, behind a single conformant HTTP surface, and lets you swap
or add harnesses without touching client code.

## Runs entirely offline

`uhpd` never calls home. It opens no outbound network connections of its own:
no telemetry, no licence check, no account, no hosted dependency. The only
processes it talks to are the harness CLIs you have installed locally, over
stdin/stdout. The single non-stdlib dependency is `github.com/google/uuid`.

This is a property of the protocol, not just of this implementation — the UHP
specification states that "nothing in the wire format requires a hosted
service, an account, a licence key, or a call home."

`UHP_API_KEYS` is **inbound** authentication: the list of bearer tokens *this*
server accepts from *its* clients. You invent the values. It is not a
credential issued by anyone, and it is unrelated to any hosted UHP service.

## Relationship to UHP

The Unified Harness Protocol is an open specification, published under
Apache-2.0 and led by HarnessRouter, who also ship a reference implementation
(HarnessRouter Community Edition) and a commercial hosted product
(HarnessRouter Cloud).

**`uhp-go` is an independent implementation and is not affiliated with,
endorsed by, or supported by HarnessRouter.** It aims to be conformant to the
published specification. Where this implementation and the specification
disagree, treat it as a bug in this implementation and please open an issue —
if the specification itself looks wrong or ambiguous, the issue is still the
right place to start, and we will raise it upstream.

## Conformance status

UHP conformance is defined by a runnable suite, not by self-assessment. The suite lives
in the protocol repository and anyone can run it against this server:

```bash
pip install -e protocol/conformance
uhp-conformance --base-url http://localhost:8080 --api-key "$UHP_API_KEY" --class full
```

**Current score: `core` 37/37 — CONFORMANT (UHP 2026-08-11, class core).**
Details, reproduction steps and the remaining gap: [docs/conformance.md](docs/conformance.md).
Across all three classes: **38/52**, with the remaining 14 in `extended` and `full`
(session listing and inspection, file input, artifacts, harness management, skills, MCP).
This server does not claim `extended` or `full`, and its discovery document reports those
capabilities as `false` rather than omitting them.

A skip is counted as a failure here, not as a pass.

## Architecture

Layered, dependency-inverted design (Clean/Hexagonal architecture):

```
cmd/uhpd/                  composition root (main.go) — the only file wiring concrete types together
internal/domain/           entities: Task, Harness, Session, Artifact, Event — no external deps
internal/harness/          the adapter contract, the shared subprocess runner, the
                           registry, and one ~30-line declaration per harness
internal/service/          application core: TaskService; declares the Registry and Store
                           interfaces it consumes (deps.go), holds all business rules
internal/store/            service.Store implementations — in-memory today, disk-backed later
internal/transport/http/   UHP wire format: discovery, tasks, streaming (SSE), cancellation
internal/config/           environment-variable configuration loader
```

### Design notes

- A harness is declared as data, not written as code: `internal/harness/<name>.go` is a
  `CLIHarness` literal naming the binary, models, capabilities, argv, and line parser.
- Everything that must never be forgotten when adding a harness — process-group
  isolation, prompt delivery that cannot be re-parsed as options, model validation,
  scanner limits — lives once in the shared runner.
- In-memory store by default, behind an interface — no database required to run or test.
- Plain `net/http` with Go 1.22 method/path routing. No framework.

## UHP surface implemented

| Endpoint | Purpose |
|---|---|
| `GET /v1/uhp` | Capability discovery. Unauthenticated by design |
| `GET /v1/harnesses` | List configured harnesses |
| `GET /v1/harnesses/{id}` | One harness |
| `GET /v1/harnesses/{id}/models` | Models for one harness, with computed availability |
| `GET /v1/models` | Model catalogue by backend |
| `POST /v1/responses` | Create a task (`stream:true` for SSE, else blocks until terminal) |
| `GET /v1/responses/{id}` | Retrieve a task's current state and output |
| `POST /v1/responses/{id}/cancel` | Cancel an in-flight task |
| `GET /healthz` | Liveness probe |

Not implemented, and reported as `false` in the discovery document: session listing and
inspection, file input, artifact download, harness management, skills, MCP.

Harness ids are `chrn_`-prefixed and derived deterministically from the base name, so they
survive a restart. The friendly base name is accepted as an alias wherever a harness id is
expected, so `{"harness_id": "claude-code"}` works as well as the canonical form.

Request body is intentionally OpenAI-Responses-shaped (`input`, `model`, `stream`,
`previous_response_id`, `metadata`), with `metadata.harness_id` as the UHP extension that
selects which harness runs the task. Continuing a conversation is done by setting
`previous_response_id` to a prior task's `id` — the router resolves the underlying session
and, where the harness supports it, its native session/thread id (`--resume`, `--session`, etc.).

## Running

```bash
go build -o bin/uhpd ./cmd/uhpd
UHP_API_KEYS=devkey ./bin/uhpd
```

Create a task:

```bash
curl -s http://localhost:8080/v1/responses \
  -H "Authorization: Bearer devkey" -H "Content-Type: application/json" \
  -d '{"input":"Summarise README.md in three bullets.","model":"claude-sonnet-4.6","metadata":{"harness_id":"claude-code"}}'
```

Stream it:

```bash
curl -N http://localhost:8080/v1/responses \
  -H "Authorization: Bearer devkey" -H "Content-Type: application/json" \
  -d '{"input":"...","model":"gpt-5.2-codex","metadata":{"harness_id":"codex"},"stream":true}'
```

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `UHP_ADDR` | `:8080` | HTTP listen address |
| `UHP_API_KEYS` | (unset = auth disabled) | Comma-separated bearer tokens this server accepts |
| `UHP_WORKSPACE` | (unset = router's own cwd) | Root for per-session working directories |
| `UHP_MAX_BODY_BYTES` | `8388608` | Maximum accepted request body |
| `UHP_CLAUDE_MODELS` | `claude-sonnet-4.6,claude-opus-4.6` | Advertised Claude Code models |
| `UHP_CODEX_MODELS` | `gpt-5.2-codex` | Advertised Codex models |
| `UHP_GROK_MODELS` | `grok-4.6,grok-4.5` | Advertised Grok models |
| `UHP_OPENCODE_MODELS` | `auto` | Advertised OpenCode models |
| `UHP_PI_MODELS` | `auto` | Advertised Pi models |

Each harness adapter shells out to its respective CLI binary (`claude`, `codex`, `grok`,
`opencode`, `pi`), which must be installed and authenticated on the host/container running
`uhpd`.

## Extending with a new harness

1. Create `internal/harness/<name>.go` returning a `*CLIHarness` literal: id, binary,
   models, capabilities, `Prompt` mode, `BuildArgs`, `ParseLine`.
2. Add it to the slice in `cmd/uhpd/main.go`.
3. Add a `BuildArgs` case to the table in `internal/harness/cli_test.go`.

**Verify the CLI by running it.** `Prompt: PromptStdin` is correct only if the CLI
actually reads a prompt from stdin — grok does not, and a `--` terminator that works for
claude and codex does not work for grok or pi. Every one of those facts was established
by executing the CLI, and none of them is guessable from its `--help`.

## Testing

```bash
make test   # go test ./... -race -cover
make vet
make fmt
```

## Building the image

```bash
make docker
```

The image installs `@anthropic-ai/claude-code` and `opencode-ai` via npm as examples;
add `codex`, `grok`, and `pi` install steps as those CLIs become available in your
environment.

## Contributing

Work is tracked as [GitHub issues](https://github.com/aenawi/uhp-go/issues). Issues carry
triage labels described in [docs/agents/triage-labels.md](docs/agents/triage-labels.md);
`ready-for-agent` marks work that is specified well enough to hand to an agent, and
`ready-for-human` marks work needing a product decision.

Security issues go through private reporting instead — see [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
