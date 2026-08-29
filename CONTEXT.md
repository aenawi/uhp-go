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
for the version being served. Retained for later reads unless the request said otherwise:
`store: false` means the record is kept only while the run needs it and dropped once the run
is terminal, so the client is answered exactly once — in the POST body or the terminal
stream event — and every later read is `404`. That one delivery is why a `background` POST
that is not streaming is refused when the Response it names is one of these: `background`
answers at acceptance rather than with the result, so the pair would send the answer nowhere,
and the request is refused rather than half-honoured. The exception is a retry whose run has
already finished, which is answered with the result — the one delivery, made to the request
that came back for it. What goes is the Response and only the
Response; the Session survives it, and so do the Artifacts on disk.
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

**Instructions** / **Standing instructions**:
The wire word is the protocol's and names one task's own system guidance: `instructions` on
the request, additional and applying to that task alone. Standing instructions are this
project's word for the harness's block — the operator's system prompt, plus whatever the
runtime cannot enforce natively and must be told instead — which applies to every task on
that harness. Two different things that were one word until the second acquired its
qualifier.

They are composed and never exchanged: standing instructions, then the task's, then the
input. A task's instructions cannot displace the harness's, because the standing block is
where a tool restriction lands when the runtime cannot enforce it and Harnesses §4.3 forbids
dropping such a restriction — so a request that could replace the block could switch off an
operator's configuration by sending one field. Each level of configuration is a floor the
next one cannot remove, which is the same rule Budget follows.
_Avoid_: System prompt (that is one input to a standing block, not the block), Prompt,
Preamble, Guidance

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
anywhere buys nothing. Revocation is the only thing that withdraws one; the deployment's
sharing capability being turned off suspends every Share instead — the endpoints stop
answering and the ids survive, so a restart with it back on resolves them all again.
_Avoid_: Link, Public URL, Share token, Session token

## Core concepts

**Harness**:
One configured runtime backend — the thing that turns a model into a working agent by
planning, calling tools, and iterating. Named by an opaque `chrn_` id and a `base`. Every
Harness is granted write access to the working directory its Session was given, uniformly
across the five bases, because an agent that cannot write cannot use the Container it was
handed — see [ADR-0008](docs/adr/0008-an-agent-may-write-in-the-directory-it-was-given.md).
Confinement to that directory is a separate claim and a weaker one: `codex` is confined by
an argument this server passes, and the other four are bounded by nothing it passes.
_Avoid_: Backend, Adapter, Agent, Provider. "Sandbox" names one base's argument, not the
grant — the grant is the same on all five and only one of them enforces a wall.

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
One Task in a Session's history, carrying enough to rebuild a transcript. Derived from the
stored Task rather than recorded beside it, so a response the client asked not to retain
(`store: false`) is not a Turn and appears in no transcript and no Share. Many Steps happen
inside one Turn.
_Avoid_: Message, Exchange. Not Step: a Step is a real and different thing here — see
below — and this entry used to say otherwise.

**Step**:
One tool call an agent makes inside a Task. The unit `max_step` bounds, and it is a call
rather than a round because rounds do not survive contact with the CLIs: `opencode` grouped
the same five writes into five of its own "steps" in one capture and into one in the next, so
a ceiling counted that way would never fire. A turn in which the agent only talks is not a
Step, or `max_step: 1` would break a task that answers without touching anything.
"Step" over "Round" because the wire word is the protocol's and not ours to change, even
though the schema calls the field a "tool-call round" budget and defines a round no further.
Established per base by capture rather than assumed — see
[internal/harness/testdata/steps/](internal/harness/testdata/steps/) and
[ADR-0009](docs/adr/0009-a-step-is-one-tool-call.md) — because three bases announce a Step
before it runs and `opencode` announces one only when it is over, which changes when a
ceiling is reached and nothing but a measurement could have said so. `grok`'s own
`--max-turns` is a third use of the word: it really does count turns, and it lives in a
vendor flag, which is where the glossary puts names that are not ours.
_Avoid_: Round, Iteration, Tool use. Not Turn — a Turn is one Task in a Session, and a Step
is one call inside a Task.

**Container**:
A Session's file store, seen from the Files chapter. A Session and its Container are the
same thing named from two places, so one id derives from the other.
_Avoid_: Workspace, Bucket, Volume

**Principal**:
The identity every object belongs to. A server has exactly one, so a Session, a Task and an
Artifact are scoped to it by existing at all, and there is never a second one to be outside
the scope of. A credential authenticates and does not identify: every configured API key is
an equivalent way of presenting the same Principal, so two people holding two keys are one
client and share everything. Two Principals means two servers — see
[ADR-0006](docs/adr/0006-one-principal-per-server.md).
_Avoid_: Tenant (that is a deployment, not an identity), User, Account, Owner, Caller. "Key"
and "credential" name what is presented, never who is presenting it, and the two words are
not interchangeable with this one.

**Budget**:
A bound on one Task, enforced rather than recorded: the wall clock it may run for.
Resolved once per run as the shortest of the three bounds that are set — the request's,
the Harness's, and the deployment's own — so each of them is a ceiling and none is a
preference, and reported back on the Response so a client can see the number that was
applied rather than the one it asked for. A Task stopped by one is `incomplete` — never
`failed`, which means the work could not be done, and never `cancelled`, which means
someone asked for a stop. Where both happen, the asking wins: a cancel that lands while a
Budget is still tearing the run down settles the Task `cancelled` rather than `incomplete`,
because `incomplete` is the status a client retries and a deliberate stop is not something
to re-run. Neither of them touches a Task that finished first, which stays `completed`. UHP
names two, `timeout_seconds` and `max_step`, and both are enforced here. They differ in one
thing worth stating: every Task has a wall clock, because Security §5 makes bounding a Task's
duration this server's obligation and there is no spelling of "unbounded"; almost no Task has
a step ceiling, because nothing supplies a default and a surprise one would break every Task
that legitimately takes forty Steps. `max_output_tokens` is not a third, though it reads like
one: it
bounds a single model call, and a Task is many of them, so no value of it is a bound on a
Task — and the accounting that could make it one arrives only once a run is over, from three
of the five bases. Declined rather than approximated, because a bound honoured approximately
is a client believing in a ceiling it does not have — see
[ADR-0007](docs/adr/0007-a-declined-field-is-not-a-pending-one.md).
_Avoid_: Timeout, Limit, Deadline, Quota. "Timeout" names one budget and not the
concept, and it stays where the name is not ours — the wire field
`timeout_seconds`, the `metadata.timeout_seconds` this server reports it back on, and
the `UHP_TASK_TIMEOUT` an operator sets. Everywhere the name is ours it is Budget:
`service.DefaultTaskBudget`, `WithTaskBudget`, `resolveBudget`, `Run.budget` — and the step
Budget beside it, `WithTaskMaxStep`, `resolveStepBudget`, `Run.maxStep`, where the wire's
`max_step` is likewise kept only where the name is not ours.

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
