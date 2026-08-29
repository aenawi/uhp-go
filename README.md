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
stdin/stdout.

There is no database to run either, and no service to point it at: the two
non-stdlib dependencies are `github.com/google/uuid` and `modernc.org/sqlite`,
and the second is a pure-Go SQLite compiled into the binary rather than a
client for something you have to host. See
[docs/adr/0001](docs/adr/0001-sqlite-for-tasks-and-sessions.md) for what that
dependency costs.

This is a property of the protocol, not just of this implementation — the UHP
specification states that "nothing in the wire format requires a hosted
service, an account, a licence key, or a call home."

`UHP_API_KEYS` is **inbound** authentication: the list of bearer tokens *this*
server accepts from *its* clients. You invent the values. It is not a
credential issued by anyone, and it is unrelated to any hosted UHP service.

**Leaving it unset makes this server non-conformant, not merely permissive.**
UHP Security §1 is unconditional — "A server MUST authenticate every endpoint
except `GET /v1/uhp`" — so a server with no keys is a local tool rather than a
UHP server, whatever it is bound to. The default configuration is that local
tool: `UHP_ADDR` defaults to `127.0.0.1:8080`, and with no keys `uhpd` binds
loopback, logs a `WARN` saying it authenticates nothing, and serves. Widen the
bind without setting keys and it refuses to start, naming the variable — an
open server is a deployment mistake, and boot is the last moment early enough
to catch one. See [Authentication](#authentication).

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

## Talking to a UHP server

`uhp.Client` speaks the protocol to any conformant server, and `uhpc` is a command that
drives it:

```bash
go install github.com/aenawi/uhp-go/cmd/uhpc@latest

export UHP_BASE_URL=http://localhost:8080 UHP_API_KEY=devkey
uhpc discover                                   # what this server can do
uhpc harnesses                                  # what it can run, and which of them are ready
uhpc run --stream "summarise the README"        # a task, rendered as it arrives
uhpc watch chrn_…                               # every task on a harness, live
```

In Go:

```go
c := uhp.NewClient("http://localhost:8080", os.Getenv("UHP_API_KEY"))

resp, err := c.Create(ctx, uhp.CreateResponseRequest{
    Input:    "summarise the README",
    Metadata: map[string]any{"harness_id": "claude-code"},
}, idempotencyKey)

if e, ok := uhp.AsError(err); ok && e.Code == uhp.CodeSessionBusy {
    // …the session already has a task in flight
}
```

What it does that a hand-rolled `net/http` call will not, unless you read the whole
specification first:

- **The error envelope is decoded.** A failure is an `*uhp.Error` with a `Code` you can
  switch on, not a status and a blob. A body that is not the envelope — a proxy's 502 —
  still becomes one, built from the status, so a caller has no second case to handle.
- **`UHP-Version` is sent and the answer is checked.** Lifecycle §1 forbids a server
  serving a different version silently; a client that ignores the reply decodes one
  version's shapes out of another's bytes.
- **Retries follow Errors §4 by class rather than by code**, so an unrecognised code is
  safe. `quota_exhausted` is the exception: it arrives as a 429 and means the opposite of
  "come back shortly".
- **A task creation is retried only when it carries an `Idempotency-Key`.** Without one, a
  retry after a timeout runs expensive, side-effecting work a second time while the first
  may still be going, which is what the header exists to prevent.
- **Streams check sequence numbers as they read.** Streaming §2 makes a dropped event
  detectable on purpose; this reports it as a `GapError` rather than rendering the hole.

`uhpc` is also how this repository knows the protocol works over a socket rather than
against its own handlers — see [Testing](#testing).

## Using the wire types

The protocol's objects are importable, so a client does not have to hand-roll them from the
specification:

```bash
go get github.com/aenawi/uhp-go/uhp
```

```go
import "github.com/aenawi/uhp-go/uhp"

var resp uhp.Response
if err := json.NewDecoder(body).Decode(&resp); err != nil { … }

// Streaming: framing only — no HTTP, no retries, no auth.
dec := uhp.NewEventDecoder(body)
for {
    ev, err := dec.Next()
    if errors.Is(err, io.EOF) { break }
    if err != nil { … }
    switch ev.Type {
    case uhp.EventOutputTextDelta:
        io.WriteString(os.Stdout, ev.Delta)
    }
    // Every other type is ignored, which is UHP's second client rule.
}
```

Four things worth knowing before you depend on it:

- **It models the protocol, not this server.** All 23 schema objects are there with every
  field, including the ones this server ignores — `docs/conformance.md` records which. A
  type narrowed to one implementation would misrepresent UHP as being that narrow.
- **`uhp` is the protocol; `uhp/uhpgo` is this server's additions.** Importing the second is
  a dependency on this implementation. Nothing in `uhp` imports anything outside the standard
  library, and module graph pruning means importing it does not pull the SQLite tree into
  your build.
- **A server's extensions need a type to land in.** Every object in the schema is
  `additionalProperties: true`, and a decoder drops what it cannot land — so
  `uhp.Client.GetHarness` returning a `uhp.Harness` silently loses this server's `status`,
  `models` and `capabilities`. `GetHarnessInto` and `ListHarnessesInto` take the shape from
  the caller instead, which is how `uhpc` shows whether a harness is reachable:

  ```go
  var h uhpgo.Harness // uhp.Harness plus this server's three additions
  err := c.GetHarnessInto(ctx, id, &h)
  ```

  Against a server that sends none of them the additions decode as zero values, so the call
  stays portable even though the extra fields are not.
- **Use keyed struct literals.** UHP permits adding response fields within a published
  version and these types will follow; `uhp.Response{a, b, c}` stops compiling when one
  arrives, and `uhp.Response{ID: …}` does not.

The types are frozen by the specification rather than by us: UHP versions are dates, and a
published version is immutable in structure. See
[ADR-0002](docs/adr/0002-uhp-package-models-the-protocol.md).

## Conformance status

UHP conformance is defined by a runnable suite, not by self-assessment. The suite lives
in the protocol repository and anyone can run it against this server:

```bash
pip install -e protocol/conformance
uhp-conformance --base-url http://localhost:8080 --api-key "$UHP_API_KEY" --class full
```

**Last measured score: `full` 52/52 — CONFORMANT (UHP 2026-08-11, class full), 0 skipped.**
Details, reproduction steps and what a green suite still cannot see:
[docs/conformance.md](docs/conformance.md).
Re-measured 2026-08-26 by `make conformance-gate` at `UHP_CLASS=full` on a server with a
workspace and `UHP_SESSION_SHARING=1`, with no `--model` pinned. Zero skips, which the runs
before 2026-08-25 could not say — and the first run scored against a server that answered
`conformance_class: full` while being graded, so `D-05` reported `class=full` rather than
the `class=core` every earlier run recorded (#65).

File support (issue #2) and harness management with skills, MCP and tool restrictions
(issues #3 and #4) had all landed after the previous run and were carried as "implemented,
unmeasured" until issue #42. The ten checks they target — `X-05`…`X-08` and
`F-01`…`F-07` — now pass against the suite rather than against this repository's own tests
alone.

The `extended` run's 44/45 is a *skip*, not a failure: `X-07` downloads an artifact, so it
only tests anything if the agent chose to write a file, and the `full` run passed the same
check ninety seconds later on the same server. Read `skipped_not_verified` in the JSON
report rather than the summary line.

**All three of those runs pinned a model, and that was doing work.** `make conformance-gate`
runs the same suite against the same server *without* `--model`, and on 2026-08-23 it scored
**36/37 core**: a task that named no model came back naming none either, so a client that did
not pin one could not tell what served it (issue #43). That is fixed, and the gate was
re-run on 2026-08-24 — **37/37 core, 0 skipped**, `T-03` reading `claude-opus-5[1m]` off a
task that named no model. The two configurations now agree. The gate stays the one place the
suite runs unpinned, because that is the configuration that found the defect.

**`conformance_class` is no longer a constant, and what it reads depends on how `uhpd` was
started** (issue #65). Three capabilities here are computed from configuration —
`files_input`, `files_output` and `harness_management` — so a hardcoded class would be a
claim the same document contradicts two fields later, which is what the suite's D-05 check
("conformance_class agrees with capabilities") exists to catch. `conformanceClass` in
`internal/transport/http/discovery.go` therefore reads the class off the very capability
struct the document publishes:

| `uhpd` started with | Answers |
| --- | --- |
| nothing | `core` |
| `UHP_WORKSPACE` | `extended` |
| `UHP_WORKSPACE` + `UHP_SESSION_SHARING=1` | `full` |

Only two variables appear because `uhpd` always hands the service a harness store — a
durable one where a path is configured, an in-memory one otherwise — so
`harness_management` is `true` on every deployment of this binary and never the reason a
class is held down. The capability is still read rather than assumed, because the service
is usable without one.

**Session sharing gates `full` here even though D-05 does not require it.** The suite's
list stops at `harness_management`; Sessions §5 is what makes sharing a `full` feature, and
the stricter reading is the one that cannot be wrong. So a server with a workspace and
sharing switched off answers `extended`, not `full`.

The default `uhpd` — no workspace, sharing off — still answers `core`, and that is now a
property of the configuration rather than a line someone remembered not to edit.
Capabilities a deployment does not offer stay reported as `false` rather than omitted — see
[UHP surface implemented](#uhp-surface-implemented).

A skip is counted as a failure here, not as a pass.

That score is defended by a maintainer running `make conformance-gate` before a merge, not
by CI. The gate points the suite at a running `uhpd` and refuses a failure, *any* skip, or a
pass count below the recorded one — but it runs on a person's machine, because the suite
starts real agent tasks against a real CLI and costs real tokens. GitHub withholds secrets
from a fork's pull request anyway, so a remote job could never have measured a contribution.
CI keeps the free checks: compilation, tests, and an image build.

That is weaker than a machine-enforced gate and is stated as such: what holds the score up
is a procedure, and a maintainer who skips it leaves nothing red behind. Why `claude-code`
is the harness measured, when to run the gate, and what `S-09` can and cannot see are all in
[docs/conformance.md](docs/conformance.md).

## Architecture

Layered, dependency-inverted design (Clean/Hexagonal architecture):

```
uhp/                       the protocol: all 23 objects of UHP 2026-08-11, an SSE event decoder,
                           a client (uhp.Client) and a vendored copy of the schema. Imports only
                           the standard library; this repository consumes it like any client
uhp/uhpgo/                 what this server adds to UHP, kept out of uhp so the boundary
                           between protocol and implementation is compiler-visible
cmd/uhpd/                  composition root (main.go) — the only file wiring concrete types together
cmd/uhpc/                  a client for any UHP server, built on uhp.Client. Imports uhp, the
                           standard library, and uhpgo in the two harness reads alone, so that
                           `status` and `models` survive the decode — an addition it renders and
                           never requires, since a client that special-cased the server next door
                           would stop being evidence about the protocol
internal/domain/           entities: Task, Session, Artifact — no external deps. Each embeds
                           the uhp type it is reported as, so there is one shape per concept
internal/harness/          the adapter contract, the shared subprocess runner, the
                           registry, and one ~30-line declaration per harness
internal/service/          application core: TaskService; declares the Registry and Store
                           interfaces it consumes (deps.go), holds all business rules
internal/store/            service.Store and service.HarnessStore implementations — tasks and
                           sessions in SQLite or in memory, created harnesses in one JSON file
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
- Two stores behind one interface: SQLite when a path is configured, in memory when not.
  The SQLite driver is pure Go, so the binary still needs nothing installed to run or test.
- Plain `net/http` with Go 1.22 method/path routing. No framework.
- No wire shape exists anywhere outside `uhp/`. `domain.Task` embeds `uhp.Response`,
  `domain.Artifact` embeds `uhp.File`, `domain.Session` embeds `uhp.Session`, and the
  router's error envelope, discovery document and model catalogue are the published types
  rather than private copies of them. A router whose purpose is to stop its clients
  duplicating integrations is a poor place to duplicate the protocol internally.

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
| `POST /v1/sessions/{id}/share` | Publish a read-only link to a session. Idempotent: a second call returns the same share |
| `GET /v1/sessions/{id}/share` | The share this session has, or `404 uhpgo_share_not_found` |
| `DELETE /v1/sessions/{id}/share` | Revoke the link. §5 requires revocation and names no endpoint; this path is ours |
| `GET /v1/shares/{share_id}` | The shared session and the harness that ran it. **Unauthenticated** |
| `GET /v1/shares/{share_id}/turns` | The shared session's history. **Unauthenticated** |
| `GET /v1/shares/{share_id}/files` | The shared session's artifacts. **Unauthenticated** |
| `GET /v1/shares/{share_id}/files/{fid}/content` | One shared artifact's bytes. **Unauthenticated** |
| `DELETE /v1/traces/{id}` | Dispose of a session: its turns, its working directory, and any run in flight. **Does** stop the work. Revokes its share |
| `POST /v1/responses` | Create a task (`stream:true` for SSE, else blocks until terminal). Honours `Idempotency-Key` |
| `GET /v1/responses/{id}` | Retrieve a task's current state and output |
| `GET /v1/responses/{id}/input_items` | The input a task was created with, verbatim |
| `DELETE /v1/responses/{id}` | Forget a task. Does **not** stop one that is running |
| `POST /v1/responses/{id}/cancel` | Cancel an in-flight task |
| `POST /v1/files` | Upload a file for use as task input (`multipart/form-data`) |
| `GET /v1/containers/{cid}/files/{fid}/content` | Download an artifact as raw bytes |
| `GET /v1/containers/{cid}/files/{fid}/pdf` | Rendered preview — always `501 preview_unavailable` |
| `GET /healthz` | Liveness probe |

Every capability is now implemented ([#57](https://github.com/aenawi/uhp-go/issues/57) was
the last one absent). Three of them are computed from configuration rather than asserted, so
what discovery reports depends on how the server was started: `files_input` and
`files_output` are `true` only when `UHP_WORKSPACE` is set, because both need a per-session
working directory, and `session_sharing` is `true` only when `UHP_SESSION_SHARING` is,
because it is the one capability that makes this server answer a request carrying no
credential. See [Files](#files), [Harness management](#harness-management) and
[Session sharing](#session-sharing).

Harness ids are `chrn_`-prefixed. The ones this server is started with derive theirs
deterministically from the base name, so they survive a restart; a harness created over
the API is given a random one and kept in the harness store, so it survives a restart too.
The friendly base name is accepted as an alias wherever a harness id is expected, so
`{"harness_id": "claude-code"}` works as well as the canonical form.

Request body is intentionally OpenAI-Responses-shaped (`input`, `model`, `stream`,
`previous_response_id`, `instructions`, `store`, `timeout_seconds`, `metadata`), with
`metadata.harness_id` as the UHP extension that
selects which harness runs the task. It is optional: Tasks §1.2 requires a server to pick a
default when it is absent and to report which one it picked, so `{"input":"hi"}` is a
complete request and the response names the harness that served it. The default is
`UHP_DEFAULT_HARNESS` if set, otherwise the sole *ready* harness. With several ready and
none configured there is no honest guess, and the refusal lists the ids to choose from. Continuing a conversation is done by setting
`previous_response_id` to a prior task's `id` — the router resolves the underlying session
and, where the harness supports it, its native session/thread id (`--resume`, `--session`, etc.).

`model` is optional, and the response names one either way. Omit it and the harness runs on
its own default; the response reports that default rather than an empty string, because a
client that pinned nothing has no other way to learn what served it. Where the runtime says
which model it is running — `claude-code`, `grok-cli` and `pi` each do, on their own output
— the response carries what was read rather than what was assumed. No captured line of
`codex` or `opencode` names a model, so for those two it stays the advertised default, and
a harness advertising no models at all reports no model rather than a guessed one. Naming a
model leaves it untouched: `model` comes back as you spelled it, and
`metadata.requested_model` is absent because there was no substitution to report.

### Which request fields are read

The schema's `CreateResponseRequest` has thirteen properties. This server reads nine and
drops four, which is what Tasks §1.1 asks of it: every field but `input` is optional, and a
server MUST ignore one it does not implement rather than reject it.

| Field | What it does here |
|---|---|
| `input` | The work. A string, or the item array — `input_text`, `input_file`, `input_image` |
| `model` | Optional; the response names one either way — see above |
| `metadata` | Yours, plus `harness_id` on the way in and this server's own keys on the way out |
| `stream` | SSE instead of one JSON object |
| `previous_response_id` | Continues that response's session |
| `instructions` | Appended to the harness's standing instructions, for this task only — never replaces them |
| `store` | `false` drops the response once the run is terminal; the answer still arrives once |
| `timeout_seconds` | Narrows the wall-clock budget, never widens it — see [Task budgets](#task-budgets) |
| `background` | `true` answers the POST as soon as the task is accepted, instead of holding it open |
| `max_output_tokens`, `tools`, `include` | Accepted and **declined** — this server will not implement them, and each is named in `metadata.ignored_fields`. See [ADR-0007](docs/adr/0007-a-declined-field-is-not-a-pending-one.md) |
| `max_step` | Accepted and dropped, pending a step counter no adapter offers — also named in `metadata.ignored_fields` |

The last two rows are the ones worth reading twice, and the difference between them is the
point: a *declined* field is a decision that will not be revisited without a reason, and a
*pending* one is work not yet done. Both look identical to a caller, which is why the
distinction lives in the code and in ADR-0007 rather than on the wire.

Dropping a field at all is specified behaviour;
dropping it silently was not, and a caller that set `max_step: 5` to bound an agent's
tool-call rounds got unbounded work and no way to learn why. So a response now says which of
its fields were dropped:

```json
{ "metadata": { "session_id": "sess_…", "ignored_fields": ["max_step", "tools"] } }
```

The key is absent when nothing was dropped, so its presence is the signal. Only fields this
server knows and does not act on appear: an unrecognised field is ignored without being
named, because §1.1's ignore-don't-reject rule is what lets a newer client talk to an older
server and naming every unknown field would turn that into a stream of warnings about valid
protocol. A `null` value asks for nothing and is not reported. This is an extension rather
than protocol — see [ADR-0004](docs/adr/0004-ignored-fields-are-declared-in-metadata.md) —
so a client must not read its absence from some other conformant server as "nothing was
dropped".

**`background: true` answers the POST at acceptance and leaves the run going.** The body is
the response object as it stands — normally `status: "in_progress"`, with an empty `output`
and its `id`, `metadata.session_id` and `metadata.timeout_seconds` already filled in. Two
ways to collect the result, both of which the server already had:

```bash
# start it, and carry a key — the second recipe below needs one, and Errors §4 asks for
# one on every POST /v1/responses anyway
curl -H 'Idempotency-Key: k1' -d '{"input":"…","background":true}' "$UHP/v1/responses"

# 1. poll the read endpoint, which answers mid-run and every read after
curl "$UHP/v1/responses/resp_…"

# 2. or repeat the POST with that same key and stream: true — the retry is handed the
#    first request's own run, so the stream replays from response.created
curl -N -H 'Idempotency-Key: k1' -d '{"input":"…","background":true,"stream":true}' \
  "$UHP/v1/responses"
```

The key is what makes the second recipe follow the first task rather than start a second one:
without it, or with one this server has forgotten, that curl is a fresh POST — a second CLI
run, or `session_busy` if it lands in the same session.

With `stream: true` it streams exactly as it always did, and the field is honoured rather
than dropped: a stream is a held-open POST by construction, and everything else `background`
asks for is already true of one — the run is detached and survives a disconnect, the response
is readable by id while it runs, and the stream is rejoinable from a `Last-Event-ID`.

**A background POST is refused when the response it names will not be retained and it is not
streaming**, `400 invalid_input` with `param: "background"`: the record is dropped when the
run ends and the request will not be there to receive it, so the answer would be delivered
nowhere. Sending `background: true` with `store: false` and no `stream` is the direct way to
get that. The rule is about the *accepted task* rather than the body, which matters in one
place: an idempotent retry need not repeat `store: false`, so `{"background": true}` against
a key naming an unretained run is refused too — and a retry whose run has already finished is
answered with the result instead, because Tasks §6 owes it that. See
[ADR-0005](docs/adr/0005-background-answers-at-acceptance.md).

**`instructions` are added to the harness's, not swapped for them.** The prompt is composed
standing-block, then the task's instructions, then the input. A harness's standing block is
where a tool restriction lands when the runtime cannot enforce it natively, and Harnesses
§4.3 forbids dropping such a restriction — so a request able to replace the block would be a
request able to switch off an operator's configuration by sending one field. They apply to
the task that sent them and do not carry into the next turn of a session.

**`store: false` means the response is not kept, not that it is not delivered.** The record
lives while the run needs it and is dropped when the run reaches a terminal state, so the
client is answered in full exactly once — in the POST body, or in the terminal stream event
— and after that `GET /v1/responses/{id}` is `404 response_not_found`, the response is not
one of its session's turns, and it cannot be a `previous_response_id`. Tasks §4 makes that
`404` a MAY, which is what permits honouring the field at all. The session survives, because
it owns the working directory and the harness binding; the run's artifacts survive on disk,
because `store` is about response retention and not about erasing files. An `Idempotency-Key`
retry is the one read that still answers, because Tasks §6 requires a retry to be given the
first request's answer.

### Capabilities are enforced, not decorative

Every harness object carries a `capabilities` list, and discovery hands that list to a
client before it sends anything. That makes it a promise, so the router keeps it or refuses
the request that takes it up:

- `previous_response_id` on a harness that does not advertise `sessions` is refused with
  `422 uhpgo_capability_unsupported`. It used to be accepted, and the harness was then
  started with no resume flag and no memory of the previous turn — a fresh conversation
  answered `200` and presented to the client as a continuation.
- A cancel for a harness that does not advertise `cancellation` is refused the same way,
  rather than answering `200` while the agent keeps running and keeps spending money.

Both refusals name the capability in `error.detail`, so a client can match them against the
list it already holds. Cancelling an already-terminal task or an idle session still
succeeds whatever the harness advertises (Sessions §4): there is no work to stop, so
nothing is being promised that cannot be delivered.

All five bases shipped here advertise `sessions`. `grok-cli` was the last holdout and
stopped being one in issue #34: grok 1.0.5 puts a `session_id` on every line of
`--output-format streaming-messages-json` and resumes it with `--resume`, so both halves
of the capability now exist where before neither did.
That is the only entry in the list a declaration decides. Three of the six capabilities are
not the CLI's to claim and no declaration names them:

- `cancellation` belongs to the shared runner — every harness runs in its own process group
  and is stopped by killing it — so all five advertise it.
- `files_in` and `files_out` belong to the router. It writes a task's attachments into the
  session working directory before the run and diffs that directory afterwards for
  artifacts, and neither step asks the adapter anything. So all five advertise both,
  wherever both are true. The declarations used to say otherwise — `pi` claimed neither and
  `grok-cli` claimed no output, while both did both.

**The per-harness list and the discovery document answer the same question.** `files_in` and
`files_out` on a harness are computed from the same configured workspace that
`files_input`/`files_output` are computed from in `GET /v1/uhp`, so the two cannot disagree:
start `uhpd` without `UHP_WORKSPACE` and no harness advertises either, discovery reports both
`false`, and a task carrying a file is refused with `501` rather than having its attachment
silently dropped. A harness never claims a file capability the deployment it is running on
would refuse.

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
reports `files_input`/`files_output` as `false` when it is unset, no harness advertises
`files_in` or `files_out`, and a task carrying a file is refused with `501` rather than
having its attachment silently dropped.

Both halves are the router's, not the CLI's: every harness gets the same treatment, and no
harness declares either capability. See
[Capabilities are enforced, not decorative](#capabilities-are-enforced-not-decorative).

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
session is deleted, and `DELETE /v1/traces/{id}` is the endpoint that does it. Access is scoped to the server's
single principal: every configured `UHP_API_KEYS` value is equivalent and carries no
identity, so a deployment serving several tenants runs one `uhpd` per tenant rather than
one server that filters — see
[`UHP_API_KEYS` is a list of credentials, not a list of tenants](#uhp_api_keys-is-a-list-of-credentials-not-a-list-of-tenants).

## Authentication

Every endpoint except `GET /v1/uhp` requires `Authorization: Bearer <key>`, where the key
is one of the comma-separated values in `UHP_API_KEYS`. Discovery is exempt by design: a
client has to be able to tell this is a UHP server before deciding what credential to
present (Lifecycle §2), and the document carries nothing principal-specific. An absent,
malformed or unknown token is `401` with an `authentication_error` envelope; the scheme is
matched case-insensitively, as RFC 7235 requires.

**With `UHP_API_KEYS` unset, that is all skipped, and such a server is not conformant.**
Security §1 has no local-development exemption. This server keeps the unauthenticated mode
anyway — it is genuinely useful, and "runs with no configuration" is a property worth
having — but it is not allowed to be a mode you end up in without noticing:

- **The default bind is loopback.** `UHP_ADDR` defaults to `127.0.0.1:8080`. An
  unauthenticated server only this machine can reach is a local tool; the same server on
  `0.0.0.0` is an open relay that will run agent tasks, spawn CLI subprocesses and serve
  artifacts for anyone who can reach the port.
- **Widening the bind without keys is fatal at boot.** If `UHP_API_KEYS` is empty and
  `UHP_ADDR` is not a loopback address, `uhpd` refuses to start and names the variable.
  This is a deployment mistake, and every per-request answer is too late to catch one —
  by the time a request arrives to be refused, the server has already been open for as
  long as it has been up. A literal IP is decided without asking anything; a hostname is
  resolved, and every address it resolves to must be loopback. That includes `localhost`,
  which is conventionally loopback and is ultimately whatever the resolver says: an
  unkeyed server on a `localhost` somebody has pointed elsewhere is exactly the open
  server this refuses. Resolving costs nothing the server was not already going to
  spend — `net.Listen` resolves the same name moments later — so "runs entirely offline"
  is untouched.
- **The narrowed default is a breaking change, and it says so.** A keyed deployment that
  relied on `UHP_ADDR` defaulting to `:8080` is correct, conformant, and — after an
  upgrade — bound where nothing can reach it. That failure is otherwise silent, since the
  posture check passes on the keys without looking at the address, so a keyed server on
  loopback logs one `INFO` line naming `UHP_ADDR`. **If you were relying on the old
  default, set `UHP_ADDR=0.0.0.0:8080` explicitly.**
- **Running unauthenticated is logged.** One `WARN` line at startup, so an operator who
  arrived here by accident finds it in their own logs:

```
{"level":"WARN","msg":"running unauthenticated; every endpoint except GET /v1/uhp answers without a credential, which UHP Security §1 forbids","addr":"127.0.0.1:8080","hint":"set UHP_API_KEYS"}
```

Nothing in the capability vocabulary covers "this server is open", and inventing a key for
it would be a private dialect — the same reasoning [docs/conformance.md](docs/conformance.md)
applies to resumption. So a client cannot tell an open server from a closed one by asking,
and this section is the obligation instead of a wire field. The recorded conformance score
was measured with `UHP_API_KEYS=devkey`; see
[Conformance](docs/conformance.md) for what that means for the number.

### `UHP_API_KEYS` is a list of credentials, not a list of tenants

**Every configured key is equivalent, and a `uhpd` process serves exactly one principal.** A
credential authenticates; it does not identify. Nothing downstream learns which key matched,
so two people holding two keys are one client: they share every session, every transcript
and every artifact, and no request either of them can make will reveal that.

That is the conformant reading rather than an exemption. "Scope every object to the principal
that created it" (Architecture), "scope file access to the owning principal" (Files §5) and
"return `404`, not `403`, for objects outside the caller's scope" are all satisfied by a
server with one principal the way a rule about every element is satisfied by an empty set —
there is no second principal for anything to be outside the scope of. What it is not is
enforcement, which is why `insufficient_scope` is a code this server can never return (see
the unreachable-codes table in [docs/conformance.md](docs/conformance.md)) and why no
conformance run says anything about tenancy either way.

**Keeping two tenants apart means running one `uhpd` per tenant** — separate keys, separate
`UHP_DB`, separate `UHP_WORKSPACE`. That boundary is the operating system's and is stronger
than a filter this server would have to remember in every query. The alternative — a
principal on each credential and an owner column on every object — was considered and
rejected in [ADR-0006](docs/adr/0006-one-principal-per-server.md), which is the thing to
supersede if one process ever has to serve two tenants.

Because the variable is plural and the obvious reading of a plural is the wrong one here,
configuring more than one key logs a line saying so:

```
{"level":"INFO","msg":"several API keys are configured; they are equivalent credentials for one principal, not one tenant each","keys":3,"hint":"run one uhpd per tenant if they must not share sessions, transcripts or artifacts"}
```

[SECURITY.md](SECURITY.md) puts this out of scope explicitly: one key holder reading another's
data is the design, not a vulnerability.

## Session sharing

Sessions §5 asks a `full` implementation for a read-only view of a conversation that
someone without a credential can open. This server implements it, and it is **off unless
you turn it on**:

```bash
UHP_SESSION_SHARING=1 UHP_PUBLIC_URL=https://uhp.example.com uhpd
```

The default is off because this is the only capability here that changes what the
deployment *is*. Everything else is behind a bearer token; switching this on makes the
server answer some requests that carry none. Every other capability is gated on having
somewhere to put something — a workspace for files, a store for harnesses — and this one is
gated on consent. With it off, discovery reports `session_sharing: false` and every share
endpoint answers `501 uhpgo_session_sharing_unsupported`.

```bash
uhpc share sess_abc          # mint (or re-read) the link
uhpc shared shr_9f2…         # read it the way its recipient does
uhpc unshare sess_abc        # revoke it
```

### Turning it off suspends the links; it does not revoke them

`UHP_SESSION_SHARING` is a switch on a capability, not a delete. Unset it and every
share endpoint answers `501`, discovery reports `session_sharing: false`, and the share
rows stay exactly where they are — so a restart with the variable set again makes every
link that was ever minted resolve again. Possibly in a different deployment, possibly for
someone who was never told it had stopped working.

**Revoking a link means revoking it**: `uhpc unshare <session_id>`, while sharing is on.
There is no way to withdraw a share with the capability turned off, because revocation is
behind the same flag as everything else.

That is a decision rather than an oversight. Off means the endpoints are not served, the
way turning off harness management does not delete the harnesses somebody created; the
alternative — revoking every share at startup whenever the variable is absent — destroys
state on a restart with a typo'd variable name, which is the same silent downgrade `uhpd`
refuses to make when a configured store will not open. What is left is the gap between
what an operator meant by turning it off and what they got, so a server that starts
without the variable and is still holding links says so:

```
{"level":"WARN","msg":"session sharing is off and this server still holds shares; they are suspended, not revoked, and every one of them resolves again if it is turned back on","shares":3,"hint":"to withdraw them, start with UHP_SESSION_SHARING=1 and revoke each one (uhpc unshare <session_id>)"}
```

It counts rather than lists: the id *is* the credential, so an operator needs to know
there are three, not what they are. Nothing is logged when there are none, which is
almost every deployment — a line printed on every start is a line nobody reads on the
start that mattered. `internal/service/shares_test.go` holds the whole cycle up: minted,
suspended by a restart without the flag, resolving again after a restart with it, and
gone for good once revoked.

### The share id is the credential

A share id is 256 bits of randomness behind a `shr_` prefix, and it is a bearer capability:
whoever holds it reads that conversation, its turns and its files, with nothing else.
Treat it the way you treat an API key. Because it necessarily travels in a URL, every
shared response carries `Cache-Control: no-store`, `X-Robots-Tag: noindex, nofollow` and
`Referrer-Policy: no-referrer` — the three channels a secret in a URL leaks through. They
are middleware rather than a line in each handler, so they are on the 404 for a revoked
link as well as on the 200, which is the case that matters more: an error response is
reached by the same address.

Sharing is idempotent per session: a second `POST` returns the share that already exists
rather than a second live id, because a client is told about one id and revokes one id.
Rotating a link means revoking and sharing again, in that order.

### Read-only is a property of the routing table

"Shared views must be read-only" is enforced by there being nothing to refuse. A share id
is a path segment and never a credential, so:

- presenting it as a bearer token is presenting an unknown token — `401`, on every endpoint;
- `POST`, `PUT`, `PATCH` and `DELETE` under `/v1/shares/` are methods no route claims — `405`,
  from the router itself;
- the shared artifact path takes no container id, so the container is derived from the share
  and another session's file id resolves to nothing rather than to a check someone has to
  remember.

A future endpoint cannot forget a check that does not exist. The tests that hold this up are
the negative ones in `internal/transport/http/share_handlers_test.go` — a share that cannot
start a task, cannot cancel, cannot upload, cannot delete the trace, and stops working the
moment it is revoked.

### What a viewer never sees

The shared view carries the harness that ran the session, projected down to what answers a
viewer's only question — *what ran this, on what, and could it do the things the transcript
shows*. It is built by copying the fields that are kept rather than by blanking the ones
that are not, because a deny-list is correct only until the next field is added to the
harness object, and the cost of forgetting one here is a configuration secret served to
whoever holds a URL.

Kept: id, name, base, base label, default model, models, capabilities, status, created-at.
Dropped: the MCP server list, the system prompt, skill bundles, disabled tools, and the
step and timeout budgets. The MCP list is the sharpest of those — Harnesses §4.1 forbids
returning a resolved credential to a client, and this is the one path where "a client"
means someone who presented nothing. Stripping `auth` alone would not have been enough:
`headers` is a free-form map, and it is the map the server materialises the resolved `auth`
into as an `Authorization` header, so the whole list goes.

The projection lands in `uhpgo.SharedHarness`, a type of its own rather than a harness with
fields removed, and that is not tidiness. Several of a harness object's fields *say
something* when they are empty: `mcpServers: []` means "this harness has none" and a null
`maxStep` means "unbounded". A stripped harness would therefore tell a viewer two untrue
things about a system they cannot see. A separate type says only what it says.

Revocation is absolute: the id stops resolving. Not hidden, not expired, not marked —
and it is the only thing that is, since turning the capability off suspends links rather
than withdrawing them, as above. And deleting the trace takes the share with it —
Sessions §6 makes a deleted session's files unreachable, and a surviving share id would
be the anonymous route back to them. Both engines are held to that in
`internal/store/share_contract_test.go`.

There is no expiry, deliberately. §5 requires revocation and says nothing about expiry, and
a stored expiry that nothing enforces is a worse promise than none.

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
so set the path anywhere you intend the ids to keep resolving. `UHP_WORKSPACE` sets this and
`UHP_DB` together, which is the reason to reach for it rather than for either alone.

```bash
curl -s http://localhost:8080/v1/harnesses   -H "Authorization: Bearer devkey" -H "Content-Type: application/json"   -d '{"name":"Research agent","base":"claude-code","default_model":"claude-sonnet-5"}'
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

Leaving it out of the generated document is necessary and not sufficient, because a
runtime can have MCP configuration of its own. Claude Code does, so its runs also carry
`--strict-mcp-config`, which confines them to the file this server wrote — see the table
below.

How much of that the runtime enforces itself differs per base, and this server does not
overstate it:

| Base | Tool block | Skill loading | Per-run MCP |
|---|---|---|---|
| `claude-code` | `--disallowedTools` (executed) | standing instruction | `--mcp-config` (executed) |
| `grok-cli` | `--disallowed-tools` (verified) | standing instruction | none — refused at config time |
| `pi` | `--exclude-tools` (verified) | `--skill` (verified) | none — refused at config time |
| `codex`, `opencode` | standing instruction | standing instruction | none — refused at config time |

"Verified" means the flag was read from that CLI's own `--help` on a machine where it is
installed — re-read for `grok-cli` on 1.0.5 (2026-08-23, issue #34), where it is still
spelled `--disallowed-tools`. The nearby `--deny <RULE>` now carries `--disallowedTools`
as a compat alias, which is a different flag with a different grammar rather than a rename
of this one, so reading only the alias list would have moved the harness onto the wrong
one.

"Executed" is the stronger claim and the only one that settles issue #19: the flag was not
read but run, and the run was watched from the far end. `make probe-claude-delivery` does
that, and a flag the CLI accepts and ignores fails it:

- `--disallowedTools Bash` removes `Bash` from the session's tool list, so the model is
  never offered it. The same run without the flag used `Bash` and returned its output.
  The list is comma-joined rather than space-separated — `--help` allows either, but the
  flag is variadic, so a space-separated list would spread across argv elements.
- `--mcp-config` reaches the server: the generated document's `Authorization` header
  arrives on every request, `tools/call` is served, and the model returns a secret only
  that server knows.
- A server the generated document does *not* name is never contacted — but only because
  `--strict-mcp-config` is also sent, unconditionally, on every run. `--mcp-config` adds
  a configuration rather than replacing the set: without the second flag, Claude Code
  also connects the host's own MCP servers, and a server the operator disabled is
  contacted anyway. The probe demonstrates both directions.

All of that is a maintainer's command rather than a settled fact. `go test` cannot reach a
logged-in CLI, so nothing in CI re-runs it, and a Claude Code release is free to change any
of it — which is the whole reason these two claims went three issues without being checked.
Run the probe after every upgrade.

Where a runtime cannot hard-block a tool, the restriction is conveyed as a standing
instruction and described to the model as unenforced — never dropped. §4.3 is explicit
that dropping it is the worst outcome: the operator believes a tool is off, and it is not.

## Running

```bash
go build -o bin/uhpd ./cmd/uhpd
UHP_API_KEYS=devkey ./bin/uhpd
```

Without `UHP_API_KEYS` it still runs, on `127.0.0.1:8080` and with a `WARN` saying it
authenticates nothing — and it refuses to start if you also widen `UHP_ADDR`. See
[Authentication](#authentication).

Create a task:

```bash
curl -s http://localhost:8080/v1/responses \
  -H "Authorization: Bearer devkey" -H "Content-Type: application/json" \
  -d '{"input":"Summarise README.md in three bullets.","model":"claude-sonnet-5","metadata":{"harness_id":"claude-code"}}'
```

Stream it:

```bash
curl -N http://localhost:8080/v1/responses \
  -H "Authorization: Bearer devkey" -H "Content-Type: application/json" \
  -d '{"input":"...","model":"gpt-5.6-sol","metadata":{"harness_id":"codex"},"stream":true}'
```

## Storage

**Set `UHP_DB`,** or a `UHP_WORKSPACE` that implies it. Tasks and sessions then live in one
SQLite file and survive a restart; with neither, they are kept in memory and `uhpd` says so
on startup:

```
{"level":"WARN","msg":"database not configured; tasks and sessions will not survive a restart"}
```

That matters more than it sounds. A client holds a response id and comes back for the
result — that is the whole shape of an asynchronous API — and an in-memory server answers
`404` for work it actually did. The same goes for a session: a conversation whose history is
gone is a fresh conversation wearing an old id.

The specification is silent on all of this. It mandates no engine, no durability guarantee
and no retention rules, so this is a product decision rather than a conformance one, and the
decision is that state belongs on a volume the operator owns.

A configured path that will not open is fatal rather than a quiet fall back to memory. The
operator asked for durability, and a server that silently serves less than it was configured
for is the hardest misconfiguration to notice.

Some notes on what is in the file:

- **The driver is pure Go** (`modernc.org/sqlite`). The image builds with `CGO_ENABLED=0`,
  so a cgo-linked SQLite would not be in the binary this repository ships. Nothing has to be
  installed to run or test.
- **The schema is two tables**, each with the columns that get searched or ordered and one
  JSON document for the rest. No query selects on a task's usage or its error code, so
  splitting a task across nineteen columns would buy nothing and would turn every new field
  into a migration.
- **`PRAGMA user_version` is checked on open.** A file written by a newer `uhpd` is refused
  rather than written to, because the columns this build would ignore are the ones the other
  binary needs.
- **WAL, `synchronous=NORMAL`.** The service writes a task on every streamed delta, so
  `FULL` would put an `fsync` between a harness and each fragment of its answer. What
  `NORMAL` gives up is the last few commits if the machine loses power; a crash of this
  process loses nothing.
- **A fresh database is created `0600`.** It holds every prompt a client sent and every
  answer a harness gave. An existing file keeps whatever mode the operator gave it.

Uploaded files and idempotency keys are still in memory, and harnesses are still a JSON
document — each is its own interface, so each moves on its own.

Putting a real disk under the service exposed a caveat that had been harmless until then: a
failure to read the store was reported to the client as `404`, because an error from an
in-memory map could only ever mean "no such row". A disk that stopped answering said the
same thing, and `404` tells a client its id is wrong and retrying will never help — so a
client polling a task that was still running would conclude the task had vanished and stop.
Fixed in [issue #28](https://github.com/aenawi/uhp-go/issues/28): `Store.GetTask` and
`Store.GetSession` answer with a `found` flag alongside the error, so an absent row and an
unreadable store reach the transport as different things and become `404` and `500`.

`internal/store/store_contract_test.go` is one suite run against both engines. Ordering,
paging and the rule that a caller may mutate whatever it hands over or is handed back are
properties of `service.Store`, not of whichever engine a deployment configured.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `UHP_ADDR` | `127.0.0.1:8080` | HTTP listen address. Loopback by default — see [Authentication](#authentication) |
| `UHP_API_KEYS` | (unset = auth disabled, loopback only) | Comma-separated bearer tokens this server accepts. Every value is an equivalent credential for the server's one principal, **not a tenant of its own**. Unset is non-conformant and `uhpd` refuses to start unauthenticated off loopback — see [Authentication](#authentication) |
| `UHP_WORKSPACE` | (unset = router's own cwd, and no file support) | Root for per-session working directories |
| `UHP_HARNESS_STORE` | `$UHP_WORKSPACE/harnesses.json`, or unset = no harness management | Where harnesses created over the API are kept |
| `UHP_DB` | `$UHP_WORKSPACE/uhp.db`, or unset = tasks and sessions in memory | SQLite file holding tasks and sessions |
| `UHP_MAX_BODY_BYTES` | `8388608` | Maximum accepted request body, and the upload limit |
| `UHP_MAX_CONCURRENT_RUNS` | `8` | Harness processes allowed to run at once; beyond it, `503 harness_unavailable` |
| `UHP_TASK_TIMEOUT` | `30m` | Longest a task may run, and the ceiling `timeout_seconds` is clamped to. A Go duration (`30m`) or a bare number of seconds (`1800`) — see [Task budgets](#task-budgets) |
| `UHP_PUBLIC_URL` | (unset = relative URLs) | Origin used to build absolute artifact download and share URLs |
| `UHP_SESSION_SHARING` | `false` | `1` or `true` serves the unauthenticated read views of Sessions §5. Off by default, and turning it back off suspends the links it minted rather than revoking them — see [Session sharing](#session-sharing) |
| `UHP_DEFAULT_HARNESS` | (unset = the sole ready harness, if there is exactly one) | Harness a task that names none runs on. `uhpd` refuses to start if it names nothing |
| `UHP_CLAUDE_MODELS` | `claude-sonnet-5,claude-opus-5` | Claude Code models — see [Where the model list comes from](#where-the-model-list-comes-from) |
| `UHP_CODEX_MODELS` | `gpt-5.6-sol` | Codex fallback models |
| `UHP_GROK_MODELS` | `grok-4.6,grok-4.5` | Grok fallback models |
| `UHP_OPENCODE_MODELS` | (unset) | OpenCode fallback models |
| `UHP_PI_MODELS` | (unset) | Pi fallback models |

These are the defaults of the `uhpd` binary. The Docker image presets `UHP_WORKSPACE`,
which changes the `UHP_WORKSPACE`, `UHP_HARNESS_STORE` and `UHP_DB` rows above, and
`UHP_ADDR`, which is why the image needs `UHP_API_KEYS` — see
[Building the image](#building-the-image).

### Where the model list comes from

The five `*_MODELS` variables are **fallbacks, not the catalogue**. Four of the five CLIs
can be asked what they serve, and are:

| Harness | Asked with | Fallback used when |
|---|---|---|
| `claude-code` | — (no listing command exists) | always |
| `codex` | `codex debug models` | codex is missing, or cannot render its catalogue |
| `grok-cli` | `grok models` | grok is missing, or cannot list |
| `opencode` | `opencode models` | opencode is missing, or cannot list |
| `pi` | `pi --list-models` | pi is missing, or cannot list |

The first request pays for the lookup so that it is right from the start; after that the
answer is served from a five-minute cache and refreshed in the background. A CLI that is
not installed is never asked.

If neither source names a model, the harness advertises none — and then accepts whatever
model you name, because a server that cannot enumerate a runtime's catalogue has no
grounds to refuse one. Nothing is promised, so nothing is over-promised; the CLI answers
in its own words.

This is why `UHP_OPENCODE_MODELS` and `UHP_PI_MODELS` have no default. Both route through
whichever providers *you* have logged in to, so there is no model id that is true on
someone else's machine. With nothing to fall back to, the server advertises no model for
them rather than a wrong one, leaves `--model` off the command line so the CLI picks its
own, and lets you pin a list if you want one.

`UHP_CLAUDE_MODELS` is the one that carries real weight, because Claude Code has no
listing command — nothing checks it, so it goes stale silently. It was last verified
against the CLI on 2026-08-21.

The reason any of this matters: `GET /v1/models` publishes these ids, and UHP §3.1 says
`available` is a promise — "A server MUST compute `available`, not assert it. Listing a
model as available and then failing the task is the worst outcome for a client." A task
naming a model this server does not advertise is refused before anything is dispatched,
so an advertised list that does not match reality passes the server's own check and then
fails at the CLI: the worst of both.

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

### Task budgets

Concurrency alone is not a bound. A slot is held for as long as its agent runs, so a CLI
that wedges holds one for ever — and enough wedged agents take the server permanently to
capacity, refusing every later task with a `503` that tells the client to retry for a
condition retrying never clears. Security §5 asks for the other half: **a server MUST bound
task duration.**

Every task therefore runs under a wall-clock budget: **the shortest of the three bounds
that are set.**

- the request's `timeout_seconds`,
- the harness's configured `timeout_seconds`,
- `UHP_TASK_TIMEOUT`, which defaults to 30 minutes.

**Each is a clamp, and none of them is a preference.** A narrower bound below always wins,
and a wider one never does: a request may narrow the harness's budget and the deployment's,
and may not widen either, because a bound a caller can raise without limit is not one. So
`timeout_seconds: 86400` against a default server is answered — not refused — with a budget
of 1800 seconds, and the same request against a harness fenced to 60 seconds gets 60. The
response says so in both cases: the resolved value is on every response as
`metadata.timeout_seconds`, which is what makes narrowing a fact the client can read rather
than an override it has to infer from when the work stopped. A `timeout_seconds` that is
zero or negative *is* refused, with `400 invalid_input` and `param: "timeout_seconds"` —
there is no reading of "a budget of no seconds" a caller could have wanted, and silently
substituting the server's own would be the same defect in a quieter form.

When the budget bites, the run is stopped through the same path `POST
/v1/responses/{id}/cancel` uses — so the process group is torn down once, in one place — and
the task reaches:

```json
{"status": "incomplete", "error": null, "incomplete_details": {"reason": "timeout"}}
```

`incomplete`, not `failed`: Lifecycle §3 reserves `failed` for work that could not be done,
and the distinction is what tells a client this work is worth continuing. `incomplete`, not
`cancelled`: nobody asked for a stop. On a stream the terminal event is `response.incomplete`
rather than `response.failed`. Whatever the agent produced before the deadline is retained on
the response, artifacts included, because the budget path settles through the same code every
other terminal path does.

**If somebody does ask for a stop, that outranks the budget.** Stopping a run is not
instantaneous — the deadline fires, the process group is signalled, and the server waits for
it — so a `POST /v1/responses/{id}/cancel` or a `POST /v1/sessions/{id}/cancel` can arrive
seconds after a deadline while the run is still being torn down. A task cancelled in that
window settles `cancelled`, with no `incomplete_details`, because a stop somebody asked for is
a fact and `incomplete` is the status a client retries: reporting a deliberate stop as the
retryable one invites a re-run of the work a user cancelled on purpose. What the cancel does
not do is take work away from an agent that beat it — an agent that actually finished inside
that window is still `completed`, on the same reading that leaves a deadline-racing
`completed` alone above.

**`max_step` is not implemented and is deliberately not implied by this.** It is a budget on
tool-call rounds, nothing in this server counts one, and only some adapters emit anything a
round could be counted from — so it stays accepted and dropped, as Tasks §1.1 permits, and is
tracked separately. A server that honoured `timeout_seconds` and let `max_step` look honoured
too would be back in the position this section is about; saying which one is enforced is the
price of enforcing either. Tracked as [#72](https://github.com/aenawi/uhp-go/issues/72).

### One task per session

A session has one working directory and one conversation, so Lifecycle §5 forbids running
two tasks in it at once. A second one is refused `409`, `code: "session_busy"` — and unlike
the capacity refusal above, not a 5xx: the request named a session that is busy, and what
has to change before it works is that session's state rather than the server's.

Errors §4 makes it retryable "once in-flight task reaches terminal state", which makes the
refusal an instruction to wait, so it carries the two things a client can act on:

```json
{"error": {"code": "session_busy",
           "detail": {"retry_after_ms": 5000, "response_id": "resp_…"}}}
```

`response_id` is the response holding the session, and it is the more useful of the two: a
client given it can stop guessing entirely and watch that response go terminal instead of
asking this endpoint again. It is not a key the protocol defines — a `detail` object is open
— so it is a courtesy from this implementation rather than something to rely on elsewhere.

`retry_after_ms` is a floor and not a prediction: an agent works for minutes and nothing here
knows when it will stop. **The task budget does not make it a real number, which is worth
saying because it looks as though it should.** Past a run's remaining budget the session is
free whatever the agent is doing, so the server does hold an upper bound on the wait — but
it is an upper bound on something that usually ends in seconds, and a client that slept for
it would sleep through the answer. Nor is it useful to quote only when it falls under the
floor: that is exactly the moment the budget is about to fire, and the teardown behind it
takes a further moment nothing here can size, so the number would be knowably too short in
the one case it applied. Too long to sleep for and too short to retry on is not a wait, and
the floor is what is left.

The field goes in the body and not in a `Retry-After` header: RFC 9110 §10.2.3 defines that
header for `503`, `429` and the redirects, and this is a `409`.

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
expired and starts the work again. Keys live in memory and do not survive a restart, so a
retry that arrives after one runs the work again. That is now the weaker half: with `UHP_DB`
set the response the key would have pointed at is still there, and only the key is missing.
Moving keys into the same store is its own issue.

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

**Check that the agent can write.** Ask a new harness to create a file and look on disk,
because "it defaults to something sensible" is not a default any two CLIs share. `codex`
defaults to a read-only workspace and refused every write for as long as nobody looked, and
it did so while reporting the run `completed` — issue #89, and
[ADR-0008](docs/adr/0008-an-agent-may-write-in-the-directory-it-was-given.md) is the policy
a sixth harness inherits: write access to the session's working directory, granted by
whatever argument that runtime needs, or none if it needs none.

**The `Capabilities` list is enforced, so declare only what the harness delivers.** Listing
`sessions` on a harness that cannot resume turns every continuation into a silent fresh
conversation. `cancellation` needs no declaring: `Build` adds it, because the shared runner
delivers it for every harness.

`TestAdvertisedSessionsReachArgv` catches half of the `sessions` mistake — it fails if a
harness advertising `sessions` builds the same argv with and without a native session id.
The other half is not mechanically checkable: the id has to be *discovered* from the CLI's
own output by `ParseLine` before there is anything to pass back, and a `passthroughParseLine`
harness can never produce one. `opencode` is the worked example, and it is worked in both
directions: it once carried the flag without the parser, so every continuation silently
started a new conversation, and issue #13 restored the two halves together — `--format json`
so the CLI prints its `sessionID`, `parseOpenCodeLine` to read it, then the capability.
Check both halves by hand before you claim `sessions`.

`pi` is the other direction of the same mistake, and the more tempting one: it withheld
`sessions` it could deliver. The id was on the wire but never read, because nobody had run
`--session-id` against the binary — so the capability was declined on the strength of not
having checked. Issue #33 checked, with `scripts/probe-pi-session.py`, and it resumes.
**Withholding a capability is not the safe default it looks like.** It is a wrong answer in
the other direction, and unlike an over-claim nothing fails to make it visible.

**`streaming` has the same two halves, and the second one is easy to miss.** Several of
these CLIs default to an output mode that prints nothing until the run is over, so an
invocation can be perfectly correct and still buffer: `pi -p` writes the finished answer
after its own `session.prompt()` resolves, `claude -p --output-format stream-json` emits
one event per *completed* assistant message, and `grok --output-format
streaming-messages-json` does the same until `--include-partial-messages` is added — the
same prompt without it produced three lines and no delta at all. All three needed a flag
before the capability they already advertised was true. Read the CLI's own streaming mode,
not just its exit code, and note that whichever text the incremental mode gives you is
usually repeated whole at the end, so `ParseLine` must read one or the other and never
both.

**Two messages in one run need a separator, and no CLI supplies one.** A run that
interleaves prose with tool calls answers in several pieces — opencode as text parts, codex
as `agent_message` items, grok as messages between `message_stop` events — and every delta
is appended into a single `output_text`, so passing the pieces through unchanged runs
"Alpha" and "Gamma" together as "AlphaGamma". Three adapters add a newline at their own
boundary: `opencode` and `codex` on the text, because their pieces are whole messages, and
`grok-cli` on `message_stop`, because its deltas are token-level and a newline per delta
would break every word apart.

`claude-code` and `pi` are the outstanding cases, and this is the honest state rather than
a claim they are fine. Both stream token-level deltas with no separator at any boundary, so
both will run two messages together the same way — `pi` says so where it declines one, and
`claude-code` does not. Neither was probed for it in issue #34, whose scope was codex and
grok, and neither should be assumed correct because it is not listed above.

**Usage is a run total, and several CLIs also publish a per-message one under the same
field names.** opencode's `step_finish`, grok's `message_delta` and codex's `turn.completed`
all carry `input_tokens`; only the last is the whole run's. Usage is applied
last-write-wins, so reading the per-message event publishes the final message's accounting
as the task's — grok's captured tool run reported 166 input tokens on its second message
against a real total of 19,838. UHP permits usage to be null; it does not permit it to be
wrong.

## Testing

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
does. See [docs/conformance.md](docs/conformance.md).

The conformance gate is separate and is *not* in that hook, because it spends real tokens on
real agent tasks. It needs the suite installed and a running server, and it is a thing you
run on purpose before merging something that could move the score — see
[docs/conformance.md](docs/conformance.md) for when, and for the pinned suite revision.

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

Unlike the bare binary, the image presets `UHP_ADDR=0.0.0.0:8080`. A published port is the
whole point of a container, and the binary's loopback default would make `-p 8080:8080`
reach nothing. The consequence is deliberate: `docker run` without `UHP_API_KEYS` refuses
to start rather than publishing an unauthenticated server — see
[Authentication](#authentication).

Also unlike the bare binary, the image presets `UHP_WORKSPACE=/workspace` — the per-session
working directory has to exist and be writable before the first session, and an image
that ships one is better than an image that fails on it. That default turns on the
three capabilities a workspace implies: `files_input`, `files_output`, and — via the
`harnesses.json` it puts there — `harness_management`. It also puts `uhp.db` there, so tasks
and sessions are durable by default in the image. Override `UHP_WORKSPACE`,
`UHP_HARNESS_STORE` or `UHP_DB` if that is not the posture you want.

`/workspace` is declared as a volume owned by the runtime user. A fresh anonymous
volume inherits that ownership but is new on every `docker run`; to keep the task
database, session working directories and the harness store across restarts, mount a
directory uid `10001` can write:

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
