#!/usr/bin/env python3
"""Print the revision of the conformance suite that is actually installed.

The suite's own report names its *version* — `2026.8.11.post1` — and that string
did not change across either of the two times the denominator moved underneath
this repository (issues #102 and #107). A version is therefore not a staleness
signal. The revision is, and the checkout is right there: `pip install -e` puts
`uhp_conformance` inside the clone it was installed from, so the clone's `HEAD`
is the revision the suite about to run was built from.

Two uses, and they are the same question asked at two moments:

  * `suite-revision.py` prints the bare revision on stdout, for the gate to
    record in the report it keeps as evidence.
  * `suite-revision.py --expect <sha>` additionally refuses a checkout that is
    not the one docs/conformance.md claims to be measured at — run *before* the
    suite, because the suite spends about six real agent tasks finding out.

Commentary goes to stderr so the revision is the only thing on stdout and
`$(suite-revision.py)` captures it cleanly.

Exit codes: 0 resolved (and matching, when asked), 1 the checkout is not the
expected revision, 2 the revision could not be resolved at all — which is not
the same answer as "it differs", and must not be reported as one.
"""
from __future__ import annotations

import argparse
import importlib.util
import os
import subprocess
import sys
from pathlib import Path


def find_installed() -> Path | None:
    """Where `import uhp_conformance` would come from, if anywhere."""
    try:
        spec = importlib.util.find_spec("uhp_conformance")
    except (ImportError, ValueError):
        return None
    if spec is None or not spec.origin:
        return None
    return Path(spec.origin).resolve().parent


def git(args: list[str], cwd: Path) -> subprocess.CompletedProcess:
    return subprocess.run(["git", "-C", str(cwd)] + args,
                          capture_output=True, text=True)


def resolve(suite_dir: Path) -> tuple[str, bool]:
    """(revision, clean) for the work tree containing suite_dir.

    Raises LookupError when the directory is not in a git work tree — a wheel
    from PyPI unpacked into site-packages has no revision to read, and inventing
    one would be worse than saying so.
    """
    head = git(["rev-parse", "HEAD"], suite_dir)
    if head.returncode != 0:
        raise LookupError(
            f"{suite_dir} is not a git work tree, so the suite it holds has no "
            f"revision: {head.stderr.strip()}")

    # Landing in *a* work tree is not landing in the suite's: `git` searches
    # upward, so a virtualenv made inside a working copy of this repository,
    # with the suite installed into its site-packages, would otherwise report
    # uhp-go's own HEAD as the conformance suite's revision. That is a confident
    # wrong answer to the one question the report is being asked, and it would
    # be stamped into the evidence. A checkout only speaks for files it tracks.
    tracked = git(["ls-files", "--", "."], suite_dir)
    if tracked.returncode != 0 or not tracked.stdout.strip():
        root = git(["rev-parse", "--show-toplevel"], suite_dir).stdout.strip() or "?"
        raise LookupError(
            f"{suite_dir} sits inside the git work tree at {root}, which does not "
            f"track it — that HEAD is another repository's revision, not the "
            f"suite's. Install the suite from a pinned clone, or point "
            f"UHP_SUITE_DIR at one.")

    # Tracked files only. `pip install -e` writes an egg-info directory into the
    # checkout, and a check that called that a modification would refuse every
    # editable install — which is the only kind of install that has a revision
    # to read in the first place.
    status = git(["status", "--porcelain", "--untracked-files=no"], suite_dir)
    if status.returncode != 0:
        # Not knowing is not the same as clean, and only one of the two can be
        # written into a report as the revision that was measured.
        raise LookupError(
            f"cannot tell whether {suite_dir} is modified: "
            f"{status.stderr.strip() or status.stdout.strip()}")
    return head.stdout.strip(), not status.stdout.strip()


def main(argv: list[str], *, suite_dir: str | None = "", find_installed=find_installed) -> int:
    parser = argparse.ArgumentParser(
        prog="suite-revision.py", description=__doc__.splitlines()[0])
    parser.add_argument("--expect", metavar="SHA",
                        help="refuse a checkout that is not this revision")
    args = parser.parse_args(argv[1:])

    # The empty-string default means "nobody said"; the tests pass a directory,
    # and the Makefile passes nothing and lets UHP_SUITE_DIR or the import win.
    if suite_dir == "":
        suite_dir = os.environ.get("UHP_SUITE_DIR") or None
    where = Path(suite_dir).resolve() if suite_dir else find_installed()

    if where is None:
        print("suite revision: `uhp_conformance` is not importable and "
              "UHP_SUITE_DIR is unset, so there is no checkout to read a "
              "revision from. Install the suite from a pinned clone "
              "(`pip install -e <clone>/protocol/conformance`) or point "
              "UHP_SUITE_DIR at it — see docs/conformance.md.", file=sys.stderr)
        return 2

    try:
        revision, clean = resolve(where)
    except LookupError as e:
        print(f"suite revision: {e}", file=sys.stderr)
        return 2

    if args.expect and revision != args.expect:
        print(f"suite revision: the installed suite is at {revision[:12]}, and "
              f"this repository is measured at {args.expect[:12]} "
              f"({where}). Check the suite out at the recorded pin, or re-measure "
              f"and move the pin — see docs/conformance.md.", file=sys.stderr)
        return 1

    if args.expect and not clean:
        print(f"suite revision: {where} has modified tracked files, so it is not "
              f"{revision[:12]} however much its HEAD says so. Stash or reset the "
              f"checkout before measuring against it.", file=sys.stderr)
        return 1

    print(revision)
    if args.expect:
        print(f"suite revision: {revision[:12]} matches the recorded pin.",
              file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
