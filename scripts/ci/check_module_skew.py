#!/usr/bin/env python3
# file: scripts/ci/check_module_skew.py
# version: 1.1.0
# guid: 2ca4a2e7-5f0f-49c6-a5de-0c94f8160845
# last-edited: 2026-11-07
"""Fail when core and the root resolve a module to different versions (03 §3.2 item 6; make module-skew).

The root build takes the MVS maximum over the root's and core/go.mod's
requirements, while core CI tests only core's own, so the binary could ship a
dependency version core CI never ran. This blocking root-side check (12 SC4(c))
carries three guarantees:

  1. no skew: every module that supplies a package to core's build or tests
     (the modules of ``(cd core && go list -deps -test ./...)``, unioned over
     linux, darwin and windows) resolves, at its version in core's
     ``go list -m all``, to the same version in the root's ``go list -m all``
     (and is present there); once core/tools/staticcheck exists, the whole
     ``go list -m all`` of it is compared with tools/staticcheck, so the two
     staticcheck tools modules stay in step;
  2. core/go.sum exists (it is committed empty while core requires nothing,
     03 §3.2 item 4, so ``COPY core/go.mod core/go.sum`` never fails);
  3. every ``--pin <path>@<version>`` holds in both modules.

The pins are the two exact pins of 13 §13.5.2 once core requires those
modules; while core has no requirements none is needed and none is passed.

Why packages and not ``go list -m all`` (R1-10, amending 03 §3.2 item 6):
core's pruned module graph also lists modules that supply no package to
core (testify's go-spew, x/tools, ...). ``go mod tidy`` deletes any require
that would raise them to the root's versions, so comparing them made this
check and ``make -C core ci``'s tidy-check impossible to pass together. Such
a module cannot change what core CI runs, so it cannot break the guarantee
above; it is skipped.

A root-side job: cutover step 6 of the repo split drops it (12 §12.8.2).
Stdlib only.
"""

from __future__ import annotations

import argparse
import os
import pathlib
import subprocess
import sys
from typing import Callable

ROOT = pathlib.Path(__file__).resolve().parents[2]

Lister = Callable[[pathlib.Path], dict[str, str]]
Supplier = Callable[[pathlib.Path], set[str]]

# Package-supplying modules are unioned over these, so a module that serves
# core only on one platform is still compared.
SUPPLIER_GOOS = ("linux", "darwin", "windows")


def parse_list_m(text: str) -> dict[str, str]:
    """Parse ``go list -m all``: {path: version}, the main module (no version)
    skipped, a replaced module keyed by its path with the version it resolves
    to (``path v => replacement [rv]``: rv when given, else v)."""
    out: dict[str, str] = {}
    for line in text.splitlines():
        parts = line.split()
        if len(parts) < 2:
            continue
        path, version = parts[0], parts[1]
        if "=>" in parts:
            i = parts.index("=>")
            rest = parts[i + 1 :]
            if len(rest) == 2:  # path v => repl rv
                version = rest[1]
            elif i == 1:  # path => ./dir (no version)
                version = rest[0]
        out[path] = version
    return out


def go_env() -> dict[str, str]:
    env = dict(os.environ)
    env.pop("GOFLAGS", None)
    env.pop("GOWORK", None)
    env["GOWORK"] = "off"
    if not env.get("GOTOOLCHAIN"):
        pin = subprocess.run(
            ["make", "-s", "-C", str(ROOT / "core"), "print-toolchain"],
            capture_output=True,
            text=True,
            check=False,
        )
        if pin.returncode == 0 and pin.stdout.strip():
            env["GOTOOLCHAIN"] = pin.stdout.strip()
    return env


def go_list_modules(module_dir: pathlib.Path) -> dict[str, str]:
    res = subprocess.run(
        ["go", "list", "-m", "all"],
        cwd=module_dir,
        env=go_env(),
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode != 0:
        raise RuntimeError(f"go list -m all in {module_dir}: {res.stderr.strip()}")
    return parse_list_m(res.stdout)


def go_package_modules(module_dir: pathlib.Path) -> set[str]:
    """The module paths that supply a package to ``go list -deps -test ./...``
    in module_dir, for each GOOS in SUPPLIER_GOOS (standard library excluded)."""
    out: set[str] = set()
    for goos in SUPPLIER_GOOS:
        env = go_env()
        env["GOOS"] = goos
        res = subprocess.run(
            ["go", "list", "-deps", "-test", "-f", "{{with .Module}}{{.Path}}{{end}}", "./..."],
            cwd=module_dir,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if res.returncode != 0:
            raise RuntimeError(
                f"GOOS={goos} go list -deps -test ./... in {module_dir}: {res.stderr.strip()}"
            )
        out.update(line.strip() for line in res.stdout.splitlines() if line.strip())
    return out


def skew(core: dict[str, str], root: dict[str, str], label: str) -> list[str]:
    errors = []
    for path in sorted(core):
        if path not in root:
            errors.append(
                f"{label}: {path}@{core[path]} is in core's build list but not the root's (run the root go mod tidy)"
            )
        elif root[path] != core[path]:
            errors.append(
                f"{label}: {path} resolves to {core[path]} in core but {root[path]} in the root"
            )
    return errors


def parse_pin(spec: str) -> tuple[str, str]:
    path, sep, version = spec.rpartition("@")
    if not sep or not path or not version:
        raise argparse.ArgumentTypeError(f"--pin wants <path>@<version>, got {spec!r}")
    return path, version


def check(
    root: pathlib.Path,
    pins: list[tuple[str, str]],
    lister: Lister = go_list_modules,
    supplier: Supplier = go_package_modules,
) -> list[str]:
    errors: list[str] = []
    core_dir = root / "core"
    if not (core_dir / "go.sum").is_file():
        errors.append(
            "core/go.sum is missing: commit it (empty while core requires nothing, 03 §3.2 item 4)"
        )
    core_mods = lister(core_dir)
    root_mods = lister(root)
    core_mods.pop("github.com/falkcorp/mediacore", None)
    supplying = supplier(core_dir)
    errors += skew(
        {p: v for p, v in core_mods.items() if p in supplying}, root_mods, "module skew"
    )
    for path, version in pins:
        for name, mods in (("core", core_mods), ("root", root_mods)):
            got = mods.get(path)
            if got != version:
                errors.append(
                    f"--pin {path}@{version}: {name} resolves {path} to {got or 'nothing'}"
                )
    core_sc, root_sc = core_dir / "tools" / "staticcheck", root / "tools" / "staticcheck"
    if (core_sc / "go.mod").is_file():
        errors += skew(lister(core_sc), lister(root_sc), "staticcheck tools skew")
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__.splitlines()[0],
        epilog="No --pin is needed while core has no requirements; 13 §13.5.2's two exact pins are passed once core requires them.",
    )
    parser.add_argument(
        "--root", type=pathlib.Path, default=ROOT, help="repository root (default: this repo)"
    )
    parser.add_argument(
        "--pin",
        type=parse_pin,
        action="append",
        default=[],
        metavar="PATH@VERSION",
        help="a module that must resolve to exactly this version in core and the root (repeatable)",
    )
    args = parser.parse_args(argv)
    try:
        errors = check(args.root.resolve(), args.pin)
    except RuntimeError as e:
        print(f"module skew check could not run: {e}")
        return 2
    for e in errors:
        print(f"::error::{e}")
    if errors:
        print(f"module skew: {len(errors)} problem(s)")
        return 1
    print(
        "module skew: none (core/go.sum present; every module supplying a package to core resolves to one version)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
