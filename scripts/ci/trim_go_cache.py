#!/usr/bin/env python3
# file: scripts/ci/trim_go_cache.py
# version: 1.0.0
# guid: 5f2d8c41-9a73-4e0b-b6d1-3c7e9f2a0d84
# last-edited: 2026-09-29
"""Cap a shared GOCACHE at a size, deleting the least recently used files.

The Woodpecker Mac and llm1 agents share one GOCACHE across pipelines. An age
rule ("delete entries unused for a day") never fired: 164 GB accumulated in
under a day on 2026-09-29, all of it younger than the cutoff. A size cap is the
bound that actually holds.

Go refreshes a cache entry's mtime when it uses it, so mtime order is LRU
order. Files touched in the last --min-age-minutes are never deleted, so a
build running concurrently in another pipeline keeps what it just wrote.

Usage: trim_go_cache.py DIR [--max-gb 40] [--target-gb 30] [--min-age-minutes 30]
"""

import argparse
import os
import sys
import time

GIB = 1024**3


def main() -> int:
    ap = argparse.ArgumentParser(description="Cap a shared GOCACHE at a size, deleting the least recently used files.")
    ap.add_argument("dir")
    ap.add_argument("--max-gb", type=float, default=40.0, help="trim only above this size")
    ap.add_argument("--target-gb", type=float, default=30.0, help="trim down to this size")
    ap.add_argument("--min-age-minutes", type=float, default=30.0, help="never delete files newer than this")
    args = ap.parse_args()

    if not os.path.isdir(args.dir):
        print(f"trim_go_cache: {args.dir} is not a directory; nothing to do")
        return 0

    files = []
    total = 0
    for root, _, names in os.walk(args.dir):
        for name in names:
            path = os.path.join(root, name)
            try:
                st = os.lstat(path)
            except OSError:
                continue  # removed by a concurrent build or trim
            files.append((st.st_mtime, st.st_size, path))
            total += st.st_size

    if total <= args.max_gb * GIB:
        print(f"trim_go_cache: {total / GIB:.1f} GiB <= {args.max_gb:g} GiB cap; nothing to do")
        return 0

    cutoff = time.time() - args.min_age_minutes * 60
    goal = args.target_gb * GIB
    freed = removed = 0
    for mtime, size, path in sorted(files):
        if total - freed <= goal or mtime >= cutoff:
            break
        try:
            os.remove(path)
        except OSError:
            continue
        freed += size
        removed += 1

    print(
        f"trim_go_cache: {total / GIB:.1f} GiB -> {(total - freed) / GIB:.1f} GiB "
        f"({removed} files removed, cap {args.max_gb:g} GiB, target {args.target_gb:g} GiB)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
