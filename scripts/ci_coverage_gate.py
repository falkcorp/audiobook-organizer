#!/usr/bin/env python3
# file: scripts/ci_coverage_gate.py
# version: 1.0.0
# guid: 0b6f2f0e-6d0c-4d5e-9a1b-3c7e2d9f4a61
# last-edited: 2026-09-26

"""Woodpecker coverage gate: the `coverage-check-short` step of `make ci`,
computed across the parallel test workflows in .woodpecker/.

Why this exists: the Go test packages are split over several workflows so the
slow ones run on different agents at the same time. Woodpecker has no way to
pass files between workflows on different agents, so no single place ever
holds the whole coverage profile. What does merge exactly is the pair of
statement counts. Every test workflow ends with an awk line that prints

    CI-COVERAGE workflow=<name> covered=<statements hit> total=<statements>

computed from its own profile, and the package sets never overlap. So the
repo-wide coverage is sum(covered) / sum(total), the same ratio `go tool cover`
reports as `total:` for a merged profile.

This script runs in the `coverage` workflow after all the test workflows. It
reads those lines from the current pipeline's step logs through the Woodpecker
API, sums them, and fails below .ci/coverage-floor.txt. It also fails when any
expected workflow printed no line, or printed more than one.

Environment:
  CI_SYSTEM_URL, CI_REPO, CI_PIPELINE_NUMBER   (set by Woodpecker)
  WOODPECKER_TOKEN                              API token (secret)
  CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET  optional; Cloudflare Access
                                                service token when the API is
                                                reached through the tunnel
  WOODPECKER_API_URL                            optional override for CI_SYSTEM_URL

Offline use (for tests, or to gate a local log dump):
  python3 scripts/ci_coverage_gate.py --logs file1.log file2.log
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

EXPECTED_WORKFLOWS = ("test-database", "test-server-scanner", "test-rest")
LINE_RE = re.compile(r"CI-COVERAGE workflow=(\S+) covered=(\d+) total=(\d+)")


def parse_lines(text: str) -> list[tuple[str, int, int]]:
    """Return (workflow, covered, total) for every CI-COVERAGE line in `text`.

    A line that merely echoes the awk program (shell tracing) has no digits
    after `covered=`, so it never matches.
    """
    return [(m.group(1), int(m.group(2)), int(m.group(3))) for m in LINE_RE.finditer(text)]


def combine(
    found: list[tuple[str, int, int]], expected: tuple[str, ...] = EXPECTED_WORKFLOWS
) -> tuple[float, list[str]]:
    """Sum the per-workflow counts. Returns (percent, problems)."""
    problems: list[str] = []
    by_wf: dict[str, list[tuple[int, int]]] = {}
    for wf, c, t in found:
        by_wf.setdefault(wf, []).append((c, t))
    for wf in expected:
        n = len(by_wf.get(wf, []))
        if n == 0:
            problems.append(f"{wf}: no CI-COVERAGE line (did its tests fail or not run?)")
        elif n > 1:
            problems.append(f"{wf}: {n} CI-COVERAGE lines, expected exactly one")
    for wf in by_wf:
        if wf not in expected:
            problems.append(f"{wf}: unexpected workflow in coverage lines")
    covered = sum(c for v in by_wf.values() for c, _ in v)
    total = sum(t for v in by_wf.values() for _, t in v)
    if total == 0:
        problems.append("no statements counted")
        return 0.0, problems
    return round(100.0 * covered / total, 1), problems


def decode_log_entries(entries: list[dict]) -> str:
    """Woodpecker returns step logs as JSON entries whose `data` is base64."""
    out = []
    for e in entries:
        raw = e.get("data", "")
        try:
            out.append(base64.b64decode(raw).decode("utf-8", "replace"))
        except (ValueError, TypeError):
            out.append(str(raw))
    return "\n".join(out)


def _get(url: str) -> object:
    headers = {"Authorization": f"Bearer {os.environ['WOODPECKER_TOKEN']}", "Accept": "application/json"}
    if os.environ.get("CF_ACCESS_CLIENT_ID"):
        headers["CF-Access-Client-Id"] = os.environ["CF_ACCESS_CLIENT_ID"]
        headers["CF-Access-Client-Secret"] = os.environ.get("CF_ACCESS_CLIENT_SECRET", "")
    req = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.load(resp)


def fetch_pipeline_logs() -> str:
    base = (os.environ.get("WOODPECKER_API_URL") or os.environ["CI_SYSTEM_URL"]).rstrip("/")
    repo = os.environ["CI_REPO"]
    number = os.environ["CI_PIPELINE_NUMBER"]
    repo_id = _get(f"{base}/api/repos/lookup/{repo}")["id"]  # type: ignore[index]
    pipeline = _get(f"{base}/api/repos/{repo_id}/pipelines/{number}")
    texts = []
    for wf in pipeline.get("workflows") or []:  # type: ignore[union-attr]
        if wf.get("name") not in EXPECTED_WORKFLOWS:
            continue
        for step in wf.get("children") or []:
            entries = _get(f"{base}/api/repos/{repo_id}/logs/{number}/{step['id']}")
            texts.append(decode_log_entries(entries or []))  # type: ignore[arg-type]
    return "\n".join(texts)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Woodpecker cross-workflow coverage gate")
    ap.add_argument("--logs", nargs="*", help="read these log files instead of the Woodpecker API")
    ap.add_argument("--floor-file", default=".ci/coverage-floor.txt")
    args = ap.parse_args(argv)

    text = "\n".join(Path(f).read_text() for f in args.logs) if args.logs else fetch_pipeline_logs()
    found = parse_lines(text)
    for wf, c, t in sorted(found):
        print(f"{wf:22s} {c:>8d} / {t:>8d} statements  ({100.0 * c / t if t else 0:.1f}%)")
    pct, problems = combine(found)
    floor = float(Path(args.floor_file).read_text().strip())
    print(f"\nTotal coverage: {pct}%  (floor {floor}%)")
    for p in problems:
        print(f"❌ {p}")
    if problems:
        return 1
    if pct < floor:
        print(f"❌ Coverage {pct}% is below committed floor {floor}%")
        return 1
    print(f"✅ Coverage {pct}% meets floor {floor}%")
    return 0


if __name__ == "__main__":
    sys.exit(main())
