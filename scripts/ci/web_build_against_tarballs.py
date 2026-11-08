#!/usr/bin/env python3
# file: scripts/ci/web_build_against_tarballs.py
# version: 1.0.0
# guid: 2f8759bd-3b1f-4b57-a730-0ec3fad10051
# last-edited: 2026-11-08
"""SC8: ABO's web/ builds against the packed @mediacore/* tarballs (12 §12.3 SC8).

The npm packages are packed on their own (``npm pack`` of each core/web
workspace, in 07 §7.3.2's build order); this script then proves web/ needs
nothing from the repository tree around it: it copies web/ to a temp directory,
rewrites every @mediacore/* dependency from its in-repo ``file:`` directory to
``file:<tarball>``, installs and runs ``npm run build``.

The install is ``npm install``, not ``npm ci``: the committed lockfile records
the ``file:`` directory links, so a manifest rewritten to tarballs no longer
matches it and ``npm ci`` would refuse. ``npm install`` reconciles the lock in
the temp copy and keeps every other locked version, so the build still sees
the committed third-party versions. Nothing is written back to web/.

Pre-split only: after the split the app pins published versions and cutover
step 6 drops or rewrites this script.

Usage: web_build_against_tarballs.py --tarballs DIR   (DIR from ``npm pack --pack-destination``)
Stdlib only; needs node and npm.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
from typing import Callable

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "core" / "scripts" / "ci"))
import core_ci_lib as lib  # noqa: E402

TITLE = "npm packages build and pack standalone"
SCOPE = "@mediacore/"
DEP_SECTIONS = ("dependencies", "devDependencies", "optionalDependencies", "peerDependencies")
# Not copied into the temp build: installs and build outputs of the checkout.
IGNORED = shutil.ignore_patterns("node_modules", "dist", "*.tsbuildinfo", "coverage", "test-results", "playwright-report")

Runner = Callable[[list[str], pathlib.Path], tuple[int, str]]


def index_tarballs(directory: pathlib.Path) -> dict[str, pathlib.Path]:
    """{package name: tarball} for every .tgz in directory, read from its package/package.json."""
    found: dict[str, pathlib.Path] = {}
    for tgz in sorted(directory.glob("*.tgz")):
        with tarfile.open(tgz, "r:gz") as archive:
            member = archive.extractfile("package/package.json")
            if member is None:
                raise ValueError(f"{tgz.name}: no package/package.json")
            name = json.load(member).get("name", "")
        if name in found:
            raise ValueError(f"{tgz.name} and {found[name].name} are both {name}")
        found[name] = tgz.resolve()
    return found


def rewrite_manifest(manifest: dict, tarballs: dict[str, pathlib.Path]) -> list[str]:
    """Point every @mediacore/* dependency at its tarball, in place; returns what was rewritten.

    A @mediacore/* dependency with no tarball raises ValueError: building against
    the in-repo directory instead would defeat the check.
    """
    rewritten: list[str] = []
    for section in DEP_SECTIONS:
        for name in sorted(manifest.get(section, {})):
            if not name.startswith(SCOPE):
                continue
            if name not in tarballs:
                raise ValueError(f"web/package.json {section} needs {name}, but no tarball of it was packed")
            manifest[section][name] = f"file:{tarballs[name]}"
            rewritten.append(f"{section}.{name}")
    return rewritten


def run_command(argv: list[str], cwd: pathlib.Path) -> tuple[int, str]:
    res = subprocess.run(argv, cwd=cwd, capture_output=True, text=True, check=False)
    return res.returncode, (res.stdout + res.stderr).strip()


def build(web: pathlib.Path, tarballs_dir: pathlib.Path, workdir: pathlib.Path, run: Runner = run_command) -> lib.Result:
    if not (web / "package.json").is_file():
        return lib.Result("SC8", TITLE, lib.ERROR, f"{web}/package.json is missing")
    if not tarballs_dir.is_dir():
        return lib.Result("SC8", TITLE, lib.ERROR, f"{tarballs_dir} is not a directory (npm pack --pack-destination)")
    tarballs = index_tarballs(tarballs_dir)
    copy = workdir / "web"
    shutil.copytree(web, copy, ignore=IGNORED)
    manifest = json.loads((copy / "package.json").read_text(encoding="utf-8"))
    details = [f"tarballs: {', '.join(sorted(tarballs)) or 'none'}"]
    try:
        rewritten = rewrite_manifest(manifest, tarballs)
    except ValueError as e:
        return lib.Result("SC8", TITLE, lib.FAIL, str(e), details)
    if not rewritten:
        return lib.Result("SC8", TITLE, lib.FAIL, "web/package.json depends on no @mediacore/* package; the check would be vacuous", details)
    details.append(f"rewritten to tarballs: {', '.join(rewritten)}")
    (copy / "package.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    for name, argv in (
        ("npm install", ["npm", "install", "--no-audit", "--no-fund"]),
        ("npm run build", ["npm", "run", "build"]),
    ):
        code, output = run(argv, copy)
        details.append(f"{name}: " + ("ok" if code == 0 else f"exit {code}"))
        if code != 0:
            details += [f"    {line}" for line in output.splitlines()[-20:]]
            return lib.Result("SC8", TITLE, lib.FAIL, f"{name} failed against the packed tarballs", details)
    return lib.Result("SC8", TITLE, lib.PASS, f"web/ builds against {len(rewritten)} packed @mediacore/* dependency(ies)", details)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--tarballs", type=pathlib.Path, required=True, help="directory of npm pack output")
    parser.add_argument("--web", type=pathlib.Path, default=REPO_ROOT / "web", help="the app's web/ (default: this checkout's)")
    parser.add_argument("--keep", action="store_true", help="keep the temp copy (its path is printed)")
    parser.add_argument("--out", type=pathlib.Path, help="write sc8.json here (split-readiness.yml)")
    parser.add_argument("--report", action="store_true", help="report only: exit 0 on FAIL or ERROR")
    args = parser.parse_args(argv)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix="web-against-tarballs-"))
    try:
        result = build(args.web.resolve(), args.tarballs.resolve(), workdir)
    except (OSError, ValueError, tarfile.TarError, json.JSONDecodeError) as e:
        result = lib.Result("SC8", TITLE, lib.ERROR, str(e))
    finally:
        if args.keep:
            print(f"kept {workdir}")
        else:
            shutil.rmtree(workdir, ignore_errors=True)
    return lib.finish(result, args.out, args.report)


if __name__ == "__main__":
    sys.exit(main())
