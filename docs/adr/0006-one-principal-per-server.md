# ADR-0006: A server has one principal, and `UHP_API_KEYS` is a list of credentials rather than a list of tenants

Date: 2026-08-28
Status: Accepted
Issue: [#56](https://github.com/aenawi/uhp-go/issues/56)

## Context

`validKey` compares the presented token against every configured key and answers yes or no.
Nothing downstream ever learns *which* key matched, so no store query filters by owner, and
`insufficient_scope` — a code `uhp/error.go` publishes because that package models the
protocol — can never be returned by this server.

Three requirements of the specification are addressed by that shape rather than enforced by
it. Architecture: "A server MUST scope every object to the principal that created it", and
"Servers MUST return `404`, not `403`, for objects outside the caller's scope". Security §2:
"Scope MUST be enforced on every operation, not only on read." Files §5: "File access MUST be
scoped to owning principal."

With one principal all three are vacuously satisfied. Every object *is* scoped to the
principal that created it, because there is only one; nothing is ever outside the caller's
scope, so there is no 403 to confuse with a 404; and file access is scoped to the owning
principal for the same reason. What that is not is *enforcement*, and the difference appears
the moment a second key is handed to a second person: they share every session, every
transcript and every artifact, and nothing on the protocol surface says so.

The variable is spelled `UHP_API_KEYS`. A reader who hands out three keys and expects three
tenants has read the name correctly and the behaviour wrongly, and until this decision the
only thing standing between them and that mistake was two paragraphs of README.

## Decision

**A `uhpd` process serves exactly one principal. Every value in `UHP_API_KEYS` is an
equivalent credential for that principal.** A credential authenticates; it does not identify.
A deployment that must keep two tenants apart runs one `uhpd` per tenant, each with its own
keys, its own SQLite file and its own working directories — which is the boundary the
operating system already enforces, rather than one this server would have to.

**This is the conformant reading, not an exemption from the requirement.** The three MUSTs
above are satisfied by a single-principal server the way a rule about "every element" is
satisfied by an empty set. The claim being made is exactly that and no more, and the places
where a reader could take it for something stronger now say so:

- `SECURITY.md` no longer offers a boundary between two authenticated clients. It could not
  honour one, and a researcher reading the old wording would have reported key-sharing as a
  vulnerability and been told it was intended.
- `docs/conformance.md` records that no check in the suite can tell a single-principal server
  from a multi-tenant one, because the suite runs with one key.
- `config.CheckAuthPosture` logs one `INFO` line when more than one key is configured, naming
  both the word this project uses (principal) and the word the operator was probably thinking
  of (tenant).

**`insufficient_scope` stays in `uhp/error.go` and stays unreachable here.** ADR-0002 makes
that package the protocol's vocabulary rather than this server's, so a code no server of ours
emits is still correctly published there. Which codes *this* server can produce is the table
in `docs/conformance.md`, and that is where the unreachability is recorded.

## Considered options

**Give the credential a principal.** `UHP_API_KEYS` becomes `name:key` pairs; the matched
principal rides the request context; `tasks`, `sessions` and `shares` grow an owner column at
schema version 3; and every read, cancel, delete, continue and download filters on it. The
404-not-403 rule then has something to do, and `insufficient_scope` becomes reachable.

This is what "a complete correct port" means read literally, and it was rejected on what it
costs against who is asking for it. It is a schema migration plus an ownership predicate in
every one of nineteen store methods and nine authenticated routes — an axis that every future
query has to remember, forever, on behalf of a deployment shape nobody has yet reported
running. Meanwhile the multi-tenant deployment it enables is *already available* by running
two processes, with a stronger boundary than a `WHERE owner = ?` provides: separate databases
and separate working directories cannot leak into each other through a forgotten filter.

The rejection is recorded here rather than left as an open issue, because an issue that will
not be worked is a false statement about the backlog. If a deployment ever needs one process
to serve two tenants, this ADR is the thing to supersede.

**Rename `UHP_API_KEYS` to something singular.** It would remove the misreading at its
source, and it would break every existing deployment and every `docker run` line in the
README to fix a misunderstanding a log line fixes for free. The plural is also not wrong: a
list of credentials for one principal is genuinely plural, which is what key rotation needs.

**Accept `name:key` pairs and use the names only for logging.** Half of option (b) with none
of its enforcement, and worse than either: it would put tenant-shaped names in the
configuration of a server that does not separate tenants, which is the exact false impression
this decision exists to remove.

## Consequences

**Two people holding two keys are one client.** They share every session, transcript and
artifact, and no request either of them can make will reveal that. This is the property the
documentation now states plainly in three places, because it cannot be discovered by asking
the server.

**A green conformance run says nothing about tenancy.** The suite runs with one key, so no
check distinguishes this server from one that enforces scope. That is now recorded in
`docs/conformance.md` alongside the other things a green run does not tell you.

**`insufficient_scope` is unreachable, and one of seven codes that are.** A client author
writing a `switch` over `uhp.Code*` has a table saying which arms can fire against this
server.

**The `403` half of the 404-not-403 rule is untested and untestable here.** There is no
out-of-scope object to answer for, so no test can assert the server prefers `404`. A future
ADR superseding this one inherits that as work rather than as a passing check.
