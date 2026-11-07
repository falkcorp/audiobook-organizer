#!/usr/bin/env python3
# file: scripts/ci/check_embed_context.py
# version: 1.0.0
# guid: ceaf6b21-fced-4629-9b13-331ea6ec1e2d
# last-edited: 2026-11-07
"""Fail when a //go:embed input is outside the Docker build context (03 §3.2 item 4; make embed-check).

Every ``//go:embed`` input of the root module and of core must lie inside the
Docker build context, or be produced in the image by a declared earlier stage.
``.dockerignore`` excludes far more than build output (``docs``, root ``*.md``,
``testdata``, ``web/dist``, ...), so a new ``//go:embed docs/x.json`` builds on
a developer machine and in every Go job and fails only in the image. A path
filter cannot catch it: the change touches only a ``*.go`` file.

The check:

  1. runs ``go list -e -json`` over the root module and over core for every
     (GOOS, tag set) of 03 §3.5.1 item 4's one list (the list and the ``go list``
     reading live in core/scripts/ci/check_embed_outputs.py, which this script
     loads, so core's half and this one cannot disagree);
  2. normalises each package's embed inputs (strip ``all:``, join with the
     package directory relative to its module root; core's get ``core/``);
  3. fails when an input matches a ``.dockerignore`` pattern (moby semantics:
     anchored at the context root, ``*`` stops at ``/``, ``**`` crosses it, a
     parent directory that matches excludes everything below, ``!`` re-includes)
     and is not an in-image output listed in scripts/ci/embed_build_outputs.txt
     (core's packages: core/scripts/ci/embed_build_outputs.txt, read relative to
     ``core/``); a ``no matching files found`` error is ignored only for a
     listed output;
  4. compares the list with each Dockerfile's ``COPY --from=`` lines into a stage
     that runs ``go build``, whose destination lies under that stage's WORKDIR:
     a listed path with no such line, or such a line with no listed path, fails.
     A runtime-stage copy (the binary into /app/) is outside the comparison.

Exit 0, or 1 with one line per offending input. Stdlib only.
"""

from __future__ import annotations

import argparse
import importlib.util
import pathlib
import posixpath
import re
import shlex
import sys
from dataclasses import dataclass, field
from typing import Sequence

ROOT = pathlib.Path(__file__).resolve().parents[2]
CORE_DIRNAME = "core"
OUTPUTS_FILE = "scripts/ci/embed_build_outputs.txt"
DOCKERIGNORE = ".dockerignore"
DOCKERFILES = ("Dockerfile", "Dockerfile.build-cgo")


