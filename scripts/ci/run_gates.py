#!/usr/bin/env python3
# file: scripts/ci/run_gates.py
# version: 1.1.0
# guid: a3c8e517-4b2d-4f90-8d16-7e5b9c0a2f43
# last-edited: 2026-10-12
"""Run the CI gates listed in ci/gates.txt, or lower their baselines.

One manifest (ci/gates.txt) names every gate once (07-G4, owner decision D47).
Ratchets fail only on a RISE; a bot (`--lower`, run by
.github/workflows/ratchet-lower.yml) lowers the baseline when the measured
count has fallen. `--lower` can only ever write a smaller number: a measured
value that is not strictly lower than the baseline is left alone.

Exit codes: 0 all gates passed (or --lower finished), 1 a gate failed,
2 manifest error or a measurement could not be taken.

  run_gates.py                 run every gate, print a table
  run_gates.py --list          print gate names
  run_gates.py --only a,b      run just these gates
  run_gates.py --skip a,b      run all but these gates
  run_gates.py --exclude-own-jobs   skip gates marked own-job in the manifest
  run_gates.py --lower         lower file baselines; hint at Go-constant ones
  run_gates.py --lower --report out.md   also write the PR-body notes
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

KINDS = {"ratchet", "ratchet-set", "ratchet-const", "check"}
GATE_TIMEOUT_S = 1800
JOBS = {"own-job", "shared"}
NOTE_RE = re.compile(r"NOTE \(not a failure\):.*")


class ManifestError(Exception):
    """ci/gates.txt is malformed (exit 2)."""


class MeasureError(Exception):
    """A baseline could not be measured or read (exit 2)."""


@dataclass(frozen=True)
class Gate:
    name: str
    command: str
    baseline: str
    kind: str
    lower: str
    own_job: bool = False


def repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def parse_manifest(text: str) -> list[Gate]:
    gates: list[Gate] = []
    seen: set[str] = set()
    for n, raw in enumerate(text.splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        cols = raw.split("\t")
        if len(cols) != 6 or any(not c.strip() for c in cols):
            raise ManifestError(f"line {n}: want 6 tab-separated columns, got {len(cols)}")
        name, command, baseline, kind, lower, job = (c.strip() for c in cols)
        if job not in JOBS:
            raise ManifestError(f"line {n}: job must be one of {sorted(JOBS)}, got {job!r}")
        if kind not in KINDS:
            raise ManifestError(f"line {n}: unknown kind {kind!r} (want one of {sorted(KINDS)})")
        if name in seen:
            raise ManifestError(f"line {n}: duplicate gate {name!r}")
        if kind in ("ratchet", "ratchet-set") and (baseline == "-" or lower == "-"):
            raise ManifestError(
                f"line {n}: {kind} gate {name!r} needs a baseline file and a lower command"
            )
        seen.add(name)
        gates.append(Gate(name, command, baseline, kind, lower, job == "own-job"))
    if not gates:
        raise ManifestError("no gates in manifest")
    return gates


def select(
    gates: list[Gate], only: str | None, skip: str | None, exclude_own_jobs: bool = False
) -> list[Gate]:
    names = {g.name for g in gates}
    for opt in (only, skip):
        for n in (opt or "").split(","):
            if n and n not in names:
                raise ManifestError(f"unknown gate {n!r}")
    if only:
        want = set(only.split(","))
        gates = [g for g in gates if g.name in want]
    if skip:
        drop = set(skip.split(","))
        gates = [g for g in gates if g.name not in drop]
    if exclude_own_jobs:
        gates = [g for g in gates if not g.own_job]
    return gates


# -- baselines ---------------------------------------------------------------


def _is_data(line: str) -> bool:
    s = line.strip()
    return bool(s) and not s.startswith("#")


def read_int_baseline(text: str) -> int:
    for line in text.splitlines():
        if _is_data(line):
            s = line.strip()
            if not s.isdigit():
                raise MeasureError(f"baseline is not a bare integer: {s!r}")
            return int(s)
    raise MeasureError("baseline file has no integer line")


def lowered_int_baseline(text: str, measured: int) -> str | None:
    """Return text with its integer line set to `measured`, or None when
    measured is not strictly lower. Never returns a higher number."""
    current = read_int_baseline(text)
    if measured >= current:
        return None
    out, done = [], False
    for line in text.splitlines(keepends=True):
        if not done and _is_data(line):
            out.append(f"{measured}\n")
            done = True
        else:
            out.append(line)
    new = "".join(out)
    assert read_int_baseline(new) < current, "lowering must lower"
    return new


def read_set(text: str) -> set[str]:
    return {line.strip() for line in text.splitlines() if _is_data(line)}


# -- running -----------------------------------------------------------------


def sh(root: Path, command: str, capture: bool) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", "-c", command],
        cwd=root,
        text=True,
        capture_output=capture,
        timeout=GATE_TIMEOUT_S,
        check=False,
    )


def run_all(root: Path, gates: list[Gate]) -> int:
    rows: list[tuple[str, str, float]] = []
    rc = 0
    for g in gates:
        print(f"::group::gate {g.name}: {g.command}", flush=True)
        t0 = time.monotonic()
        try:
            res = sh(root, g.command, capture=False)
            status = "PASS" if res.returncode == 0 else f"FAIL({res.returncode})"
        except subprocess.TimeoutExpired:
            status = "TIMEOUT"
            res = None
        print("::endgroup::", flush=True)
        if status != "PASS":
            rc = 1
        rows.append((g.name, status, time.monotonic() - t0))
    w = max(len(r[0]) for r in rows)
    print()
    for name, status, secs in rows:
        print(f"{name:<{w}}  {status:<10} {secs:7.1f}s")
    return rc


def measure_int(root: Path, g: Gate) -> int:
    res = sh(root, g.lower, capture=True)
    if res.returncode != 0:
        raise MeasureError(
            f"{g.name}: measure exited {res.returncode}: {res.stderr.strip()[-400:]}"
        )
    out = res.stdout.strip()
    if not out.isdigit():
        raise MeasureError(f"{g.name}: measure printed {out[-80:]!r}, not a bare integer")
    return int(out)


def lower_all(root: Path, gates: list[Gate], report: Path | None) -> int:
    lowered: list[str] = []
    hints: list[str] = []
    errors: list[str] = []
    for g in gates:
        try:
            if g.kind == "ratchet":
                path = root / g.baseline
                measured = measure_int(root, g)
                new = lowered_int_baseline(path.read_text(), measured)
                if new is None:
                    print(
                        f"{g.name}: measured {measured}, baseline {read_int_baseline(path.read_text())}; unchanged"
                    )
                    continue
                old = read_int_baseline(path.read_text())
                path.write_text(new)
                lowered.append(f"`{g.baseline}`: {old} -> {measured} ({g.name})")
                print(f"{g.name}: lowered {old} -> {measured}")
            elif g.kind == "ratchet-set":
                path = root / g.baseline
                before = read_set(path.read_text())
                original = path.read_text()
                res = sh(root, g.lower, capture=True)
                if res.returncode != 0:
                    raise MeasureError(
                        f"{g.name}: prune exited {res.returncode}: {res.stderr.strip()[-400:]}"
                    )
                after = read_set(path.read_text())
                if not after <= before:
                    path.write_text(original)  # a prune that adds is refused, not kept
                    raise MeasureError(
                        f"{g.name}: prune ADDED {len(after - before)} entries; reverted"
                    )
                if after != before:
                    lowered.append(
                        f"`{g.baseline}`: {len(before)} -> {len(after)} entries ({g.name})"
                    )
                print(f"{g.name}: {len(before)} -> {len(after)} entries")
            elif g.kind == "ratchet-const":
                res = sh(root, g.command.replace("go test", "go test -v", 1), capture=True)
                if res.returncode != 0:
                    raise MeasureError(
                        f"{g.name}: gate itself fails (exit {res.returncode}); not a lowering candidate"
                    )
                for note in NOTE_RE.findall(res.stdout):
                    hints.append(f"`{g.baseline}` ({g.name}): {note}")
        except MeasureError as e:
            print(f"::error::{e}", file=sys.stderr)
            errors.append(str(e))
    if report is not None:
        lines = ["Lowered baselines (a one-way ratchet: these only ever go down):", ""]
        lines += [f"- {x}" for x in lowered] or ["- none"]
        lines += [
            "",
            "Go-constant baselines the bot never edits; lower by hand in the PR that caused the drop:",
            "",
        ]
        lines += [f"- {x}" for x in hints] or ["- none"]
        if errors:
            lines += ["", "Could not measure:", ""] + [f"- {x}" for x in errors]
        report.write_text("\n".join(lines) + "\n")
    return 2 if errors else 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--manifest", default="ci/gates.txt")
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--lower", action="store_true")
    ap.add_argument("--report", type=Path)
    ap.add_argument("--only")
    ap.add_argument("--skip")
    ap.add_argument(
        "--exclude-own-jobs",
        action="store_true",
        help="skip gates the manifest marks own-job (they run in their own CI job)",
    )
    args = ap.parse_args(argv)
    root = repo_root()
    try:
        gates = select(
            parse_manifest((root / args.manifest).read_text()),
            args.only,
            args.skip,
            args.exclude_own_jobs,
        )
    except (ManifestError, OSError) as e:
        print(f"run_gates: manifest error: {e}", file=sys.stderr)
        return 2
    if args.list:
        print("\n".join(g.name for g in gates))
        return 0
    if args.lower:
        return lower_all(root, gates, args.report)
    return run_all(root, gates)


if __name__ == "__main__":
    sys.exit(main())
