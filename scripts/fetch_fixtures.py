#!/usr/bin/env python3
# file: scripts/fetch_fixtures.py
# version: 1.0.0
# guid: 4d0c8e21-9b7a-4f35-8c16-2ae5b9d47f10
# last-edited: 2026-09-22

"""Fetch the audio test fixtures from archive.org.

These used to live in Git LFS. Every CI job pulled 1.8 GB of them on checkout,
on 23 checkout steps across 13 workflows, and not one of those workflows reads
testdata/audio -- roughly 36 GB of LFS bandwidth per pull-request push, which
exhausted the repository's LFS budget and failed every job at checkout. The
fixtures now come from their upstream home instead, on demand.

The recordings are LibriVox, which is public domain, hosted on archive.org.

Every download is pinned by sha256. For the mp3s those hashes are the LFS oids
the repository already recorded -- an LFS oid *is* the file's sha256 -- so the
bytes are provably the same ones the tests were written against. A re-encode
upstream fails loudly here rather than silently testing different audio.

Idempotent: a file already present with the right hash is left alone, so this
is cheap to re-run and safe to put in front of a test target.

Usage:
    python3 scripts/fetch_fixtures.py            # fetch what is missing
    python3 scripts/fetch_fixtures.py --check    # verify only, no download
    python3 scripts/fetch_fixtures.py --force    # re-download everything
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import sys
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
MANIFEST = REPO_ROOT / "testdata" / "audio" / "manifest.json"
CHUNK = 1 << 20
RETRIES = 3


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for block in iter(lambda: fh.read(CHUNK), b""):
            h.update(block)
    return h.hexdigest()


def already_good(path: Path, want: str) -> bool:
    return path.exists() and sha256_of(path) == want


def download(url: str, dest: Path, want: str) -> None:
    """Download url to dest, verifying sha256 before the file is put in place.

    The download lands on a .part file and is renamed only after the hash
    matches, so an interrupted run can never leave a truncated fixture that
    looks complete to the next one.
    """
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_name(dest.name + ".part")
    last: Exception | None = None

    for attempt in range(1, RETRIES + 1):
        try:
            with urllib.request.urlopen(url, timeout=120) as resp, tmp.open("wb") as out:
                shutil.copyfileobj(resp, out, CHUNK)
            got = sha256_of(tmp)
            if got != want:
                tmp.unlink(missing_ok=True)
                raise ValueError(f"sha256 mismatch\n  want {want}\n  got  {got}")
            tmp.replace(dest)
            return
        except (urllib.error.URLError, TimeoutError, ValueError) as err:
            last = err
            tmp.unlink(missing_ok=True)
            if attempt < RETRIES:
                print(f"    attempt {attempt} failed ({err}); retrying", file=sys.stderr)

    raise SystemExit(f"FAILED {dest.name}: {last}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true", help="verify only, download nothing")
    ap.add_argument("--force", action="store_true", help="re-download even if present")
    args = ap.parse_args()

    manifest = json.loads(MANIFEST.read_text())
    downloads = manifest["downloads"]
    copies = manifest.get("copies", [])

    missing = 0
    fetched = 0

    for entry in downloads:
        dest = REPO_ROOT / entry["path"]
        want = entry["sha256"]

        if not args.force and already_good(dest, want):
            continue

        if args.check:
            state = "WRONG HASH" if dest.exists() else "missing"
            print(f"  {state}: {entry['path']}")
            missing += 1
            continue

        print(f"  fetching {entry['path']}")
        download(entry["url"], dest, want)
        fetched += 1

    for entry in copies:
        dest = REPO_ROOT / entry["path"]
        src = REPO_ROOT / entry["from"]
        if args.check:
            if not dest.exists():
                print(f"  missing: {entry['path']}")
                missing += 1
            continue
        if not src.exists():
            raise SystemExit(f"cannot copy {entry['path']}: source {entry['from']} absent")
        if args.force or not dest.exists() or sha256_of(dest) != sha256_of(src):
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(src, dest)
            print(f"  copied  {entry['path']}")

    if args.check:
        if missing:
            print(f"\n{missing} fixture(s) missing or wrong. Run: make fixtures")
            return 1
        print("all fixtures present and verified")
        return 0

    print(f"fixtures ready ({fetched} downloaded, {len(downloads) - fetched} already present)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
