#!/usr/bin/env python3
"""Say whether the conformance suite has moved on without this repository.

Twice the suite's denominator grew upstream and the numerator here was copied
forward: #102 found it eight days late, #107 twelve hours late, and both were
found by a person reading the upstream repository rather than by anything in
this tree. `check-conformance.py` cannot catch it, because a run at an old pin
produces a perfectly green report — the floor and the report agree with each
other and both disagree with upstream.

This asks GitHub one question instead: how far is `HarnessRouter/harnessrouter`
ahead of the revision docs/conformance.md says the score was measured at. It
starts no server, runs no agent task and spends no token; it is one unauthenticated
API call.

**Drift is advisory.** A revision moving upstream is not a defect in this
repository — it is a reason to re-run the gate and re-record the result, and a
maintainer's own `make conformance-drift` stays green while they decide when to
do that. `--fail-on-drift` is for the scheduled job, whose whole purpose is to
go red so that somebody hears about it without opening GitHub.

**A pin the documentation disagrees with is not advisory**, and needs no network
to detect: it means the Makefile and docs/conformance.md name different suites,
which is the copied-numerator failure one level down. That exits non-zero always.

The same goes for a pin upstream reports as `behind` or `diverged`. That is zero
commits of drift and is not a pin at all: it names a revision the branch does not
contain, so nobody can reproduce the score by checking that branch out.

Exit codes: 0 answered, 1 drifted (only with --fail-on-drift) or the pin is
broken — the documentation disagrees with it, or the branch does not contain it,
2 could not answer, GitHub being unreachable or not recognising the revision.
"Could not answer" is not "no drift" and is never reported as one, which is why
2 wins over 1 when both are true; anything already found is printed first
regardless.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

UPSTREAM = "HarnessRouter/harnessrouter"


class UpstreamError(Exception):
    """GitHub answered, and the answer was not a comparison."""


def fetch_json(url: str) -> dict:
    request = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github+json",
        "User-Agent": "uhp-go-conformance-drift",
    })
    # The upstream repository is public, so a token is optional. CI has one and
    # passing it buys the higher rate limit, nothing else.
    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if token:
        request.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as e:
        # A 404 here is usually a pin upstream has never heard of — a rewritten
        # history, or a typo — and that is not a measurement of drift.
        raise UpstreamError(f"{e.code} {e.reason}") from e


def subject(message: str) -> str:
    return (message or "").split("\n", 1)[0].strip()


def main(argv: list[str], *, fetch=fetch_json) -> int:
    parser = argparse.ArgumentParser(
        prog="check-suite-drift.py", description=__doc__.splitlines()[0])
    parser.add_argument("--pin", required=True, metavar="SHA",
                        help="the revision this repository is measured at")
    parser.add_argument("--repo", default=UPSTREAM, help=f"default {UPSTREAM}")
    parser.add_argument("--branch", default="main")
    parser.add_argument("--doc", default="docs/conformance.md",
                        help="the document that must name the same pin")
    parser.add_argument("--fail-on-drift", action="store_true",
                        help="exit non-zero when upstream is ahead of the pin")
    # A pin left alone for a few months is hundreds of commits behind, and the
    # count is the actionable part — the subjects are there to show whether the
    # move touched the suite at all. GitHub's compare truncates at 250 in any
    # case, so the list was never the whole story and should not pretend to be.
    parser.add_argument("--max-commits", type=int, default=20, metavar="N")
    args = parser.parse_args(argv[1:])

    problems = []

    # Checked first, because it is the one thing here that is knowable without
    # a network and is a defect rather than a notification.
    try:
        with open(args.doc) as f:
            documented = args.pin in f.read()
    except OSError as e:
        print(f"suite drift: cannot read {args.doc}: {e}", file=sys.stderr)
        return 2
    if not documented:
        problems.append(
            f"{args.doc} does not name {args.pin[:12]}, which is the pin this "
            f"gate defends. One of the two is stale, and a score is only "
            f"reproducible while they agree.")

    def report(problems: list[str]) -> None:
        for problem in problems:
            print(f"suite drift FAILED — {problem}")

    url = (f"https://api.github.com/repos/{args.repo}/compare/"
           f"{args.pin}...{args.branch}")
    try:
        comparison = fetch(url)
    except (UpstreamError, OSError) as e:
        # Reported before returning, because the contradiction above needed no
        # network to find and an unreachable GitHub must not be what hides it.
        report(problems)
        if isinstance(e, UpstreamError):
            print(f"suite drift: {args.repo} did not compare {args.pin[:12]} "
                  f"with {args.branch}: {e}", file=sys.stderr)
        else:
            print(f"suite drift: cannot reach {args.repo}: {e}", file=sys.stderr)
        # 2 rather than 1 even when a problem was printed: whatever else is
        # wrong, the drift question went unanswered, and that is the more
        # misleading thing for a caller to mistake for a clean result.
        return 2

    ahead = comparison.get("ahead_by", 0)
    status = comparison.get("status", "")
    where = f"{args.repo}@{args.branch}"

    if status in ("behind", "diverged"):
        # `ahead_by` is 0 here, which without this would print as "current".
        # The pin carries commits the branch does not: a rewritten history, or a
        # pin taken from a branch that never merged. Either way it is not a
        # revision somebody else can check out from that branch, so it is not a
        # pin — and it is not drift either, which is why it is a problem rather
        # than a report.
        problems.append(
            f"upstream reports {args.pin[:12]} as '{status}' relative to {where}: "
            f"it carries commits that branch does not, so nobody can reproduce "
            f"this score by checking out {where}. Pin a revision on the branch.")
    elif ahead:
        print(f"suite drift: the pin {args.pin[:12]} is behind {where} by "
              f"{ahead} commit{'s' if ahead != 1 else ''}:")
        # Oldest first, as GitHub returns them, so the list reads forward from
        # the pin. When it is long the elision is at the top: the commits
        # nearest upstream's head are the ones worth reading.
        commits = comparison.get("commits", [])
        # Sliced from the front rather than with a negative index, because
        # `commits[-0:]` is the whole list and would announce an elision it did
        # not perform.
        shown = commits[max(0, len(commits) - max(0, args.max_commits)):]
        elided = len(commits) - len(shown)
        if elided > 0:
            print(f"  … {elided} earlier commit{'s' if elided != 1 else ''}")
        for commit in shown:
            print(f"  {commit.get('sha', '?')[:7]}  "
                  f"{subject(commit.get('commit', {}).get('message', ''))}")
        print("\nRe-run `make conformance-gate` against a checkout at "
              f"{where}, then record the result and move the pin. A revision "
              "moving is not a failure; measuring against one that moved and "
              "not noticing is.")
    else:
        print(f"suite drift: the pin {args.pin[:12]} is current — {where} is "
              f"the same revision.")

    report(problems)

    if problems:
        return 1
    return 1 if (ahead and args.fail_on_drift) else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
