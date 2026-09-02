#!/usr/bin/env python3
"""Refuse a conformance report that is worse than the recorded result.

`uhp-conformance` already exits non-zero when a check fails or errors, so this
exists for the three ways a run can be worse than the recorded one and still
exit zero:

  * **Skips.** A skip is never a pass — the suite says so, and so does
    docs/conformance.md. A run where every task check skipped because no
    harness was reachable reports "0 failed" and exits 0, which is exactly the
    green summary a gate is supposed to stop.
  * **Fewer checks.** "0 failed" over 12 checks is not the result that
    "0 failed" over 37 checks was. Selecting a narrower class, or pointing the
    suite somewhere that answers less, shrinks the denominator silently.
  * **Another suite.** A run at a revision this repository is not measured at
    is green in exactly the same way, and is the one this file could not see
    until #109: the report names the suite's *version*, and that string did not
    change across either of the two times the denominator moved (#102, #107).
    `--expect-revision` is the pin from the Makefile and `--suite-revision` is
    what the checkout actually was; when they differ the run is refused, and
    the revision is written into the report either way so the evidence says
    which suite produced it.

Usage: check-conformance.py <report.json> <floor>
                            [--suite-revision SHA] [--expect-revision SHA]
"""
from __future__ import annotations

import argparse
import json
import sys


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        prog="check-conformance.py", description=__doc__.splitlines()[0])
    parser.add_argument("report")
    parser.add_argument("floor", type=int)
    parser.add_argument("--suite-revision", default="", metavar="SHA",
                        help="the revision of the checkout that ran, from "
                             "suite-revision.py; recorded in the report")
    parser.add_argument("--expect-revision", default="", metavar="SHA",
                        help="the revision this repository claims to be "
                             "measured at, from CONFORMANCE_SUITE_REVISION")
    args = parser.parse_args(argv[1:])

    try:
        with open(args.report) as f:
            report = json.load(f)
    except (OSError, ValueError) as e:
        # No report at all is a failure, not an absence. The suite writes one
        # even for a run that failed every check, so a missing file means the
        # run did not happen — and a gate that treats "did not run" as "passed"
        # defends nothing.
        print(f"conformance gate: cannot read {args.report}: {e}", file=sys.stderr)
        return 1

    # Recorded before the verdict, and rewritten even for a run this refuses:
    # the report is evidence first, and a refused run still has to say which
    # suite refused it. The suite does not write this field — it names its
    # version and the moment of the run, neither of which places a revision —
    # so a report carrying one was stamped here.
    if args.suite_revision:
        report["suite_revision"] = args.suite_revision
        try:
            with open(args.report, "w") as f:
                json.dump(report, f, indent=2)
        except OSError as e:
            print(f"conformance gate: cannot record the suite revision in "
                  f"{args.report}: {e}", file=sys.stderr)
            return 1

    summary = report.get("summary") or {}
    passed = summary.get("pass", 0)
    skipped = report.get("skipped_not_verified") or []
    measured = args.suite_revision or report.get("suite_revision") or ""

    problems = []
    if summary.get("fail") or summary.get("error"):
        problems.append(f"{summary.get('fail', 0)} failed, {summary.get('error', 0)} errored")
    if skipped:
        problems.append("not verified (a skip is never a pass): " + ", ".join(skipped))
    if passed < args.floor:
        problems.append(f"{passed} checks passed, the recorded result passed {args.floor}")
    if args.expect_revision and not measured:
        problems.append(
            "the suite revision could not be resolved, so this run cannot be "
            f"shown to have measured {args.expect_revision[:12]} — see "
            "scripts/suite-revision.py")
    elif args.expect_revision and measured != args.expect_revision:
        problems.append(
            f"measured against suite {measured[:12]}, and this repository is "
            f"measured at {args.expect_revision[:12]}")

    where = f"{report.get('target', '?')} · class {report.get('requested_class', '?')}"
    suite = report.get("suite_version", "?")
    if measured:
        suite += f" @ {measured[:12]}"

    if problems:
        print(f"conformance gate FAILED — {where} · suite {suite}")
        for p in problems:
            print(f"  - {p}")
        return 1

    print(f"conformance gate ok — {passed}/{summary.get('total', passed)} passed, "
          f"0 skipped · {where} · suite {suite}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
