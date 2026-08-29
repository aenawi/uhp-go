# ADR-0007: A declined request field is not a pending one

Date: 2026-08-28
Status: Accepted
Issue: [#48](https://github.com/aenawi/uhp-go/issues/48)

## Context

`CreateResponseRequest` has thirteen properties. This server reads nine and drops four:
`max_output_tokens`, `max_step`, `tools` and `include`.

[ADR-0004](0004-ignored-fields-are-declared-in-metadata.md) made the dropping visible — a
response names them under `metadata.ignored_fields` — and in doing so framed the list as
transitional: *"A field leaves it by being implemented, and no compiler notices that."* That
was true of `background`, which was implemented ([ADR-0005](0005-background-answers-at-acceptance.md))
and left. It is not true of three of the four that remain, and reading the list as a
to-do list has kept them alive as work nobody can start.

The obstacle is one thing wearing three costumes, and #48 names it in a sentence: **this
server does not talk to a model. It drives five CLIs that each talk to a model.** Not one of
`claude`, `codex`, `grok`, `opencode` or `pi` accepts a sampling parameter, offers a way to
grant a tool for one invocation, or emits a uniform set of optional extras.

Three facts settle what is and is not reachable from here:

- **Token accounting arrives only when a run is over, and not from every base.** `claude`
  emits it on `result` ([claude.go:249](../../internal/harness/claude.go#L249)), `codex` on
  `turn.completed` ([codex.go:201](../../internal/harness/codex.go#L201)), `grok` on `result`
  ([grok.go:238](../../internal/harness/grok.go#L238)). `opencode` and `pi` emit none at all.
  Nothing can be stopped at a token ceiling because nothing knows the count until it is too
  late to stop.
- **A tool can be withheld and not granted.** The delivery hooks in
  [harness.Delivery](../../internal/harness/harness.go#L65) are `DisallowArgs` on three bases
  and `MCPArgs` on one — `claude` alone ([claude.go:122](../../internal/harness/claude.go#L122)).
  The harness-level machinery is subtractive because withholding is what a flag can express.
- **The schema describes neither `tools` nor `include`.** `tools` is `array of object,
  additionalProperties: true` with no description; `include` is `array of string` with no
  enumeration. There is nothing to implement without first deciding what a client meant.

## Decision

**A field only some bases can honour is honoured where it can be and named in
`metadata.ignored_fields` where it cannot — except where dropping it changes what the client
believes about the bound on its own task.**

A grant degrades safely: a task that did not get an extra tool has less, and the response
says so. A bound does not degrade: a caller who set one and was not told believes their work
is capped when it is unbounded, and the belief costs money. So a grant may be per-base, and a
bound must hold on all five bases or not be claimed at all.

Measured against that rule, all three of the fields #48 is scoped to are **declined**: this
server will not implement them, and each is on the ignore-list as a decision rather than as
work not yet started.

### `max_output_tokens` — declined

It is a bound, so the rule requires all five. No base takes it on the command line, and the
server cannot substitute for them: the accounting it would have to count arrives only at the
end of a run, and from three bases of the five. A ceiling that is checked after the tokens are
generated has not bounded anything — Tasks §1.1 requires a budget to *stop* the task at or
after the bound, and post-hoc detection stops nothing.

Passing it to the agent as prose — "keep your answer under N tokens" — is the one option that
would have worked on all five, and it is rejected deliberately. Harnesses §4.3's rule, that a
restriction the runtime cannot enforce MUST still reach the agent, is about an *operator's*
restriction, where reaching the agent imperfectly beats dropping a safety rule silently. A
client's cost ceiling is the opposite case: honouring it in prose takes the field off
`ignored_fields`, and the caller stops being told the number was not applied. **The silence
ADR-0004 removed would come back wearing the appearance of success.** An honest no is worth
more than a wish.

### `tools` — declined, and asked upstream

The schema says these are objects and declines to say anything else. Any meaning this server
picks is invented, and an invented meaning is worse than none: a client that gets `tools`
right against this server gets it wrong against every other conformant one, which is
fragmentation introduced from inside an implementation.

The nearest thing that works — treating each entry as an MCP server declaration — would be
real on `claude` today, and the per-run MCP document already exists
([harness_runtime.go:271](../../internal/service/harness_runtime.go#L271)). It is still
declined, because the rule that permits per-base behaviour assumes the request is understood.
Here it is not. #48 asks for a meaning "that is not a lie", and for this field **the guess is
the lie**.

The two readings do not merely differ, which is the sharpest form of the argument and worth
stating here rather than only upstream. Under the MCP reading `tools` is implementable on one
of the five bases today. Under the function-declaration reading it is implementable on none
of them, because these are agent harnesses rather than model endpoints and not one exposes a
per-call tool list. Same field, same schema, opposite answers to whether a conformant server
can support it at all — so this is not a detail an implementer can settle by picking sensibly.

This is the one of the three that is blocked rather than impossible, and the block is not in
this repository. The question is raised upstream as
[HarnessRouter/harnessrouter#42](https://github.com/HarnessRouter/harnessrouter/issues/42),
prose-first per their `GOVERNANCE.md`, as [README.md](../../README.md#L61) already undertakes
to do for exactly this kind of gap; [#86](https://github.com/aenawi/uhp-go/issues/86) tracks
it here and closes when upstream answers. Three resolutions were offered, and the cheapest is
worth naming because it needs no schema work: a statement that the field is deliberately
opaque and servers are expected to ignore it would close the ambiguity outright. If instead
UHP describes the shape, this decision is revisited for `tools` alone, on its merits.

### `include` — declined

Two problems, either of which is sufficient. There is no vocabulary: the schema enumerates no
values, so the same string means whatever each server decided. And there is nothing to
return: everything this server collects already reaches the response, and the one extra
signal an adapter could have produced, `UpdateToolCall`
([harness.go:95](../../internal/harness/harness.go#L95)), is declared and emitted by no
adapter and read by nothing. Implementing `include` is inventing a vocabulary nobody shares
*and* building the plumbing to fill it.

### The list says which is which

`droppableFields` carries a status per entry — `declined` for these three, `pending` for
`max_step` (#72, which needs a step counter no adapter offers, on top of the `incomplete`
path #54 built). *(Amended 2026-08-29: the adapters offer one now, and `max_step` has left
the list entirely — [ADR-0009](0009-a-step-is-one-tool-call.md).)* Nothing on the wire changes: `metadata.ignored_fields` is the same array of names
in the same schema order, because the distinction is a fact about this server's intentions and
not about the caller's request.

It is in the code rather than only in this document because the list is the thing a
maintainer reads. A comment saying "three of these are settled" is a claim the next edit can
falsify silently; a field on each entry is one the test can pin.

## Considered options

**Implement `tools` as MCP servers on `claude` and report it ignored elsewhere.** The most
useful option, and the reason this ADR exists rather than a one-line comment. Rejected because
the per-base rule presumes an understood request, and this one is undefined at the schema.
Revisit if UHP defines it.

**Honour `max_output_tokens` as a standing instruction.** Rejected above: it converts a
disclosed omission into an undisclosed approximation, which is the defect ADR-0004 was written
to remove.

**Detect a `max_output_tokens` overrun after the fact and report `incomplete`.** Honest about
what happened and useless as a bound — the tokens are generated and paid for, and two bases
cannot tell. `incomplete` would then mean "this exceeded a number" on three bases and nothing
on the other two, which is precisely the non-uniformity the rule forbids for a bound.

**Define our own `include` vocabulary.** Fragmentation with extra steps, and no caller has
asked for a value it would carry.

**Leave all three on the list as-is and say nothing.** The status quo, and the reason #48 has
been rewritten twice. A field with no decision against it reads as work, and three
undecidable pieces of work at the top of a list is what stopped anyone reading the rest.

## Consequences

**Three fields are permanently on `metadata.ignored_fields`.** A client sending any of them
gets it named on every response, forever, on every base. That is the intended outcome and not
a regression to be fixed later.

**The `pending` bucket now has one member.** `max_step` is the only field on the list that
anyone should expect to move, which makes #72 legible as the one open piece of work rather
than one of four. *(Amended 2026-08-29: it moved.
[ADR-0009](0009-a-step-is-one-tool-call.md) implements `max_step`, so the bucket is empty and
the list is entirely decisions. The rule in the Decision above was what decided it: a step can be counted as
a call is requested and the run cancelled before it runs, which is exactly what
`max_output_tokens` could not do — so the same test that declined one admitted the other.
ADR-0009 also records where the rule bit hardest: `pi` had no reachable provider and `grok`
counts turns rather than calls, and neither could be waved through on the grounds that the
other four were fine.)*

**A declined field is still accepted.** Tasks §1.1 requires ignore-don't-reject, and nothing
here rejects anything: a request setting all three succeeds, runs, and is answered. Declining
is a statement about implementation, never about validation.

**`tools` depends on someone else.** If UHP defines the object shape, this ADR is superseded
for that field alone. Filing the question is part of this change; the answer is not ours to
schedule.

**The rule is now written down and will be applied again.** The next request field that only
some bases can serve is decided by the grant/bound test in the Decision above, not from
scratch.
