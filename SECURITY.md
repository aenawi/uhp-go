# Security policy

## Reporting a vulnerability

Report security issues privately through GitHub's
[private vulnerability reporting](https://github.com/aenawi/uhp-go/security/advisories/new)
on this repository. Please do not open a public issue for a security defect.

Include what you need to reproduce it: the request, the configuration, and the observed
behaviour. You will get an acknowledgement within seven days and a decision on a fix or a
rejection within thirty.

This is a volunteer-maintained project with no paid support and no SLA beyond that.

## Scope

`uhpd` executes local agent CLI binaries as subprocesses on behalf of authenticated HTTP
clients. That is its purpose, not a vulnerability. The security boundary is:

**In scope**

- Bypassing bearer-token authentication on an authenticated endpoint.
- Injecting arguments or options into a harness CLI invocation through request fields.
- Making `uhpd` itself read or write outside a task's working directory — path traversal
  through an artifact or file identifier, an attachment name, or any other request field.
  Read the matching entry under "Out of scope": this covers what the server does, not what
  the agent it started chooses to do.
- Causing `uhpd` to make outbound network connections of its own (it is designed to make
  none — see "Runs entirely offline" in the README).
- Leaking task, session, or artifact data to a caller presenting no credential, or serving
  it through a share id that does not cover it.
- Denial of service that is disproportionate to the request: unbounded memory from a
  single request, or unbounded process spawning.

**Out of scope**

- Anything an authenticated client can do that the agent CLI itself permits. A harness
  agent runs commands; a client authorised to start a task is authorised to do that.
- An agent writing outside the working directory it was given. `uhpd` grants every harness
  write access to that directory and confines only one of the five to it: `codex`, with
  `-c sandbox_mode=workspace-write`. `claude`, `opencode`, `grok` and `pi` take no argument
  that would make a wall, so the directory is where they start and not a boundary this
  server maintains. That is stated rather than implied because it cannot be discovered by
  asking the server, and the decision behind it — including why withholding write access
  was not an option — is
  [ADR-0008](docs/adr/0008-an-agent-may-write-in-the-directory-it-was-given.md). An escape
  `uhpd` causes is still in scope; an agent going where its own runtime allows is not.
- One holder of a configured key reading another's tasks, sessions, or artifacts. A `uhpd`
  process serves one principal, and every value in `UHP_API_KEYS` is an equivalent
  credential for it rather than a tenant of its own — so two people holding two keys are
  one client and share everything by design. Keeping two tenants apart means running one
  `uhpd` per tenant. See [ADR-0006](docs/adr/0006-one-principal-per-server.md).
- Vulnerabilities in the harness CLIs themselves (`claude`, `codex`, `grok`, `opencode`,
  `pi`). Report those to their vendors.
- Running `uhpd` with `UHP_API_KEYS` unset. That disables authentication by design and is
  documented as local-development-only — see [Authentication](README.md#authentication).
  Such a server binds `127.0.0.1` by default, refuses to start on any other address, and
  warns at startup, so exposing one to a network is a deployment error made against three
  refusals. A way to reach an unkeyed server from off the machine it runs on — a bind the
  loopback check accepts and the network does not agree is loopback — is in scope.
- Running the container as root, or otherwise not applying the deployment hardening the
  README describes.

## Supported versions

This project has not yet cut a release. Until it does, only `main` is supported.
