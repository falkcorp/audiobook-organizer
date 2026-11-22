#!/usr/bin/env python3
# file: scripts/config_cow_codemod.py
# version: 0.1.0
# guid: 2b7d9e41-6c03-4f58-9a1e-d4c8f0b3a726
# last-edited: 2026-11-22
"""Rewrite reads of the package-global config.AppConfig to the copy-on-write API.

Design: docs/plans/platform-core/config-cow.md. The syntax analysis is a go/ast
program behind a build tag (scripts/config_cow_codemod/main.go); this driver
feeds it the files, applies the edits it proposes, runs gofmt on what changed,
and prints every site it refused as the manual list.

Automatic rewrites (exact, never guesses):
  config.AppConfig.X...        ->  config.Get().X...       (a field read)
  config.AppConfig  (a value)  ->  config.Snapshot()       (a whole-struct copy)
With --test-writes, in _test.go files only (exact: the closure runs at once,
under the write lock, on a copy of the config the statement used to touch):
  config.AppConfig.X = v       ->  config.SetForTest(func(c *config.Config) { c.X = v })
  config.AppConfig.N += 2      ->  config.SetForTest(func(c *config.Config) { c.N += 2 })
  x, config.AppConfig.Y = 1, 2 ->  config.SetForTest(func(c *config.Config) { x, c.Y = 1, 2 })
  config.AppConfig.A = config.AppConfig.B -> ... { c.A = c.B }
  config.AppConfig = v         ->  config.ReplaceForTest(v)

Refused, printed for a human: any write outside a test, &config.AppConfig (a
pointer hand-off), the address of a field, a method call on a config field (it
may have a pointer receiver) unless READONLY_METHODS lists it,
append/copy/clear/delete or a sort/slices mutator on a config slice or map,
re-slicing a config slice, and a range loop assigning into the config. (Inside
a rewritten test write, a root in a function literal becomes Get(), the live
read the literal used to make whenever it ran, rather than the closure's c.)

--skip-field F (repeatable) leaves alone every NON-TEST field read whose
field path is F or starts with F (F = "RootDir", "ITunes.PathMappings",
"DownloadClient"): the class-i fields F7-09 re-points to their core accessor
rather than to config.Get(). They are counted as "skipped-field" and are not
on the manual list. Test files are never skipped.

Re-runnable: a rewritten site no longer mentions AppConfig, so a second run
over the same tree proposes nothing new. Run it again after every rebase.

Usage:
  scripts/config_cow_codemod.py [--write] [--test-writes] [--json] [--summary]
                                [--exclude GLOB ...] [--skip-field F ...]
                                PATH [PATH ...]
Without --write it is a dry run: it prints what it would change.
Exit status: 0 on success (manual sites are not an error), 1 when the analyzer
failed on a file, 2 on a usage error.
"""

from __future__ import annotations

import argparse
import collections
import fnmatch
import json
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ANALYZER_PKG = "./scripts/config_cow_codemod"
ANALYZER_TAG = "config_cow_codemod"
# Config methods that only read their receiver. Calls to them on a config field
# chain are rewritten like any read; every other method call is refused, since
# a pointer receiver would mutate the published snapshot. Validate and Problems
# are pure since the copy-on-write foundation but nothing reads them off a
# snapshot, so they are not listed. internal/config's
# TestReadOnlyMethodsDoNotMutate must list exactly these names.
READONLY_METHODS = (
    "EffectiveEmbeddingMode",
    "ResolveAIParseBatch",
    "ResolveChapterConsolidationThresholdMin",
    "ResolveRepairChapterMaxMin",
    "ThresholdsForModel",
)
SKIP_DIRS = {"node_modules", "testdata", "vendor", ".worktrees", ".git", "web"}


@dataclass
class Edit:
    start: int
    end: int
    new: str


@dataclass
class Site:
    file: str
    line: int
    col: int
    kind: str
    auto: bool
    text: str
    detail: str = ""
    edits: list[Edit] = field(default_factory=list)

    @classmethod
    def from_json(cls, obj: dict) -> Site:
        return cls(
            file=obj["file"],
            line=obj["line"],
            col=obj["col"],
            kind=obj["kind"],
            auto=obj.get("auto", False),
            text=obj.get("text", ""),
            detail=obj.get("detail", ""),
            edits=[Edit(e["start"], e["end"], e["new"]) for e in obj.get("edits") or []],
        )


def collect_go_files(paths: list[str], excludes: list[str]) -> list[str]:
    """Every .go file under paths, skipping vendored, generated-test and web trees."""
    out: list[str] = []
    for raw in paths:
        p = Path(raw)
        if p.is_file():
            if p.suffix == ".go":
                out.append(str(p))
            continue
        for dirpath, dirnames, filenames in os.walk(p):
            dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
            for name in sorted(filenames):
                if name.endswith(".go"):
                    out.append(os.path.join(dirpath, name))
    if excludes:
        out = [f for f in out if not any(fnmatch.fnmatch(f, g) for g in excludes)]
    # The analyzer itself spells AppConfig only in strings, but keep it out.
    return [f for f in out if "scripts/config_cow_codemod/" not in f.replace(os.sep, "/")]


