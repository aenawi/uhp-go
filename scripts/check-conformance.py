#!/usr/bin/env python3
"""Refuse a conformance report that is worse than the recorded result.

`uhp-conformance` already exits non-zero when a check fails or errors, so this
exists for the two ways a run can be worse than the recorded one and still exit
zero:

  * **Skips.** A skip is never a pass — the suite says so, and so does
    docs/conformance.md. A run where every task check skipped because no
    harness was reachable reports "0 failed" and exits 0, which is exactly the
    green summary a gate is supposed to stop.
  * **Fewer checks.** "0 failed" over 12 checks is not the result that
    "0 failed" over 37 checks was. Selecting a narrower class, or pointing the
    suite somewhere that answers less, shrinks the denominator silently.

Usage: check-conformance.py <report.json> <floor>
"""
from __future__ import annotations

import json
import sys


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print(__doc__.strip().splitlines()[-1], file=sys.stderr)
        return 2

    path, floor = argv[1], int(argv[2])
    try:
        with open(path) as f:
            report = json.load(f)
    except (OSError, ValueError) as e:
        # No report at all is a failure, not an absence. The suite writes one
        # even for a run that failed every check, so a missing file means the
        # run did not happen — and a gate that treats "did not run" as "passed"
        # defends nothing.
        print(f"conformance gate: cannot read {path}: {e}", file=sys.stderr)
        return 1

    summary = report.get("summary") or {}
    passed = summary.get("pass", 0)
    skipped = report.get("skipped_not_verified") or []

    problems = []
    if summary.get("fail") or summary.get("error"):
        problems.append(f"{summary.get('fail', 0)} failed, {summary.get('error', 0)} errored")
    if skipped:
        problems.append("not verified (a skip is never a pass): " + ", ".join(skipped))
    if passed < floor:
        problems.append(f"{passed} checks passed, the recorded result passed {floor}")

    where = f"{report.get('target', '?')} · class {report.get('requested_class', '?')}"
    suite = report.get("suite_version", "?")

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
