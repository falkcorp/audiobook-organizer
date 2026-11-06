#!/usr/bin/env python3
# file: core/scripts/ci/check_core_toolchain.py
# version: 1.0.0
# guid: 86ca4be5-f5f5-4bc6-9a5b-4ba3fbb9b4d3
# last-edited: 2026-11-05
"""Check core's own copies of the Go toolchain pin (12 §12.3.11, 13 §13.2.2; make -C core toolchain-check).

The copies core carries, and so the copies that stay meaningful in the core
repo after the split:

  core/Makefile           exactly one ``export GOTOOLCHAIN := goX.Y.Z``: the core pin;
  core/tools/*/go.mod     the ``go`` directive on the pin's minor, at most its
                          patch, and no ``toolchain`` directive;
  core-ci.yml             every literal ``go-version:`` equals the pin's X.Y.Z.

Parity with the root pin is not checked here: that is the root guard
scripts/check_toolchain_versions.py (12 SC10), dropped at the split's cutover
step 6. core-ci.yml is looked up at core/.github/workflows/ (the core repo)
and then at ../.github/workflows/ (this monorepo); an export of core/ alone
carries neither and reports that.

Stdlib only.
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

MAKEFILE_PIN_RE = re.compile(r"^export\s+GOTOOLCHAIN\s*:=\s*go(\S+)\s*$", re.M)
FULL_VERSION_RE = re.compile(r"^(\d+)\.(\d+)\.(\d+)$")
GOMOD_GO_RE = re.compile(r"^go\s+(\d+)\.(\d+)(?:\.(\d+))?\s*$", re.M)
GOMOD_TOOLCHAIN_RE = re.compile(r"^toolchain\s+\S+", re.M)
GO_VERSION_RE = re.compile(r"^\s*(?:-\s+)?go-version\s*:\s*(.*?)\s*$")


def _unquote(v: str) -> str:
    v = re.sub(r"\s+#.*$", "", v).strip()
    if len(v) >= 2 and v[0] == v[-1] and v[0] in "'\"":
        v = v[1:-1]
    return v


def check(core: pathlib.Path) -> tuple[list[str], list[str]]:
    """Return (errors, notes)."""
    errors: list[str] = []
    notes: list[str] = []
    mk = core / "Makefile"
    if not mk.is_file():
        return [f"{mk.name} is missing"], notes
    pins = MAKEFILE_PIN_RE.findall(mk.read_text(encoding="utf-8"))
    if len(pins) != 1 or not FULL_VERSION_RE.match(pins[0]):
        return [
            f"core/Makefile: expected exactly one 'export GOTOOLCHAIN := goX.Y.Z', found {pins or 'none'}"
        ], notes
    pin = pins[0]
    major, minor, patch = (int(x) for x in FULL_VERSION_RE.match(pin).groups())
    notes.append(f"core/Makefile pin go{pin}")

    for gm in sorted(core.glob("tools/*/go.mod")):
        rel = gm.relative_to(core).as_posix()
        text = gm.read_text(encoding="utf-8")
        found = GOMOD_GO_RE.findall(text)
        if len(found) != 1:
            errors.append(f"core/{rel}: expected exactly one 'go' directive, found {len(found)}")
        else:
            ma, mi, pa = found[0]
            if (int(ma), int(mi)) != (major, minor):
                errors.append(f"core/{rel}: go {ma}.{mi} is not on the pin's {major}.{minor} line")
            elif int(pa or 0) > patch:
                errors.append(f"core/{rel}: go directive requires more than the pin go{pin}")
        if GOMOD_TOOLCHAIN_RE.search(text):
            errors.append(f"core/{rel}: must not carry a 'toolchain' directive")
        notes.append(f"core/{rel} checked")

    candidates = [
        core / ".github/workflows/core-ci.yml",
        core.parent / ".github/workflows/core-ci.yml",
    ]
    wf = next((c for c in candidates if c.is_file()), None)
    if wf is None:
        if (core.parent / ".github" / "workflows").is_dir() or (
            core / ".github" / "workflows"
        ).is_dir():
            errors.append("core-ci.yml is missing beside the other workflows")
        else:
            notes.append("no core-ci.yml in this export (workflow check not applicable)")
        return errors, notes
    literals = 0
    for lineno, line in enumerate(wf.read_text(encoding="utf-8").splitlines(), 1):
        if line.lstrip().startswith("#"):
            continue
        m = GO_VERSION_RE.match(line)
        if not m:
            continue
        value = _unquote(m.group(1))
        if not value or "${{" in value:
            continue
        literals += 1
        if value != pin:
            errors.append(f"core-ci.yml:{lineno}: go-version '{value}' != core pin {pin}")
    if literals == 0:
        errors.append("core-ci.yml: no literal go-version found; the check would be vacuous")
    notes.append(f"core-ci.yml go-version literals: {literals}")
    return errors, notes


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--core",
        type=pathlib.Path,
        default=pathlib.Path(__file__).resolve().parents[2],
        help="core module root (default: this script's module)",
    )
    args = parser.parse_args(argv)
    errors, notes = check(args.core.resolve())
    for n in notes:
        print(f"  {n}")
    for e in errors:
        print(f"::error::{e}")
    if errors:
        print(f"core toolchain copies: {len(errors)} problem(s)")
        return 1
    print("core toolchain copies consistent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
