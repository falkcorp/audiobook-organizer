#!/usr/bin/env python3
# file: core/scripts/ci/core_vulncheck.py
# version: 1.0.0
# guid: c58bb0af-fb8a-415d-9a66-8c1b4ff8b2e2
# last-edited: 2026-11-08
"""SC9: no known vulnerabilities in core's graph (12 §12.3 SC9; make -C core vulncheck).

Runs the pinned govulncheck (core/tools/govulncheck, ``make -C core tools``
builds it into core/bin) as ``govulncheck ./...`` from core/ and records the
verdict. govulncheck exits 0 when it finds nothing, 3 when it finds
vulnerabilities, and anything else when it could not run (no network for the
vulnerability database, a build failure): the last is ERROR, not a finding.
The full output goes to sc9-govulncheck.txt beside the result.

A readiness report, never a merge gate. Stdlib only.
"""

from __future__ import annotations

import argparse
import pathlib
import subprocess
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import core_ci_lib as lib  # noqa: E402

CORE_DIR = pathlib.Path(__file__).resolve().parents[2]
TITLE = "No known vulnerabilities in core's graph"
VULNS_FOUND = 3  # govulncheck's documented exit status for "vulnerabilities found"


def measure(core: pathlib.Path, govulncheck: pathlib.Path, out: pathlib.Path | None) -> lib.Result:
    if not govulncheck.is_file():
        return lib.Result("SC9", TITLE, lib.ERROR, f"{govulncheck} is missing (make -C core tools builds it)")
    res = subprocess.run(
        [str(govulncheck), "./..."], cwd=core, env=lib.go_env(), capture_output=True, text=True, check=False
    )
    text = res.stdout + res.stderr
    if out is not None:
        out.mkdir(parents=True, exist_ok=True)
        (out / "sc9-govulncheck.txt").write_text(text, encoding="utf-8")
    tail = [ln for ln in text.strip().splitlines() if ln][-20:]
    if res.returncode == 0:
        return lib.Result("SC9", TITLE, lib.PASS, "govulncheck ./... found nothing", tail)
    if res.returncode == VULNS_FOUND:
        return lib.Result("SC9", TITLE, lib.FAIL, "govulncheck ./... found vulnerabilities", tail)
    return lib.Result("SC9", TITLE, lib.ERROR, f"govulncheck exited {res.returncode} without a verdict", tail)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--core", type=pathlib.Path, default=CORE_DIR, help="core module root (default: this script's module)")
    parser.add_argument("--govulncheck", type=pathlib.Path, default=CORE_DIR / "bin" / "govulncheck", help="the pinned govulncheck binary")
    parser.add_argument("--out", type=pathlib.Path, help="write sc9.json (and the full output) here (make -C core readiness)")
    parser.add_argument("--report", action="store_true", help="report only: exit 0 on FAIL or ERROR")
    args = parser.parse_args(argv)
    result = measure(args.core.resolve(), args.govulncheck.resolve(), args.out)
    return lib.finish(result, args.out, args.report)


if __name__ == "__main__":
    sys.exit(main())
