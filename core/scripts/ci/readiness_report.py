#!/usr/bin/env python3
# file: core/scripts/ci/readiness_report.py
# version: 1.0.0
# guid: 186aafab-4851-45e2-a368-2b19368a5094
# last-edited: 2026-11-08
"""Fold the per-criterion results into the readiness report (12 §12.3.11; make -C core readiness).

Reads sc1.json, sc5.json, sc6.json and sc9.json from the readiness directory
(each written by its script's --out) and writes readiness.json and
readiness.md beside them, with the date and the commit. A result that was never
written is reported as ERROR "did not run", so a skipped step cannot read as a
pass. Always exits 0: the report never fails a merge. Writes only inside the
directory it is given (default core/.ci/readiness/, ignored by core/.gitignore).

Stdlib only.
"""

from __future__ import annotations

import argparse
import datetime
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import core_ci_lib as lib  # noqa: E402

CORE_DIR = pathlib.Path(__file__).resolve().parents[2]
# The criteria `make -C core readiness` measures: SC1 (apidiff-gate), SC5
# (coverage), SC6 (coredoc-check), SC9 (vulncheck). SC3 and SC8 run from the root.
CRITERIA = {
    "SC1": "Public API stable across 3 consecutive core checkpoints",
    "SC5": "Coverage: total and per SDK package",
    "SC6": "Every core contribution point is documented",
    "SC9": "No known vulnerabilities in core's graph",
}
LABELS = {lib.PASS: "PASS", lib.FAIL: "FAIL", lib.NOT_APPLICABLE: "N/A", lib.ERROR: "ERROR"}


def head_commit(repo: pathlib.Path) -> str:
    res = lib.git(repo, "rev-parse", "HEAD", check=False)
    return res.stdout.strip() if res.returncode == 0 else "unknown"


def collect(out_dir: pathlib.Path) -> list[lib.Result]:
    loaded = lib.load_results(out_dir, list(CRITERIA))
    return [
        r if r is not None else lib.Result(ident, CRITERIA[ident], lib.ERROR, "did not run (no result was written)")
        for ident, r in zip(CRITERIA, loaded)
    ]


def render_markdown(date: str, commit: str, results: list[lib.Result]) -> str:
    lines = [
        f"# Core readiness report, {date}",
        "",
        f"Commit `{commit}`. Report only: nothing here gates a merge (12 §12.3.11).",
        "",
        "| Criterion | Status | Measured |",
        "|---|---|---|",
    ]
    for r in results:
        lines.append(f"| {r.id} {r.title} | {LABELS.get(r.status, r.status)} | {r.measured} |")
    for r in results:
        if r.details:
            lines += ["", f"### {r.id}", ""] + [f"    {d}" for d in r.details]
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", type=pathlib.Path, default=CORE_DIR / ".ci" / "readiness", help="the readiness directory")
    parser.add_argument("--repo", type=pathlib.Path, default=CORE_DIR, help="git work tree for the commit (default: core's)")
    parser.add_argument("--date", default="", help="YYYY-MM-DD (default: today, UTC)")
    parser.add_argument("--commit", default="", help="commit to record (default: HEAD of --repo)")
    args = parser.parse_args(argv)
    date = args.date or datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    commit = args.commit or head_commit(args.repo)
    results = collect(args.out)
    report = {"date": date, "commit": commit, "criteria": [vars(r) for r in results]}
    args.out.mkdir(parents=True, exist_ok=True)
    (args.out / "readiness.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    markdown = render_markdown(date, commit, results)
    (args.out / "readiness.md").write_text(markdown, encoding="utf-8")
    print(markdown, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
