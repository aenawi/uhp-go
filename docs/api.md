# The HTTP surface

Every endpoint this server answers, which request fields it reads, and how a client picks a dropped stream back up.

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
credential. See [Files](files.md), [Harness management](harnesses.md) and
[Session sharing](session-sharing.md).

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
| `timeout_seconds` | Narrows the wall-clock budget, never widens it — see [Task budgets](operations.md#task-budgets) |
| `max_step` | Narrows the step budget, never widens it — see [Step budgets](operations.md#step-budgets) |
| `background` | `true` answers the POST as soon as the task is accepted, instead of holding it open |
| `max_output_tokens`, `tools`, `include` | Accepted and **declined** — this server will not implement them, and each is named in `metadata.ignored_fields`. See [ADR-0007](adr/0007-a-declined-field-is-not-a-pending-one.md) |

The last row is the one worth reading twice. A *declined* field is a decision that will not
be revisited without a reason, not work somebody has yet to do — and every field still
dropped is now one of those. `max_step` was the only exception, and it left the list by being
implemented ([ADR-0009](adr/0009-a-step-is-one-tool-call.md)); `background` did the same
before it (ADR-0005). Both looked identical to a caller either way, which is why the
distinction lives in the code and in ADR-0007 rather than on the wire.

Dropping a field at all is specified behaviour;
dropping it silently was not, and a caller that set `max_output_tokens` to cap its spend got
uncapped work and no way to learn why. So a response now says which of its fields were
dropped:

```json
{ "metadata": { "session_id": "sess_…", "ignored_fields": ["max_output_tokens", "tools"] } }
```

The key is absent when nothing was dropped, so its presence is the signal. Only fields this
server knows and does not act on appear: an unrecognised field is ignored without being
named, because §1.1's ignore-don't-reject rule is what lets a newer client talk to an older
server and naming every unknown field would turn that into a stream of warnings about valid
protocol. A `null` value asks for nothing and is not reported. This is an extension rather
than protocol — see [ADR-0004](adr/0004-ignored-fields-are-declared-in-metadata.md) —
so a client must not read its absence from some other conformant server as "nothing was
dropped".

**The same caveat covers `metadata.timeout_seconds` and `metadata.max_step`**, which report
the budgets actually applied. All three keys are this server's own: the schema has nowhere on
a response to put them, `metadata` is the open object they fit in, and none of the three is
something another conformant server owes you. Read their absence elsewhere as "this server
does not say", never as "there was no bound". Naming one extension key and leaving two
undisclosed would make the inconsistency the rule.

**`background: true` answers the POST at acceptance and leaves the run going.** The body is
the response object as it stands — normally `status: "in_progress"`, with an empty `output`
and its `id`, `metadata.session_id` and `metadata.timeout_seconds` already filled in (plus
`metadata.max_step`, when the task asked for a step ceiling). Two
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
[ADR-0005](adr/0005-background-answers-at-acceptance.md).

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

### What a turn item carries

`GET /v1/sessions/{id}/turns` — and `GET /v1/shares/{share_id}/turns`, which renders the
same objects to whoever holds the link — answers items shaped by Sessions §3:

| Field | |
| --- | --- |
| `id` | the turn's response id. **Required by §3.** `GET /v1/responses/{id}` returns the turn in full; everything else here is a summary |
| `status` | the response's status. **Required by §3** |
| `user` | the input that opened the turn |
| `assistant` | the text the agent answered with |
| `files` | what the turn wrote, as the schema's six-property file object. Always an array, empty when the turn wrote nothing |
| `model` | what served the turn. An extension, kept because a session may span models |
| `created_at` | Unix seconds |

`tools` is not answered. §3 asks for it as a SHOULD, and this server counts a turn's tool
calls so `max_step` can stop a run (ADR-0009) and then discards them — no name, no
arguments, no result reaches a stored task. Answering an empty array would say the turn
called no tools, which is a different and usually false claim.

`files` carries `id`, `container_id`, `filename`, `bytes` and `created_at` but not the
`download_url` and `mime_type` that `GET /v1/sessions/{id}/files` adds. The ids are enough
to ask that endpoint, and rendering one file two ways would leave a client deciding which
is authoritative.

**Three fields are deprecated and still answered.** `response_id`, `input` and `output` are
the names this server used before Sessions §3 described the shape — its items were
`object` with `additionalProperties: true`, so there was nothing to mirror. They hold
exactly what `id`, `user` and `assistant` hold, and they go in a later release: `uhpc` is
installed separately from `uhpd`, so a client is routinely a version behind a server, and
the rename that added the specified names is not the release to remove the old ones in.

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

Every base shipped here advertises `sessions`. `grok-cli` was the last holdout and
stopped being one in issue #34: grok 1.0.5 puts a `session_id` on every line of
`--output-format streaming-messages-json` and resumes it with `--resume`, so both halves
of the capability now exist where before neither did.
That is the only entry in the list a declaration decides. Three of the six capabilities are
not the CLI's to claim and no declaration names them:

- `cancellation` belongs to the shared runner — every harness runs in its own process group
  and is stopped by killing it — so every base advertises it, including any you add.
- `files_in` and `files_out` belong to the router. It writes a task's attachments into the
  session working directory before the run and diffs that directory afterwards for
  artifacts, and neither step asks the adapter anything. So every base advertises both,
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
