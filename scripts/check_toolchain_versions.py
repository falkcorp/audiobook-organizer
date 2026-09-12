#!/usr/bin/env python3
# file: scripts/check_toolchain_versions.py
# version: 1.1.0
# guid: 983ded55-56c4-4f7e-b15e-a2f724b54016
# last-edited: 2026-09-12
"""Fail when any Go or Node toolchain copy drifts from its pin (CI-04, CI-03).

Run by ``.github/workflows/test-action-integration.yml`` ("Check version
consistency"). It replaced an inline shell check that truncated ``go.mod`` to
major.minor, compared it against the FIRST ``go-version:`` in ci.yml only,
never read ``.envrc``/the Dockerfiles/``.vscode``, and only ever emitted
``::warning::`` -- so a patch-level drift reported itself and then passed.

Go -- three tiers, because the copies legitimately carry different precision.
Do NOT "simplify" this into one full-equality comparison: CI and go.mod are
two-component / minimum by design and the check would go red on a correct tree.

  1. Exact full version (``X.Y.Z``) -- the pin is ``export GOTOOLCHAIN :=
     goX.Y.Z`` in ``Makefile``; every one of these must equal it:
       - ``.envrc``                 ``export GOTOOLCHAIN=goX.Y.Z``
       - ``.vscode/settings.json``  every ``"GOTOOLCHAIN": "goX.Y.Z"``
       - ``Dockerfile``, ``Dockerfile.build-cgo`` (and any other top-level
         ``Dockerfile*`` with a golang stage) ``FROM golang:X.Y.Z-...``. EVERY
         golang stage, in the required files and in any other ``Dockerfile*``,
         must carry an ``@sha256:`` digest (a stage without one is an error at
         its file:line), and the digests are compared per stage: every golang
         stage in every file must name the same digest.
  2. Major.minor (``X.Y``) -- every literal ``go-version:`` in
     ``.github/workflows/*.yml`` and ``versions.go`` in
     ``.github/repository-config.yml`` must equal the pin's ``X.Y``
     (setup-go takes a floating ``'X.Y'``; the Makefile pin does the rest).
  3. ``go.mod`` -- the ``go`` directive is a minimum: same ``X.Y`` as the pin,
     patch <= the pin's patch, and no ``toolchain`` directive at all.

Node -- the pin is the single ``versions.node`` entry in
``.github/repository-config.yml``; every literal ``node-version:`` in
``.github/workflows/*.yml`` must equal it exactly, and so must the
``gha-get-frontend-config`` action output when ``--action-node-output`` is
given. The Dockerfiles' ``node:`` stages are deliberately NOT checked here.

Values containing ``${{`` are expressions resolved at runtime and are skipped.
The copy list mirrors ``.standards/instructions/go.md`` ("Pin the toolchain")
but is hardcoded: the submodule is not always populated.

Stdlib only -- ci.yml runs the unittest suite with no pip step.
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys
from collections import Counter

MAKEFILE_PIN_RE = re.compile(r"^export\s+GOTOOLCHAIN\s*:=\s*go(\S+)\s*$", re.M)
ENVRC_PIN_RE = re.compile(r"^\s*export\s+GOTOOLCHAIN=go(\S+)\s*$", re.M)
VSCODE_PIN_RE = re.compile(r'"GOTOOLCHAIN"\s*:\s*"go([^"]*)"')
# Deliberately loose: every FROM line naming the golang image is a stage, even
# one whose reference is malformed, so a typo cannot drop a stage from the
# check. GOLANG_REF_RE then parses the reference strictly. Anchored on FROM so
# the "golang:X.Y.Z-alpine" comment above each stage is not mistaken for one.
DOCKER_FROM_GOLANG_RE = re.compile(
    r"^\s*FROM\s+(?:--\S+\s+)*(?:(?:docker\.io/)?library/)?golang(?P<ref>[:@]\S*)?(?:\s|$)",
    re.M | re.I,
)
GOLANG_REF_RE = re.compile(
    r"^:(?P<version>[^\s@-]+)(?:-[a-z][^\s@]*)?(?:@sha256:(?P<digest>[0-9a-f]{64}))?$",
    re.I,
)
GOMOD_GO_RE = re.compile(r"^go\s+(\S+)\s*$", re.M)
GOMOD_TOOLCHAIN_RE = re.compile(r"^toolchain\s+\S+", re.M)
WORKFLOW_KEY_RE = re.compile(r"^\s*(?:-\s+)?(go-version|node-version)\s*:\s*(.*?)\s*$")
FULL_VERSION_RE = re.compile(r"^(\d+)\.(\d+)\.(\d+)$")
GOMOD_VERSION_RE = re.compile(r"^(\d+)\.(\d+)(?:\.(\d+))?$")

REQUIRED_DOCKERFILES = ("Dockerfile", "Dockerfile.build-cgo")


class Checker:
    def __init__(self, root: pathlib.Path) -> None:
        self.root = root
        self.errors: list[str] = []

    def error(self, rel: str, msg: str, line: int | None = None) -> None:
        loc = f"file={rel}" + (f",line={line}" if line else "")
        self.errors.append(f"::error {loc}::{msg}")

    def info(self, rel: str, value: str) -> None:
        print(f"  {rel:<50} {value}")

    def read(self, rel: str) -> str | None:
        path = self.root / rel
        if not path.is_file():
            self.error(rel, f"{rel} is missing; it is a required toolchain copy")
            return None
        return path.read_text(encoding="utf-8")


def _line_of(text: str, pos: int) -> int:
    return text.count("\n", 0, pos) + 1


def _unquote(value: str) -> str:
    value = re.sub(r"\s+#.*$", "", value).strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
        value = value[1:-1]
    return value


def _repo_config_versions(c: Checker) -> dict[str, list[tuple[int, list[str]]]]:
    """Return every ``<lang>: [...]`` flow list under EVERY ``versions:`` block.

    repository-config.yml carries more than one (a top-level ``versions:`` with
    only ``node`` and a nested one with ``go`` and ``node``); each is a copy of
    the pin, so all of them are returned as ``{lang: [(line, values), ...]}``.
    """
    rel = ".github/repository-config.yml"
    text = c.read(rel)
    if text is None:
        return {}
    lines = text.splitlines()
    out: dict[str, list[tuple[int, list[str]]]] = {}
    for i, line in enumerate(lines):
        m = re.match(r"^(\s*)versions:\s*(#.*)?$", line)
        if not m:
            continue
        indent = len(m.group(1))
        for j in range(i + 1, len(lines)):
            sub = lines[j]
            if not sub.strip() or sub.lstrip().startswith("#"):
                continue
            if len(sub) - len(sub.lstrip()) <= indent:
                break
            km = re.match(r"^\s*(\w+):\s*\[(.*)\]\s*(#.*)?$", sub)
            if km:
                values = [_unquote(v) for v in km.group(2).split(",") if v.strip()]
                out.setdefault(km.group(1), []).append((j + 1, values))
    return out


def check(root: pathlib.Path, action_node_output: str | None) -> list[str]:
    c = Checker(root)
    print("--- Go toolchain pin (exact X.Y.Z) ---")
    pin: tuple[int, int, int] | None = None
    text = c.read("Makefile")
    if text is not None:
        pins = MAKEFILE_PIN_RE.findall(text)
        if len(pins) != 1 or not FULL_VERSION_RE.match(pins[0]):
            c.error("Makefile", f"expected exactly one 'export GOTOOLCHAIN := goX.Y.Z', found {pins or 'none'}")
        else:
            m = FULL_VERSION_RE.match(pins[0])
            assert m
            pin = (int(m.group(1)), int(m.group(2)), int(m.group(3)))
            c.info("Makefile", pins[0])
    if pin is None:
        return c.errors
    pin_full = "%d.%d.%d" % pin
    pin_minor = "%d.%d" % pin[:2]

    for rel, regex in ((".envrc", ENVRC_PIN_RE), (".vscode/settings.json", VSCODE_PIN_RE)):
        text = c.read(rel)
        if text is None:
            continue
        found = list(regex.finditer(text))
        if not found:
            c.error(rel, f"no GOTOOLCHAIN=go{pin_full} pin found")
        for m in found:
            c.info(rel, m.group(1))
            if m.group(1) != pin_full:
                c.error(rel, f"GOTOOLCHAIN go{m.group(1)} != Makefile pin go{pin_full}", _line_of(text, m.start()))

    dockerfiles = sorted({p.name for p in root.glob("Dockerfile*") if p.is_file()} | set(REQUIRED_DOCKERFILES))
    # One entry per golang STAGE, not per file: a file with two golang stages
    # must not let the second overwrite the first.
    stages: list[tuple[str, int, str]] = []
    for rel in dockerfiles:
        text = c.read(rel)
        if text is None:
            continue
        found = list(DOCKER_FROM_GOLANG_RE.finditer(text))
        if not found and rel in REQUIRED_DOCKERFILES:
            c.error(rel, f"no 'FROM golang:{pin_full}-...' stage found")
        for m in found:
            line = _line_of(text, m.start())
            ref = m.group("ref") or ""
            rm = GOLANG_REF_RE.match(ref)
            if not rm:
                c.error(rel, f"golang stage 'golang{ref}' is not 'golang:<version>[-<variant>]@sha256:<64 hex>'", line)
                continue
            version, digest = rm.group("version"), rm.group("digest")
            c.info(f"{rel}:{line}", f"golang:{version}" + (f" @sha256:{digest[:12]}..." if digest else " (no digest)"))
            if version != pin_full:
                c.error(rel, f"golang:{version} != Makefile pin {pin_full}", line)
            if not digest:
                c.error(rel, f"golang:{version} stage has no @sha256: digest; every golang stage must be pinned by digest", line)
                continue
            stages.append((rel, line, digest))
    if len({d for _, _, d in stages}) > 1:
        # Flag every stage that disagrees with the most common digest, each at
        # its own file:line, and list them all so the odd one out is obvious.
        majority = Counter(d for _, _, d in stages).most_common(1)[0][0]
        detail = ", ".join(f"{r}:{ln}={d[:12]}" for r, ln, d in stages)
        for rel, line, digest in stages:
            if digest != majority:
                c.error(rel, f"golang image digests differ across stages: {detail}", line)

    print(f"--- Go major.minor ({pin_minor}) and go.mod minimum ---")
    text = c.read("go.mod")
    if text is not None:
        directives = list(GOMOD_GO_RE.finditer(text))
        if len(directives) != 1:
            c.error("go.mod", f"expected exactly one 'go' directive, found {len(directives)}")
        else:
            m = directives[0]
            c.info("go.mod", m.group(1))
            vm = GOMOD_VERSION_RE.match(m.group(1))
            line = _line_of(text, m.start())
            if not vm or (int(vm.group(1)), int(vm.group(2))) != pin[:2]:
                c.error("go.mod", f"go {m.group(1)} is not on the pin's {pin_minor} line (pin go{pin_full})", line)
            elif int(vm.group(3) or 0) > pin[2]:
                c.error("go.mod", f"go {m.group(1)} requires more than the pin go{pin_full}", line)
        for m in GOMOD_TOOLCHAIN_RE.finditer(text):
            c.error("go.mod", "go.mod must not carry a 'toolchain' directive; the pin lives in Makefile/.envrc", _line_of(text, m.start()))

    versions = _repo_config_versions(c)
    cfg = ".github/repository-config.yml"
    go_cfgs = versions.get("go", [])
    if not go_cfgs:
        c.error(cfg, "no versions.go entry found")
    for line, go_cfg in go_cfgs:
        c.info(f"{cfg}:{line} versions.go", str(go_cfg))
        if go_cfg != [pin_minor]:
            c.error(cfg, f"versions.go {go_cfg} != ['{pin_minor}'] (Makefile pin go{pin_full})", line)
    node_cfgs = versions.get("node", [])
    node_pin: str | None = None
    if not node_cfgs:
        c.error(cfg, "no versions.node entry found")
    for line, node_cfg in node_cfgs:
        c.info(f"{cfg}:{line} versions.node", str(node_cfg))
        if len(node_cfg) != 1:
            c.error(cfg, f"versions.node must hold exactly one version to act as the pin, found {node_cfg}", line)
            node_pin = None
            break
        if node_pin is None:
            node_pin = node_cfg[0]
        elif node_cfg[0] != node_pin:
            c.error(cfg, f"versions.node blocks disagree: '{node_pin}' vs '{node_cfg[0]}'", line)

    workflow_files = sorted(p for p in (root / ".github/workflows").glob("*.y*ml") if p.is_file())
    if not workflow_files:
        c.error(".github/workflows", "no workflow files found")
    counts = {"go-version": 0, "node-version": 0}
    node_rows: list[tuple[str, int, str]] = []
    for path in workflow_files:
        rel = path.relative_to(root).as_posix()
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if line.lstrip().startswith("#"):
                continue
            m = WORKFLOW_KEY_RE.match(line)
            if not m:
                continue
            key, value = m.group(1), _unquote(m.group(2))
            if not value or "${{" in value:
                continue
            counts[key] += 1
            if key == "go-version":
                if value != pin_minor:
                    c.error(rel, f"go-version '{value}' != '{pin_minor}' (Makefile pin go{pin_full})", lineno)
            else:
                node_rows.append((rel, lineno, value))
    c.info(".github/workflows go-version literals", str(counts["go-version"]))
    if counts["go-version"] == 0:
        c.error(".github/workflows", "no literal go-version found in any workflow; the check would be vacuous")

    print("--- Node version ---")
    if node_pin is not None:
        c.info(f"{cfg} versions.node", node_pin)
        for rel, lineno, value in node_rows:
            if value != node_pin:
                c.error(rel, f"node-version '{value}' != '{node_pin}' (repository-config versions.node)", lineno)
        c.info(".github/workflows node-version literals", str(counts["node-version"]))
        if counts["node-version"] == 0:
            c.error(".github/workflows", "no literal node-version found in any workflow; the check would be vacuous")
        if action_node_output is not None:
            c.info("gha-get-frontend-config output", action_node_output or "(empty)")
            if action_node_output != node_pin:
                c.error(cfg, f"frontend-config action node-version '{action_node_output}' != '{node_pin}'")
    return c.errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[1],
                        help="repository root to check (default: this script's repo)")
    parser.add_argument("--action-node-output", default=None,
                        help="node-version emitted by gha-get-frontend-config; must equal versions.node")
    args = parser.parse_args(argv)
    errors = check(args.root.resolve(), args.action_node_output)
    for err in errors:
        print(err)
    if errors:
        print(f"Toolchain version drift: {len(errors)} problem(s)")
        return 1
    print("Toolchain versions consistent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
