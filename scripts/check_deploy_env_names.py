#!/usr/bin/env python3
# file: scripts/check_deploy_env_names.py
# version: 1.0.0
# guid: c4a8e2f1-5b7d-4c93-a6e0-3d9f1b7a2c58
# last-edited: 2026-11-19
"""Fail when a HEAD environment-variable name from the rename table survives.

F3-07 cuts configuration over to core's layers, where every setting has one
canonical name (ABO_CORE_*, ABO_BOOKS_*, ABO_INTEGRATIONS_*). The rename table
(internal/books/cutover, pinned by testdata/rename_table.golden) lists every
HEAD env name as a ``retired env`` row. A shipped deploy file, compose file,
Dockerfile, script or skill that still sets or documents one of them fails
the host at boot (a retired admin or secret name is refused), so this check
walks the checked-in files and reports each occurrence by file, line and name.

The names come from the golden, iterated, never listed by hand. Rows whose
verdict is ``kept`` (the OPENAI_* and OTEL_* ecosystem names) are not retired
and are skipped, as is any name that is also some row's canonical env name.

Matching:
  * A name containing an underscore or a dot matches as a whole token
    anywhere in the file, prose and comments included.
  * A single-word name (LANGUAGE, TOOLS, SEARCH ...) is also an ordinary
    English or Python identifier, so it matches only where it is plainly
    environment syntax: ``NAME=``, ``<key>NAME</key>`` or ``ENV NAME``.

Files: every tracked text file under deploy/, scripts/ and .claude/skills/,
plus docker-compose*.yml and Dockerfile*. ``git ls-files`` (tracked and untracked, ignores applied) is the file
list so the gitignored deploy/local.conf (real host config) is never read. Deliberate
HEAD-name users are in ALLOW with the reason; each entry is a path glob and
the names it may keep. An allowlist entry that no longer matches anything
fails the check, so the list cannot rot.

Usage: scripts/check_deploy_env_names.py [--root DIR]   (exit 1 on a finding)
"""

from __future__ import annotations

import argparse
import fnmatch
import os
import pathlib
import re
import subprocess
import sys
from dataclasses import dataclass

GOLDEN = "internal/books/cutover/testdata/rename_table.golden"

SCAN_DIRS = ("deploy", "scripts", ".claude/skills")
SCAN_TOP_GLOBS = ("docker-compose*.yml", "docker-compose*.yaml", "Dockerfile*")
TEXT_SUFFIXES = {
    ".py", ".sh", ".yml", ".yaml", ".md", ".conf", ".example", ".plist",
    ".service", ".txt", ".go", ".json", ".ps1", ".toml", ".env", "",
}
# Never read: real host configuration, gitignored.
NEVER_READ = ("deploy/local.conf",)

# A retired-env row of the golden: ``retired env NAME -> canonical verdict ...``.
_RETIRED_RE = re.compile(r"^retired env (\S+) -> (\S+) (\S+)")
_TARGET_ENV_RE = re.compile(r"\benv=(\S+)")


@dataclass(frozen=True)
class Allow:
    glob: str
    names: frozenset[str]  # empty = every retired name in matching files
    reason: str


ALLOW = (
    Allow(
        "scripts/r2corpus/**",
        frozenset(),
        "drives the R0 (HEAD) binary, which reads HEAD names; recording its behaviour is the point",
    ),
    Allow(
        "scripts/tests/test_record_r0_reach.py",
        frozenset(),
        "tests the R0 reach recorder (scripts/r2corpus), which sets HEAD names on purpose",
    ),
    Allow(
        "scripts/tests/test_write_r0_corpus.py",
        frozenset(),
        "tests the R0 corpus writer (scripts/r2corpus), which sets HEAD names on purpose",
    ),
    Allow(
        "scripts/check_deploy_env_names.py",
        frozenset(),
        "the allowlist below spells the HEAD names it permits",
    ),
    Allow(
        "scripts/tests/test_check_deploy_env_names.py",
        frozenset(),
        "this check's own tests carry HEAD names as input",
    ),
    Allow(
        "scripts/finish_credential_migration.py",
        frozenset({"ABK_STATE_DIR"}),
        "the one-shot migration probes the deployed executable's bytes for the HEAD name as a "
        "capability marker; it does not set or read the variable",
    ),
    Allow(
        "scripts/whisper_server.py",
        frozenset({"WHISPER_BATCH_SIZE"}),
        "the standalone whisper server's own environment, not an audiobook-organizer setting",
    ),
    Allow(
        "scripts/tests/test_whisper_server_cpu.py",
        frozenset({"WHISPER_BATCH_SIZE"}),
        "tests the standalone whisper server's own environment",
    ),
    Allow(
        "scripts/dedup_bench_apply.py",
        frozenset({"ROOT_DIR"}),
        "a Python path constant, not the ROOT_DIR environment variable",
    ),
    Allow(
        "scripts/test-organize-import*.py",
        frozenset({"SUPPORTED_EXTENSIONS"}),
        "a Python class constant, not an environment variable",
    ),
)


