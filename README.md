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

File support (issue #2) and harness management with skills, MCP and tool restrictions
(issues #3 and #4) both landed after that run and have **not been re-measured**. The
checks they target — `X-05`…`X-08` and `F-01`…`F-07` — are covered by this repository's
own tests, but a passing local test is not a conformance result, so the score above is
still the honest one to quote.

This server does not yet claim `extended` or `full` in its discovery document, because
that claim is the suite's to confirm, not this file's. Capabilities it does not implement
are reported as `false` rather than omitted.

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
internal/store/            service.Store and service.HarnessStore implementations — tasks and
                           sessions in memory, created harnesses in one JSON file on disk
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
| `POST /v1/harnesses` | Create a harness — `422 unsupported_base` if the base cannot be run |
| `GET /v1/harnesses/{id}` | One harness |
| `PUT /v1/harnesses/{id}` | Replace a harness's mutable configuration; `id`, `base` and `createdAt` are immutable |
| `PATCH /v1/harnesses/{id}` | Merge into it, leaving unsent fields alone. An extension; §5.2 defines only the PUT |
| `DELETE /v1/harnesses/{id}` | Delete a harness; its sessions and responses are kept |
| `GET /v1/harnesses/{id}/skills/{skill_id}/files` | A skill's complete folder, nested and binary members included |
| `GET /v1/harnesses/{id}/models` | Models for one harness, with computed availability |
| `GET /v1/harnesses/{id}/events` | Live SSE feed of every task on that harness; resumable with `Last-Event-ID` |
| `GET /v1/models` | Model catalogue by backend |
| `GET /v1/sessions` | List sessions; cursor paging via `limit`, `cursor`, `harness` |
| `GET /v1/sessions/{id}` | One session |
| `GET /v1/sessions/{id}/turns` | A session's ordered task history |
| `GET /v1/sessions/{id}/files` | Every artifact of a session, including earlier tasks' |
| `GET /v1/sessions/{id}/files/archive` | The same artifacts as one zip |
| `POST /v1/sessions/{id}/cancel` | Stop whatever is running in a session |
| `POST /v1/responses` | Create a task (`stream:true` for SSE, else blocks until terminal). Honours `Idempotency-Key` |
| `GET /v1/responses/{id}` | Retrieve a task's current state and output |
| `POST /v1/responses/{id}/cancel` | Cancel an in-flight task |
| `POST /v1/files` | Upload a file for use as task input (`multipart/form-data`) |
| `GET /v1/containers/{cid}/files/{fid}/content` | Download an artifact as raw bytes |
| `GET /v1/containers/{cid}/files/{fid}/pdf` | Rendered preview — always `501 preview_unavailable` |
| `GET /healthz` | Liveness probe |

Not implemented, and reported as `false` in the discovery document: session sharing.
`files_input` and `files_output` are computed from configuration rather than asserted —
`true` only when `UHP_WORKSPACE` is set, because both need a per-session working
directory. See [Files](#files) and [Harness management](#harness-management).

Harness ids are `chrn_`-prefixed. The ones this server is started with derive theirs
deterministically from the base name, so they survive a restart; a harness created over
the API is given a random one and kept in the harness store, so it survives a restart too.
The friendly base name is accepted as an alias wherever a harness id is expected, so
`{"harness_id": "claude-code"}` works as well as the canonical form.

Request body is intentionally OpenAI-Responses-shaped (`input`, `model`, `stream`,
`previous_response_id`, `metadata`), with `metadata.harness_id` as the UHP extension that
selects which harness runs the task. Continuing a conversation is done by setting
`previous_response_id` to a prior task's `id` — the router resolves the underlying session
and, where the harness supports it, its native session/thread id (`--resume`, `--session`, etc.).

A stream that has gone 15 seconds with nothing to send writes an SSE comment line
(`: keep-alive`). UHP tells clients to time a stream out on inactivity rather than on total
duration, and an agent that thinks for two minutes before its first token would otherwise
look exactly like a dropped connection. A comment carries no data and is discarded by any
conformant SSE client, so nothing downstream has to know about it.

## Reconnecting

A dropped connection never aborts a task — the supervisor owns the run, and a disconnect
only unsubscribes. Getting the rest of the answer afterwards is what this section is about.

Every event on every stream carries an SSE `id:` line holding its `sequence_number`. Send
that back as `Last-Event-ID` and the stream resumes at the event *after* it; nothing the
client already saw is replayed.

**Following a harness.** `GET /v1/harnesses/{id}/events` is a live feed of every task
running on one harness, not just the one you started:

```bash
curl -N http://localhost:8080/v1/harnesses/claude-code/events \
  -H "Authorization: Bearer devkey" -H "Last-Event-ID: 41"
```

A feed numbers its own stream, because it multiplexes many tasks and each task numbers
from zero — so the ids on a feed are the feed's, not any task's. Each event carries
`response_id` and `session_id` so it can be attributed; a `response.output_text.delta`
names an item, not a response, and two tasks writing at once would otherwise be one
interleaved text with no way to separate them.

A feed keeps a **reconnection window, not a history**: at least the last 512 events per
harness. That covers the seconds between noticing a dead socket and dialling again. Omit
`Last-Event-ID` and you get everything still in the window; send one older than it and the
request is refused with `400 uhpgo_event_gap` and `detail.oldest_retained`, rather than
answered from wherever the log now starts — a silently later event is a gap the client has
no way to see. A subscriber that falls behind *while reading* gets the same thing as an
`error` event before its stream ends — that one carries an empty `id:`, which clears the
client's resume point so its automatic reconnection is served from the window rather than
refused for the same gap forever. An id past the end of the stream is refused too, with
`detail.next_sequence_number`: nobody can have seen an event that was never produced.
Deleting a harness ends the streams following it.

**Following one task.** A task's own log is retained in full for the life of the task, so
there is no window to fall out of. Reconnect by repeating the `POST /v1/responses` with the
original request's `Idempotency-Key` plus a `Last-Event-ID`; the retry starts nothing and
resumes the first request's stream. A `Last-Event-ID` whose key this server does not
remember — absent, or expired — is refused, because that request would start a fresh task
whose stream begins at 0 and skipping into it would swallow the beginning of a stream the
client has never seen.

There is no capability flag for any of this. The capability vocabulary is the
specification's, and inventing a key for it would be a dialect no other implementation
speaks; the `id:` lines on the wire are how a client discovers resumption is on offer,
which is how SSE answers that question everywhere else.

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

## Harness management

A harness is configuration: a name, a base runtime, a default model, a standing prompt,
and — once issue #4 lands — the skills, MCP servers and tool restrictions its agent runs
with. UHP class `full` expects that configuration to be created over HTTP rather than
compiled in.

**Set `UHP_HARNESS_STORE`,** or a `UHP_WORKSPACE` that implies it. Harness management is
always offered, but only durable when one of those is set; with neither, `uhpd` keeps
created harnesses in memory and says so on startup:

```
{"level":"WARN","msg":"harness store not configured; created harnesses will not survive a restart"}
```

That warning is the whole of the notice a client gets, because nothing in the discovery
document can express "this works until the next deploy". A harness a client created,
stored the id of, and came back to after a restart is not configuration if it is gone —
so set the path anywhere you intend the ids to keep resolving. Issue #15's durable engine
implements the same interface and removes the caveat.

```bash
curl -s http://localhost:8080/v1/harnesses   -H "Authorization: Bearer devkey" -H "Content-Type: application/json"   -d '{"name":"Research agent","base":"claude-code","default_model":"claude-sonnet-4.6"}'
```

A base this server cannot run is refused at configuration time:

```json
{"error":{"type":"invalid_request_error","code":"unsupported_base",
  "message":"this server cannot run harness base hermes","param":null,
  "detail":{"supported":["claude-code","codex","grok-cli","opencode","pi"]}}}
```

That refusal is the point of the endpoint, not an edge case. Accepting a base and
discovering it cannot run at the first task fails after the client has already committed
to it, and `detail.supported` is what lets a client recover without guessing.

Three more rules the endpoints enforce rather than assume:

- **`id`, `base` and `createdAt` are immutable.** `PUT` replaces the mutable configuration
  and refuses a body that names a different base with `422`, because changing a base would
  silently change the behaviour of every session already attached to the harness. The
  update verb is `PUT`, not `PATCH`: §5.2 defines a replacement and the conformance suite
  sends one, so `PATCH` answers `405` rather than quietly clearing the fields a
  merge-minded client left out.
- **A skill is a folder and must carry a `SKILL.md`,** rejected at configuration time
  rather than ignored at run time. A member whose path escapes its own folder is refused
  for the same reason, as is a `content_b64` that is not valid base64.
- **An MCP credential is never returned.** `auth` is stored and used, never serialized
  back; a `PUT` that omits it — which is all a client can do, having never been given it —
  carries the stored one forward instead of dropping it.
- **MCP is refused on a base that cannot deliver it.** §4.1 forbids advertising MCP
  support a server cannot provide, so a harness carrying MCP servers on a runtime with no
  per-run mechanism is a `422` rather than a task that quietly runs without them.

Deleting a harness leaves its sessions and responses alone: history that disappears when
configuration changes cannot be audited. The harnesses this server is started with are not
managed over the API, and trying to change or delete one is a `409` rather than a silent
no-op.

### What reaches the agent

A harness's configuration is delivered, not just stored. Before each task the router
writes the enabled skill folders to `<session>/.uhp/skills/<name>/` — the whole folder,
because materialising only `SKILL.md` breaks every skill carrying references, scripts or
data — and the enabled MCP servers to `<session>/.uhp/mcp.json`, with `auth` materialised
as the `Authorization` header that actually connects. A disabled entry of either kind is
never written at all: §4.1 requires that a disabled server not be contacted, and
"connected then hidden" still tells its operator the turn happened.

How much of that the runtime enforces itself differs per base, and this server does not
overstate it:

| Base | Tool block | Skill loading | Per-run MCP |
|---|---|---|---|
| `claude-code` | `--disallowedTools` **(unverified)** | standing instruction | `--mcp-config` **(unverified)** |
| `grok-cli` | `--disallowed-tools` (verified) | standing instruction | none — refused at config time |
| `pi` | `--exclude-tools` (verified) | `--skill` (verified) | none — refused at config time |
| `codex`, `opencode` | standing instruction | standing instruction | none — refused at config time |

"Verified" means the flag was read from that CLI's own `--help` on a machine where it is
installed. The two marked **unverified** are documented for Claude Code but have not been
run against the real binary here, so they carry the same warning issue #13 carries for
opencode's prompt delivery. If either is wrong the task fails to start rather than running
with its tools quietly unblocked, which is the right direction for an unverified claim.

Where a runtime cannot hard-block a tool, the restriction is conveyed as a standing
instruction and described to the model as unenforced — never dropped. §4.3 is explicit
that dropping it is the worst outcome: the operator believes a tool is off, and it is not.

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
| `UHP_HARNESS_STORE` | `$UHP_WORKSPACE/harnesses.json`, or unset = no harness management | Where harnesses created over the API are kept |
| `UHP_MAX_BODY_BYTES` | `8388608` | Maximum accepted request body, and the upload limit |
| `UHP_MAX_CONCURRENT_RUNS` | `8` | Harness processes allowed to run at once; beyond it, `503 harness_unavailable` |
| `UHP_PUBLIC_URL` | (unset = relative URLs) | Origin used to build absolute artifact download URLs |
| `UHP_CLAUDE_MODELS` | `claude-sonnet-4.6,claude-opus-4.6` | Advertised Claude Code models |
| `UHP_CODEX_MODELS` | `gpt-5.2-codex` | Advertised Codex models |
| `UHP_GROK_MODELS` | `grok-4.6,grok-4.5` | Advertised Grok models |
| `UHP_OPENCODE_MODELS` | `auto` | Advertised OpenCode models |
| `UHP_PI_MODELS` | `auto` | Advertised Pi models |

These are the defaults of the `uhpd` binary. The Docker image presets `UHP_WORKSPACE`,
which changes the `UHP_WORKSPACE` and `UHP_HARNESS_STORE` rows above — see
[Building the image](#building-the-image).

Each harness adapter shells out to its respective CLI binary (`claude`, `codex`, `grok`,
`opencode`, `pi`), which must be installed and authenticated on the host/container running
`uhpd`.

### Concurrency

Every accepted task forks one of those CLI processes, so the number of them is bounded:
`UHP_MAX_CONCURRENT_RUNS` at a time, and a request that arrives when they are all busy is
refused with `503`, `code: "harness_unavailable"`, a `Retry-After` header, and the limit in
`detail.max_concurrent_runs`. A 5xx and not a 4xx, because nothing about the request is
wrong — it arrived at a bad moment, and retrying is exactly what will work. `Retry-After` is
a floor, not a prediction: runs last minutes and the server does not know which one ends
first.

Refusals specific to the request are answered before capacity is even considered, so an
unknown `harness_id` or `previous_response_id` still gets its own permanent error rather
than a retryable one it would chase forever.

There is no queue behind it. A task holds its slot for as long as the agent runs, so
queueing would mostly hold connections open until they time out. Refusing immediately tells
the client the truth while it can still act on it.

The bound is not tuned for your hardware. Raise it once you know what a run actually costs
on the host; a value of zero or less is treated as a misconfiguration and falls back to the
default rather than meaning "unbounded".

## Idempotency

**Put an `Idempotency-Key` on every retry of `POST /v1/responses`.** Without one, a retry
after a timeout runs the task a second time — and the first may still be running, editing
the same files in the same working directory. Errors §4 calls this the single most damaging
mistake a UHP client can make.

```bash
KEY=$(uuidgen)
curl -s http://localhost:8080/v1/responses -H "Authorization: Bearer devkey" \
  -H "Content-Type: application/json" -H "Idempotency-Key: $KEY" \
  -d '{"input":"refactor the parser","metadata":{"harness_id":"codex"}}'
# time out, retry with the same $KEY → the same response, not a second run
```

A repeat of a key returns the first request's response and starts nothing. If the first
request is still running, the repeat **waits for it** rather than being refused: Tasks §6 is
explicit that a slow answer beats running expensive, side-effecting work twice, so a retry
arriving into its own in-flight first attempt is answered with that attempt rather than with
`409 session_busy`. Both forms work — a non-streaming retry blocks until the original is
terminal, and a streaming one replays the original's events, with the same sequence numbers,
from the beginning.

The answer is bound to the key, not to the body. A key sent with different input still gets
the first request's response, which is what §6 requires and is the reason to generate a
fresh key per logical request rather than per client.

Keys are kept for 24 hours **after the run they started is terminal**, not after the request
that sent them. An agent can work for longer than a day, and dating the key from the request
would mean the retry that finally came to collect the result is the one that finds the key
expired and starts the work again. Keys live in memory and do not survive a restart; neither
do responses, in the default store, so a key that outlived one would point at something that
is gone. Both become durable together (issue #15).

A request that never started anything — an unknown `harness_id`, a full server answering
`503` — leaves its key free. Errors §4 tells a client to retry those *with the same key*,
and a key bound to the refusal would answer the retry with the same refusal for a day.

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
make docker        # build uhp-go:local
make docker-check  # build it, then assert what it promises
```

The image installs `@anthropic-ai/claude-code` and `opencode-ai` via npm as examples;
add `codex`, `grok`, and `pi` install steps as those CLIs become available in your
environment, and add them to the `docker-check` list too or the gate stops covering
them. Those installs are not permitted to fail quietly: an image missing a CLI it
claims to ship fails the build, rather than the first request that reaches for it.

It runs as the unprivileged user `uhp`, uid and gid `10001`. Harness CLIs execute
commands on behalf of authenticated clients, which is the product, but not as uid 0.

Unlike the bare binary, the image presets `UHP_WORKSPACE=/workspace` — the per-session
working directory has to exist and be writable before the first session, and an image
that ships one is better than an image that fails on it. That default turns on the
three capabilities a workspace implies: `files_input`, `files_output`, and — via the
`harnesses.json` it puts there — `harness_management`. Override `UHP_WORKSPACE` or
`UHP_HARNESS_STORE` if that is not the posture you want.

`/workspace` is declared as a volume owned by the runtime user. A fresh anonymous
volume inherits that ownership but is new on every `docker run`; to keep sessions,
uploaded files and the harness store across restarts, mount a directory uid `10001`
can write:

```bash
mkdir -p ./workspace && sudo chown 10001:10001 ./workspace
docker run -p 8080:8080 -e UHP_API_KEYS=devkey \
  -v "$PWD/workspace:/workspace" uhp-go:local
```

A bind mount keeps the host's ownership, so skipping that `chown` leaves the first
session failing on a directory it cannot create.

## Contributing

Work is tracked as [GitHub issues](https://github.com/aenawi/uhp-go/issues). Issues carry
triage labels described in [docs/agents/triage-labels.md](docs/agents/triage-labels.md);
`ready-for-agent` marks work that is specified well enough to hand to an agent, and
`ready-for-human` marks work needing a product decision.

Security issues go through private reporting instead — see [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
