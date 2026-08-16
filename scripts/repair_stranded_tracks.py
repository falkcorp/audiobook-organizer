#!/usr/bin/env python3
# file: scripts/repair_stranded_tracks.py
# version: 1.0.0
# guid: 4d7a1e93-58c6-4b02-a71f-6c3e90b8d245
# last-edited: 2026-08-16

"""Recover audio stranded inside directories the path-construction bug created.

From 2026-03-03 (f29c3ce6) to 2026-08-15 (c54721c7) the shipped default of
segment_title_format was "{title} - {track}/{total_tracks}". The slash was a
real path separator, so a track expanded into a DIRECTORY plus a file named
after the total track count:

    <book>/Project Hail Mary - 24/31.mp3.tmp-rename
           ^^^^^^^^^^^^^^^^^^^^^^ directory, from "<title> - <track>"
                                  ^^ file, from "<total_tracks>"

This script puts each such file back where it belongs:

    <book>/Project Hail Mary - 24.mp3

Measured on prod 2026-08-16: 2,535 such directories across 82 books, 35.2 GB,
77 books with no other copy on disk.

SAFETY
    * Dry-run is the default. --apply is required to touch anything.
    * Nothing is ever deleted and nothing is ever overwritten. A target that
      already exists is compared and reported, never clobbered.
    * Moves are os.rename, which is atomic within a filesystem and moves no
      data. If the target is on another filesystem the move is refused rather
      than silently turned into a copy.
    * Every move is verified after the fact: the target must exist, be a
      regular file, and have exactly the source's size.

EDITION COLLISIONS
    A book can carry two editions with different track counts (Foundation and
    Empire has both a 23-track and a 201-track rip). Both would reduce to the
    same recovered name, so when a book has more than one distinct total the
    total is kept in the name: "Foundation and Empire - 10 (201-track).mp3".
    Nothing is merged or dropped; the version system can reconcile them later.
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import os
import re
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field, asdict

TMP_SUFFIX = ".tmp-rename"

# "<title> - <track>" -- the directory the bug created.
DIR_RE = re.compile(r"^(?P<title>.+) - (?P<track>\d+)$")
# "<total_tracks>.<ext>" with an optional failed-rename suffix.
FILE_RE = re.compile(r"^(?P<total>\d+)\.(?P<ext>[A-Za-z0-9]+)(?P<tmp>\.tmp-rename)?$")

AUDIO_EXTS = {"mp3", "m4b", "m4a", "aac", "flac", "ogg", "opus", "wma"}


@dataclass
class Action:
    src: str
    dst: str
    book: str
    title: str
    track: int
    total: int
    status: str = "planned"
    detail: str = ""


@dataclass
class Report:
    planned: list = field(default_factory=list)
    conflicts: list = field(default_factory=list)
    skipped: list = field(default_factory=list)
    done: list = field(default_factory=list)
    failed: list = field(default_factory=list)


def sha256(path: str, chunk: int = 1 << 20) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        while True:
            b = fh.read(chunk)
            if not b:
                break
            h.update(b)
    return h.hexdigest()


def audio_md5(path: str) -> str | None:
    """Hash the DECODED audio stream, ignoring all container metadata.

    Two files can differ byte-for-byte purely because one has embedded cover
    art or a rewritten tag, while the audio is identical. This is the
    tag-insensitive comparison for that case. It is exact, unlike an acoustic
    fingerprint, which is what makes it safe to reason about.
    """
    try:
        out = subprocess.run(
            ["ffmpeg", "-v", "error", "-i", path, "-map", "0:a", "-f", "md5", "-"],
            capture_output=True, text=True, timeout=600,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if out.returncode != 0:
        return None
    line = out.stdout.strip()
    return line.split("=", 1)[1] if line.startswith("MD5=") else None


def find_bogus_dirs(root: str) -> list[str]:
    """Find the directories the bug created, WITHOUT matching legitimate ones.

    The naive rule -- named "<x> - <n>", holding only "<n>.<ext>" -- is far too
    broad: an ordinary multi-disc folder ("The Stand - 1" holding "01.mp3",
    "02.mp3", ...) matches it exactly. Measured here it claimed 42,668
    directories where ~2,500 exist, aimed at an irreplaceable dataset.

    Seeding from ".tmp-rename" is too NARROW, and that error is the more
    dangerous of the two because it looks like it worked. That suffix marks a
    rename that FAILED. Where the bug's rename succeeded, the file sits in the
    wrong directory under a perfectly ordinary name with nothing to betray it.
    "Rath's Deception" has 31 such directories and not one .tmp-rename.
    Recovering only the failures would have left most of the damage in place
    while reporting success.

    So the rule is structural, and rests on one invariant the bug guarantees:

        THE FILENAME IS THE TOTAL TRACK COUNT.

    "<title> - <track>/<total>.<ext>" means the directory's number is a track
    and the file's number is the total, so track <= total ALWAYS. In a real
    multi-disc layout the filename is a track within that disc and bears no
    relation to the number of discs, so "Disc - 1/01.mp3, Disc - 2/01.mp3" has
    max(dir)=2 > max(file)=1 and is rejected.

    Combined with "every such directory holds one or two files" (a real disc
    folder holds the whole disc) and "at least two sibling directories share
    the title stem", this selects the wreckage and nothing else.
    """
    groups: dict[tuple[str, str], list[tuple[str, int, list[int]]]] = collections.defaultdict(list)

    for dirpath, dirnames, filenames in os.walk(root):
        dm = DIR_RE.match(os.path.basename(dirpath))
        if not dm or dirnames or not filenames:
            continue
        matches = [FILE_RE.match(f) for f in filenames]
        if not all(matches):
            continue
        # A real disc directory holds the disc. Wreckage holds one file per
        # (track, edition) -- observed as 1, or 2 where two editions collided.
        if len(filenames) > 2:
            continue
        totals = [int(m.group("total")) for m in matches if m]
        book = os.path.dirname(dirpath)
        groups[(book, dm.group("title"))].append((dirpath, int(dm.group("track")), totals))

    found: list[str] = []
    for (_book, _title), members in groups.items():
        if len(members) < 2:
            continue
        all_totals = {t for _, _, ts in members for t in ts}
        max_track = max(tr for _, tr, _ in members)
        # The invariant. One violation disqualifies the whole group -- a real
        # layout that happens to satisfy it for some members is not wreckage.
        if max_track > max(all_totals):
            continue
        # Sanity: a book does not have more distinct "totals" than editions.
        if len(all_totals) > 3:
            continue
        found.extend(d for d, _, _ in members)

    return sorted(found)


def plan(root: str) -> tuple[list[Action], list[Action], list[Action]]:
    dirs = find_bogus_dirs(root)

    # A book needs the total kept in the name only when it has more than one.
    totals_per_book: dict[str, set[int]] = collections.defaultdict(set)
    for d in dirs:
        for f in os.listdir(d):
            m = FILE_RE.match(f)
            if m:
                totals_per_book[os.path.dirname(d)].add(int(m.group("total")))

    planned: list[Action] = []
    conflicts: list[Action] = []
    skipped: list[Action] = []

    for d in sorted(dirs):
        book = os.path.dirname(d)
        dm = DIR_RE.match(os.path.basename(d))
        if not dm:
            continue
        title, track = dm.group("title"), int(dm.group("track"))

        for fname in sorted(os.listdir(d)):
            src = os.path.join(d, fname)
            fm = FILE_RE.match(fname)
            if not fm:
                skipped.append(Action(src, "", book, title, track, 0,
                                      "skipped", "filename does not match the bug's shape"))
                continue
            total, ext = int(fm.group("total")), fm.group("ext")

            if ext.lower() not in AUDIO_EXTS:
                skipped.append(Action(src, "", book, title, track, total,
                                      "skipped", f"extension {ext!r} is not audio"))
                continue

            # Match file_naming_pattern exactly -- "{title} - {track:02d}".
            # Recovery must land where organize would put the file anyway,
            # otherwise the next organize renames all 2,687 of them again.
            # {track:02d} is a MINIMUM width of two, so a 131-track book still
            # prints track 131 as "131"; it is not padded to the total's width.
            stem = f"{title} - {track:02d}"
            if len(totals_per_book[book]) > 1:
                stem += f" ({total}-track)"
            dst = os.path.join(book, f"{stem}.{ext}")

            act = Action(src, dst, book, title, track, total)
            if os.path.exists(dst):
                act.status, act.detail = "conflict", "destination already exists"
                conflicts.append(act)
            else:
                planned.append(act)

    return planned, conflicts, skipped


def resolve_conflict(act: Action) -> Action:
    """Compare a stranded file against the file already occupying its target."""
    try:
        s_size, d_size = os.path.getsize(act.src), os.path.getsize(act.dst)
    except OSError as e:
        act.status, act.detail = "error", f"stat failed: {e}"
        return act

    if s_size == d_size and sha256(act.src) == sha256(act.dst):
        act.status = "duplicate-identical"
        act.detail = ("byte-identical to the file already in place; the stranded copy is "
                      "redundant. NOT deleted -- deletion needs explicit sign-off.")
        return act

    src_a, dst_a = audio_md5(act.src), audio_md5(act.dst)
    if src_a and dst_a and src_a == dst_a:
        act.status = "duplicate-same-audio"
        act.detail = ("decoded audio is identical; the files differ only in container "
                      "metadata (embedded artwork or rewritten tags). NOT deleted.")
        return act

    act.status = "conflict-different"
    act.detail = (f"different content: sizes {s_size} vs {d_size}; "
                  f"audio md5 {src_a} vs {dst_a}. Left untouched for review.")
    return act


def verify_moved(src: str, dst: str, expected_size: int) -> str | None:
    """Post-move validation. A rename that reports success is not evidence."""
    if not os.path.isfile(dst):
        return f"target missing after move: {dst}"
    actual = os.path.getsize(dst)
    if actual != expected_size:
        return f"size changed during move: expected {expected_size}, found {actual}"
    if os.path.exists(src):
        return f"source still present after move: {src}"
    try:
        with open(dst, "rb") as fh:
            fh.read(4096)
    except OSError as e:
        return f"target not readable: {e}"
    return None


def apply_actions(planned: list[Action], report: Report) -> None:
    for act in planned:
        try:
            size = os.path.getsize(act.src)
            if os.path.exists(act.dst):
                act.status = "conflict"
                act.detail = "destination appeared between planning and apply"
                report.conflicts.append(act)
                continue
            # Same-filesystem rename. os.rename raises OSError(EXDEV) across
            # devices rather than degrading to a copy, which is what we want:
            # a silent copy would double 35 GB and break the atomicity.
            os.rename(act.src, act.dst)
        except OSError as e:
            act.status, act.detail = "failed", str(e)
            report.failed.append(act)
            continue

        problem = verify_moved(act.src, act.dst, size)
        if problem:
            act.status, act.detail = "failed-verification", problem
            report.failed.append(act)
            continue

        act.status = "moved"
        report.done.append(act)

        parent = os.path.dirname(act.src)
        try:
            if not os.listdir(parent):
                os.rmdir(parent)
        except OSError:
            pass


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("root", help="library root to scan")
    ap.add_argument("--apply", action="store_true",
                    help="actually move files (default is a dry run)")
    ap.add_argument("--report", default="/tmp/repair-stranded-report.json")
    ap.add_argument("--jobs", type=int, default=8,
                    help="parallel workers for hashing conflicts")
    args = ap.parse_args()

    print(f"scanning {args.root} ...", flush=True)
    planned, conflicts, skipped = plan(args.root)
    print(f"  {len(planned)} recoverable, {len(conflicts)} conflicts, {len(skipped)} skipped",
          flush=True)

    if conflicts:
        print(f"  hashing {len(conflicts)} conflicts ...", flush=True)
        with ThreadPoolExecutor(max_workers=args.jobs) as pool:
            conflicts = list(pool.map(resolve_conflict, conflicts))

    report = Report(planned=planned, conflicts=conflicts, skipped=skipped)

    if args.apply:
        print(f"applying {len(planned)} moves ...", flush=True)
        report.planned = []
        apply_actions(planned, report)

    by_status = collections.Counter(
        a.status for a in report.planned + report.conflicts + report.skipped
        + report.done + report.failed)

    print("\n=== summary ===")
    for status, n in sorted(by_status.items()):
        print(f"  {status:24} {n}")
    total_bytes = sum(os.path.getsize(a.dst) for a in report.done if os.path.exists(a.dst))
    if args.apply:
        print(f"  {'bytes recovered':24} {total_bytes / (1 << 30):.1f} GiB")

    with open(args.report, "w") as fh:
        json.dump({k: [asdict(a) for a in v] for k, v in vars(report).items()},
                  fh, indent=2)
    print(f"\nfull report: {args.report}")

    if not args.apply:
        print("\nDRY RUN -- nothing was moved. Re-run with --apply to execute.")
        for a in report.planned[:5]:
            print(f"  would move: {a.src}\n           -> {a.dst}")

    return 1 if report.failed else 0


if __name__ == "__main__":
    sys.exit(main())
