# ADR-0002: `uhp/` models the protocol, not this implementation

Date: 2026-08-24
Status: Accepted
Issue: [#16](https://github.com/aenawi/uhp-go/issues/16)

## Context

Everything in this repository lives under `internal/`, so the only consumable artefact is a
binary. UHP is a protocol, and a protocol implementation that cannot be imported forces
every client author to hand-roll the wire types from the specification.

Issue #16 proposed extracting `uhp/` at the repository root and framed the decision as a
timing problem: from the moment the package ships, its types are public API and changing
them is a breaking change, so the issue weighed extracting now against waiting until the
`extended` and `full` work settled the shapes.

**Both halves of that framing have since stopped holding.** The work the issue was waiting
on has landed — file input parses `file_data` / `file_id` / `image_url`, every `ContentPart`
carries an always-present `Annotations`, and `docs/conformance.md` records 52/52 `full`. And
the freezing argument was answered by the protocol itself rather than by us: UHP versions
are **dates, not semantic versions**, and `protocol/VERSIONING.md` makes a published version
immutable in structure. Within `2026-08-11` a server may add fields, event types and
vendor-prefixed error codes, and MUST NOT rename a field, change its type or meaning, or
remove an event type. The wire shapes were frozen by the specification on the day it was
published. They were never ours to freeze or to defer freezing.

What remained was therefore not *when* to extract but *what the package is a model of*. The
two candidates are genuinely different, and the difference is measurable. Reading this
server against `uhp-2026-08-11.schema.json`, which defines 23 named objects:

- `CreateResponseRequest` has 13 properties. `createTaskBody` has 5. Absent:
  `instructions`, `store`, `max_output_tokens`, `max_step`, `timeout_seconds`, `tools`,
  `include`.
- `Event` has 17 properties. `domain.Event` has 15 — no `annotation`, `arguments`,
  `summary_index` or `annotation_index` — and adds `response_id` and `session_id` of its own.
- `Error.type` is required and is a six-value enum. This server defines three of the six.
- Six schema objects have no counterpart here at all: `Discovery`, `Capabilities`, `Model`,
  `ModelCatalog`, `HarnessModels`, `HarnessCreate`.

A package modelling this server would ship all of those gaps as if they were the protocol.

## Decision

`uhp/` mirrors the schema, completely, for the version this server implements. Where the
server is narrower than the protocol, the package follows the protocol and the shortfall is
recorded in `docs/conformance.md` rather than hidden by omission.

**The schema's names win inside `uhp/`.** `Response`, `ResponseStatus`, `Error`,
`ErrorEnvelope`, `File`, `SessionList`, `CreateResponseRequest` — not the internal words.
`internal/domain` keeps its own vocabulary where the internal concept is genuinely a
different thing: a `Task` is the unit of work and a `Response` is what goes on the wire; an
`Artifact` is a file a run produced and a `File` is any file. Both words are recorded in
`CONTEXT.md`.

**Extensions live in `uhp/uhpgo`, not in `uhp`.** Every schema object is
`additionalProperties: true`, so this server's additions are legal — but they are not
protocol, and a reader of godoc cannot tell the difference from a comment. Moving them makes
the boundary a compiler-visible fact. The set is `Harness.Models`, `Harness.Capabilities`,
`Harness.Status`, `Event.ResponseID`, `Event.SessionID`, `Error.Retryable`, `Upload`, and
the eleven `uhpgo_`-prefixed error codes.

**This repository is a complete project that consumes its own `uhp` package.** No wire
shape may exist anywhere in the tree except in `uhp`. That reaches past `internal/domain`
into `internal/transport/http`, which currently hand-rolls four schema objects as
unexported structs: `errorEnvelope`, `errorPayload`, `discoveryDoc`, and the bare
`map[string]bool` returned by `capabilities()`. All four become `uhp.ErrorEnvelope`,
`uhp.Error`, `uhp.Discovery` and `uhp.Capabilities`. Four of the six objects that appeared
to need writing from scratch turn out to be ones this server already wrote privately.

This widens #16 deliberately. The issue scopes the work to "the wire types only — not the
adapters, not the store, not the router", and collapsing transport's private structs is
outside that line. The line was drawn before anyone had noticed that the router already held
private renderings of four schema objects, two of which contradict each other. Nothing in
UHP requires an implementation to hold one internal copy of a wire type; a server with five
copies of every struct can still score 52/52. This is a choice, not a conformance
obligation — made because a router whose purpose is to stop its clients duplicating
integrations is a poor place to duplicate the protocol internally.

**`uhp.Turn` ships with a caveat in its godoc.** `GET /v1/sessions/{id}/turns` is specified
in `sessions.md` §3, but its response shape is not among the schema's 23 objects. Publishing
this server's shape gives a client calling a specified endpoint something to unmarshal into;
saying so in godoc is what keeps that honest. The gap is worth reporting upstream.

**A vendored copy of `uhp-2026-08-11.schema.json` gates the package in CI.** Each public
type is marshalled and validated against the schema on every push.

## Consequences

**The package will advertise eight request fields this server ignores.** A caller setting
`MaxStep` on a `uhp.CreateResponseRequest` gets silence. This is legal on both sides —
`tasks.md` §1.1 marks all eight optional, and §1.1 also requires a server to ignore request
fields it does not understand rather than reject them — but it is a real gap between what
the type offers and what this server does. Note that `max_step` and `timeout_seconds` carry
a MUST *if honoured*: stop at or after the budget, report `incomplete`, and never report
`completed` for truncated work. Implementing them later means implementing that too.

**The conformance suite measures none of those eight fields.** Its 52 checks contain no
reference to `max_step`, `timeout_seconds`, `max_output_tokens`, `instructions` or
`include`. The 52/52 `full` result is silent about all of them, and the same is true of
`store`, which is hardcoded `true` at `internal/service/task_service.go:358` with the
request field never read. That is permitted — `tasks.md` §4 says a server MAY return `404`
for a `store: false` response, not that it must honour the flag — but it is unmeasured
rather than verified, which is the distinction `docs/conformance.md` exists to draw.

**Moving `Harness.Capabilities` out of `uhp` is the decision that stings.** Capability
enforcement is load-bearing here: a `previous_response_id` sent to a harness without
`sessions` is refused `422`. But the harness object in the schema has thirteen properties
and `capabilities` is not one of them, and the nine-boolean `Capabilities` object belongs to
`Discovery`. A client that depended on the harness-level array against a different
conformant server would find nothing there.

**Adding a field to a published Go struct is not as additive as the protocol's rule.** UHP
permits adding response fields within a version, and Go permits adding struct fields — but
an unkeyed composite literal, `uhp.Response{a, b, c}`, stops compiling. The package's godoc
should require keyed literals, which is the only mitigation there is.

**The schema's `Error` object already had two divergent renderings here, and neither was
complete.** `domain.TaskError` carries `retryable`, which is not in the schema, and lacks
`param` and `detail`, which are. `http.errorPayload` carries `param` and `detail` and lacks
`retryable`. Both model the same object; a client reading `retryable` off a response's
`error` finds it absent from an HTTP error envelope for no reason the specification
explains. Collapsing them onto `uhp.Error` is not a tidying pass — it resolves a
contradiction that shipped.

**Extraction found a live defect rather than merely relocating types.** `Error.type` is
required by the schema and constrained to six values; `domain.TaskError.Type` carries
`omitempty` and is left empty at `internal/service/task_service.go:392`, which also uses an
unprefixed vendor code, `adapter_start_failed`. A client following `VERSIONING.md`'s fourth
client rule — treat an unrecognised `code` as its `type` — is handed nothing to fall back
on. Tracked as [#47](https://github.com/aenawi/uhp-go/issues/47) and fixed before extraction,
because it ships wrong today whether or not `uhp/` ever exists.

**The schema test and the conformance suite do not overlap.** The suite proves *this server*
conformant end-to-end, costs real tokens, and runs on a maintainer's machine rather than in
CI. It never touches the published Go types, so nothing in it would catch `uhp.Response`
drifting from what the server emits. The schema test is free, runs on every push, and
catches exactly that.
