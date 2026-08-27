# ADR-0005: `background: true` answers at acceptance, and streaming is already background

Date: 2026-08-27
Status: Accepted
Issue: [#78](https://github.com/aenawi/uhp-go/issues/78),
from [#48](https://github.com/aenawi/uhp-go/issues/48)

## Context

`background` is the thirteenth property of `CreateResponseRequest` and was the last of the
five this server accepted and dropped that needed no machinery to implement. Tasks §1.1 has
it return as soon as the task is accepted, to be followed elsewhere rather than held open.

Everything that would be needed for that already existed, and was already reachable by
another route:

- The run outlives the request. `task_service.startTask` detaches with
  `context.WithoutCancel` precisely so a client disconnecting does not stop the agent.
- A run retains its whole event log, which is what makes an idempotent stream replay
  identical to the original rather than a reconstruction of it (`service/idempotency.go`).
- `GET /v1/responses/{id}` already answers mid-run, and a repeated `Idempotency-Key` already
  hands a retry the first request's own run.

So the work was never machinery. It was three decisions: what a `background: true` POST
returns and when, what it does in the one combination the schema permits and does not
describe — `background` with `stream` — and what it does in the one combination that has no
honest answer.

Until this was decided, `background: true` was named in `metadata.ignored_fields`
(ADR-0004), and `background: false` was not, because `false` was the behaviour this server
already provided.

## Decision

**A `background: true` POST is answered `200` as soon as the task is accepted,** with the
response object as it stands at that moment — normally `in_progress`, with `id`,
`metadata.session_id`, `metadata.harness_id` and `metadata.timeout_seconds` all already set.
It does not wait for `run.Wait`.

The object is read back from the store rather than marshalled from the pointer `StartTask`
returned. That pointer belongs to the supervisor from the moment the run is handed over, so
encoding it in the handler would be a data race against the goroutine writing the task's
status and output. Reading it back is also the more truthful answer: a run that finished in
the microseconds between acceptance and the read is reported as finished.

A retained response is answered from the store, which mid-run is the only thing holding the
task. An unretained one is answered from the run, which keeps the terminal task precisely
because the store will not — `Run.Settled` being `Run.Result` for a caller that must not
block, which is exactly what a background POST is.

Two ways to follow the task, both of which already worked:

- `GET /v1/responses/{id}`, which answers mid-run and every later read.
- The same POST again, with the same `Idempotency-Key` and `stream: true` — Tasks §6 hands
  the retry the first request's run, so the stream replays the whole event log from
  `response.created`, including everything that happened while nobody was listening. A
  `Last-Event-ID` narrows that to a resume point.

**`background: true` with `stream: true` streams, exactly as it always did, and the field is
honoured rather than dropped.** A stream is a held-open POST by construction, so "return
immediately" cannot be what `background` means there. What it means in every other sense is
already true of this server's streams: the run is detached and survives the client
disconnecting, the response is readable by id while it runs, and the stream can be rejoined
from where it was left. Reporting the field as ignored on a streaming request would name a
field this server acted on as one it discarded.

**A background POST that is not streaming and names a response that will not be retained is
refused, `400 invalid_input`, with `param: "background"`.** The two together ask this server
to deliver the answer nowhere. `background` says "do not deliver it here, I will come back
for it"; `store: false` says the record is dropped the moment the run is terminal, so there
is nothing to come back to. The client would receive an `in_progress` object and then `404`
for the rest of time. The same pair on a stream is fine and is not refused, because the
terminal event carries the whole response before the record goes — which is what makes this a
refusal about a combination rather than about either field.

It is checked in two places, and the wording above is why. `handleCreateTask` checks the
request body before starting anything, which is where the refusal belongs for a fresh
request: nothing should be forked for a task whose answer has nowhere to go. But an
idempotent retry is not obliged to repeat the `store: false` its first request sent, so a
body carrying nothing but `background: true` can name a run whose response will never be
retained, and a check on the body cannot see it. `writeAccepted` therefore checks again
against the accepted task, where `store` actually lives — and only after asking the run, so a
retry whose run has already finished is answered with the result rather than turned away over
a record that is already gone.

The pre-flight check is skipped entirely for a request carrying a key this server already
knows, and that is a rule about Tasks §6 rather than an optimisation. Both refusals would
otherwise land on the same retry differently: the one that faithfully repeats its original
`store: false` would be refused by the body check, while the one that dropped the field would
reach `writeAccepted` and be handed the first request's answer. §6 owes both of them that
answer. A key that expires between the check and the claim behind it starts a fresh task and
is refused by `writeAccepted` instead — a run nobody collects, and the same `400`, for a
window narrower than the request itself.

`writeAccepted`'s refusal carries `error.detail.response_id`, because by then the run is going
and a client told only "no" is holding one it can neither follow nor stop. The pre-flight one
does not, and cannot: it fires before anything is started, so there is no id and no run —
which is the whole reason to prefer it where it applies.

**`background` leaves `droppableFields`.** Neither of its values is reported in
`metadata.ignored_fields` any more: `false` is the POST held open that it always was, and
`true` is now implemented. The `carriesInstruction` special case that existed only for
`background: false` goes with it.

## Considered options

**Return `202 Accepted`.** Truer to HTTP, and wrong for this protocol. Tasks §1.1 describes
one response object with a `status` field carrying exactly this information, and Errors §1
attaches meaning to non-2xx bodies rather than to the difference between two 2xx codes. A
client switching on the status code would have to learn a second vocabulary to be told
something `status: "in_progress"` already says.

**Refuse `background` with `stream`.** The schema permits the combination, so refusing it
would invent a conflict the protocol does not have — and would refuse a request whose
behaviour this server already implements correctly.

**Report `background` as ignored when streaming.** Considered because it is conservative,
and rejected because it is false: it would tell a client that a field was discarded on
precisely the requests where the thing it asks for is delivered.

**Let `store: false` win over `background` and hold the POST open.** Silently dropping a
field the client set, which is the defect ADR-0004 exists to end.

**Let `background` win and keep the response until it is read once.** A read-once retention
policy is new machinery, a new expiry question, and a new failure mode for a client that
never comes back. The refusal costs a client one round trip and tells it exactly which half
to change.

## Consequences

**A background POST's answer is a starting point, not a result.** `output` is empty and
`status` is `in_progress`. A client that reads the body as a finished response gets an empty
answer rather than an error, which is the cost of the field working at all; it is why the
refusal above exists for the one case where the finished answer would never arrive.

**An idempotent retry of a background POST is answered with the object as it stands then,
not with a copy of the first answer.** Tasks §6 requires a retry to be given the first
request's answer rather than "a partial or a conflict", and for a background request the
answer *is* the object as it stands — the same response, further along. A retry that was
handed a stale `in_progress` snapshot would be the partial one.

**A background POST that cannot read its task back still names it.** The task was accepted
and the run is on its way to a terminal state, so a client told only `500` would be holding a
run slot it can neither poll nor cancel. The id is the handle for both and goes in
`error.detail.response_id`. It is also the one field safe to read from the handler: it is
written once, before the supervisor is handed the task, and never again.

**Session concurrency is unchanged, and is now easier to hit.** Lifecycle §5 still allows one
task in flight per session, so a client that fires background tasks into one session gets
`session_busy` on the second. Background makes it cheap to try, where holding the POST open
made it self-limiting.

**`uhp.Client.Create` clears the field rather than sending it.** The method is documented as
waiting for the finished Response, and `background` is the request that makes that
impossible — a caller who set it would get an `in_progress` object with an empty `output`
from a method whose whole promise is the opposite. It is cleared exactly as `stream` already
is, for exactly the same reason, and the doc comment now says so.

**Four request fields are still dropped**: `max_output_tokens`, `max_step` (#72), `tools` and
`include`. Nine of thirteen are now read.
