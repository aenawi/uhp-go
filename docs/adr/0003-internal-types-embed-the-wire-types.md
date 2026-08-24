# ADR-0003: Internal types embed the wire types, and `metadata` is assembled before marshalling

Date: 2026-08-24
Status: Accepted
Issue: [#16](https://github.com/aenawi/uhp-go/issues/16)

## Context

[ADR-0002](./0002-uhp-package-models-the-protocol.md) settles what `uhp/` contains. This
records how the public wire types and the internal ones relate, which is a separate decision
and the one with the sharper failure mode.

`domain.Task` is two things in one struct: thirteen fields that reach the wire and six
commented "internal bookkeeping, not part of the wire object". Issue #16 suggested keeping
`domain` as the internal shape and making `uhp` the wire shape, citing the custom
`MarshalJSON` on `Task` as evidence that the two are not the same thing.

That evidence is real, but read carefully it argues for separating *fields*, not for
maintaining two parallel type trees. Two full copies of `Response`, `Event`, `Harness`,
`Session` and their children is a drift machine: the day they disagree, the server is
conformant and the published types are a lie about it.

**The obvious alternative — embed `uhp.Response` in `domain.Task` — is broken as the field
comment draws the line.** `Task.MarshalJSON` reads `t.SessionID`, `t.HarnessID` and
`t.RequestedModel`, and all three sit in the block labelled "not part of the wire object".
If those stay on the outer struct while `MarshalJSON` moves to the embedded one, Go promotes
the method, the code compiles clean, and `metadata.session_id` silently disappears from
every response. `tasks.md` §3 makes that field a MUST. The compiler cannot catch it.

The comment is simply wrong about those three. They *are* on the wire — inside `metadata`.
But the fix is not to move them onto `uhp.Response` either, because the schema's `Response`
has twelve properties and none of them is `session_id`, `harness_id` or `requested_model`.
The schema has nowhere to put them. They are contents of the `metadata` object, not fields
beside it.

## Decision

`domain.Task` embeds `uhp.Response`. `domain.Artifact` embeds `uhp.File`. One shape per
concept, no converters, no parallel tree.

`uhp.Response.Metadata` is the wire truth and marshals verbatim. The projection into it —
`session_id`, `harness_id`, and the `requested_model` / `model_fallback` pair from
`tasks.md` §1.3 — happens when the values are known, not when the response is marshalled.
There are two such points: task creation (`internal/service/task_service.go:351`) and
mid-run model resolution (`:567`).

`domain.Task`'s genuinely internal fields are three, not six: `Input`, `Artifacts` and
`NativeSessionID`. `SessionID`, `HarnessID` and `RequestedModel` remain on the internal
struct as the inputs to that projection, but they are bookkeeping that *feeds* the wire
rather than bookkeeping that is absent from it.

## Consequences

**Two sync points must stay correct, and that is a real cost.** Computing metadata at
marshal time was not an accident: `task.Model` is assigned mid-run at `task_service.go:567`,
the fix from [#43](https://github.com/aenawi/uhp-go/issues/43), because three adapters
replace the harness default with the model their own output names. `requested_model` and
`model_fallback` therefore cannot be known at creation. Moving the projection earlier means
it has to run again whenever `Model` changes. Miss one and a response goes out without a
substitution it should have declared.

**The schema test from ADR-0002 is what makes that cost bearable.** A missed sync point
produces a response whose `metadata` is wrong, which is exactly what marshalling each public
type and validating it against the vendored schema is there to catch. The two decisions are
load-bearing for each other.

**The alternative would have made the public type a lie.** Keeping marshal-time computation
means overriding `MarshalJSON` on the outer `domain.Task`, so `uhp.Response` marshalled by a
client produces different JSON than this server emits. A published type that does not
round-trip to the wire format defeats the reason for publishing it.

**Embedding promotes more than fields.** `uhp.Response`'s methods become `domain.Task`'s
methods, which is convenient for `MarshalJSON` and a hazard everywhere else: a method added
to a public type later appears on the internal one without anybody deciding it should. The
same promotion is why the broken variant above compiles.

**The storage format becomes coupled to the public types, deliberately.** `taskRecord` in
`internal/store/sqlite_records.go` reuses `domain.OutputItem`, `domain.Usage` and
`domain.TaskError`, which all become `uhp` types, so adding a protocol field changes what is
written into SQLite. That is accepted rather than avoided. The alternative — self-contained
record types — means hand-maintaining a copy of `OutputItem` whose only job is to be
identical to the real one, which is the drift machine this ADR rejected, pointed at disk
instead of at the wire. UHP forbids renaming or retyping a field within a version, so what
reaches disk is additions, and an old row decodes with the new field zero. `PRAGMA
user_version` remains for a break that is genuinely structural.

The record types themselves stay storage-shaped and are not wire objects — that is
[ADR-0001](./0001-sqlite-for-tasks-and-sessions.md)'s decision and it is unchanged. Only the
leaf types they compose from move.

**`Task.Text()`, `AppendText` and `MessageItem` operate on promoted fields.** They read and
write `t.Output`, which now lives on the embedded struct. They keep working unchanged, but
they are internal helpers reaching into public state, and whether they belong on
`uhp.Response` instead is a question this decision leaves open rather than answers.
