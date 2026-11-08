#!/usr/bin/env python3
# file: scripts/ci/fold_split_readiness.py
# version: 1.0.0
# guid: 817f6112-7352-436b-ba0b-848c9462f73c
# last-edited: 2026-11-08
"""Fold a night's readiness results into docs/plans/platform-core/split-readiness.md (12 §12.3, §12.3.11).

split-readiness.yml runs ``make -C core readiness`` (SC1, SC5, SC6, SC9), SC3
and SC8, then calls this with the report (core/.ci/readiness/readiness.json)
and the directory holding sc3.json and sc8.json. It rewrites two regions of the
document, between marker comments, and nothing else:

  * "Latest": one row per measured criterion, from the newest night;
  * "History": one row per night, newest first (a rerun on the same date
    replaces that date's row; rows past --keep fall off, git keeps them).

The document's header version is bumped and last-edited set only when a region
changed, so an identical rerun leaves the file untouched and the workflow opens
no PR. Exit 0 either way; the number of changes is printed.

A root-side file: cutover step 6 drops it with the nightly job (12 §12.8.2).
Stdlib only.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "core" / "scripts" / "ci"))
import core_ci_lib as lib  # noqa: E402

DOC = pathlib.Path("docs/plans/platform-core/split-readiness.md")
# The nightly measures these (SC2, SC4, SC7 and SC10 are merge gates; SC4(b) and (c) read CI).
COLUMNS = ("SC1", "SC3", "SC5", "SC6", "SC8", "SC9")
LABELS = {lib.PASS: "PASS", lib.FAIL: "FAIL", lib.NOT_APPLICABLE: "N/A", lib.ERROR: "ERROR"}
KEEP = 60

LATEST_RE = re.compile(r"(<!-- readiness:latest:start -->\n)(.*?)(<!-- readiness:latest:end -->)", re.S)
HISTORY_RE = re.compile(r"(<!-- readiness:history:start -->\n)(.*?)(<!-- readiness:history:end -->)", re.S)
VERSION_RE = re.compile(r"^(<!-- version: )(\d+)\.(\d+)\.(\d+)( -->)$", re.M)
EDITED_RE = re.compile(r"^(<!-- last-edited: )\d{4}-\d{2}-\d{2}( -->)$", re.M)


def load_night(report_json: pathlib.Path, extra_dir: pathlib.Path) -> tuple[str, str, dict[str, lib.Result]]:
    """(date, commit, {criterion: result}) for the night: core's four, then SC3 and SC8."""
    report = json.loads(report_json.read_text(encoding="utf-8"))
    results = {c["id"]: lib.Result(**c) for c in report["criteria"]}
    for ident, loaded in zip(("SC3", "SC8"), lib.load_results(extra_dir, ["SC3", "SC8"])):
        results[ident] = loaded or lib.Result(ident, "", lib.ERROR, "did not run (no result was written)")
    return report["date"], report["commit"], results


def cell(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ")


def render_latest(date: str, commit: str, results: dict[str, lib.Result]) -> str:
    lines = [
        f"Night of {date}, commit `{commit[:12]}`.",
        "",
        "| Criterion | Status | Measured |",
        "|---|---|---|",
    ]
    for ident in COLUMNS:
        r = results[ident]
        lines.append(f"| {ident} {cell(r.title)} | {LABELS.get(r.status, r.status)} | {cell(r.measured)} |")
    return "\n".join(lines) + "\n"


def history_row(date: str, commit: str, results: dict[str, lib.Result]) -> str:
    cells = [date, f"`{commit[:12]}`"] + [LABELS.get(results[i].status, results[i].status) for i in COLUMNS]
    return "| " + " | ".join(cells) + " |"


def render_history(existing: str, date: str, row: str, keep: int) -> str:
    head = ["| Date | Commit | " + " | ".join(COLUMNS) + " |", "|---|---|" + "---|" * len(COLUMNS)]
    rows = [ln for ln in existing.splitlines() if ln.startswith("| 20")]
    rows = [row] + [ln for ln in rows if not ln.startswith(f"| {date} |")]
    rows.sort(key=lambda ln: ln.split("|")[1].strip(), reverse=True)
    return "\n".join(head + rows[:keep]) + "\n"


def bump_header(text: str, date: str) -> str:
    def bump(m: re.Match) -> str:
        return f"{m.group(1)}{m.group(2)}.{m.group(3)}.{int(m.group(4)) + 1}{m.group(5)}"

    text, n = VERSION_RE.subn(bump, text, count=1)
    if n != 1:
        raise ValueError("the document has no '<!-- version: X.Y.Z -->' header line")
    return EDITED_RE.sub(lambda m: f"{m.group(1)}{date}{m.group(2)}", text, count=1)


def fold(doc_text: str, date: str, commit: str, results: dict[str, lib.Result], keep: int = KEEP) -> str:
    latest, history = LATEST_RE.search(doc_text), HISTORY_RE.search(doc_text)
    if not latest or not history:
        raise ValueError("the document lacks its readiness:latest / readiness:history marker pairs")
    new = LATEST_RE.sub(lambda m: m.group(1) + render_latest(date, commit, results) + m.group(3), doc_text, count=1)
    new = HISTORY_RE.sub(
        lambda m: m.group(1) + render_history(m.group(2), date, history_row(date, commit, results), keep) + m.group(3),
        new,
        count=1,
    )
    return doc_text if new == doc_text else bump_header(new, date)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--report", type=pathlib.Path, required=True, help="core's readiness.json")
    parser.add_argument("--extra", type=pathlib.Path, required=True, help="directory holding sc3.json and sc8.json")
    parser.add_argument("--doc", type=pathlib.Path, default=REPO_ROOT / DOC)
    parser.add_argument("--keep", type=int, default=KEEP, help="history rows to keep")
    args = parser.parse_args(argv)
    try:
        date, commit, results = load_night(args.report, args.extra)
        old = args.doc.read_text(encoding="utf-8")
        new = fold(old, date, commit, results, args.keep)
    except (OSError, ValueError, KeyError, json.JSONDecodeError) as e:
        print(f"::error::fold_split_readiness: {e}")
        return 1
    if new == old:
        print(f"{args.doc}: unchanged")
        return 0
    args.doc.write_text(new, encoding="utf-8")
    print(f"{args.doc}: folded the night of {date} (commit {commit[:12]})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
