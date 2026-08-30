# Talking to a UHP server

How to drive this server — or any conformant one — from the command line, from Go, or by importing the protocol's types directly.

`uhp.Client` speaks the protocol to any conformant server, and `uhpc` is a command that
drives it:

```bash
go install github.com/aenawi/uhp-go/cmd/uhpc@latest

export UHP_BASE_URL=http://localhost:8080 UHP_API_KEY=devkey
uhpc discover                                   # what this server can do
uhpc harnesses                                  # what it can run, and which of them are ready
uhpc run --stream "summarise the README"        # a task, rendered as it arrives
uhpc watch chrn_…                               # every task on a harness, live
```

In Go:

```go
c := uhp.NewClient("http://localhost:8080", os.Getenv("UHP_API_KEY"))

resp, err := c.Create(ctx, uhp.CreateResponseRequest{
    Input:    "summarise the README",
    Metadata: map[string]any{"harness_id": "claude-code"},
}, idempotencyKey)

if e, ok := uhp.AsError(err); ok && e.Code == uhp.CodeSessionBusy {
    // …the session already has a task in flight
}
```

What it does that a hand-rolled `net/http` call will not, unless you read the whole
specification first:

- **The error envelope is decoded.** A failure is an `*uhp.Error` with a `Code` you can
  switch on, not a status and a blob. A body that is not the envelope — a proxy's 502 —
  still becomes one, built from the status, so a caller has no second case to handle.
- **`UHP-Version` is sent and the answer is checked.** Lifecycle §1 forbids a server
  serving a different version silently; a client that ignores the reply decodes one
  version's shapes out of another's bytes.
- **Retries follow Errors §4 by class rather than by code**, so an unrecognised code is
  safe. `quota_exhausted` is the exception: it arrives as a 429 and means the opposite of
  "come back shortly".
- **A task creation is retried only when it carries an `Idempotency-Key`.** Without one, a
  retry after a timeout runs expensive, side-effecting work a second time while the first
  may still be going, which is what the header exists to prevent.
- **Streams check sequence numbers as they read.** Streaming §2 makes a dropped event
  detectable on purpose; this reports it as a `GapError` rather than rendering the hole.

`uhpc` is also how this repository knows the protocol works over a socket rather than
against its own handlers — see [Testing](testing.md).

## Using the wire types

The protocol's objects are importable, so a client does not have to hand-roll them from the
specification:

```bash
go get github.com/aenawi/uhp-go/uhp
```

```go
import "github.com/aenawi/uhp-go/uhp"

var resp uhp.Response
if err := json.NewDecoder(body).Decode(&resp); err != nil { … }

// Streaming: framing only — no HTTP, no retries, no auth.
dec := uhp.NewEventDecoder(body)
for {
    ev, err := dec.Next()
    if errors.Is(err, io.EOF) { break }
    if err != nil { … }
    switch ev.Type {
    case uhp.EventOutputTextDelta:
        io.WriteString(os.Stdout, ev.Delta)
    }
    // Every other type is ignored, which is UHP's second client rule.
}
```

Four things worth knowing before you depend on it:

- **It models the protocol, not this server.** All 23 schema objects are there with every
  field, including the ones this server ignores — `docs/conformance.md` records which. A
  type narrowed to one implementation would misrepresent UHP as being that narrow.
- **`uhp` is the protocol; `uhp/uhpgo` is this server's additions.** Importing the second is
  a dependency on this implementation. Nothing in `uhp` imports anything outside the standard
  library, and module graph pruning means importing it does not pull the SQLite tree into
  your build.
- **A server's extensions need a type to land in.** Every object in the schema is
  `additionalProperties: true`, and a decoder drops what it cannot land — so
  `uhp.Client.GetHarness` returning a `uhp.Harness` silently loses this server's `status`,
  `models` and `capabilities`. `GetHarnessInto` and `ListHarnessesInto` take the shape from
  the caller instead, which is how `uhpc` shows whether a harness is reachable:

  ```go
  var h uhpgo.Harness // uhp.Harness plus this server's three additions
  err := c.GetHarnessInto(ctx, id, &h)
  ```

  Against a server that sends none of them the additions decode as zero values, so the call
  stays portable even though the extra fields are not.
- **Use keyed struct literals.** UHP permits adding response fields within a published
  version and these types will follow; `uhp.Response{a, b, c}` stops compiling when one
  arrives, and `uhp.Response{ID: …}` does not.

The types are frozen by the specification rather than by us: UHP versions are dates, and a
published version is immutable in structure. See
[ADR-0002](adr/0002-uhp-package-models-the-protocol.md).
