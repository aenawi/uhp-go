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

**Last measured score: `core` 37/37 — CONFORMANT (UHP 2026-08-11, class core).**
Details, reproduction steps and the remaining gap: [docs/conformance.md](docs/conformance.md).
Across all three classes, that run measured **42/52** (`extended` 42/45).

File support — input items, artifact capture and artifact download — landed after that
run and has **not been re-measured**. The four checks it targets (`X-05`…`X-08`) are
covered by this repository's own tests, but the published suite has not been run against
it yet, so the score above is still the honest one to quote. The areas that remain
unimplemented are harness management, skills and MCP.

This server does not claim `extended` or `full`, and its discovery document reports the
capabilities it does not implement as `false` rather than omitting them.

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
internal/transport/http/   UHP wire format: discovery, tasks, streaming (SSE), cancellation,
                           input items, artifact listing and download
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
| `GET /v1/sessions` | List sessions; cursor paging via `limit`, `cursor`, `harness` |
| `GET /v1/sessions/{id}` | One session |
| `GET /v1/sessions/{id}/turns` | A session's ordered task history |
| `GET /v1/sessions/{id}/files` | Every artifact of a session, including earlier tasks' |
| `GET /v1/sessions/{id}/files/archive` | The same artifacts as one zip |
| `POST /v1/sessions/{id}/cancel` | Stop whatever is running in a session |
| `POST /v1/responses` | Create a task (`stream:true` for SSE, else blocks until terminal) |
| `GET /v1/responses/{id}` | Retrieve a task's current state and output |
| `POST /v1/responses/{id}/cancel` | Cancel an in-flight task |
| `POST /v1/files` | Upload a file for use as task input (`multipart/form-data`) |
| `GET /v1/containers/{cid}/files/{fid}/content` | Download an artifact as raw bytes |
| `GET /v1/containers/{cid}/files/{fid}/pdf` | Rendered preview — always `501 preview_unavailable` |
| `GET /healthz` | Liveness probe |

Not implemented, and reported as `false` in the discovery document: harness management,
skills, MCP. `files_input` and `files_output` are reported as `true` only when
`UHP_WORKSPACE` is set, because both need a per-session working directory — see
[Files](#files).

Harness ids are `chrn_`-prefixed and derived deterministically from the base name, so they
survive a restart. The friendly base name is accepted as an alias wherever a harness id is
expected, so `{"harness_id": "claude-code"}` works as well as the canonical form.

Request body is intentionally OpenAI-Responses-shaped (`input`, `model`, `stream`,
`previous_response_id`, `metadata`), with `metadata.harness_id` as the UHP extension that
selects which harness runs the task. Continuing a conversation is done by setting
`previous_response_id` to a prior task's `id` — the router resolves the underlying session
and, where the harness supports it, its native session/thread id (`--resume`, `--session`, etc.).

## Files

A harness that can only return text is a chatbot. `uhpd` implements the UHP "Files"
chapter: files in as task input, files out as session artifacts.

**Set `UHP_WORKSPACE`.** Both halves need a per-session working directory: without one
there is nowhere to put a client's file, and nothing to diff for artifacts. Discovery
reports `files_input`/`files_output` as `false` when it is unset, and a task carrying a
file is refused with `501` rather than having its attachment silently dropped.

### In

`input` accepts a bare string or an array of items. A file is inlined as a data URL, or
uploaded once and referenced by id:

```bash
# Inline
curl -s http://localhost:8080/v1/responses -H "Authorization: Bearer devkey" \
  -H "Content-Type: application/json" -d '{
    "input": [{"role":"user","content":[
      {"type":"input_text","text":"Summarise this."},
      {"type":"input_file","filename":"q3.txt","file_data":"data:text/plain;base64,cTMK"}]}],
    "metadata": {"harness_id":"codex"}}'

# Upload once, reference by id
curl -s -F file=@q3.pdf http://localhost:8080/v1/files -H "Authorization: Bearer devkey"
# → {"id":"file_…"} → {"type":"input_file","file_id":"file_…"}
```

A file must arrive as a data URL or as an uploaded `file_id`: a bare base64 string is
refused, because ordinary text is often valid base64 and decoding it would hand the
harness a different file than the client sent. An item whose `role` is anything but
`user` is refused too — everything in `input` becomes one prompt, so an `assistant` item
would silently become user text; prior conversation belongs in `previous_response_id`.

Attachments are written into the session's working directory under a sanitised name, and
the prompt is appended with a line naming them — no CLI harness has a generic "attach this
file" flag, and a file the model is never told about is a file it will not read. Remote
`image_url`s are refused rather than fetched: this server opens no outbound connections of
its own.

### Out

There is no harness that reports the files it wrote, so artifacts are captured by diffing
the session's working directory across a run: anything new or modified is an artifact of
that session's container. Files the router itself wrote as task input are fingerprinted
first and never come back as output, symlinks are never followed or captured, and
dot-directories (`.git`) are skipped. Capture is bounded at 200 files per task, and a
truncated capture is logged rather than silently trimmed.

Artifacts are reported twice, as the specification requires: as
`container_file_citation` annotations on the assistant message, and by
`GET /v1/sessions/{id}/files` — which lists every artifact of the session, including
earlier tasks'.

### Download safety

Artifact ids are opaque digests, not paths, so resolving one is a lookup in records this
server wrote rather than a path join of client input; the resolved path is then checked to
be inside its container anyway. Downloads are served as raw bytes with
`X-Content-Type-Options: nosniff` and a `Content-Disposition` filename: an artifact is
content an agent can be persuaded to write, and serving it without `nosniff` turns it into
stored XSS against the client's own origin. A path containing a `.` or `..` segment is
refused before routing rather than redirected to a cleaned one.

Artifacts are reachable only through their session's records, so an artifact of a session
this server no longer has is a 404 — which is what the specification asks for when a
session is deleted. There is no `DELETE /v1/sessions/{id}` yet, so that is a property of
the lookup rather than an endpoint you can exercise. Access is scoped to the server's
single principal: every configured `UHP_API_KEYS` value is equivalent and carries no
identity, so a deployment serving several tenants needs a principal on the credential
before artifact lookup can filter by one.

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
| `UHP_WORKSPACE` | (unset = router's own cwd, and no file support) | Root for per-session working directories |
| `UHP_MAX_BODY_BYTES` | `8388608` | Maximum accepted request body, and the upload limit |
| `UHP_PUBLIC_URL` | (unset = relative URLs) | Origin used to build absolute artifact download URLs |
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
