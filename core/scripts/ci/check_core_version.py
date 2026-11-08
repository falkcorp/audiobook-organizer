#!/usr/bin/env python3
# file: core/scripts/ci/check_core_version.py
# version: 1.0.0
# guid: dc7979e1-0185-4f56-950e-e2411b36e198
# last-edited: 2026-11-08
"""Keep core's version constant honest (12 §12.4.3; make -C core version-check).

Three checks, over what core itself holds:

  1. version.Version >= the newest refs/core-checkpoints/v0.* (fetched
     explicitly, 12 §12.4.1; none recorded yet passes). >=, not ==: the one
     "core version" PR raises the constant above the newest checkpoint before
     the release that records the next one. In an export of core/ alone (SC4(a))
     there is no remote, so the checkpoint half reports "not applicable".
  2. hello's Manifest.MinCoreVersion <= version.Version (empty means no floor).
     Books' own MinCoreVersion is a root-side test in internal/books: a
     core-owned script cannot read an app's manifest.
  3. npm lockstep: every core/web/packages/*/package.json "version" equals
     version.Version, so the shell's reported version (07 §7.5.2) is the Go
     constant's; the in-repo file: dependencies make the usual lockstep check a no-op.

Exit 0 when every check holds, 1 when one fails. A checkpoint fetch that fails
is a failure, never "no checkpoints": pass --no-fetch to work offline.

Stdlib only.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import core_ci_lib as lib  # noqa: E402

HELLO_GO = pathlib.Path("examples/hello/hello.go")
_MIN_CORE_RE = re.compile(r"MinCoreVersion\s*:\s*(.*?)\s*,?\s*(?://.*)?$", re.M)
_STRING_LITERAL_RE = re.compile(r'^"([^"]*)"$')


def check_checkpoints(
    core: pathlib.Path, version: lib.Semver, fetch: bool, remote: str
) -> tuple[list[str], list[str]]:
    errors: list[str] = []
    notes: list[str] = []
    if not lib.is_git_repo(core):
        return errors, ["checkpoints: not applicable (not a git work tree)"]
    if fetch and lib.has_remote(core, remote):
        try:
            lib.fetch_checkpoints(core, remote)
        except RuntimeError as e:
            return [
                f"checkpoints could not be fetched: {e} (rerun, or pass --no-fetch offline)"
            ], notes
    elif fetch:
        notes.append(f"checkpoints: no '{remote}' remote, nothing fetched")
    try:
        found = lib.list_checkpoints(core)
    except ValueError as e:
        return [str(e)], notes
    if not found:
        notes.append("checkpoints: none recorded yet (passes)")
        return errors, notes
    name, newest = found[0]
    if version < newest:
        errors.append(
            f"version.Version {version} is below the newest checkpoint {name} ({newest}); "
            "the constant must stay at or above it (12 §12.4.3)"
        )
    else:
        notes.append(f"version.Version {version} >= newest checkpoint {newest}")
    return errors, notes


def check_hello_floor(core: pathlib.Path, version: lib.Semver) -> tuple[list[str], list[str]]:
    path = core / HELLO_GO
    if not path.is_file():
        return [f"{HELLO_GO}: missing, so hello's MinCoreVersion cannot be checked"], []
    values = _MIN_CORE_RE.findall(path.read_text(encoding="utf-8"))
    if not values:
        return [], [f"{HELLO_GO}: no MinCoreVersion (no floor)"]
    errors: list[str] = []
    notes: list[str] = []
    for raw in values:
        m = _STRING_LITERAL_RE.match(raw)
        if not m:
            errors.append(
                f"{HELLO_GO}: MinCoreVersion is {raw!r}, not a string literal; this check reads the literal"
            )
            continue
        if m.group(1) == "":
            notes.append(f"{HELLO_GO}: MinCoreVersion is empty (no floor)")
            continue
        try:
            floor = lib.parse_semver(m.group(1))
        except ValueError as e:
            errors.append(f"{HELLO_GO}: MinCoreVersion {e}")
            continue
        if floor > version:
            errors.append(
                f"{HELLO_GO}: MinCoreVersion {floor} is above version.Version {version}; "
                "the kernel would refuse to boot hello (ErrCoreTooOld)"
            )
        else:
            notes.append(f"hello MinCoreVersion {floor} <= version.Version {version}")
    return errors, notes


def check_npm_lockstep(core: pathlib.Path, version: lib.Semver) -> tuple[list[str], list[str]]:
    web = core / "web"
    if not (web / "package.json").is_file():
        return [], ["npm lockstep: not applicable (no core/web/package.json)"]
    manifests = sorted(web.glob("packages/*/package.json"))
    if not manifests:
        return [
            "npm lockstep: core/web/packages/*/package.json matched nothing; the check would be vacuous"
        ], []
    errors: list[str] = []
    for path in manifests:
        rel = f"core/{path.relative_to(core).as_posix()}"
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as e:
            errors.append(f"{rel}: not valid JSON: {e}")
            continue
        got = data.get("version")
        if got != str(version):
            errors.append(
                f"{rel}: version {got!r} != version.Version {str(version)!r} "
                "(npm packages are versioned in lockstep with the Go module, 12 §12.4.2)"
            )
    notes = [f"npm lockstep: {len(manifests)} package(s) checked against {version}"]
    return errors, notes


def check(
    core: pathlib.Path, fetch: bool = True, remote: str = "origin"
) -> tuple[list[str], list[str]]:
    try:
        version = lib.parse_semver(lib.read_version(core))
    except ValueError as e:
        return [f"version.Version: {e}"], []
    errors: list[str] = []
    notes = [f"version.Version = {version}"]
    for e, n in (
        check_checkpoints(core, version, fetch, remote),
        check_hello_floor(core, version),
        check_npm_lockstep(core, version),
    ):
        errors += e
        notes += n
    return errors, notes


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--core",
        type=pathlib.Path,
        default=pathlib.Path(__file__).resolve().parents[2],
        help="core module root (default: this script's module)",
    )
    parser.add_argument(
        "--no-fetch", action="store_true", help="do not fetch refs/core-checkpoints/* first"
    )
    parser.add_argument(
        "--remote", default="origin", help="remote holding the checkpoints (default origin)"
    )
    args = parser.parse_args(argv)
    errors, notes = check(args.core.resolve(), fetch=not args.no_fetch, remote=args.remote)
    for n in notes:
        print(f"  {n}")
    for e in errors:
        print(f"::error::{e}")
    if errors:
        print(f"core version: {len(errors)} problem(s)")
        return 1
    print("core version consistent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
