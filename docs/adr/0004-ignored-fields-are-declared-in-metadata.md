# ADR-0004: A dropped request field is named in `metadata.ignored_fields`

Date: 2026-08-27
Status: Accepted
Issue: [#80](https://github.com/aenawi/uhp-go/issues/80),
from [#48](https://github.com/aenawi/uhp-go/issues/48)

## Context

`CreateResponseRequest` has thirteen properties. This server now reads eight of them and
drops five: `max_output_tokens`, `max_step`, `tools`, `include` and `background`.

Dropping them is not a defect and is not a choice. Tasks §1.1 marks every field but `input`
optional and *requires* a server to ignore a request field it does not understand rather
than reject it. A server that answered `400` for `max_step` would be the non-conformant one.

What the specification does not require is that the dropping be silent, and silent is what
it was. Issue #48 put the cost in one sentence: *"A caller that sets `max_step: 5` to bound
an agent's tool-call rounds gets unbounded work and no signal that the budget was
discarded."* The caller's next move — wait, retry, halve the number, file a bug — depends on
information the response did not carry. Two of the five fields are budgets that carry a MUST
once honoured, so a client cannot even distinguish "this server bounded my task at five
steps" from "this server ran unbounded" by looking at the result.

`docs/conformance.md` has recorded the gap since the field audit, and documentation is the
wrong channel for it: it answers a reader of this repository, and the person who needs the
answer is a client author whose only view of this server is a response object.

## Decision

A response names the request fields this server accepted and did not act on, as a string
array under `metadata.ignored_fields`, absent entirely when there are none.

```json
{ "metadata": { "session_id": "sess_…", "ignored_fields": ["max_step", "tools"] } }
```

`metadata` is where it goes because the schema has nowhere else to put it and because this
server already adds four keys there — `session_id` (a MUST, Tasks §3), `harness_id`,
`timeout_seconds`, and the model-substitution pair from §1.3. Metadata is
`additionalProperties: true` and is the extension point the task surface already defines.

**Absent, not empty, when nothing was dropped.** A client can test presence, and a request
that sent nothing unread produces exactly the response it always did.

**Only fields this server knows and does not act on.** Unrecognised fields are deliberately
not named. §1.1's ignore-don't-reject rule exists so a newer client can talk to an older
server; a server that reported every field it did not recognise would turn forward
compatibility into a stream of warnings about perfectly valid protocol. "We do not implement
this" is a fact about this server. "We have never heard of this" is a guess about who is out
of date.

**Only values that ask for something.** `null` is a key with no instruction in it. And
`background: false` names the behaviour this server actually provides — a POST held open
until the task is done — so reporting it would claim a request was ignored that was honoured
exactly. `background: true` is dropped, and is reported.

## Considered options

**Reject with `400`.** Forbidden by §1.1, and it would break the compatibility rule the
whole ignore-don't-reject design exists to protect.

**Keep the silence and improve the documentation.** The status quo. It answers a reader of
this repository rather than a caller of this server, and #48 is a complaint about the
caller's experience.

**Log it server-side.** Helps the operator, who is not the party that lost information. A
client author debugging against a hosted deployment cannot read the server's logs.

**A per-field object with reasons** — `{"max_step": "not implemented"}`. Invites a
vocabulary of reasons that nothing consumes, and turns a fact into prose that would need its
own compatibility rules.

## Consequences

**This is an extension, and it is now effectively permanent.** Clients may come to depend on
the key, and withdrawing it would be a silent regression for anyone who did. That is the
cost of the decision and the reason it is recorded here rather than in a comment.

**A key a client sends is overwritten.** `metadata` is caller-supplied, so a client that
sends its own `ignored_fields` finds it replaced — exactly as `session_id` already is. The
server's answer to the question has to be the server's.

**The list is hand-maintained and cannot be derived.** A field leaves it by being
implemented, and no compiler notices that. `TestTheDroppableListIsTheFieldsThisServerDoesNotRead`
is what notices: it pins the five names and checks that eight-read-plus-five-dropped is the
schema's thirteen.

**It reports what a server does, not what a protocol says.** A different conformant UHP
server will not emit this key, and a client must not read its absence as "nothing was
dropped" when talking to one. The honest framing is that this key is a courtesy from this
implementation, which is why it is namespaced by nothing and promised by nobody.
