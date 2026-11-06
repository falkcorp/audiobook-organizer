#!/usr/bin/env python3
# file: core/scripts/ci/check_genproto_exclude.py
# version: 1.0.0
# guid: 48375226-bc0f-48cc-a72d-c365f2e7c9e9
# last-edited: 2026-11-05
"""The genproto exclude check, core mode (03 §3.2 item 1; make -C core exclude-check).

cockroachdb/errors v1.14.0 (the latest release) still requires the monolithic
google.golang.org/genproto, which workspace mode loads beside the split
genproto/googleapis/{api,rpc} modules: ``go build`` then fails with
``ambiguous import``. core/go.mod carries the one exclude of that version, and
no other go.mod may carry one. The check fails unless all of these hold:

  present         core/go.mod carries exactly the exclude line and no other
                  go.mod under core/ carries a genproto exclude (else
                  "missing" or "misplaced");
  green with it   the core-mode workspace probe builds:
                  (cd core && go work init . && go build ./...);
  load-bearing    after go mod edit -dropexclude in the copy, the probe's
  without it      go mod graph lists <requirer> genproto@<version> AND its
                  go build fails with "ambiguous import". A probe whose graph
                  does not reach the requirer is "not applicable" (reported,
                  not failed); one whose graph reaches it but builds green is
                  "stale" (failed, naming the requirer).

Probing the live graph cannot work (an excluded version never appears in
go mod graph), so every probe runs on a copy made under a temporary directory
outside the repository and every worktree, never in the checkout: CLAUDE.md
forbids go work init there.

The root mode (the root workspace probe and the tree-wide scan) is the root
job scripts/ci/genproto_exclude_root.py, which imports this module's helpers.
This script reads nothing above core/, so it runs from an export of core/
alone (12 SC4(a)).

Stdlib only.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

GENPROTO = "google.golang.org/genproto"
GENPROTO_VERSION = "v0.0.0-20230410155749-daa745c078e1"
EXCLUDE_LINE = f"exclude {GENPROTO} {GENPROTO_VERSION}"
# Directories a probe copy never needs: VCS data, npm trees, build output.
COPY_IGNORE = (".git", "node_modules", ".worktrees", "dist", "bin", "coverage", ".ci")


class CheckError(Exception):
    """The check failed; the message says which rule."""


def go_env(core_dir: pathlib.Path) -> dict[str, str]:
    """The environment for go commands: GOTOOLCHAIN from the core pin when
    unset (read through make -s -C core print-toolchain, never a literal),
    GOFLAGS cleared so a caller's -mod=... cannot change the probe, and GOWORK
    left to the probe to set."""
    env = dict(os.environ)
    env.pop("GOFLAGS", None)
    env.pop("GOWORK", None)
    if not env.get("GOTOOLCHAIN"):
        pin = subprocess.run(
            ["make", "-s", "-C", str(core_dir), "print-toolchain"],
            capture_output=True,
            text=True,
            check=False,
        )
        if pin.returncode == 0 and pin.stdout.strip():
            env["GOTOOLCHAIN"] = pin.stdout.strip()
    return env


def gomod_excludes(gomod: pathlib.Path, env: dict[str, str]) -> list[tuple[str, str]]:
    """The (path, version) exclude directives of one go.mod."""
    out = subprocess.run(
        ["go", "mod", "edit", "-json", str(gomod)],
        capture_output=True,
        text=True,
        env=env,
        check=False,
    )
    if out.returncode != 0:
        raise CheckError(f"go mod edit -json {gomod}: {out.stderr.strip()}")
    data = json.loads(out.stdout)
    return [(e["Path"], e["Version"]) for e in data.get("Exclude") or []]


def find_gomods(tree: pathlib.Path) -> list[pathlib.Path]:
    """Every go.mod under tree, skipping npm trees, VCS data and testdata."""
    found = []
    for dirpath, dirnames, filenames in os.walk(tree):
        dirnames[:] = [
            d for d in dirnames if d not in (".git", "node_modules", "testdata", ".worktrees")
        ]
        if "go.mod" in filenames:
            found.append(pathlib.Path(dirpath) / "go.mod")
    return sorted(found)


def scan_present(tree: pathlib.Path, owner: pathlib.Path, env: dict[str, str]) -> None:
    """present/misplaced: owner carries exactly the exclude line; no other
    go.mod under tree carries a genproto exclude."""
    text = owner.read_text(encoding="utf-8")
    lines = [ln.strip() for ln in text.splitlines()]
    if EXCLUDE_LINE not in lines:
        raise CheckError(f"missing: {owner} does not carry '{EXCLUDE_LINE}'")
    genproto = [e for e in gomod_excludes(owner, env) if e[0] == GENPROTO]
    if genproto != [(GENPROTO, GENPROTO_VERSION)]:
        raise CheckError(f"{owner} genproto excludes are {genproto}, want exactly {GENPROTO_VERSION}")
    for gm in find_gomods(tree):
        if gm.resolve() == owner.resolve():
            continue
        if any(p == GENPROTO for p, _ in gomod_excludes(gm, env)):
            raise CheckError(
                f"misplaced: {gm} carries a genproto exclude; it lives only in core/go.mod"
            )


def copy_tree(src: pathlib.Path, dst: pathlib.Path, extra_ignore: tuple[str, ...] = ()) -> None:
    """Copy src to dst for a probe, skipping what no Go build reads."""
    names = set(COPY_IGNORE) | set(extra_ignore)
    shutil.copytree(src, dst, ignore=shutil.ignore_patterns(*names), symlinks=True)


def run(cmd: list[str], cwd: pathlib.Path, env: dict[str, str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, check=False)


def probe(
    copy_root: pathlib.Path,
    members: list[str],
    build: list[str],
    exclude_owner: pathlib.Path,
    env: dict[str, str],
    label: str,
) -> str:
    """Run one workspace probe in copy_root (a throwaway copy). Returns the
    verdict text; raises CheckError when the probe is red with the exclude or
    stale without it."""
    wenv = dict(env)
    wenv["GOWORK"] = str(copy_root / "go.work")
    init = run(["go", "work", "init", *members], copy_root, wenv)
    if init.returncode != 0:
        raise CheckError(f"{label}: go work init failed in the probe copy: {init.stderr.strip()}")
    with_it = run(["go", "build", *build], copy_root, wenv)
    if with_it.returncode != 0:
        raise CheckError(
            f"{label}: red with the exclude (go build {' '.join(build)}):\n{with_it.stderr.strip()}"
        )
    drop = run(
        ["go", "mod", "edit", f"-dropexclude={GENPROTO}@{GENPROTO_VERSION}", str(exclude_owner)],
        copy_root,
        env,
    )
    if drop.returncode != 0:
        raise CheckError(f"{label}: dropexclude failed: {drop.stderr.strip()}")
    graph = run(["go", "mod", "graph"], copy_root, wenv)
    if graph.returncode != 0:
        raise CheckError(f"{label}: go mod graph failed without the exclude: {graph.stderr.strip()}")
    target = f"{GENPROTO}@{GENPROTO_VERSION}"
    requirers = sorted(
        {ln.split()[0] for ln in graph.stdout.splitlines() if len(ln.split()) == 2 and ln.split()[1] == target}
    )
    if not requirers:
        return f"{label}: not applicable (the probe's graph does not reach {target})"
    without = run(["go", "build", *build], copy_root, wenv)
    if without.returncode != 0 and "ambiguous import" in without.stderr:
        return f"{label}: load-bearing without it (requirer {', '.join(requirers)})"
    raise CheckError(
        f"{label}: stale: the graph still lists {', '.join(requirers)} -> {target} but go build "
        f"{'succeeds' if without.returncode == 0 else 'fails for another reason'} without the exclude:\n"
        f"{without.stderr.strip()}"
    )


def core_mode(core_dir: pathlib.Path) -> list[str]:
    """present/misplaced over core/ and the core-mode probe."""
    env = go_env(core_dir)
    scan_present(core_dir, core_dir / "go.mod", env)
    lines = ["present (core/go.mod; no other go.mod under core/ carries one)"]
    with tempfile.TemporaryDirectory(prefix="genproto-core-") as tmp:
        copy = pathlib.Path(tmp) / "core"
        copy_tree(core_dir, copy, extra_ignore=("web",))
        lines.append("green with it")
        lines.append(probe(copy, ["."], ["./..."], copy / "go.mod", env, "core probe"))
    return lines


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--core",
        type=pathlib.Path,
        default=pathlib.Path(__file__).resolve().parents[2],
        help="core module root (default: this script's module)",
    )
    args = parser.parse_args(argv)
    try:
        lines = core_mode(args.core.resolve())
    except CheckError as e:
        print(f"genproto exclude check (core mode) FAILED: {e}")
        return 1
    print("genproto exclude check (core mode): " + "; ".join(lines))
    return 0


if __name__ == "__main__":
    sys.exit(main())
