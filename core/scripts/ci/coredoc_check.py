#!/usr/bin/env python3
# file: core/scripts/ci/coredoc_check.py
# version: 1.0.0
# guid: d401ad56-d058-424b-9782-2a75c368d63b
# last-edited: 2026-11-08
"""SC6: every core contribution point is documented (12 §12.3 SC6; make -C core coredoc-check).

Runs the generator in check mode, ``go run ./internal/cmd/coredoc -check``,
which boots the coretest harness with zero modules and exits non-zero when the
generated reference under core/docs/reference/ differs from what core
registers. The generator is a later row's deliverable: until
core/internal/cmd/coredoc exists this reports NOT_APPLICABLE, which is not a
pass (the criterion is unmeasured) and not a failure. It cannot outlive its
reason: the moment the directory appears the real command runs.

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
GENERATOR = pathlib.Path("internal/cmd/coredoc")
TITLE = "Every core contribution point is documented"


def measure(core: pathlib.Path) -> lib.Result:
    if not (core / GENERATOR).is_dir():
        return lib.Result(
            "SC6", TITLE, lib.NOT_APPLICABLE,
            f"not applicable: the generator core/{GENERATOR.as_posix()} has not landed yet",
        )
    res = subprocess.run(
        ["go", "run", f"./{GENERATOR.as_posix()}", "-check"],
        cwd=core, env=lib.go_env(), capture_output=True, text=True, check=False,
    )
    tail = [ln for ln in (res.stdout + res.stderr).strip().splitlines() if ln][-20:]
    if res.returncode == 0:
        return lib.Result("SC6", TITLE, lib.PASS, "the generated reference matches what core registers", tail)
    return lib.Result("SC6", TITLE, lib.FAIL, f"coredoc -check exited {res.returncode}", tail)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--core", type=pathlib.Path, default=CORE_DIR, help="core module root (default: this script's module)")
    parser.add_argument("--out", type=pathlib.Path, help="write sc6.json here (make -C core readiness)")
    parser.add_argument("--report", action="store_true", help="report only: exit 0 on FAIL or ERROR")
    args = parser.parse_args(argv)
    return lib.finish(measure(args.core.resolve()), args.out, args.report)


if __name__ == "__main__":
    sys.exit(main())
