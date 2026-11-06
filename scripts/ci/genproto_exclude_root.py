#!/usr/bin/env python3
# file: scripts/ci/genproto_exclude_root.py
# version: 1.0.0
# guid: c14b5e8a-8154-49c6-829c-954ca9824bd9
# last-edited: 2026-11-05
"""The genproto exclude check, root mode (03 §3.2 item 1; make genproto-exclude-root).

The root-side half of the check whose core half is
core/scripts/ci/check_genproto_exclude.py (make -C core exclude-check):

  present         core/go.mod carries exactly the exclude line, and no other
                  go.mod in the tree (the root's, tools/*, core/tools/*)
                  carries a genproto exclude ("missing" / "misplaced");
  green with it   the root workspace probe builds in a throwaway copy:
                  go work init . ./core && go build ./... ./core/...;
  load-bearing    after go mod edit -dropexclude on the copy's core/go.mod,
  without it      the probe's go mod graph lists <requirer> genproto@<version>
                  and its build fails with "ambiguous import". Otherwise the
                  exclude is stale and the check fails, naming the requirer.

The copy is made under a temporary directory outside the repository; nothing
runs go work init in the checkout (CLAUDE.md). It holds the working tree as
it is (uncommitted edits included) minus VCS data, npm trees and docs. The
probe builds the root graph twice in workspace mode; expect minutes on a cold
build cache.

A root-side job: cutover step 6 of the repo split drops it (12 §12.8.2). It
reuses the core script's helpers rather than a second copy of the rules.
"""

from __future__ import annotations

import importlib.util
import pathlib
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
CORE_SCRIPT = ROOT / "core" / "scripts" / "ci" / "check_genproto_exclude.py"


def _core_helpers():
    spec = importlib.util.spec_from_file_location("check_genproto_exclude", CORE_SCRIPT)
    if spec is None or spec.loader is None:
        raise SystemExit(f"cannot load {CORE_SCRIPT}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main() -> int:
    g = _core_helpers()
    core = ROOT / "core"
    env = g.go_env(core)
    try:
        if any(p == g.GENPROTO for p, _ in g.gomod_excludes(ROOT / "go.mod", env)):
            raise g.CheckError(
                "misplaced: the root go.mod carries a genproto exclude; it lives only in core/go.mod"
            )
        g.scan_present(ROOT, core / "go.mod", env)
        verdicts = ["present", "green with it"]
        with tempfile.TemporaryDirectory(prefix="genproto-root-") as tmp:
            copy = pathlib.Path(tmp) / "repo"
            # web/ is ignored by go.mod and holds no Go the default build needs.
            g.copy_tree(ROOT, copy, extra_ignore=("web", "docs", ".claude"))
            verdict = g.probe(
                copy,
                [".", "./core"],
                ["./...", "./core/..."],
                copy / "core" / "go.mod",
                env,
                "root probe",
            )
        if "load-bearing" not in verdict:
            raise g.CheckError(
                f"stale: {verdict}; the root probe must reach the requirer while ABO requires cockroachdb/errors"
            )
        verdicts.append(verdict.split(": ", 1)[1])
    except g.CheckError as e:
        print(f"genproto-exclude-root FAILED: {e}")
        return 1
    print("; ".join(verdicts))
    return 0


if __name__ == "__main__":
    sys.exit(main())
