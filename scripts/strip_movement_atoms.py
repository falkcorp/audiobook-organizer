#!/usr/bin/env python3
# file: scripts/strip_movement_atoms.py
# version: 1.0.1
#
# Removes shwm (Show Work & Movement), ©mvi (Movement Number), and ©mvn
# (Movement Name) atoms from M4B/M4A audiobook files.
#
# These atoms are classical music metadata. The audiobook organizer
# incorrectly wrote them to audiobook files (stik=2), causing Apple Devices
# for Windows to crash during sync at "Determining Tracks to Sync".
#
# Usage:
#   python3 scripts/strip_movement_atoms.py /path/to/audiobooks [--dry-run]

import argparse
import sys
from pathlib import Path

try:
    from mutagen.mp4 import MP4, MP4StreamInfoError
except ImportError:
    print("ERROR: mutagen not installed. Run: pip3 install mutagen")
    sys.exit(1)

TARGET_ATOMS = ["\xa9mvi", "\xa9mvn", "shwm"]  # ©mvi, ©mvn, shwm


def strip_file(path: Path, dry_run: bool) -> bool:
    try:
        audio = MP4(str(path))
    except (MP4StreamInfoError, Exception) as e:
        print(f"  SKIP (unreadable): {path.name}: {e}")
        return False

    found = [a for a in TARGET_ATOMS if a in audio.tags]
    if not found:
        return False

    if dry_run:
        print(f"  WOULD strip {found}: {path}")
        return True

    for atom in found:
        del audio.tags[atom]
    try:
        audio.save()
        print(f"  stripped {found}: {path}")
        return True
    except Exception as e:
        print(f"  ERROR saving {path}: {e}")
        return False


def main():
    parser = argparse.ArgumentParser(description="Strip movement atoms from audiobook files")
    parser.add_argument("root", help="Root directory to scan")
    parser.add_argument("--dry-run", action="store_true", help="Report without modifying files")
    args = parser.parse_args()

    root = Path(args.root)
    if not root.is_dir():
        print(f"ERROR: {root} is not a directory")
        sys.exit(1)

    print(f"Scanning {root} {'(dry run)' if args.dry_run else ''}...")
    total = modified = 0

    for path in sorted(root.rglob("*.m4b")):
        total += 1
        if strip_file(path, args.dry_run):
            modified += 1

    for path in sorted(root.rglob("*.m4a")):
        total += 1
        if strip_file(path, args.dry_run):
            modified += 1

    action = "Would modify" if args.dry_run else "Modified"
    print(f"\nDone. Scanned {total} files. {action} {modified}.")


if __name__ == "__main__":
    main()