def run_analyzer(
    files: list[str], test_writes: bool, go: str = "go"
) -> tuple[list[Site], str, int]:
    """Run the go/ast analyzer over files; return its sites, stderr and exit code."""
    if not files:
        return [], "", 0
    cmd = [
        go,
        "run",
        "-tags",
        ANALYZER_TAG,
        ANALYZER_PKG,
        "-readonly-methods",
        ",".join(READONLY_METHODS),
    ]
    if test_writes:
        cmd.append("-test-writes")
    abs_files = "\n".join(str(Path(f).resolve()) for f in files) + "\n"
    proc = subprocess.run(
        cmd, input=abs_files, capture_output=True, text=True, cwd=REPO_ROOT, check=False
    )
    sites = [Site.from_json(json.loads(line)) for line in proc.stdout.splitlines() if line.strip()]
    return sites, proc.stderr, proc.returncode


def apply_edits(source: bytes, edits: list[Edit]) -> bytes:
    """Apply non-overlapping byte-offset edits; overlapping ones are an error."""
    ordered = sorted(edits, key=lambda e: e.start)
    for a, b in zip(ordered, ordered[1:]):
        if b.start < a.end:
            raise ValueError(f"overlapping edits at bytes {a.start}-{a.end} and {b.start}-{b.end}")
    out = bytearray(source)
    for e in reversed(ordered):
        out[e.start : e.end] = e.new.encode()
    return bytes(out)


_SET_PREFIX_RE = re.compile(
    r"^((?:\w+\.)?SetForTest\(func\(\w+ \*(?:\w+\.)?Config\) \{ )(.*)( \}\))$", re.S
)


def merge_adjacent_sets(source: bytes, edits: list[Edit]) -> list[Edit]:
    """Fold SetForTest rewrites of consecutive statements into one closure.

    Three adjacent `config.AppConfig.X = v` lines become one SetForTest with
    three assignments instead of three publishes. Only statements separated by
    nothing but whitespace with a single line break are folded, so a comment
    or a blank line between them keeps them apart, and the order of the
    assignments is unchanged.
    """
    # Each group: [start, end, prefix or None, bodies, indent]. A group with a
    # None prefix is an ordinary edit passed through unchanged.
    groups: list[list] = []
    for e in sorted(edits, key=lambda e: e.start):
        m = _SET_PREFIX_RE.match(e.new)
        if groups and m and groups[-1][2] == m.group(1):
            g = groups[-1]
            gap = source[g[1] : e.start]
            if gap.strip() == b"" and gap.count(b"\n") == 1:
                g[1] = e.end
                g[3].append(m.group(2))
                g[4] = gap.decode().split("\n", 1)[1]
                continue
        groups.append([e.start, e.end, m.group(1) if m else None, [m.group(2)] if m else [], "", e])
    out: list[Edit] = []
    for start, end, prefix, bodies, indent, first in groups:
        if prefix is None or len(bodies) == 1:
            out.append(first)
            continue
        # gofmt re-indents the statements; the bodies are joined verbatim so a
        # multi-line right-hand side (a raw string, a literal) is not touched.
        inner = "".join(f"\n{indent}\t{b}" for b in bodies)
        out.append(Edit(start, end, f"{prefix.rstrip()}{inner}\n{indent}}})"))
    return out


def plan_file_edits(sites: list[Site]) -> dict[str, list[Edit]]:
    """Group the automatic edits by file.

    A test write rewrite replaces the whole statement, which contains the
    root's own span, and its right-hand side may hold reads that the
    statement edit already spells as c; nested edits are dropped. A multi-assignment
    yields one site per target with the same statement edit; identical edits
    are kept once. Any overlap that still appears is a bug and apply_edits
    raises on it.
    """
    by_file: dict[str, list[Edit]] = collections.defaultdict(list)
    seen: set[tuple[str, int, int, str]] = set()
    for s in sites:
        if not s.auto:
            continue
        for e in s.edits:
            key = (s.file, e.start, e.end, e.new)
            if key not in seen:
                seen.add(key)
                by_file[s.file].append(e)
    # A read inside a rewritten test write statement is already spelled c in
    # the statement's own edit; drop the nested read edit.
    for path, edits in by_file.items():
        by_file[path] = [
            e
            for e in edits
            if not any(
                o is not e
                and o.start <= e.start
                and e.end <= o.end
                and (o.start, o.end) != (e.start, e.end)
                for o in edits
            )
        ]
    return by_file


def relpath(p: str) -> str:
    try:
        return str(Path(p).resolve().relative_to(REPO_ROOT))
    except ValueError:
        return p


