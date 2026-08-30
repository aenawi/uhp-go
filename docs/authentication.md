# Authentication and tenancy

Who this server lets in, why it refuses to start in one particular configuration, and why several API keys are still only one tenant.

Every endpoint except `GET /v1/uhp` requires `Authorization: Bearer <key>`, where the key
is one of the comma-separated values in `UHP_API_KEYS`. Discovery is exempt by design: a
client has to be able to tell this is a UHP server before deciding what credential to
present (Lifecycle §2), and the document carries nothing principal-specific. An absent,
malformed or unknown token is `401` with an `authentication_error` envelope; the scheme is
matched case-insensitively, as RFC 7235 requires.

**With `UHP_API_KEYS` unset, that is all skipped, and such a server is not conformant.**
Security §1 has no local-development exemption. This server keeps the unauthenticated mode
anyway — it is genuinely useful, and "runs with no configuration" is a property worth
having — but it is not allowed to be a mode you end up in without noticing:

- **The default bind is loopback.** `UHP_ADDR` defaults to `127.0.0.1:8080`. An
  unauthenticated server only this machine can reach is a local tool; the same server on
  `0.0.0.0` is an open relay that will run agent tasks, spawn CLI subprocesses and serve
  artifacts for anyone who can reach the port.
- **Widening the bind without keys is fatal at boot.** If `UHP_API_KEYS` is empty and
  `UHP_ADDR` is not a loopback address, `uhpd` refuses to start and names the variable.
  This is a deployment mistake, and every per-request answer is too late to catch one —
  by the time a request arrives to be refused, the server has already been open for as
  long as it has been up. A literal IP is decided without asking anything; a hostname is
  resolved, and every address it resolves to must be loopback. That includes `localhost`,
  which is conventionally loopback and is ultimately whatever the resolver says: an
  unkeyed server on a `localhost` somebody has pointed elsewhere is exactly the open
  server this refuses. Resolving costs nothing the server was not already going to
  spend — `net.Listen` resolves the same name moments later — so "runs entirely offline"
  is untouched.
- **The narrowed default is a breaking change, and it says so.** A keyed deployment that
  relied on `UHP_ADDR` defaulting to `:8080` is correct, conformant, and — after an
  upgrade — bound where nothing can reach it. That failure is otherwise silent, since the
  posture check passes on the keys without looking at the address, so a keyed server on
  loopback logs one `INFO` line naming `UHP_ADDR`. **If you were relying on the old
  default, set `UHP_ADDR=0.0.0.0:8080` explicitly.**
- **Running unauthenticated is logged.** One `WARN` line at startup, so an operator who
  arrived here by accident finds it in their own logs:

```
{"level":"WARN","msg":"running unauthenticated; every endpoint except GET /v1/uhp answers without a credential, which UHP Security §1 forbids","addr":"127.0.0.1:8080","hint":"set UHP_API_KEYS"}
```

Nothing in the capability vocabulary covers "this server is open", and inventing a key for
it would be a private dialect — the same reasoning [docs/conformance.md](conformance.md)
applies to resumption. So a client cannot tell an open server from a closed one by asking,
and this section is the obligation instead of a wire field. The recorded conformance score
was measured with `UHP_API_KEYS=devkey`; see
[Conformance](conformance.md) for what that means for the number.

### `UHP_API_KEYS` is a list of credentials, not a list of tenants

**Every configured key is equivalent, and a `uhpd` process serves exactly one principal.** A
credential authenticates; it does not identify. Nothing downstream learns which key matched,
so two people holding two keys are one client: they share every session, every transcript
and every artifact, and no request either of them can make will reveal that.

That is the conformant reading rather than an exemption. "Scope every object to the principal
that created it" (Architecture), "scope file access to the owning principal" (Files §5) and
"return `404`, not `403`, for objects outside the caller's scope" are all satisfied by a
server with one principal the way a rule about every element is satisfied by an empty set —
there is no second principal for anything to be outside the scope of. What it is not is
enforcement, which is why `insufficient_scope` is a code this server can never return (see
the unreachable-codes table in [docs/conformance.md](conformance.md)) and why no
conformance run says anything about tenancy either way.

**Keeping two tenants apart means running one `uhpd` per tenant** — separate keys, separate
`UHP_DB`, separate `UHP_WORKSPACE`. That boundary is the operating system's and is stronger
than a filter this server would have to remember in every query. The alternative — a
principal on each credential and an owner column on every object — was considered and
rejected in [ADR-0006](adr/0006-one-principal-per-server.md), which is the thing to
supersede if one process ever has to serve two tenants.

Because the variable is plural and the obvious reading of a plural is the wrong one here,
configuring more than one key logs a line saying so:

```
{"level":"INFO","msg":"several API keys are configured; they are equivalent credentials for one principal, not one tenant each","keys":3,"hint":"run one uhpd per tenant if they must not share sessions, transcripts or artifacts"}
```

[SECURITY.md](../SECURITY.md) puts this out of scope explicitly: one key holder reading another's
data is the design, not a vulnerability.
