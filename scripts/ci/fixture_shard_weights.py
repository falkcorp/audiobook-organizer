#!/usr/bin/env python3
# file: scripts/ci/fixture_shard_weights.py
# version: 1.0.0
# guid: 0137f1ac-12f5-4d15-b785-2c5973073dda
# last-edited: 2026-10-30
"""Recalibrate the fixture-shard weights from measured CI times.

`scripts/ci/fixture_test_packages.py --run` (make test-fixtures) reports how long
each package, or each slice of a split package, took:

  * one `::timing::<unit>=<seconds>` line per unit on stdout, where <unit> is the
    repo-relative package (`internal/undo`) or a slice of one
    (`internal/server#1/2`), and
  * when FIXTURE_TIMES_FILE is set, a JSON file `fixture-times.json`:
    {"version": 1, "timings": [{"unit": "...", "pkg": "...", "part": 0,
    "parts": 2, "seconds": 605.4}, ...]} (part is 0-based). GitHub uploads it as
    an artifact; Woodpecker has no artifacts, so its step log is the source.

This script reads those outputs and rewrites scripts/ci/fixture_weights.json, the
table the planner weighs packages by. It accepts, per input, any of: a
fixture-times.json, text holding `::timing::` lines, or a raw Woodpecker /
`make test-fixtures` log (the `=== <label>: ok in Ns` headers and the
`ok <pkg> <secs>s` lines under them, which is how the numbers were first taken).

A package's shards run apart, so one measurement needs every shard of the same
pipeline: inputs are grouped into samples with --sample. Files after one --sample
are one sample (typically shard 1, 2 and 3 of one pipeline). Bare positional
files, or stdin when none are given, form one more sample. A package's weight in
a sample is the sum of its slices' times (a whole package has one), and a sample
that lacks one of a package's slices contributes nothing for it. The weight
written is the median over the samples (--stat max for the slowest), rounded up to
whole seconds. A package no
sample measured keeps its previous weight and is listed under "estimated", so an
estimate is never mistaken for a measurement.

Usage:
  fixture_shard_weights.py --sample s1.log s2.log s3.log --sample t1.log t2.log t3.log
  cat fixture-times-*.json | fixture_shard_weights.py          # one sample from stdin
  fixture_shard_weights.py --dry-run ...                       # print, do not write
"""

from __future__ import annotations

import argparse
import json
import math
import re
import statistics
import sys
from pathlib import Path
from typing import NamedTuple

MODULE = "github.com/falkcorp/audiobook-organizer"
WEIGHTS_FILE = Path(__file__).resolve().with_name("fixture_weights.json")
TIMES_VERSION = 1

_TIMING_RE = re.compile(r"^::timing::(?P<unit>\S+?)=(?P<secs>\d+(?:\.\d+)?)\s*$")
_OK_RE = re.compile(r"^ok\s+(?P<pkg>\S+)\s+(?P<secs>\d+(?:\.\d+)?)s\b")
_HEADER_RE = re.compile(r"^=== (?P<label>.+): (?P<state>ok|FAIL|INTERRUPTED)\b")
_UNIT_RE = re.compile(r"^(?P<pkg>[^#\s]+?)(?:#(?P<part>\d+)/(?P<parts>\d+))?$")


class Timing(NamedTuple):
    pkg: str  # repo-relative, e.g. internal/server
    part: int  # 0-based
    parts: int  # 1 = whole package
    seconds: float

    @property
    def unit(self) -> str:
        return self.pkg if self.parts == 1 else f"{self.pkg}#{self.part + 1}/{self.parts}"


def rel(pkg: str) -> str:
    return pkg.removeprefix(MODULE + "/")


def parse_unit(unit: str, seconds: float) -> Timing:
    m = _UNIT_RE.match(unit)
    if not m:
        raise ValueError(f"unparseable timing unit {unit!r}")
    if m["part"] is None:
        return Timing(rel(m["pkg"]), 0, 1, seconds)
    part, parts = int(m["part"]), int(m["parts"])
    if not 1 <= part <= parts:
        raise ValueError(f"timing unit {unit!r}: part out of range")
    return Timing(rel(m["pkg"]), part - 1, parts, seconds)


def timing_line(t: Timing) -> str:
    return f"::timing::{t.unit}={t.seconds:.1f}"


def to_json(timings: list[Timing]) -> str:
    rows = [
        {"unit": t.unit, "pkg": t.pkg, "part": t.part, "parts": t.parts, "seconds": round(t.seconds, 1)}
        for t in sorted(timings, key=lambda t: (t.pkg, t.part))
    ]
    return json.dumps({"version": TIMES_VERSION, "timings": rows}, indent=2) + "\n"


def ok_timings(output: str, label: str = "") -> list[Timing]:
    """Timings from the `ok <pkg> <secs>s` lines `go test` printed. Under a
    slice's label (`pkg#2/3`) its one ok line is that slice's time; elsewhere
    each line is a whole package."""
    out = []
    for line in output.splitlines():
        m = _OK_RE.match(line)
        if m:
            unit = label if "#" in label else m["pkg"]
            out.append(parse_unit(unit, float(m["secs"])))
    return out


