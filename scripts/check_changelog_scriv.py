#!/usr/bin/env python3
# file: scripts/check_changelog_scriv.py
# version: 1.0.0
# guid: 3e8b6f41-9a2d-4c57-b0e3-7d1f5a92c864
# last-edited: 2026-09-12

"""Fail when the next release's `scriv collect` would refuse CHANGELOG.md.

`scriv collect --version X` reads every existing entry heading in CHANGELOG.md
(at `md_header_level`, between the insert marker and `scriv-end-here`) and exits
1 on the first one that is not a version. The release workflow runs that step
only at release time, so a bad heading merges silently and breaks the *next*
release. v0.222.0 failed that way on 2026-09-12: an unreleased fragment had
been amended with a `## Corrections, made before release` section, the v0.221.1
collect copied it verbatim into CHANGELOG.md, and every collect after that one
was refused.

The heading can arrive by two routes, and this check covers both:

1. Directly in CHANGELOG.md. Checked by running scriv's own parser over it and
   applying the same `Version.from_text` test `scriv collect` applies.
2. Inside a pending changelog.d/ fragment. A fragment is folded into
   CHANGELOG.md verbatim, so its bad heading only becomes fatal one release
   later. Checked by running the real `scriv collect` on a scratch copy of the
   tree with a sentinel version, then re-checking the result.

Nothing in the working tree is modified. Requires scriv (`pip install scriv`).
Exit 0 when clean, 1 when a heading would be refused, 2 on a setup problem.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

# A version no real release will use, so the simulated collect never trips
# scriv's "already uses version" check.
SENTINEL_VERSION = "v999999.0.0"


def bad_entry_titles(workdir: Path) -> list[str]:
    """Return every entry title scriv collect would reject in workdir."""
    from scriv.scriv import Scriv
    from scriv.util import Version

    cwd = os.getcwd()
    os.chdir(workdir)  # scriv reads changelog.d/scriv.ini relative to cwd
    try:
        changelog = Scriv().changelog()
        changelog.read()
        return [
            title
            for title in changelog.entries()
            if title is not None and Version.from_text(title) is None
        ]
    finally:
        os.chdir(cwd)


def fragments_with_heading(fragment_dir: Path, title: str) -> list[str]:
    """Name the pending fragments whose text contains a heading `title`."""
    hits = []
    for frag in sorted(fragment_dir.glob("*.md")):
        if frag.name == "README.md":
            continue
        for line in frag.read_text(encoding="utf-8").splitlines():
            if line.lstrip("#").strip() == title and line.startswith("#"):
                hits.append(frag.name)
                break
    return hits


def report(titles: list[str], where: str) -> None:
    for title in titles:
        print(f"::error file=CHANGELOG.md::{where}: entry heading {title!r} is not a version.")
    print(
        "The version-entry heading level in CHANGELOG.md may hold only release\n"
        "versions. Demote the heading so it sits inside a release entry (a\n"
        "#### entry title, or lower). Corrections to an unreleased fragment go\n"
        "inside that fragment's own entry; see changelog.d/README.md."
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".", help="repository root (default: cwd)")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    if not (root / "changelog.d" / "scriv.ini").is_file():
        print("changelog.d/scriv.ini not present; changelog fragments not enabled here.")
        return 0
    try:
        import scriv  # noqa: F401
    except ImportError:
        print("::error::scriv is not installed (pip install scriv).")
        return 2

    with tempfile.TemporaryDirectory(prefix="scriv-check-") as tmp:
        work = Path(tmp)
        shutil.copy2(root / "CHANGELOG.md", work / "CHANGELOG.md")
        shutil.copytree(root / "changelog.d", work / "changelog.d")

        # Route 1: headings already in CHANGELOG.md. Report all of them, not
        # only the first one scriv collect would stop at.
        existing = bad_entry_titles(work)
        if existing:
            report(existing, "CHANGELOG.md")
            return 1
        print("CHANGELOG.md: every entry heading parses as a version.")

        # Route 2: headings a pending fragment would introduce.
        proc = subprocess.run(
            [sys.executable, "-m", "scriv", "collect", "--version", SENTINEL_VERSION],
            cwd=work,
            capture_output=True,
            text=True,
        )
        output = (proc.stdout + proc.stderr).strip()
        if proc.returncode == 2 and "No changelog fragments" in output:
            print("No pending fragments to simulate.")
            return 0
        if proc.returncode != 0:
            print(output)
            print(f"::error::simulated 'scriv collect' exited {proc.returncode}.")
            return 1

        introduced = bad_entry_titles(work)
        if introduced:
            for title in introduced:
                names = fragments_with_heading(root / "changelog.d", title) or ["(unknown)"]
                for name in names:
                    print(
                        f"::error file=changelog.d/{name}::fragment adds entry-level heading "
                        f"{title!r}; the release after next would fail to collect."
                    )
            report(introduced, "after collecting pending fragments")
            return 1
        print("Pending fragments collect cleanly and add no non-version entry heading.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
