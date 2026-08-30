# Running it

Starting the server, where its state lives, every environment variable, and the bounds that keep one wedged agent from taking the whole thing down.

```bash
go build -o bin/uhpd ./cmd/uhpd
UHP_API_KEYS=devkey ./bin/uhpd
```

Without `UHP_API_KEYS` it still runs, on `127.0.0.1:8080` and with a `WARN` saying it
authenticates nothing — and it refuses to start if you also widen `UHP_ADDR`. See
[Authentication](authentication.md).

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
| `UHP_ADDR` | `127.0.0.1:8080` | HTTP listen address. Loopback by default — see [Authentication](authentication.md) |
| `UHP_API_KEYS` | (unset = auth disabled, loopback only) | Comma-separated bearer tokens this server accepts. Every value is an equivalent credential for the server's one principal, **not a tenant of its own**. Unset is non-conformant and `uhpd` refuses to start unauthenticated off loopback — see [Authentication](authentication.md) |
| `UHP_WORKSPACE` | (unset = router's own cwd, and no file support) | Root for per-session working directories |
| `UHP_HARNESS_STORE` | `$UHP_WORKSPACE/harnesses.json`, or unset = no harness management | Where harnesses created over the API are kept |
| `UHP_DB` | `$UHP_WORKSPACE/uhp.db`, or unset = tasks and sessions in memory | SQLite file holding tasks and sessions |
| `UHP_MAX_BODY_BYTES` | `8388608` | Maximum accepted request body, and the upload limit |
| `UHP_MAX_CONCURRENT_RUNS` | `8` | Harness processes allowed to run at once; beyond it, `503 harness_unavailable` |
| `UHP_TASK_TIMEOUT` | `30m` | Longest a task may run, and the ceiling `timeout_seconds` is clamped to. A Go duration (`30m`) or a bare number of seconds (`1800`) — see [Task budgets](operations.md#task-budgets) |
| `UHP_TASK_MAX_STEP` | unset | Most tool calls a task may make, and the ceiling `max_step` is clamped to. A positive whole number; anything else is ignored **and warned about at startup**, because unset here means unbounded rather than a default. Unlike the row above, which has no "off" — see [Step budgets](operations.md#step-budgets) |
| `UHP_PUBLIC_URL` | (unset = relative URLs) | Origin used to build absolute artifact download and share URLs |
| `UHP_SESSION_SHARING` | `false` | `1` or `true` serves the unauthenticated read views of Sessions §5. Off by default, and turning it back off suspends the links it minted rather than revoking them — see [Session sharing](session-sharing.md) |
| `UHP_DEFAULT_HARNESS` | (unset = the sole ready harness, if there is exactly one) | Harness a task that names none runs on. `uhpd` refuses to start if it names nothing |
| `UHP_CLAUDE_MODELS` | `claude-sonnet-5,claude-opus-5` | Claude Code models — see [Where the model list comes from](operations.md#where-the-model-list-comes-from) |
| `UHP_CODEX_MODELS` | `gpt-5.6-sol` | Codex fallback models |
| `UHP_GROK_MODELS` | `grok-4.6,grok-4.5` | Grok fallback models |
| `UHP_OPENCODE_MODELS` | (unset) | OpenCode fallback models |
| `UHP_PI_MODELS` | (unset) | Pi fallback models |

These are the defaults of the `uhpd` binary. The Docker image presets `UHP_WORKSPACE`,
which changes the `UHP_WORKSPACE`, `UHP_HARNESS_STORE` and `UHP_DB` rows above, and
`UHP_ADDR`, which is why the image needs `UHP_API_KEYS` — see
[Building the image](operations.md#building-the-image).

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

**`max_step` is the other budget, and it is enforced too** — see
[Step budgets](operations.md#step-budgets). The two are ranked here rather than left to chance: whichever
fires first wins, a cancel outranks both, and the reason travels with the stop, so a step
ceiling reports `reason: "max_step"` rather than telling a client to wait for a clock that
had not run out.

### Step budgets

`max_step` bounds how many tool calls an agent may make in one task. A task stopped by it is
`incomplete` with `incomplete_details.reason` of `"max_step"` — never `failed`, which would
say the work could not be done rather than that it was cut short.

```jsonc
// POST /v1/responses
{ "input": "refactor this package", "max_step": 20 }

// the response, once the ceiling bites
{ "status": "incomplete",
  "incomplete_details": { "reason": "max_step" },
  "metadata": { "max_step": 20 } }
```

Three rules, and each is the wall clock's:

- **Every level is a ceiling, none is a preference.** The resolved budget is the shortest of
  the three that are set — the request's, the harness's `maxStep`, and `UHP_TASK_MAX_STEP`.
  A client may narrow what its operator set and may not widen it.
- **The number applied comes back**, on `metadata.max_step`, so a caller that asked for 100
  against a harness capped at 10 reads 10 rather than finding out by being stopped early.
- **`0` is a real request** — run, but call no tools — and is accepted on `claude-code`,
  `codex` and `pi`, which announce a call before making it. On `opencode` and `grok-cli` it
  is refused `422`: neither says anything about a call until it has happened, so a single
  overshoot is the whole of that budget. Every *positive* ceiling works on all five. A
  negative value is a `400`, and omitting the field leaves the agent unbounded.

**There is no default, and that is the one place this differs from the wall clock.** Every
task has a timeout because Security §5 makes bounding task duration this server's obligation;
almost no task has a step ceiling, because the wall clock already stops a runaway agent and a
surprise step budget would break every task that legitimately takes forty calls.

**A step is one tool call, and the unit is not identical across bases.** The schema calls
`max_step` a "tool-call round" budget and defines a round no further, so this server says
plainly which reading is in use, per base:

| base | what one step is | when the ceiling stops the run | `max_step: 0` |
| --- | --- | --- | --- |
| `claude-code` | one tool call | when the call after the ceiling is asked for | yes |
| `codex` | one tool call | when the call after the ceiling is asked for | yes |
| `pi` | one tool call | when the call after the ceiling is asked for | yes |
| `opencode` | one tool call | one call **past** the ceiling finishes — it announces no earlier | `422` |
| `grok-cli` | one **turn**, counted by `grok --max-turns` | grok stops itself and reports it | `422` |

**A ceiling stops a run, not a call, so it can be overshot.** This server reads a CLI's
stdout and kills its process group; nothing makes the CLI wait. By the time a tool call has
been read and counted, the agent has already dispatched it. So `max_step: N` means "stop this
run as soon as it goes past N", and the run may have taken a call or two more:

- `claude-code`, `codex` and `pi` act at the first possible moment — the call is seen as it
  is *requested* — so the overshoot is whatever runs in the moment before the kill lands.
  `claude-code` also puts a whole parallel batch of calls on one line, so a ceiling can be
  overshot by the size of one batch.
- `opencode` overshoots by at least one, always, because it says nothing until a call is over.
- `grok-cli` bounds itself and stops on its own turn boundary.

Overshooting is the tolerable direction — a run stops early rather than never, which is the
failure this field exists to prevent — and it is the reason `max_step` is a budget rather
than a guarantee of a call count. It is not a licence to run away: the ceiling stops the
*next* round of work in every case.

`grok` is the row to read twice. It bounds its own agent loop, so `max_step: 5` there buys
five turns rather than five calls — a coarser ceiling, and one that can cover several calls
made in parallel. Every other base counts calls. Neither reading is wrong against a schema
that declines to define a round; only silence about which is in use would be.

`opencode`'s row is the other measured surprise: it narrates a tool call only once the call is
over, established by running a twelve-second shell command and watching it say nothing until
the command finished. **So `max_step: 5` on `opencode` can cost six calls**, because the sixth
has already run by the time `opencode` mentions it. Stopping a call earlier is not available
— the alternative is to stop on the fifth call's own completion, which would kill a run that
used exactly its budget before it could answer, and a bound that breaks the runs obeying it is
worse than one that overshoots by a call. It is also why `max_step: 0` is the one ceiling
`opencode` cannot take at all.

A turn in which the agent only talks is not a step, so `max_step: 1` does not break a task
that answers without touching anything.

**Retries are not capped.** A client handed `incomplete` can send another task in the same
session for another N steps. That is how `timeout_seconds` already behaves, and per-task is
the only thing the wire lets a client express: `max_step` stops a runaway agent, it does not
cap a client's spend.

**A harness that cannot hold the bound refuses the task**, `422` with
`code: "uhpgo_step_budget_unsupported"`, rather than accepting a ceiling nobody enforces. No
base shipped today fails this outright — all five either narrate a countable call or bound
themselves — and the refusal exists so that adding a sixth cannot quietly un-honour the
field. The cases it reaches today are the two `max_step: 0` rows above. The
reasoning is [ADR-0009](adr/0009-a-step-is-one-tool-call.md), and the rule it applies is
ADR-0007's: a grant may be per-base, a bound may not.

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
[Authentication](authentication.md).

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
