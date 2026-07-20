#!/usr/bin/env python3
# file: scripts/assemble_todo.py
# version: 1.2.0
# guid: af7ef324-6c69-411e-b1a9-98c9ba2b31e3
# last-edited: 2026-07-19

"""Assemble TODO.md from per-task fragment files in todo.d/.

This is the TODO-file counterpart of the ``changelog.d/`` + ``scriv`` system.
The problem is the same one scriv solves for changelogs: when many contributors
and AI agents open PRs in parallel, every PR that wants to add a task edits the
same region of the same file and they all conflict. A fragment-per-task means
no two PRs ever touch the same file.

scriv is changelog-only and has no TODO equivalent, so the assembler is ours.
The model is deliberately identical to scriv's, including fragment deletion:

* **Opt-in by presence.** No ``todo.d/todo.ini`` means this script exits 0
  having done nothing, so the collecting workflow is safe to ship to repos that
  have not adopted the system.
* **Add-only.** Fragments *add* tasks at the insert marker. Checking a task off
  or deleting it stays a direct edit of ``TODO.md`` — a low-collision operation
  that gains nothing from fragments.
* **Consumed fragments are deleted**, via ``git rm`` inside a work tree so the
  removal lands in the same commit as the insertion. Without this, every collect
  would re-append every task forever.

Usage:
    python3 scripts/assemble_todo.py              # collect and delete fragments
    python3 scripts/assemble_todo.py --dry-run    # print the result, touch nothing
    python3 scripts/assemble_todo.py --keep       # collect but leave fragments
    python3 scripts/assemble_todo.py --check      # exit 1 if fragments are pending
"""

from __future__ import annotations

import argparse
import configparser
import datetime
from pathlib import Path
import re
import subprocess
import sys

CONFIG_FILE = Path("todo.d/todo.ini")

# A fragment whose body is nothing but HTML comments (an untouched scaffold) is
# an intentional no-op: it is deleted without contributing anything, matching how
# scriv treats a fully-commented changelog fragment.
HTML_COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)

VERSION_HEADER = re.compile(
    r"^(<!--\s*version:\s*)(\d+)\.(\d+)\.(\d+)(\s*-->)$",
    re.M,
)
LAST_EDITED_HEADER = re.compile(
    r"^(<!--\s*last-edited:\s*)(\S+)(\s*-->)$",
    re.M,
)


class AssemblyError(RuntimeError):
    """A condition that should fail the run loudly rather than silently pass."""


def load_config() -> configparser.SectionProxy | None:
    """Return the ``[todo]`` config section, or None when not opted in."""
    if not CONFIG_FILE.is_file():
        return None
    parser = configparser.ConfigParser()
    parser.read(CONFIG_FILE)
    if not parser.has_section("todo"):
        raise AssemblyError(f"{CONFIG_FILE} exists but has no [todo] section.")
    return parser["todo"]


def find_fragments(fragment_dir: Path) -> list[Path]:
    """Return real fragments, oldest filename first.

    The scaffolding — ``README.md``, ``templates/``, and the ``.ini`` config —
    lives in the same directory and is never a fragment. Sorting by name is what
    makes the ``<YYYY-MM-DD>-<slug>.md`` convention yield chronological order.
    """
    if not fragment_dir.is_dir():
        return []
    candidates = fragment_dir.glob("*.md")
    return sorted(p for p in candidates if p.name != "README.md")


def fragment_body(path: Path) -> str:
    """Return a fragment's contributed Markdown, or '' if it is a no-op."""
    text = path.read_text(encoding="utf-8")
    if not HTML_COMMENT.sub("", text).strip():
        return ""
    return text.strip("\n")


