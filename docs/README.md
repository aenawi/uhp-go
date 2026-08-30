# Documentation

The [README](../README.md) covers what this is and how to start it. Everything else lives
here.

| | |
|---|---|
| [Talking to a UHP server](client.md) | The `uhpc` CLI, the Go client, and importing the protocol's types |
| [The HTTP surface](api.md) | Every endpoint, which request fields are read, reconnecting, idempotency |
| [Running it](operations.md) | Configuration, storage, concurrency, task and step budgets, the Docker image |
| [Authentication](authentication.md) | Bearer keys, the loopback default, and why several keys are still one tenant |
| [Harnesses](harnesses.md) | Configuring one over HTTP, what reaches the agent, adding a sixth |
| [Files](files.md) | Files as task input, artifacts as output, download safety |
| [Session sharing](session-sharing.md) | Read-only public links, and the consent switch in front of them |
| [Conformance](conformance.md) | The score, how to reproduce it, and what a green suite still can't see |
| [Architecture](architecture.md) | How the tree is laid out |
| [Testing](testing.md) | What's free on every push, and what costs tokens |

## Decisions

[`adr/`](adr/) holds the architecture decision records — the *why* behind choices that
would otherwise look arbitrary, and the reasoning to argue with if you want to change one.

## Upstream

[`upstream/`](upstream/) holds work aimed at the protocol repository rather than at this
one — proposed conformance checks and the probes that back them up. Nothing there is built
or tested by this repository's CI.

## For agents

[`agents/`](agents/) describes how work is tracked here: the issue tracker conventions, the
triage labels, and where the domain vocabulary lives.
