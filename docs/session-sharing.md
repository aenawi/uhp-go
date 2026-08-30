# Session sharing

The one feature here that answers a request carrying no credential. It is off unless you turn it on, and this page is why.

Sessions §5 asks a `full` implementation for a read-only view of a conversation that
someone without a credential can open. This server implements it, and it is **off unless
you turn it on**:

```bash
UHP_SESSION_SHARING=1 UHP_PUBLIC_URL=https://uhp.example.com uhpd
```

The default is off because this is the only capability here that changes what the
deployment *is*. Everything else is behind a bearer token; switching this on makes the
server answer some requests that carry none. Every other capability is gated on having
somewhere to put something — a workspace for files, a store for harnesses — and this one is
gated on consent. With it off, discovery reports `session_sharing: false` and every share
endpoint answers `501 uhpgo_session_sharing_unsupported`.

```bash
uhpc share sess_abc          # mint (or re-read) the link
uhpc shared shr_9f2…         # read it the way its recipient does
uhpc unshare sess_abc        # revoke it
```

### Turning it off suspends the links; it does not revoke them

`UHP_SESSION_SHARING` is a switch on a capability, not a delete. Unset it and every
share endpoint answers `501`, discovery reports `session_sharing: false`, and the share
rows stay exactly where they are — so a restart with the variable set again makes every
link that was ever minted resolve again. Possibly in a different deployment, possibly for
someone who was never told it had stopped working.

**Revoking a link means revoking it**: `uhpc unshare <session_id>`, while sharing is on.
There is no way to withdraw a share with the capability turned off, because revocation is
behind the same flag as everything else.

That is a decision rather than an oversight. Off means the endpoints are not served, the
way turning off harness management does not delete the harnesses somebody created; the
alternative — revoking every share at startup whenever the variable is absent — destroys
state on a restart with a typo'd variable name, which is the same silent downgrade `uhpd`
refuses to make when a configured store will not open. What is left is the gap between
what an operator meant by turning it off and what they got, so a server that starts
without the variable and is still holding links says so:

```
{"level":"WARN","msg":"session sharing is off and this server still holds shares; they are suspended, not revoked, and every one of them resolves again if it is turned back on","shares":3,"hint":"to withdraw them, start with UHP_SESSION_SHARING=1 and revoke each one (uhpc unshare <session_id>)"}
```

It counts rather than lists: the id *is* the credential, so an operator needs to know
there are three, not what they are. Nothing is logged when there are none, which is
almost every deployment — a line printed on every start is a line nobody reads on the
start that mattered. `internal/service/shares_test.go` holds the whole cycle up: minted,
suspended by a restart without the flag, resolving again after a restart with it, and
gone for good once revoked.

### The share id is the credential

A share id is 256 bits of randomness behind a `shr_` prefix, and it is a bearer capability:
whoever holds it reads that conversation, its turns and its files, with nothing else.
Treat it the way you treat an API key. Because it necessarily travels in a URL, every
shared response carries `Cache-Control: no-store`, `X-Robots-Tag: noindex, nofollow` and
`Referrer-Policy: no-referrer` — the three channels a secret in a URL leaks through. They
are middleware rather than a line in each handler, so they are on the 404 for a revoked
link as well as on the 200, which is the case that matters more: an error response is
reached by the same address.

Sharing is idempotent per session: a second `POST` returns the share that already exists
rather than a second live id, because a client is told about one id and revokes one id.
Rotating a link means revoking and sharing again, in that order.

### Read-only is a property of the routing table

"Shared views must be read-only" is enforced by there being nothing to refuse. A share id
is a path segment and never a credential, so:

- presenting it as a bearer token is presenting an unknown token — `401`, on every endpoint;
- `POST`, `PUT`, `PATCH` and `DELETE` under `/v1/shares/` are methods no route claims — `405`,
  from the router itself;
- the shared artifact path takes no container id, so the container is derived from the share
  and another session's file id resolves to nothing rather than to a check someone has to
  remember.

A future endpoint cannot forget a check that does not exist. The tests that hold this up are
the negative ones in `internal/transport/http/share_handlers_test.go` — a share that cannot
start a task, cannot cancel, cannot upload, cannot delete the trace, and stops working the
moment it is revoked.

### What a viewer never sees

The shared view carries the harness that ran the session, projected down to what answers a
viewer's only question — *what ran this, on what, and could it do the things the transcript
shows*. It is built by copying the fields that are kept rather than by blanking the ones
that are not, because a deny-list is correct only until the next field is added to the
harness object, and the cost of forgetting one here is a configuration secret served to
whoever holds a URL.

Kept: id, name, base, base label, default model, models, capabilities, status, created-at.
Dropped: the MCP server list, the system prompt, skill bundles, disabled tools, and the
step and timeout budgets. The MCP list is the sharpest of those — Harnesses §4.1 forbids
returning a resolved credential to a client, and this is the one path where "a client"
means someone who presented nothing. Stripping `auth` alone would not have been enough:
`headers` is a free-form map, and it is the map the server materialises the resolved `auth`
into as an `Authorization` header, so the whole list goes.

The projection lands in `uhpgo.SharedHarness`, a type of its own rather than a harness with
fields removed, and that is not tidiness. Several of a harness object's fields *say
something* when they are empty: `mcpServers: []` means "this harness has none" and a null
`maxStep` means "unbounded". A stripped harness would therefore tell a viewer two untrue
things about a system they cannot see. A separate type says only what it says.

Revocation is absolute: the id stops resolving. Not hidden, not expired, not marked —
and it is the only thing that is, since turning the capability off suspends links rather
than withdrawing them, as above. And deleting the trace takes the share with it —
Sessions §6 makes a deleted session's files unreachable, and a surviving share id would
be the anonymous route back to them. Both engines are held to that in
`internal/store/share_contract_test.go`.

There is no expiry, deliberately. §5 requires revocation and says nothing about expiry, and
a stored expiry that nothing enforces is a worse promise than none.
