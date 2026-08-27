# uhp-go

A server implementing the Unified Harness Protocol (UHP): one HTTP contract that drives
several agent harnesses. The protocol is specified externally at
unifiedharnessprotocol.org; this context covers the words this implementation uses.

## Wire and internal names

UHP names the objects it puts on the wire. This project keeps its own word for the
internal thing whenever the internal thing is genuinely different — a unit of work is not
the same as the object describing it, and a file a run produced is not the same as any
file. Where both words exist, the wire word is the protocol's and is not ours to change.

Every wire word below is a type in `uhp/`, and lives nowhere else in the tree. The internal
type embeds it rather than restating it, so the two words name one shape and cannot drift
apart. What this server adds to the protocol is in `uhp/uhpgo` and is not part of this
vocabulary — see [ADR-0002](docs/adr/0002-uhp-package-models-the-protocol.md) and
[ADR-0003](docs/adr/0003-internal-types-embed-the-wire-types.md).

**Response**:
The wire object UHP returns for a unit of work. Its shape is fixed by the protocol schema
for the version being served.
_Avoid_: Task (that is the internal word), Result, Completion

**Task**:
The internal unit of work: an input, the harness that runs it, and the run's bookkeeping.
Carries the Response it will be reported as.
_Avoid_: Job, Run, Request

**File**:
The wire object for a file, in either direction.
_Avoid_: Attachment, Document, Blob

**Artifact**:
A file produced by a run, discovered by walking the session's working directory. Every
Artifact is reported as a File; not every File is an Artifact.
_Avoid_: Output file, Result file

**SessionList** / **SessionPage**:
The wire object for one page of a session listing, and its internal counterpart.
_Avoid_: Sessions response, Page of sessions

**Share**:
A read-only view of a Session, published under an unguessable id that is itself the
credential for it. The wire object and the internal record are genuinely different — the
wire one carries a URL, which is a property of the origin this server is reached on rather
than of the share — so `domain.Share` does not embed `uhp.Share` and renders it instead.
The id is a capability, never an identifier for anything else, and it is not a bearer
token: it is a path segment under `/v1/shares/`, and presenting it as a credential
anywhere buys nothing.
_Avoid_: Link, Public URL, Share token, Session token

## Core concepts

**Harness**:
One configured runtime backend — the thing that turns a model into a working agent by
planning, calling tools, and iterating. Named by an opaque `chrn_` id and a `base`.
_Avoid_: Backend, Adapter, Agent, Provider

**Base**:
Which runtime a Harness runs, as a string the protocol deliberately does not enumerate.
_Avoid_: Type, Kind, Engine

**Session**:
A continued conversation across several Tasks, preserving conversational context, the
working directory, and the configured Harness. UHP spells one path over it `traces` —
`DELETE /v1/traces/{session_id}` is how a Session is deleted, while every read of it is
under `/v1/sessions/{id}` — and the id is the same id. That is a path segment, not a second
concept, so it stays in URLs and out of everything else, including test names.
_Avoid_: Conversation, Thread, Trace

**Turn**:
One Task in a Session's history, carrying enough to rebuild a transcript.
_Avoid_: Message, Exchange, Step

**Container**:
A Session's file store, seen from the Files chapter. A Session and its Container are the
same thing named from two places, so one id derives from the other.
_Avoid_: Workspace, Bucket, Volume

**Budget**:
A bound on one Task, enforced rather than recorded: the wall clock it may run for.
Resolved once per run as the shortest of the three bounds that are set — the request's,
the Harness's, and the deployment's own — so each of them is a ceiling and none is a
preference, and reported back on the Response so a client can see the number that was
applied rather than the one it asked for. A Task stopped by one is `incomplete` — never
`failed`, which means the work could not be done, and never `cancelled`, which means
someone asked for a stop. UHP names two, `timeout_seconds` and `max_step`; only the
first is enforced here, and the second is accepted and dropped.
_Avoid_: Timeout, Limit, Deadline, Quota. "Timeout" names one budget and not the
concept, and it stays where the name is not ours — the wire field
`timeout_seconds`, the `metadata.timeout_seconds` this server reports it back on, and
the `UHP_TASK_TIMEOUT` an operator sets. Everywhere the name is ours it is Budget:
`service.DefaultTaskBudget`, `WithTaskBudget`, `resolveBudget`, `Run.budget`.

**Capability**:
Something a Harness or the server advertises before a client relies on it. Advertised
capabilities are enforced, not merely reported.
_Avoid_: Feature, Flag, Support

**Conformance class**:
What the conformance suite grades an implementation into: `core`, `extended`, or `full`.
A claim, falsifiable by running the suite.
_Avoid_: Level, Tier, Compliance level

**Protocol version**:
A date, `YYYY-MM-DD`, naming a published version of UHP. Immutable in structure once
published: within a version, fields may be added but never renamed, removed, or
redefined.
_Avoid_: API version, Semver, Release
