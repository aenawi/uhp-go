#!/usr/bin/env python3
"""Tests for the three scripts that defend the conformance score.

Run with `make test-scripts`, or `python3 -m unittest discover -s scripts`.

Stdlib `unittest` and nothing else, for the same reason `make hooks` is one
line of `git config`: this is a Go repository with no Python package manager to
borrow, and a test suite that needs `pip install` before it runs is one that
does not run. Nothing here touches the network — the drift check takes its
GitHub call as an argument so a test can answer it.

The scripts are named with hyphens, like every other script in this directory,
so they are loaded by path rather than imported by name.
"""
from __future__ import annotations

import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
REPO = SCRIPTS.parent


def load(name: str):
    """Import scripts/<name>.py, whose hyphen keeps it out of `import`."""
    spec = importlib.util.spec_from_file_location(
        name.replace("-", "_"), SCRIPTS / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


check_conformance = load("check-conformance")
suite_revision = load("suite-revision")
check_suite_drift = load("check-suite-drift")


def run(module, argv: list[str], **kwargs) -> tuple[int, str, str]:
    """Call a script's main() and capture what a shell would have seen."""
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = module.main(["script"] + argv, **kwargs)
    return code, out.getvalue(), err.getvalue()


def report(**overrides) -> dict:
    """A report shaped like the suite's own, green at 63 checks."""
    base = {
        "protocol": "uhp",
        "protocol_version": "2026-08-11",
        "suite_version": "2026.8.11.post1",
        "generated_at": "2026-09-01T09:00:00Z",
        "target": "http://localhost:8080",
        "requested_class": "full",
        "conformant": True,
        "conformant_with_skips": True,
        "skipped_not_verified": [],
        "highest_class_passed": "full",
        "summary": {"pass": 63, "fail": 0, "skip": 0, "error": 0, "total": 63},
    }
    base.update(overrides)
    return base


class ReportTempDir(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)

    def write_report(self, data: dict) -> str:
        path = self.dir / "conformance-report.json"
        path.write_text(json.dumps(data, indent=2))
        return str(path)

    def read_report(self, path: str) -> dict:
        return json.loads(Path(path).read_text())


PIN = "08d61ea145d6b78c433f6910547c1e7ee293c948"
OTHER = "95b96d7ce473ab59d510e1690c73cc6660d0a73e"


# --------------------------------------------------------------------------
# check-conformance.py — the floor and the skips it already defended, plus the
# revision that says which suite the numbers are about.
# --------------------------------------------------------------------------
class CheckConformance(ReportTempDir):
    def test_a_green_report_at_the_floor_passes(self):
        path = self.write_report(report())
        code, out, _ = run(check_conformance, [path, "63"])
        self.assertEqual(code, 0, out)
        self.assertIn("63/63 passed", out)

    def test_a_failure_is_refused(self):
        path = self.write_report(report(
            summary={"pass": 62, "fail": 1, "skip": 0, "error": 0, "total": 63}))
        code, out, _ = run(check_conformance, [path, "63"])
        self.assertEqual(code, 1)
        self.assertIn("1 failed", out)

    def test_a_skip_is_never_a_pass(self):
        path = self.write_report(report(
            skipped_not_verified=["X-07"],
            summary={"pass": 62, "fail": 0, "skip": 1, "error": 0, "total": 63}))
        code, out, _ = run(check_conformance, [path, "62"])
        self.assertEqual(code, 1)
        self.assertIn("X-07", out)

    def test_a_shrunken_denominator_is_refused(self):
        path = self.write_report(report(
            requested_class="core",
            summary={"pass": 40, "fail": 0, "skip": 0, "error": 0, "total": 40}))
        code, out, _ = run(check_conformance, [path, "63"])
        self.assertEqual(code, 1)
        self.assertIn("40 checks passed", out)

    def test_a_missing_report_is_a_failure_rather_than_an_absence(self):
        code, _, err = run(check_conformance, [str(self.dir / "nope.json"), "63"])
        self.assertEqual(code, 1)
        self.assertIn("cannot read", err)

    # The new half: a green report measured against a suite this repository is
    # not pinned to is the failure #102 and #107 both were.
    def test_the_measured_revision_is_recorded_in_the_report(self):
        path = self.write_report(report())
        code, out, _ = run(check_conformance,
                           [path, "63", "--suite-revision", PIN,
                            "--expect-revision", PIN])
        self.assertEqual(code, 0, out)
        self.assertEqual(self.read_report(path)["suite_revision"], PIN)
        self.assertIn(PIN[:12], out)

    def test_a_report_from_another_revision_is_refused(self):
        path = self.write_report(report())
        code, out, _ = run(check_conformance,
                           [path, "63", "--suite-revision", OTHER,
                            "--expect-revision", PIN])
        self.assertEqual(code, 1)
        self.assertIn(OTHER[:12], out)
        self.assertIn(PIN[:12], out)

    def test_the_revision_is_recorded_even_when_the_run_is_refused(self):
        """The report is evidence before it is a verdict; a refused run still
        has to say which suite refused it."""
        path = self.write_report(report(
            summary={"pass": 62, "fail": 1, "skip": 0, "error": 0, "total": 63}))
        code, _, _ = run(check_conformance,
                         [path, "63", "--suite-revision", OTHER,
                          "--expect-revision", PIN])
        self.assertEqual(code, 1)
        self.assertEqual(self.read_report(path)["suite_revision"], OTHER)

    def test_an_unresolvable_revision_is_not_the_pin(self):
        """`$(suite-revision.py)` collapses to an empty string when it fails,
        and an empty string must not read as agreement."""
        path = self.write_report(report())
        code, out, _ = run(check_conformance,
                           [path, "63", "--suite-revision", "",
                            "--expect-revision", PIN])
        self.assertEqual(code, 1)
        self.assertIn("could not be resolved", out)

    def test_a_report_carrying_its_own_revision_is_checked_against_the_pin(self):
        """A report arriving from somebody else's machine — a pull request —
        is checkable without re-running anything."""
        path = self.write_report(report(suite_revision=OTHER))
        code, out, _ = run(check_conformance, [path, "63", "--expect-revision", PIN])
        self.assertEqual(code, 1)
        self.assertIn(OTHER[:12], out)

    def test_the_revision_check_is_opt_in(self):
        """The two-argument call the gate used before this existed still works,
        so an ad-hoc run of the suite is still readable."""
        path = self.write_report(report())
        code, out, _ = run(check_conformance, [path, "63"])
        self.assertEqual(code, 0, out)
        self.assertNotIn("suite_revision", self.read_report(path))


# --------------------------------------------------------------------------
# suite-revision.py — which checkout of the suite is actually installed.
# --------------------------------------------------------------------------
class SuiteRevision(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)

    def git_suite(self) -> tuple[Path, str]:
        """A clone-shaped tree: uhp_conformance/ inside a git work tree."""
        pkg = self.dir / "protocol" / "conformance" / "uhp_conformance"
        pkg.mkdir(parents=True)
        (pkg / "__init__.py").write_text('__version__ = "2026.8.11.post1"\n')
        env = {**os.environ, "GIT_CONFIG_GLOBAL": "/dev/null",
               "GIT_CONFIG_SYSTEM": "/dev/null"}
        for cmd in (["init", "-q", "-b", "main"],
                    ["config", "user.email", "t@example.com"],
                    ["config", "user.name", "T"],
                    ["add", "-A"],
                    ["commit", "-qm", "suite"]):
            subprocess.run(["git", "-C", str(self.dir)] + cmd, check=True, env=env)
        head = subprocess.run(["git", "-C", str(self.dir), "rev-parse", "HEAD"],
                              capture_output=True, text=True, check=True).stdout.strip()
        return pkg.parent, head

    def test_it_resolves_the_head_of_the_checkout(self):
        suite, head = self.git_suite()
        code, out, _ = run(suite_revision, [], suite_dir=str(suite))
        self.assertEqual(code, 0)
        self.assertEqual(out.strip(), head)

    def test_the_bare_revision_is_the_only_thing_on_stdout(self):
        """The Makefile captures this in `$(...)`; commentary belongs on stderr."""
        suite, head = self.git_suite()
        code, out, err = run(suite_revision, ["--expect", head], suite_dir=str(suite))
        self.assertEqual(code, 0)
        self.assertEqual(out.strip(), head)
        self.assertIn("matches", err)

    def test_a_checkout_at_another_revision_is_refused(self):
        suite, head = self.git_suite()
        code, _, err = run(suite_revision, ["--expect", PIN], suite_dir=str(suite))
        self.assertEqual(code, 1)
        self.assertIn(PIN[:12], err)
        self.assertIn(head[:12], err)

    def test_a_modified_checkout_is_not_the_revision_it_reports(self):
        suite, head = self.git_suite()
        (suite / "uhp_conformance" / "__init__.py").write_text("# edited\n")
        code, _, err = run(suite_revision, ["--expect", head], suite_dir=str(suite))
        self.assertEqual(code, 1)
        self.assertIn("modified", err)

    def test_an_untracked_file_is_not_a_modification(self):
        """`pip install -e` writes an egg-info directory into the checkout, and
        refusing that would refuse every editable install."""
        suite, head = self.git_suite()
        (suite / "uhp_conformance.egg-info").mkdir()
        (suite / "uhp_conformance.egg-info" / "PKG-INFO").write_text("x\n")
        code, out, _ = run(suite_revision, ["--expect", head], suite_dir=str(suite))
        self.assertEqual(code, 0)
        self.assertEqual(out.strip(), head)

    def test_a_suite_outside_a_work_tree_cannot_be_placed(self):
        """A wheel from PyPI has no revision, and unknown is not the pin."""
        pkg = self.dir / "site-packages" / "uhp_conformance"
        pkg.mkdir(parents=True)
        (pkg / "__init__.py").write_text("\n")
        code, out, err = run(suite_revision, [], suite_dir=str(pkg.parent))
        self.assertEqual(code, 2)
        self.assertEqual(out, "")
        self.assertIn("not a git", err.lower())

    def test_a_suite_that_is_not_installed_says_so(self):
        code, _, err = run(suite_revision, [], suite_dir=None, find_installed=lambda: None)
        self.assertEqual(code, 2)
        self.assertIn("UHP_SUITE_DIR", err)

    def test_a_work_tree_that_does_not_track_the_suite_is_not_its_revision(self):
        """The likely shape of this: a virtualenv made inside a working copy of
        *this* repository, with the suite installed into its site-packages. `git`
        searches upward, so the revision found there is uhp-go's own HEAD — and
        stamping that into the report would be a confident, wrong answer to the
        one question the report is being asked."""
        suite, _ = self.git_suite()
        stowaway = suite.parent.parent / ".venv" / "site-packages" / "uhp_conformance"
        stowaway.mkdir(parents=True)
        (stowaway / "__init__.py").write_text("\n")
        code, out, err = run(suite_revision, [], suite_dir=str(stowaway))
        self.assertEqual(code, 2)
        self.assertEqual(out, "")
        self.assertIn("does not track", err)

    def test_a_cleanliness_check_that_did_not_run_is_not_a_clean_checkout(self):
        suite, head = self.git_suite()
        real = suite_revision.git

        def flaky(args, cwd):
            if args[0] == "status":
                return subprocess.CompletedProcess(args, 128, "", "fatal: bad index")
            return real(args, cwd)

        with unittest.mock.patch.object(suite_revision, "git", flaky):
            code, out, err = run(suite_revision, ["--expect", head], suite_dir=str(suite))
        self.assertEqual(code, 2)
        self.assertEqual(out, "")
        self.assertIn("bad index", err)


# --------------------------------------------------------------------------
# check-suite-drift.py — has upstream moved past the pin.
# --------------------------------------------------------------------------
def comparison(status: str, ahead: int, commits: list[tuple[str, str]] | None = None) -> dict:
    return {
        "status": status,
        "ahead_by": ahead,
        "behind_by": 0,
        "total_commits": ahead,
        "commits": [{"sha": sha, "commit": {"message": msg}}
                    for sha, msg in (commits or [])],
    }


class CheckSuiteDrift(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.doc = Path(self.tmp.name) / "conformance.md"
        self.doc.write_text(f"pinned at harnessrouter `{PIN}`, at class `full`\n")

    def drift(self, argv, fetch):
        return run(check_suite_drift, argv + ["--doc", str(self.doc)], fetch=fetch)

    def test_a_pin_level_with_upstream_is_current(self):
        code, out, _ = self.drift(
            ["--pin", PIN], lambda url: comparison("identical", 0))
        self.assertEqual(code, 0)
        self.assertIn("current", out)

    def test_commits_since_the_pin_are_named_rather_than_counted(self):
        code, out, _ = self.drift(["--pin", PIN], lambda url: comparison(
            "ahead", 2, [("aaaaaaaaaaaa1111", "conformance: R-09 asserts the thing\n\nbody"),
                         ("bbbbbbbbbbbb2222", "docs: a word")]))
        self.assertEqual(code, 0)
        self.assertIn("behind", out)
        self.assertIn("R-09 asserts the thing", out)
        self.assertIn("aaaaaaa", out)
        self.assertNotIn("body", out)

    def test_a_long_list_is_elided_from_the_far_end(self):
        """A pin left alone for months is hundreds of commits behind, and the
        commits nearest upstream's head are the ones worth reading."""
        many = [(f"{i:040x}", f"commit {i}") for i in range(30)]
        code, out, _ = self.drift(
            ["--pin", PIN, "--max-commits", "5"],
            lambda url: comparison("ahead", 30, many))
        self.assertEqual(code, 0)
        self.assertIn("25 earlier commits", out)
        self.assertIn("commit 29", out)
        self.assertNotIn("commit 24", out)

    def test_drift_is_advisory_until_it_is_asked_not_to_be(self):
        """A revision moving upstream is not a defect here, so the maintainer's
        own run stays green. The scheduled job asks for the other answer."""
        args = ["--pin", PIN]
        fetch = lambda url: comparison("ahead", 1, [("cccccccccccc3333", "new check")])
        self.assertEqual(self.drift(args, fetch)[0], 0)
        self.assertEqual(self.drift(args + ["--fail-on-drift"], fetch)[0], 1)

    def test_the_pin_the_doc_names_must_be_the_pin_that_was_checked(self):
        """Not advisory: this one needs no network to know the tree contradicts
        itself, which is the shape of the copied numerator in the first place."""
        code, out, _ = self.drift(
            ["--pin", OTHER], lambda url: comparison("identical", 0))
        self.assertEqual(code, 1)
        self.assertIn(str(self.doc), out)
        self.assertIn(OTHER[:12], out)

    def test_a_pin_upstream_does_not_carry_on_that_branch_is_not_current(self):
        """`ahead_by` alone cannot tell "level with main" from "on a commit main
        has never contained" — a rewritten history, or a pin taken from an
        unmerged branch. Both are zero commits behind, and only one of them is a
        pin anybody else can reproduce."""
        for status in ("behind", "diverged"):
            with self.subTest(status=status):
                code, out, _ = self.drift(
                    ["--pin", PIN, "--fail-on-drift"],
                    lambda url: comparison(status, 0))
                self.assertEqual(code, 1)
                self.assertNotIn("is current", out)
                self.assertIn(status, out)

    def test_a_github_that_cannot_be_reached_is_not_an_answer(self):
        def fetch(url):
            raise OSError("nope")
        code, _, err = self.drift(["--pin", PIN], fetch)
        self.assertEqual(code, 2)
        self.assertIn("nope", err)

    def test_the_contradiction_survives_a_github_that_cannot_be_reached(self):
        """The pin-versus-doc check needs no network to know the answer, so an
        unreachable GitHub must not be what hides it."""
        def fetch(url):
            raise OSError("nope")
        code, out, _ = self.drift(["--pin", OTHER], fetch)
        self.assertEqual(code, 2)
        self.assertIn(str(self.doc), out)

    def test_asking_for_no_commits_lists_no_commits(self):
        many = [(f"{i:040x}", f"commit {i}") for i in range(3)]
        code, out, _ = self.drift(
            ["--pin", PIN, "--max-commits", "0"],
            lambda url: comparison("ahead", 3, many))
        self.assertEqual(code, 0)
        self.assertIn("3 earlier commits", out)
        self.assertNotIn("commit 0", out)
        self.assertNotIn("commit 2", out)

    def test_a_pin_upstream_has_never_heard_of_is_reported(self):
        """A rewritten history, or a typo in the Makefile: either way the
        comparison is not a drift measurement and must not read as one."""
        def fetch(url):
            raise check_suite_drift.UpstreamError("404 Not Found")
        code, _, err = self.drift(["--pin", PIN], fetch)
        self.assertEqual(code, 2)
        self.assertIn("404", err)

    def test_it_asks_about_the_pin_and_the_branch_it_was_given(self):
        seen = []

        def fetch(url):
            seen.append(url)
            return comparison("identical", 0)

        self.drift(["--pin", PIN, "--repo", "HarnessRouter/harnessrouter",
                    "--branch", "main"], fetch)
        self.assertIn("HarnessRouter/harnessrouter", seen[0])
        self.assertIn(f"{PIN}...main", seen[0])


# --------------------------------------------------------------------------
# The pin is written down in three places. This is the one that notices.
# --------------------------------------------------------------------------
class ThePinIsOneNumber(unittest.TestCase):
    def test_the_makefile_pin_is_the_pin_the_doc_records(self):
        makefile = (REPO / "Makefile").read_text()
        line = next(l for l in makefile.splitlines()
                    if l.startswith("CONFORMANCE_SUITE_REVISION"))
        pin = line.split("?=")[1].strip()
        self.assertRegex(pin, r"^[0-9a-f]{40}$")
        self.assertIn(pin, (REPO / "docs" / "conformance.md").read_text())


if __name__ == "__main__":
    unittest.main()
