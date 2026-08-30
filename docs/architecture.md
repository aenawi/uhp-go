# Architecture

How the tree is laid out, and the handful of decisions that shape it.

Layered, dependency-inverted design (Clean/Hexagonal architecture):

```
uhp/                       the protocol: all 23 objects of UHP 2026-08-11, an SSE event decoder,
                           a client (uhp.Client) and a vendored copy of the schema. Imports only
                           the standard library; this repository consumes it like any client
uhp/uhpgo/                 what this server adds to UHP, kept out of uhp so the boundary
                           between protocol and implementation is compiler-visible
cmd/uhpd/                  composition root (main.go) — the only file wiring concrete types together
cmd/uhpc/                  a client for any UHP server, built on uhp.Client. Imports uhp, the
                           standard library, and uhpgo in the two harness reads alone, so that
                           `status` and `models` survive the decode — an addition it renders and
                           never requires, since a client that special-cased the server next door
                           would stop being evidence about the protocol
internal/domain/           entities: Task, Session, Artifact — no external deps. Each embeds
                           the uhp type it is reported as, so there is one shape per concept
internal/harness/          the adapter contract, the shared subprocess runner, the
                           registry, and one ~30-line declaration per harness
internal/service/          application core: TaskService; declares the Registry and Store
                           interfaces it consumes (deps.go), holds all business rules
internal/store/            service.Store and service.HarnessStore implementations — tasks and
                           sessions in SQLite or in memory, created harnesses in one JSON file
internal/transport/http/   UHP wire format: discovery, tasks, streaming (SSE), cancellation,
                           input items, artifact listing and download
internal/config/           environment-variable configuration loader
```

### Design notes

- A harness is declared as data, not written as code: `internal/harness/<name>.go` is a
  `CLIHarness` literal naming the binary, models, capabilities, argv, and line parser.
- Everything that must never be forgotten when adding a harness — process-group
  isolation, prompt delivery that cannot be re-parsed as options, model validation,
  scanner limits — lives once in the shared runner.
- Two stores behind one interface: SQLite when a path is configured, in memory when not.
  The SQLite driver is pure Go, so the binary still needs nothing installed to run or test.
- Plain `net/http` with Go 1.22 method/path routing. No framework.
- No wire shape exists anywhere outside `uhp/`. `domain.Task` embeds `uhp.Response`,
  `domain.Artifact` embeds `uhp.File`, `domain.Session` embeds `uhp.Session`, and the
  router's error envelope, discovery document and model catalogue are the published types
  rather than private copies of them. A router whose purpose is to stop its clients
  duplicating integrations is a poor place to duplicate the protocol internally.