def _load_core_lib(root: pathlib.Path):
    path = root / CORE_DIRNAME / "scripts" / "ci" / "check_embed_outputs.py"
    spec = importlib.util.spec_from_file_location("check_embed_outputs", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules["check_embed_outputs"] = mod  # dataclasses resolves annotations through it
    spec.loader.exec_module(mod)
    return mod


core_lib = _load_core_lib(ROOT)
TAG_SETS = core_lib.TAG_SETS
clean_path = core_lib.clean_path
covered = core_lib.covered


# -- .dockerignore (moby pattern semantics) ----------------------------------


@dataclass(frozen=True)
class IgnorePattern:
    line: int  # 1-based line in .dockerignore
    text: str  # the pattern as written
    exclusion: bool  # a leading "!"
    regex: re.Pattern[str]

    def match(self, path: str) -> bool:
        return self.regex.fullmatch(path) is not None


def compile_pattern(pattern: str) -> re.Pattern[str]:
    """moby/patternmatcher's translation: ``*`` is ``[^/]*``, ``?`` is ``[^/]``,
    ``**/`` is an optional run of directories, a trailing ``**`` is ``.*``."""
    out: list[str] = []
    i, n = 0, len(pattern)
    while i < n:
        ch = pattern[i]
        if ch == "*":
            if i + 1 < n and pattern[i + 1] == "*":
                i += 1
                while i + 1 < n and pattern[i + 1] == "*":
                    i += 1
                if i + 1 < n and pattern[i + 1] == "/":
                    i += 1
                out.append(".*" if i + 1 >= n else "(?:.*/)?")
            else:
                out.append("[^/]*")
        elif ch == "?":
            out.append("[^/]")
        elif ch == "\\" and i + 1 < n:
            i += 1
            out.append(re.escape(pattern[i]))
        elif ch == "[":
            j = pattern.find("]", i + 1)
            if j == -1:
                out.append(re.escape(ch))
            else:
                body = pattern[i + 1 : j]
                if body.startswith("!") or body.startswith("^"):
                    body = "^" + body[1:]
                out.append("[" + body + "]")
                i = j
        else:
            out.append(re.escape(ch))
        i += 1
    return re.compile("".join(out))


def parse_dockerignore(text: str) -> list[IgnorePattern]:
    patterns: list[IgnorePattern] = []
    for lineno, raw in enumerate(text.splitlines(), 1):
        pat = raw.strip()
        if not pat or pat.startswith("#"):
            continue
        exclusion = pat.startswith("!")
        if exclusion:
            pat = pat[1:].strip()
        if not pat:
            continue
        pat = posixpath.normpath(pat)
        if len(pat) > 1 and pat.startswith("/"):
            pat = pat[1:]
        patterns.append(IgnorePattern(lineno, pat, exclusion, compile_pattern(pat)))
    return patterns


def ignored_by(path: str, patterns: Sequence[IgnorePattern]) -> IgnorePattern | None:
    """The pattern that excludes ``path`` (a later ``!`` pattern may re-include it),
    or None. A pattern also matches when it matches one of the path's parent
    directories (moby's MatchesOrParentMatches)."""
    path = clean_path(path)
    parts = path.split("/")
    parents = ["/".join(parts[: i + 1]) for i in range(len(parts) - 1)]
    decided: IgnorePattern | None = None
    matched = False
    for p in patterns:
        if p.exclusion != matched:
            continue  # an exclusion only matters once matched; an inclusion only before
        hit = p.match(path) or any(p.match(d) for d in parents)
        if hit:
            matched = not p.exclusion
            decided = p if matched else None
    return decided if matched else None


# -- Dockerfile --------------------------------------------------------------


@dataclass
class Copy:
    line: int
    source: str  # "--from=<stage or image> <src>"
    dest: str | None  # relative to the stage's WORKDIR at that line; None when outside it


@dataclass
class Stage:
    name: str
    base: str
    workdir: str = "/"
    builds_go: bool = False
    copies: list[Copy] = field(default_factory=list)


def logical_lines(text: str) -> list[tuple[int, str]]:
    """Dockerfile instructions with continuations joined; comment lines skipped
    (also inside a continuation). Returns (first physical line, instruction)."""
    out: list[tuple[int, str]] = []
    buf: list[str] = []
    start = 0
    for lineno, raw in enumerate(text.splitlines(), 1):
        stripped = raw.strip()
        if stripped.startswith("#"):
            continue
        if not buf:
            if not stripped:
                continue
            start = lineno
        if stripped.endswith("\\"):
            buf.append(stripped[:-1].rstrip())
            continue
        buf.append(stripped)
        out.append((start, " ".join(b for b in buf if b)))
        buf = []
    if buf:
        out.append((start, " ".join(b for b in buf if b)))
    return out


def _tokens(rest: str) -> list[str]:
    try:
        return shlex.split(rest)
    except ValueError:
        return rest.split()


def parse_dockerfile(text: str) -> list[Stage]:
    """Stages with their WORKDIR-resolved ``COPY --from=`` lines. A stage's
    ``builds_go`` is set by any ``RUN`` of it that runs ``go build``."""
    stages: list[Stage] = []
    cur: Stage | None = None
    for lineno, ins in logical_lines(text):
        word, _, rest = ins.partition(" ")
        word = word.upper()
        if word == "FROM":
            toks = [t for t in _tokens(rest) if not t.startswith("--")]
            base = toks[0] if toks else ""
            name = toks[2] if len(toks) >= 3 and toks[1].upper() == "AS" else ""
            cur = Stage(name=name, base=base)
            parent = next((s for s in stages if s.name and s.name == base), None)
            if parent:
                cur.workdir = parent.workdir
            stages.append(cur)
        elif cur is None:
            continue
        elif word == "WORKDIR":
            wd = rest.strip().strip("\"'")
            cur.workdir = posixpath.normpath(
                wd if wd.startswith("/") else posixpath.join(cur.workdir, wd)
            )
        elif word == "RUN":
            if re.search(r"\bgo\s+build\b", rest):
                cur.builds_go = True
        elif word in ("COPY", "ADD"):
            toks = _tokens(rest)
            flags = dict(t[2:].split("=", 1) for t in toks if t.startswith("--") and "=" in t)
            args = [t for t in toks if not t.startswith("--")]
            if "from" not in flags or len(args) < 2:
                continue
            dest = args[-1]
            absdest = posixpath.normpath(
                dest if dest.startswith("/") else posixpath.join(cur.workdir, dest)
            )
            rel = posixpath.relpath(absdest, cur.workdir)
            outside = rel == ".." or rel.startswith("../")
            for src in args[:-1]:
                cur.copies.append(
                    Copy(
                        lineno,
                        f"--from={flags['from']} {src}",
                        None if outside else clean_path(rel),
                    )
                )
    return stages


def dockerfile_outputs(text: str) -> list[Copy]:
    """The ``COPY --from=`` lines into a stage that runs ``go build`` whose
    destination lies under the WORKDIR in effect at that line: the embed outputs
    of the image build. The runtime stage's binary copy is not one."""
    return [
        c for st in parse_dockerfile(text) if st.builds_go for c in st.copies if c.dest is not None
    ]


def dockerfile_problems(name: str, text: str, outputs: Sequence[str]) -> list[str]:
    copies = dockerfile_outputs(text)
    problems: list[str] = []
    copied = {c.dest for c in copies}
    for o in outputs:
        if o not in copied:
            problems.append(
                f"{name}: listed output {o} has no COPY --from= line into a go build stage "
                f"under its WORKDIR (see {OUTPUTS_FILE})"
            )
    for c in copies:
        if c.dest not in outputs:
            problems.append(
                f"{name}:{c.line}: COPY {c.source} puts {c.dest} into a go build stage, but "
                f"{c.dest} is not listed in {OUTPUTS_FILE}"
            )
    return problems


# -- the check ---------------------------------------------------------------


def check(
    root: pathlib.Path,
    tag_sets: Sequence = TAG_SETS,
    lister=None,
) -> list[str]:
    """One line per problem; empty when the tree passes."""
    lib = core_lib
    lister = lister or lib.run_go_list
    ignore = parse_dockerignore((root / DOCKERIGNORE).read_text(encoding="utf-8"))
    root_outputs = lib.read_outputs(root / OUTPUTS_FILE)
    core_list = root / CORE_DIRNAME / lib.OUTPUTS_FILE
    core_outputs = (
        [f"{CORE_DIRNAME}/{o}" for o in lib.read_outputs(core_list)] if core_list.is_file() else []
    )

    problems: list[str] = []
    modules = [("", root, root_outputs)]
    if (root / CORE_DIRNAME / "go.mod").is_file():
        modules.append((f"{CORE_DIRNAME}/", root / CORE_DIRNAME, core_outputs))
    for prefix, mdir, outputs in modules:
        # outputs are context-relative for the comparison, module-relative for go list
        module_outputs = [o[len(prefix) :] for o in outputs]
        for use in lib.module_embeds(mdir, tag_sets, lister, module_outputs):
            ctx = clean_path(prefix + use.path)
            what = lib.describe(use, prefix)
            if use.kind == "error":
                problems.append(f"{what}: {use.detail}")
                continue
            hit = ignored_by(ctx, ignore)
            if use.kind == "missing":
                tail = f" and matches .dockerignore:{hit.line} {hit.text!r}" if hit else ""
                problems.append(
                    f"{what}: embed pattern matches no file{tail} and is not a listed in-image "
                    f"output of {OUTPUTS_FILE}"
                )
            elif hit and not covered(ctx, outputs):
                problems.append(
                    f"{what}: embedded file is excluded by .dockerignore:{hit.line} {hit.text!r} "
                    f"and is not a listed in-image output of {OUTPUTS_FILE}"
                )
    for name in DOCKERFILES:
        f = root / name
        if f.is_file():
            problems += dockerfile_problems(name, f.read_text(encoding="utf-8"), root_outputs)
        else:
            problems.append(f"{name}: missing")
    return sorted(set(problems))


def main(argv: Sequence[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n", 1)[0])
    ap.add_argument(
        "--root",
        type=pathlib.Path,
        default=ROOT,
        help="the repository root (default: this checkout)",
    )
    args = ap.parse_args(argv)
    try:
        problems = check(args.root.resolve())
    except (RuntimeError, OSError) as e:
        print(f"check_embed_context: {e}", file=sys.stderr)
        return 1
    for p in problems:
        print(p, file=sys.stderr)
    if problems:
        print(f"check_embed_context: {len(problems)} problem(s)", file=sys.stderr)
        return 1
    print(
        "check_embed_context: every //go:embed input is in the Docker build context or a listed in-image output"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
