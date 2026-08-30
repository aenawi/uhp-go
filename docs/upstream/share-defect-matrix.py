"""Run the R-series against the adversarial stub once per defect, and print the outcome matrix.

The point is the diagonal: every defect must be caught by the check that claims to test it, and
nothing else should light up except where a skip is the honest answer. A check that only ever
passes proves nothing.

    HARNESSROUTER=~/src/harnessrouter python3 docs/upstream/share-defect-matrix.py

`HARNESSROUTER` is a checkout of HarnessRouter/harnessrouter with
`docs/upstream/session-sharing-checks.patch` applied. Needs no network and no agent tokens: the
stub answers every task itself.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
ROOT = os.environ.get("HARNESSROUTER", "")
if not ROOT:
    sys.exit("set HARNESSROUTER to a checkout of HarnessRouter/harnessrouter (see this file's docstring)")
SUITE = pathlib.Path(ROOT).expanduser() / "protocol" / "conformance"
if not (SUITE / "uhp_conformance" / "checks.py").exists():
    sys.exit(f"no conformance suite under {SUITE}")

IDS = [f"R-0{i}" for i in range(1, 8)]
DEFECTS = ["none", "view_needs_auth", "get_share_disagrees", "share_id_is_token",
           "view_accepts_writes", "leaks_credentials", "revoke_lies", "multi_mint",
           "share_outlives_session"]


def wait(port: int, tries: int = 60) -> bool:
    for _ in range(tries):
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{port}/v1/uhp", timeout=1).read()
            return True
        except Exception:  # noqa: BLE001 — the server is simply not up yet
            time.sleep(0.1)
    return False


def run(defect: str, port: int) -> dict:
    env = {**os.environ, "DEFECT": defect, "PORT": str(port)}
    srv = subprocess.Popen([sys.executable, str(HERE / "share-defect-stub.py")], env=env,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        if not wait(port):
            return {i: "noserver" for i in IDS}
        out = HERE / f"report-{defect}.json"
        subprocess.run(
            [sys.executable, "-m", "uhp_conformance.cli", "--base-url", f"http://127.0.0.1:{port}",
             "--api-key", "stub-key", "--class", "full", "--only", ",".join(IDS),
             "--json", str(out), "--plain"],
            cwd=str(SUITE), env={**os.environ, "PYTHONPATH": str(SUITE)},
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, check=False)
        try:
            return {c["id"]: c["outcome"] for c in json.loads(out.read_text())["checks"]}
        finally:
            out.unlink(missing_ok=True)
    finally:
        srv.terminate()
        srv.wait(timeout=5)


rows = {d: run(d, 8931 + n) for n, d in enumerate(DEFECTS)}

print("| Defect | " + " | ".join(IDS) + " |")
print("| --- |" + " --- |" * len(IDS))
for d, r in rows.items():
    cells = [f"**{o.upper()}**" if (o := r.get(i, "?")) == "fail" else o for i in IDS]
    print(f"| {'*(none)*' if d == 'none' else '`' + d + '`'} | " + " | ".join(cells) + " |")

missed = [f"{d} not caught by {i}" for d, i in
          zip(DEFECTS[1:], ["R-01", "R-02", "R-03", "R-04", "R-05", "R-06", "R-06", "R-07"])
          if rows[d].get(i) != "fail"]
clean = all(o == "pass" for o in rows["none"].values())
print()
print("clean server passes all seven:", clean)
print("every defect caught by its own check:", not missed, missed or "")
sys.exit(0 if clean and not missed else 1)