def format_manual(sites: list[Site]) -> list[str]:
    lines = []
    manual = (s for s in sites if not s.auto and s.kind != "skipped-field")
    for s in sorted(manual, key=lambda s: (s.file, s.line, s.col)):
        text = " ".join(s.text.split())
        if len(text) > 140:
            text = text[:137] + "..."
        lines.append(
            f"{relpath(s.file)}:{s.line}:{s.col}: {s.kind}: {text}"
            + (f"  -- {s.detail}" if s.detail else "")
        )
    return lines


_FIELD_READ = re.compile(r"^(?:[A-Za-z_][A-Za-z0-9_]*\.)?AppConfig\.(.+)$")


def skip_fields(sites: list[Site], fields: list[str]) -> list[Site]:
    """Mark the non-test field reads of the given field paths as skipped-field.

    A skipped site keeps auto=False and loses its edits, so --write leaves it
    alone, and its kind tells the report it is deliberate, not refused.
    """
    if not fields:
        return sites
    for s in sites:
        if s.kind != "read" or s.file.endswith("_test.go"):
            continue
        m = _FIELD_READ.match(s.text)
        if not m:
            continue
        path = m.group(1)
        if any(path == f or path.startswith(f + ".") or path.startswith(f + "[") for f in fields):
            s.kind, s.auto, s.edits = "skipped-field", False, []
            s.detail = "class-i field: left for its F7-09 accessor (--skip-field)"
    return sites


def summarize(sites: list[Site]) -> dict:
    """Counts by kind, split into test and non-test files."""
    out: dict = {"auto": 0, "manual": 0, "by_kind": {}, "files_auto": 0, "files_manual": 0}
    kinds: dict[str, collections.Counter] = collections.defaultdict(collections.Counter)
    auto_files, manual_files = set(), set()
    for s in sites:
        bucket = "test" if s.file.endswith("_test.go") else "nontest"
        kinds[s.kind][bucket] += 1
        if s.auto:
            out["auto"] += 1
            auto_files.add(s.file)
        elif s.kind == "skipped-field":
            out["skipped"] = out.get("skipped", 0) + 1
        else:
            out["manual"] += 1
            manual_files.add(s.file)
    out["by_kind"] = {k: dict(v) for k, v in sorted(kinds.items())}
    out["files_auto"] = len(auto_files)
    out["files_manual"] = len(manual_files)
    return out


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("paths", nargs="+", help="files or directories to process")
    ap.add_argument(
        "--write", action="store_true", help="apply the automatic rewrites (default: dry run)"
    )
    ap.add_argument(
        "--test-writes",
        action="store_true",
        help="also rewrite the simple write shapes in _test.go files",
    )
    ap.add_argument(
        "--json", action="store_true", help="print every site as JSON lines instead of the report"
    )
    ap.add_argument("--summary", action="store_true", help="print only the counts")
    ap.add_argument(
        "--exclude", action="append", default=[], help="glob of files to skip (repeatable)"
    )
    ap.add_argument(
        "--skip-field",
        action="append",
        default=[],
        help="leave non-test reads of this field path (and below it) alone (repeatable)",
    )
    ap.add_argument(
        "--go", default=os.environ.get("GO", "go"), help="go binary (default: $GO or go)"
    )
    args = ap.parse_args(argv)

    if shutil.which(args.go) is None and not Path(args.go).exists():
        print(f"config_cow_codemod: {args.go} not found", file=sys.stderr)
        return 2

    files = collect_go_files(args.paths, args.exclude)
    sites, stderr, code = run_analyzer(files, args.test_writes, args.go)
    sites = skip_fields(sites, args.skip_field)
    if stderr:
        sys.stderr.write(stderr)

    if args.json:
        for s in sites:
            print(
                json.dumps(
                    {**s.__dict__, "file": relpath(s.file), "edits": [e.__dict__ for e in s.edits]}
                )
            )
        return 1 if code else 0

    changed: list[str] = []
    if args.write:
        for path, edits in plan_file_edits(sites).items():
            src = Path(path).read_bytes()
            new = apply_edits(src, merge_adjacent_sets(src, edits))
            if new != src:
                Path(path).write_bytes(new)
                changed.append(path)
        if changed:
            fmt = subprocess.run(
                ["gofmt", "-w", *changed], capture_output=True, text=True, check=False
            )
            if fmt.returncode:
                sys.stderr.write(fmt.stderr)
                return 1

    summary = summarize(sites)
    if not args.summary:
        manual = format_manual(sites)
        if manual:
            print(f"# MANUAL ({len(manual)} sites) -- refused, needs a human:")
            for line in manual:
                print(line)
    print(
        f"# {'rewrote' if args.write else 'would rewrite'} {summary['auto']} sites in {summary['files_auto']} files; "
        f"{summary['manual']} manual sites in {summary['files_manual']} files; "
        f"{summary.get('skipped', 0)} skipped-field sites"
    )
    for kind, counts in summary["by_kind"].items():
        print(f"#   {kind}: nontest={counts.get('nontest', 0)} test={counts.get('test', 0)}")
    if args.write:
        print(f"# changed {len(changed)} files")
    return 1 if code else 0


if __name__ == "__main__":
    sys.exit(main())
