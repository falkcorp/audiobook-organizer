#!/usr/bin/env python3
# file: core/scripts/ci/check_embed_outputs.py
# version: 1.0.0
# guid: dbf08da6-5642-4354-bcf0-47a4326a82e9
# last-edited: 2026-11-07
"""Check core's own //go:embed inputs (12 §12.3.11, 03 §3.2 item 4; make -C core embed-check).

core builds no image, so it has no .dockerignore and no Dockerfile to compare
with. Its half of the embed rule is smaller: every embed pattern of every core
package resolves to a file, except a pattern that names a build output listed
in core/scripts/ci/embed_build_outputs.txt (hello's UI, ``examples/hello/web/dist``,
once it exists) and that is simply not built in this tree. ``go list -e -json``
runs over core for every (GOOS, tag set) of 03 §3.5.1 item 4's one list; a
``pattern ...: no matching files found`` error is ignored only for a listed
output, and any other embed error fails.

The same file is the library of the root's scripts/ci/check_embed_context.py
(it loads this module), so the tag-set list, the pattern normalisation and the
``go list`` reading exist once. It reads nothing above core/: ``make -C core
embed-check`` must pass from an export of core/ alone (12 SC4).

The list file holds one module-relative path per line; blank lines and lines
starting with ``#`` are ignored. It is committed empty until hello's UI lands.

Exit 0, or 1 with one line per offending embed input. Stdlib only.
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import pathlib
import posixpath
import re
import subprocess
import sys
from dataclasses import dataclass, field
from typing import Callable, Iterable, Sequence

MODULE_DIR = pathlib.Path(__file__).resolve().parents[2]
OUTPUTS_FILE = "scripts/ci/embed_build_outputs.txt"

NO_MATCH_RE = re.compile(r"pattern (.+?): no matching files found")
# A package error that is about an embed directive but is not "no matching
# files": a bad pattern, an embedded directory with no files, an irregular file.
EMBED_ERROR_RE = re.compile(r"\bpattern\b|\bembed", re.I)


@dataclass(frozen=True)
class TagSet:
    """One (GOOS, CGO_ENABLED, build tags) combination CI or goreleaser builds."""

    name: str
    goos: str = ""
    cgo: str = ""
    tags: tuple[str, ...] = ()
    # When set, the set covers only these package patterns and only a module
    # that holds them (the GOOS=windows set is the root's ./cmd/mtls-bridge).
    patterns: tuple[str, ...] = ()


# 03 §3.5.1 item 4's one list. core/archtest/tags.go holds the same list for
# the Go tools; scripts/tests/test_check_embed_context.py fails when the names
# differ, so change both together (R1-28 adds `unitlifecycle` to both).
TAG_SETS: tuple[TagSet, ...] = (
    TagSet("default"),
    TagSet("bench", tags=("bench",)),
    TagSet("integration", tags=("integration",)),
    TagSet("pprof", tags=("pprof",)),
    TagSet("race", tags=("race",)),
    TagSet("native_taglib", tags=("native_taglib",)),
    TagSet("image", tags=("embed_frontend", "fts5", "native_taglib")),
    TagSet("darwin", goos="darwin", cgo="0", tags=("embed_frontend",)),
    TagSet("windows-mtls-bridge", goos="windows", patterns=("./cmd/mtls-bridge",)),
)


@dataclass
class EmbedUse:
    """One embed input of a module, normalised to a module-relative path."""

    package: str  # module-relative package directory ("" for the module root)
    path: str  # module-relative, ``all:`` stripped, joined with the package dir
    kind: str  # "file" | "missing" | "error"
    tag_sets: list[str] = field(default_factory=list)
    detail: str = ""


GoLister = Callable[[pathlib.Path, TagSet], "list[dict]"]


def parse_outputs(text: str) -> list[str]:
    """The listed paths of an embed_build_outputs.txt, cleaned and de-duplicated."""
    out: list[str] = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        p = clean_path(line)
        if p not in out:
            out.append(p)
    return out


def read_outputs(path: pathlib.Path) -> list[str]:
    return parse_outputs(path.read_text(encoding="utf-8"))


def clean_path(p: str) -> str:
    """A slash path without ``./`` prefix or trailing slash."""
    p = posixpath.normpath(p.strip())
    return "" if p == "." else p


def normalise_pattern(pkg_dir: str, pattern: str) -> str:
    """Strip ``all:`` and join with the package's directory relative to its module root.

    ``all:web/dist`` in package ``examples/hello`` is ``examples/hello/web/dist``
    (MOD8-11): the same normalisation for both checks.
    """
    if pattern.startswith("all:"):
        pattern = pattern[len("all:") :]
    return clean_path(posixpath.join(pkg_dir, pattern))


def covered(path: str, outputs: Iterable[str]) -> bool:
    """Is ``path`` a listed output or inside one?"""
    path = clean_path(path)
    return any(path == o or path.startswith(o + "/") for o in outputs)


def run_go_list(module_dir: pathlib.Path, ts: TagSet) -> list[dict]:
    """``go list -e -json`` of the module under one tag set; [] when the set does not apply."""
    patterns = list(ts.patterns) or ["./..."]
    if ts.patterns and not all((module_dir / p.removeprefix("./")).is_dir() for p in ts.patterns):
        return []
    args = ["go", "list", "-e", "-json=ImportPath,Dir,Module,EmbedPatterns,EmbedFiles,Error"]
    if ts.tags:
        args += ["-tags", ",".join(ts.tags)]
    env = dict(os.environ)
    if ts.goos:
        env["GOOS"] = ts.goos
    if ts.cgo:
        env["CGO_ENABLED"] = ts.cgo
    proc = subprocess.run(
        args + patterns, cwd=module_dir, env=env, capture_output=True, text=True, check=False
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"{' '.join(args + patterns)} (tag set {ts.name}) in {module_dir.name}: "
            f"exit {proc.returncode}\n{proc.stderr.strip()}"
        )
    pkgs: list[dict] = []
    dec = json.JSONDecoder()
    text, i = proc.stdout, 0
    while i < len(text):
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        obj, i = dec.raw_decode(text, i)
        pkgs.append(obj)
    return pkgs


def package_dir(pkg: dict) -> str:
    """The package's directory relative to its module root, read from the import
    path so a symlinked temp directory cannot skew it."""
    mod = (pkg.get("Module") or {}).get("Path", "")
    ip = pkg.get("ImportPath", "")
    if mod and (ip == mod or ip.startswith(mod + "/")):
        return clean_path(ip[len(mod) :].lstrip("/"))
    return ""


def expand_pattern(pkg_path: pathlib.Path, pattern: str) -> list[str]:
    """Files a pattern would embed, relative to the package directory, read from
    the file system (go list reports no EmbedFiles for a package with an error)."""
    if pattern.startswith("all:"):
        pattern = pattern[len("all:") :]
    files: list[str] = []
    for hit in sorted(glob.glob(str(pkg_path / pattern), recursive=False)):
        h = pathlib.Path(hit)
        if h.is_dir():
            for dirpath, _dirs, names in os.walk(h):
                for n in sorted(names):
                    files.append(os.path.relpath(os.path.join(dirpath, n), pkg_path))
        elif h.exists():
            files.append(os.path.relpath(h, pkg_path))
    return [f.replace(os.sep, "/") for f in files]


def module_embeds(
    module_dir: pathlib.Path,
    tag_sets: Sequence[TagSet] = TAG_SETS,
    lister: GoLister = run_go_list,
    outputs: Sequence[str] = (),
) -> list[EmbedUse]:
    """Every embed input of the module under every tag set, de-duplicated.

    ``outputs`` are the module's listed in-image outputs. A package whose go list
    reports ``pattern ...: no matching files found`` has no EmbedFiles, so each of
    its patterns is resolved on the file system: a pattern naming a listed output
    is tolerated (the tree has no build), another one that matches nothing is a
    "missing" use, and the files of the rest are "file" uses.
    """
    uses: dict[tuple[str, str, str], EmbedUse] = {}

    def add(pkg: str, path: str, kind: str, ts: str, detail: str = "") -> None:
        u = uses.setdefault((pkg, path, kind), EmbedUse(pkg, path, kind, [], detail))
        if ts not in u.tag_sets:
            u.tag_sets.append(ts)

    for ts in tag_sets:
        for pkg in lister(module_dir, ts):
            pdir = package_dir(pkg)
            err = ((pkg.get("Error") or {}).get("Err")) or ""
            if err and NO_MATCH_RE.search(err):
                base = pathlib.Path(pkg.get("Dir") or module_dir / pdir)
                for pat in pkg.get("EmbedPatterns") or []:
                    norm = normalise_pattern(pdir, pat)
                    if covered(norm, outputs):
                        continue  # a listed output that this tree has not built
                    files = expand_pattern(base, pat)
                    if not files:
                        add(pdir, norm, "missing", ts.name, "matches no file")
                    for f in files:
                        add(pdir, clean_path(posixpath.join(pdir, f)), "file", ts.name)
                continue
            if err and EMBED_ERROR_RE.search(err):
                add(pdir, pdir or ".", "error", ts.name, err)
                continue
            for f in pkg.get("EmbedFiles") or []:
                add(pdir, clean_path(posixpath.join(pdir, f)), "file", ts.name)
    return list(uses.values())


def describe(use: EmbedUse, prefix: str = "") -> str:
    where = f"package {prefix}{use.package or '.'}, tags {','.join(use.tag_sets)}"
    return f"{prefix}{use.path} ({where})"


def core_problems(
    module_dir: pathlib.Path,
    outputs: Sequence[str],
    tag_sets: Sequence[TagSet] = TAG_SETS,
    lister: GoLister = run_go_list,
) -> list[str]:
    """One line per offending embed input of core, judged against core's own list."""
    problems: list[str] = []
    for use in module_embeds(module_dir, tag_sets, lister, outputs):
        if use.kind == "missing":
            problems.append(
                f"{describe(use)}: embed pattern matches no file and is not a listed "
                f"output of {OUTPUTS_FILE}"
            )
        elif use.kind == "error":
            problems.append(f"{describe(use)}: {use.detail}")
    return sorted(problems)


def main(argv: Sequence[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n", 1)[0])
    ap.add_argument("--module-dir", type=pathlib.Path, default=MODULE_DIR, help="core's directory")
    args = ap.parse_args(argv)
    module_dir: pathlib.Path = args.module_dir.resolve()
    list_file = module_dir / OUTPUTS_FILE
    if not list_file.is_file():
        print(
            f"check_embed_outputs: {list_file.name} is missing: create {OUTPUTS_FILE} (empty is fine)"
        )
        return 1
    try:
        problems = core_problems(module_dir, read_outputs(list_file))
    except RuntimeError as e:
        print(f"check_embed_outputs: {e}", file=sys.stderr)
        return 1
    for p in problems:
        print(p, file=sys.stderr)
    if problems:
        return 1
    print("check_embed_outputs: every embed pattern of core resolves or is a listed output")
    return 0


if __name__ == "__main__":
    sys.exit(main())
