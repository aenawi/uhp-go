# Upstream work

Patches and probes aimed at [HarnessRouter/harnessrouter][hr], the repository that holds the
specification, the schema and the conformance suite. They live here rather than in a scratch
directory because the first version of the patch below was written, measured, and then lost
before anyone said the word — see [#66][i66].

Nothing here is built, tested or vetted by this repository's CI. It is not Go, and it is not
part of the server.

## `session-sharing-checks.patch`

Seven Full-class conformance checks (`R-01`…`R-07`) for Sessions §5, session sharing —
the one Full chapter the published suite does not test at all. Proposed upstream as
[harnessrouter#44][i44]; **not yet accepted**, so this is a proposal, not a dependency.

Apply against a checkout of the suite:

```sh
git -C ~/src/harnessrouter apply /path/to/uhp-go/docs/upstream/session-sharing-checks.patch
```

`295 +` to `protocol/conformance/uhp_conformance/checks.py`, no other file touched. If it is
accepted, `full` moves from 52 checks to 59, and `CONFORMANCE_FLOOR` and the recorded scores in
[../conformance.md](../conformance.md) move with it.

Why §5 needs checks that are mostly about refusals, and the four specification gaps that stop
some of them asserting outright, are argued in [#44][i44] and not repeated here.

## `share-defect-stub.py` and `share-defect-matrix.py`

The half that makes the patch worth reading. A check that only ever passes proves nothing —
D-05 passed against this server for months while being vacuous — so the stub is a deliberately
wrong UHP server with one toggleable defect per rule in §5, and the matrix runs the series
against each defect in turn.

```sh
HARNESSROUTER=~/src/harnessrouter python3 docs/upstream/share-defect-matrix.py
```

It needs the patch applied to that checkout. It needs no network, no credentials and no agent
tokens: the stub answers every task itself, so the whole matrix runs in a couple of seconds and
costs nothing. It exits non-zero if the clean server fails any check, or if any defect is not
caught by the check that claims to test it.

Measured at harnessrouter `6fdb96e` and uhp-go `37d8598`: a clean diagonal, and 7/7 against
this server (`claude-code` / `claude-sonnet-5`, with `UHP_API_KEYS` set — an unauthenticated
server cannot exercise R-03 at all).

[hr]: https://github.com/HarnessRouter/harnessrouter
[i44]: https://github.com/HarnessRouter/harnessrouter/issues/44
[i66]: https://github.com/aenawi/uhp-go/issues/66