def bump_header(text: str, today: str) -> str:
    """Bump the output file's patch version and refresh ``last-edited``.

    The collect commit is a real modification of the output file, so the
    assembler maintains its header rather than leaving CI to complain about a
    stale one.

    Best-effort by design: each header line is updated only if present. Adding
    tasks is the job, and header upkeep must never be the reason a scheduled
    collect fails — repos that adopt this system carry heterogeneous headers,
    and plenty have a `version:` line but no `last-edited:` (or neither). A
    missing line is reported as a warning and the collect proceeds.
    """

    def _bump(match: re.Match[str]) -> str:
        major, minor, patch = (
            match.group(2),
            match.group(3),
            int(match.group(4)),
        )
        return f"{match.group(1)}{major}.{minor}.{patch + 1}{match.group(5)}"

    text, count = VERSION_HEADER.subn(_bump, text, count=1)
    if not count:
        print("warning: no '<!-- version: X.Y.Z -->' header to bump.")

    text, count = LAST_EDITED_HEADER.subn(
        lambda m: f"{m.group(1)}{today}{m.group(3)}",
        text,
        count=1,
    )
    if not count:
        print("warning: no '<!-- last-edited: ... -->' header to refresh.")
    return text


def insert_at_marker(text: str, marker: str, addition: str) -> str:
    """Insert ``addition`` directly below the marker line.

    Each collect lands its batch at the marker, so the most recently collected
    tasks sit at the top of the inbox — the same "newest at the marker" ordering
    scriv uses for release sections.
    """
    marker_line = re.compile(
        rf"^([ \t]*<!--\s*{re.escape(marker)}\s*-->[ \t]*)$",
        re.M,
    )
    match = marker_line.search(text)
    if not match:
        raise AssemblyError(
            f"No '<!-- {marker} -->' marker in the output file; refusing to guess where tasks belong.",
        )
    return text[: match.end()] + f"\n\n{addition}" + text[match.end() :]


def git_rm(paths: list[Path]) -> None:
    """Delete consumed fragments, staging the removal when inside a work tree."""
    try:
        subprocess.run(
            ["git", "rm", "--quiet", "--", *(str(p) for p in paths)],
            check=True,
            capture_output=True,
        )
    except (subprocess.CalledProcessError, FileNotFoundError):
        # Not a git work tree (or git is unavailable) — a plain unlink still
        # leaves the tree in the correct end state.
        for path in paths:
            path.unlink(missing_ok=True)


def main() -> int:
    """Parse arguments, collect fragments, and return the process exit code."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="print the assembled output file to stdout and change nothing",
    )
    parser.add_argument(
        "--keep",
        action="store_true",
        help="write the output file but leave the consumed fragments in place",
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if any fragment is pending collection; change nothing",
    )
    args = parser.parse_args()

    config = load_config()
    if config is None:
        print(f"{CONFIG_FILE} not present — TODO fragments not enabled here.")
        return 0

    fragment_dir = Path(config.get("fragment_directory", "todo.d"))
    output_file = Path(config.get("output_file", "TODO.md"))
    marker = config.get("insert_marker", "todo-insert-here")

    fragments = find_fragments(fragment_dir)
    if args.check:
        for path in fragments:
            print(f"pending: {path}")
        return 1 if fragments else 0

    if not fragments:
        print(f"No fragments in {fragment_dir}/ — nothing to collect.")
        return 0

    if not output_file.is_file():
        raise AssemblyError(f"{output_file} does not exist.")

    bodies = [(path, fragment_body(path)) for path in fragments]
    contributed = [body for _, body in bodies if body]

    for path, body in bodies:
        print(f"{'no-op ' if not body else 'collect'} {path}")

    if contributed:
        text = insert_at_marker(
            output_file.read_text(encoding="utf-8"),
            marker,
            "\n\n".join(contributed),
        )
        if config.getboolean("bump_version", fallback=True):
            today = datetime.date.today().isoformat()
            text = bump_header(text, today)

        if args.dry_run:
            sys.stdout.write(text)
            return 0
        output_file.write_text(text, encoding="utf-8")
        print(f"Wrote {len(contributed)} task block(s) into {output_file}.")
    elif args.dry_run:
        print("All fragments are no-ops; output file unchanged.")
        return 0

    if not args.keep:
        git_rm(fragments)
        print(f"Removed {len(fragments)} consumed fragment(s).")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except AssemblyError as exc:
        print(f"error: {exc}", file=sys.stderr)
        sys.exit(1)
