#!/usr/bin/env python3
# file: core/scripts/ci/core_coverage_by_package.py
# version: 1.0.0
# guid: caa00dbd-8862-449a-b80f-0c967eeafbf0
# last-edited: 2026-11-08
"""SC5: core's statement coverage, total and per package (12 §12.3 SC5; make -C core coverage).

Reads a ``go test -short -coverprofile`` profile and requires the total to be
at least --min-total (70, the library floor) and every package outside
--exclude to be at least --min (60). Total and package figures are statements
covered over statements in the profile, which is what ``go tool cover -func``
reports, so a total from either agrees. A package with no test file is in the
profile at 0 % (go 1.22+), so an untested SDK package fails the per-package
floor instead of dropping out of it.

A readiness report, never a merge gate (the merge gate carries a ratchet floor,
core/.ci/coverage-floor.txt). --exclude is a regular expression searched in
each file's module-relative path: the spec's ``kernel/|internal/|examples/``
leaves the SDK packages (03 §3.1).

Stdlib only.
"""

from __future__ import annotations

import argparse
import collections
import pathlib
import re
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import core_ci_lib as lib  # noqa: E402

CORE_DIR = pathlib.Path(__file__).resolve().parents[2]
_BLOCK_RE = re.compile(r"^(?P<file>.+):(?P<span>\d+\.\d+,\d+\.\d+) (?P<stmts>\d+) (?P<count>\d+)$")


def parse_profile(text: str) -> dict[str, tuple[int, int]]:
    """{file: (statements, covered statements)} from a cover profile.

    A block listed more than once (several test binaries over one package)
    counts once, covered when any listing covered it.
    """
    lines = text.splitlines()
    if not lines or not lines[0].startswith("mode:"):
        raise ValueError("not a cover profile: the first line is not 'mode: ...'")
    blocks: dict[tuple[str, str], tuple[int, int]] = {}
    for n, line in enumerate(lines[1:], 2):
        if not line.strip():
            continue
        m = _BLOCK_RE.match(line)
        if not m:
            raise ValueError(f"profile line {n}: unrecognised block {line!r}")
        key = (m["file"], m["span"])
        stmts, count = int(m["stmts"]), int(m["count"])
        prev = blocks.get(key)
        blocks[key] = (stmts, max(count, prev[1]) if prev else count)
    files: dict[str, list[int]] = collections.defaultdict(lambda: [0, 0])
    for (name, _), (stmts, count) in blocks.items():
        files[name][0] += stmts
        if count > 0:
            files[name][1] += stmts
    return {name: (t, c) for name, (t, c) in files.items()}


def pct(covered: int, total: int) -> float:
    return 100.0 * covered / total if total else 100.0


def evaluate(
    files: dict[str, tuple[int, int]],
    module: str,
    minimum: float,
    min_total: float,
    exclude: str,
) -> lib.Result:
    title = "Coverage: total and per SDK package"
    prefix = module.rstrip("/") + "/"
    skip = re.compile(exclude) if exclude else None
    total = [0, 0]
    packages: dict[str, list[int]] = collections.defaultdict(lambda: [0, 0])
    for name, (stmts, covered) in files.items():
        rel = name[len(prefix):] if name.startswith(prefix) else name
        total[0] += stmts
        total[1] += covered
        if skip and skip.search(rel):
            continue
        pkg = rel.rpartition("/")[0] or "."
        packages[pkg][0] += stmts
        packages[pkg][1] += covered
    total_pct = pct(total[1], total[0])
    below = sorted(
        ((pct(c, t), pkg) for pkg, (t, c) in packages.items() if pct(c, t) < minimum),
    )
    details = [f"total {total_pct:.1f}% of {total[0]} statements (needs {min_total:g}%)"]
    details.append(f"{len(packages)} SDK package(s) measured, {len(below)} below {minimum:g}%")
    details += [f"    {p:5.1f}%  {pkg}" for p, pkg in below]
    ok = total_pct >= min_total and not below
    return lib.Result(
        "SC5",
        title,
        lib.PASS if ok else lib.FAIL,
        f"total {total_pct:.1f}%, {len(below)} of {len(packages)} SDK package(s) below {minimum:g}%",
        details,
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("profile", type=pathlib.Path, help="a go test -coverprofile file")
    parser.add_argument("--min", type=float, default=60.0, help="per-package floor in percent (default 60)")
    parser.add_argument("--min-total", type=float, default=70.0, help="total floor in percent (default 70)")
    parser.add_argument("--exclude", default="", help="regexp over module-relative file paths left out of the per-package floor")
    parser.add_argument("--module", default="", help="core's module path (default: read core/go.mod)")
    parser.add_argument("--test-status", type=int, default=0, help="exit status of the go test run that wrote the profile")
    parser.add_argument("--out", type=pathlib.Path, help="write sc5.json here (make -C core readiness)")
    parser.add_argument("--report", action="store_true", help="report only: exit 0 on FAIL or ERROR")
    args = parser.parse_args(argv)
    try:
        module = args.module or lib.module_path(CORE_DIR)
        files = parse_profile(args.profile.read_text(encoding="utf-8"))
        if not files:
            raise ValueError("the profile holds no blocks")
        result = evaluate(files, module, args.min, args.min_total, args.exclude)
    except (OSError, ValueError) as e:
        result = lib.Result("SC5", "Coverage: total and per SDK package", lib.ERROR, f"profile unusable: {e}")
    if args.test_status:
        result.details.append(f"go test exited {args.test_status}: figures include the partial coverage of failing packages")
    return lib.finish(result, args.out, args.report)


if __name__ == "__main__":
    sys.exit(main())
