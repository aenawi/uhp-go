# ADR-0001: SQLite behind `service.Store`, on a pure-Go driver

Date: 2026-08-23
Status: Accepted
Issue: [#15](https://github.com/aenawi/uhp-go/issues/15)

## Context

Tasks, sessions and native session ids were held in memory only. A client that stored a
response id and came back after a restart got a `404` for work this server had actually
done, and a resumed session came back with no history behind it.

UHP is silent on storage: it mandates no engine, no durability guarantee, no retention or
TTL rules, and no session-survives-restart requirement. So this is a product decision rather
than a conformance one. The reference implementation makes the same one — HarnessRouter
Community Edition stores everything on a volume the operator owns.

`service.Store` was already a single interface declared at its consumer, with
`store.MemoryStore` satisfying it structurally. Only `cmd/uhpd/main.go` named a concrete
engine. (The issue calls it seven methods; it is nine — `ListSessions` and
`ListSessionTasks` included — and all nine are implemented and exercised against both
engines.)

## Decision

Add `store.SQLiteStore` as a second implementation of `service.Store`, selected by
`UHP_DB` (implied by `UHP_WORKSPACE`, the same rule `UHP_HARNESS_STORE` follows).
`MemoryStore` stays as the engine for a server with nowhere to write, and warns on startup.

The driver is **`modernc.org/sqlite`**, which is pure Go. The image builds with
`CGO_ENABLED=0`, so a cgo-linked SQLite (`mattn/go-sqlite3`) would not be in the binary this
repository ships. That constraint decided the driver; there was no second candidate.

The schema is two tables. Each carries the columns that are searched, filtered or ordered —
`id`, `session_id` / `harness_id`, `created_at` — plus one JSON document holding the rest.
`created_at` is Unix nanoseconds and is *derived*: the authoritative timestamp lives inside
the document, and the column exists only so an index can order on it.

The stored document has its own record types in `internal/store/sqlite_records.go`, not
`domain.Task` and friends. Those carry a `MarshalJSON` that renders the UHP wire object —
it drops internal bookkeeping, folds `session_id` into metadata, and truncates timestamps to
the second — and none of them has an `UnmarshalJSON` to match.

`PRAGMA user_version` is checked on open. A newer version is refused rather than written to.

## Consequences

**This is the repository's first substantial dependency.** `modernc.org/sqlite` pulls in
`modernc.org/libc` and about ten transitive modules. Until now the whole module required
only `github.com/google/uuid`, and "no database required to run or test" was a design note
in the README. That note is now narrower: no database has to be *installed*, because the
engine is compiled in, but the dependency tree is no longer trivial and `go.sum` is no
longer something a reviewer reads at a glance.

**`synchronous=NORMAL`, not `FULL`.** The service calls `UpdateTask` on every streamed
delta, so `FULL` would put an `fsync` between a harness and each fragment of its answer.
`NORMAL` with WAL survives a crash of this process; what it gives up is the last few commits
if the machine loses power.

**A whole task document is rewritten per delta.** That is the cost of storing a task as one
JSON blob rather than as rows, and it is the first thing to measure if long responses get
slow. It is not the first thing to fix speculatively — the write is a few kilobytes and
SQLite is not the bottleneck in a pipeline that is waiting on an agent.

**One connection.** SQLite serialises writers whatever the pool does, and every read here is
a single-row lookup or one page of a listing.

**An existing deployment changes engine on upgrade.** `UHP_DB` defaults to
`$UHP_WORKSPACE/uhp.db`, and the Docker image presets `UHP_WORKSPACE`, so anything already
running with a workspace becomes durable without being asked. That is the intent — the
alternative ships an image whose default is to lose every task — but it is a behaviour
change on upgrade rather than an opt-in, and it is announced only by the `database open`
line at startup. `UHP_DB=` cannot switch it back off; removing `UHP_WORKSPACE` would, at
the cost of the file capabilities.

**The shared contract suite is the real deliverable of the second engine.** Running
`store_contract_test.go` against both found two bugs in `MemoryStore` that one engine alone
could not expose. `copyTask` deep-copied `Metadata`, `Artifacts` and `Error` but not
`Output`, so the service's per-delta `Task.AppendText` was writing through to stored state.
Fixing that introduced a second: `append([]T(nil), s...)` returns nil for an empty input,
which turned the empty `Annotations` list every run mints into a `null` on the wire. An
interface with one implementation is a description of that implementation.

**`ctx` is honoured by one engine and ignored by the other,** and that is not in the shared
suite. `MemoryStore` takes `_ context.Context`; SQLite passes it to the driver, so a
cancelled request can fail a write that memory would have completed. It is close to moot in
practice — `supervise` runs the whole task on `context.WithoutCancel`, so only the calls on
the request's own path are affected, and abandoning a write for a client that has gone is
the better answer anyway.

**A store error was reported as a 404, and the fix changed the interface.** `TaskService.GetTask`
mapped every error from the store to `ErrResponseNotFound`, which was harmless when the only
possible error was "not in the map" and stopped being harmless the moment a real disk was
underneath: a failed read answered `404`, telling a client polling a running task that the
task no longer existed. Resolved in [#28](https://github.com/aenawi/uhp-go/issues/28) by
giving `Store.GetTask` and `Store.GetSession` a `found bool`, the way `HarnessStore.GetHarness`
always had, so an absent row and an unreadable store arrive as different values rather than
as one error the caller has to guess about. A sentinel error would have separated them too,
but only by convention; two return values cannot be merged by an engine that forgets to wrap
something. The contract suite asserts `found == false` **with no error**, which is the half
that keeps a third engine honest.

**Uploads, idempotency keys and created harnesses did not move.** `Uploads`, `HarnessStore`
and the key index are separate interfaces, each with a different lifetime, and each can move
on its own or not at all. Harnesses remain a JSON document; idempotency keys remain in
memory, which is now the weaker half of that feature rather than a matched pair.