def parse_text(text: str) -> list[Timing]:
    """::timing:: lines when the text has any (they are the authoritative form);
    otherwise the `=== label` blocks and `ok` lines of a raw log."""
    explicit = []
    for line in text.splitlines():
        m = _TIMING_RE.match(line.strip())
        if m:
            explicit.append(parse_unit(m["unit"], float(m["secs"])))
    if explicit:
        return explicit
    out: list[Timing] = []
    label = ""
    block: list[str] = []

    def flush() -> None:
        out.extend(ok_timings("\n".join(block), label))
        block.clear()

    for line in text.splitlines():
        h = _HEADER_RE.match(line)
        if h:
            flush()
            label = h["label"]
        else:
            block.append(line)
    flush()
    return out


def parse_source(text: str) -> list[Timing]:
    stripped = text.lstrip()
    if stripped.startswith("{"):
        doc = json.loads(text)
        if doc.get("version") != TIMES_VERSION:
            raise ValueError(f"fixture-times.json version {doc.get('version')!r}, want {TIMES_VERSION}")
        return [
            Timing(rel(r["pkg"]), int(r["part"]), int(r["parts"]), float(r["seconds"]))
            for r in doc["timings"]
        ]
    return parse_text(text)


def sample_weights(timings: list[Timing]) -> tuple[dict[str, float], list[str]]:
    """Per package: the summed seconds of its slices in one sample. Returns
    (weights, problems); a package missing a slice, or seen twice, is a problem
    and is left out rather than reported low."""
    by_pkg: dict[str, list[Timing]] = {}
    for t in timings:
        by_pkg.setdefault(t.pkg, []).append(t)
    weights, problems = {}, []
    for pkg, ts in sorted(by_pkg.items()):
        parts = {t.parts for t in ts}
        got = sorted(t.part for t in ts)
        if len(parts) != 1 or got != list(range(next(iter(parts)))):
            problems.append(f"{pkg}: slices {[t.unit for t in ts]} are not exactly one complete set")
            continue
        weights[pkg] = sum(t.seconds for t in ts)
    return weights, problems


STATS = {"median": statistics.median, "max": max}


def calibrate(
    samples: list[list[Timing]], previous: dict, stat: str = "median"
) -> tuple[dict, list[str]]:
    """The new weights document and the problems found. Packages in `previous`
    that no sample measured keep their weight and are marked estimated;
    measured packages not in `previous` are added."""
    per_pkg: dict[str, list[float]] = {}
    problems: list[str] = []
    for i, s in enumerate(samples, 1):
        w, probs = sample_weights(s)
        problems += [f"sample {i}: {p}" for p in probs]
        for pkg, secs in w.items():
            per_pkg.setdefault(pkg, []).append(secs)
    weights = dict(previous.get("weights", {}))
    measured = {}
    for pkg, vals in per_pkg.items():
        weights[pkg] = math.ceil(STATS[stat](vals))
        measured[pkg] = len(vals)
    estimated = sorted(p for p in weights if p not in measured)
    doc = {
        "version": 1,
        "stat": stat,
        "samples": len(samples),
        "measured_samples_per_package": dict(sorted(measured.items())),
        "estimated": estimated,
        "weights": dict(sorted(weights.items(), key=lambda kv: (-kv[1], kv[0]))),
    }
    return doc, problems


def read_inputs(paths: list[str]) -> list[Timing]:
    out: list[Timing] = []
    if not paths:
        out += parse_source(sys.stdin.read())
    for p in paths:
        out += parse_source(Path(p).read_text(encoding="utf-8", errors="replace"))
    return out


def load_weights(path: Path = WEIGHTS_FILE) -> dict:
    try:
        doc = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise SystemExit(f"fixture_shard_weights: cannot read {path.name}: {e}")
    w = doc.get("weights")
    if not isinstance(w, dict) or not w or not all(
        isinstance(v, (int, float)) and v > 0 for v in w.values()
    ):
        raise SystemExit(f"fixture_shard_weights: {path.name} has no valid positive 'weights' table")
    return doc


def main() -> None:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("files", nargs="*", help="inputs forming one sample (stdin if none and no --sample)")
    ap.add_argument(
        "--sample", action="append", nargs="+", default=[], metavar="FILE",
        help="the files of one pipeline (all its shards); repeat for more samples",
    )
    ap.add_argument("--weights", type=Path, default=WEIGHTS_FILE)
    ap.add_argument(
        "--stat", choices=sorted(STATS), default="median",
        help="how samples combine (default median; max sizes for the slowest agent seen)",
    )
    ap.add_argument("--dry-run", action="store_true", help="print the new table, do not write it")
    args = ap.parse_args()
    groups = [list(g) for g in args.sample]
    if args.files or not groups:
        groups.append(list(args.files))
    samples = [read_inputs(g) for g in groups]
    if not any(samples):
        raise SystemExit("fixture_shard_weights: no timings found in the inputs")
    previous = load_weights(args.weights) if args.weights.exists() else {"weights": {}}
    doc, problems = calibrate(samples, previous, args.stat)
    for p in problems:
        sys.stderr.write(f"::warning::{p}\n")
    text = json.dumps(doc, indent=2) + "\n"
    if args.dry_run:
        sys.stdout.write(text)
    else:
        args.weights.write_text(text, encoding="utf-8")
        sys.stderr.write(f"fixture_shard_weights: wrote {args.weights}\n")
    if doc["estimated"]:
        sys.stderr.write(
            f"fixture_shard_weights: {len(doc['estimated'])} package(s) not measured, kept as "
            f"estimates: {', '.join(doc['estimated'])}\n"
        )


if __name__ == "__main__":
    main()