def retired_names(golden_text: str) -> dict[str, str]:
    """HEAD env name -> canonical path, for every row that is truly retired."""
    canonical_envs: set[str] = set()
    for line in golden_text.splitlines():
        for m in _TARGET_ENV_RE.finditer(line):
            canonical_envs.add(m.group(1))
    out: dict[str, str] = {}
    for line in golden_text.splitlines():
        m = _RETIRED_RE.match(line)
        if not m:
            continue
        name, canonical, verdict = m.groups()
        if verdict == "kept" or name in canonical_envs:
            continue
        out[name] = canonical
    return out


def _is_single_word(name: str) -> bool:
    return "_" not in name and "." not in name


def build_matcher(names: dict[str, str]):
    """Return find(line) -> list of retired names the line uses."""
    multi = sorted((n for n in names if not _is_single_word(n)), key=len, reverse=True)
    single = sorted((n for n in names if _is_single_word(n)), key=len, reverse=True)
    multi_re = (
        re.compile(r"(?<![A-Za-z0-9_])(" + "|".join(map(re.escape, multi)) + r")(?![A-Za-z0-9_])")
        if multi
        else None
    )
    alt = "|".join(map(re.escape, single))
    single_re = (
        re.compile(
            r"(?<![A-Za-z0-9_.])(" + alt + r")="
            r"|<key>(" + alt + r")</key>"
            r"|^\s*ENV\s+(" + alt + r")(?![A-Za-z0-9_])"
        )
        if single
        else None
    )

    def find(line: str) -> list[str]:
        found: list[str] = []
        if multi_re:
            found += [m.group(1) for m in multi_re.finditer(line)]
        if single_re:
            for m in single_re.finditer(line):
                found.append(next(g for g in m.groups() if g))
        return found

    return find


def _tracked(root: pathlib.Path) -> list[str]:
    try:
        out = subprocess.run(
            ["git", "-C", str(root), "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", *SCAN_DIRS, *SCAN_TOP_GLOBS],
            check=True, capture_output=True, text=True,
        ).stdout
        files = [f for f in out.split("\0") if f]
    except (OSError, subprocess.CalledProcessError):
        files = []
        for d in SCAN_DIRS:
            for dp, _dirs, fns in os.walk(root / d):
                files += [str((pathlib.Path(dp) / f).relative_to(root)) for f in fns]
        for pat in SCAN_TOP_GLOBS:
            files += [p.name for p in root.glob(pat)]
    return sorted(set(files))


def _scannable(rel: str) -> bool:
    if rel in NEVER_READ:
        return False
    base = os.path.basename(rel)
    if base.startswith("Dockerfile") or base.startswith("docker-compose"):
        return True
    return pathlib.PurePosixPath(rel).suffix in TEXT_SUFFIXES


def _allowed(rel: str, name: str, used: set[int], allow: tuple[Allow, ...]) -> bool:
    for i, a in enumerate(allow):
        if fnmatch.fnmatchcase(rel, a.glob) and (not a.names or name in a.names):
            used.add(i)
            return True
    return False


def check(root: pathlib.Path, allow: tuple[Allow, ...] = ALLOW) -> list[str]:
    names = retired_names((root / GOLDEN).read_text(encoding="utf-8"))
    if len(names) < 100:  # a parse that found almost nothing would pass everything
        return [f"{GOLDEN}: only {len(names)} retired env names parsed; the golden format changed?"]
    find = build_matcher(names)
    problems: list[str] = []
    used: set[int] = set()
    for rel in _tracked(root):
        if not _scannable(rel):
            continue
        try:
            text = (root / rel).read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for n, line in enumerate(text.splitlines(), 1):
            for name in find(line):
                if _allowed(rel, name, used, allow):
                    continue
                problems.append(f"{rel}:{n}: {name} is retired; use {names[name]}'s canonical env name")
    for i, a in enumerate(allow):
        if i not in used:
            problems.append(f"allowlist entry {a.glob!r} ({a.reason}) matched nothing; delete it")
    return problems


def main(argv: list[str] | None = None, allow: tuple[Allow, ...] = ALLOW) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--root", default=str(pathlib.Path(__file__).resolve().parents[1]))
    args = ap.parse_args(argv)
    problems = check(pathlib.Path(args.root), allow)
    for p in problems:
        print(p)
    if problems:
        print(f"check_deploy_env_names: {len(problems)} finding(s)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
