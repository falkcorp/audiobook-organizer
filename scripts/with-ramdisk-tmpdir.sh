#!/usr/bin/env bash
# file: scripts/with-ramdisk-tmpdir.sh
# version: 1.0.0
# guid: 5b9d2e70-8f41-4c6a-b3d5-1e07a9c48f26
# last-edited: 2026-08-14
#
# Run a command with TMPDIR on a RAM disk (H111 / TODO-SRVTIMEOUT).
#
# WHY: the per-test setup in this repo (PebbleStore + migrations) is
# write-heavy, and on macOS/APFS that write cost dominates test wall-clock:
# measured 532s for internal/server with a normal TMPDIR vs 33.7s with TMPDIR
# on a RAM disk — same commit, same machine (~15.8x).
#
# macOS: creates (or reuses) an APFS RAM disk mounted at /Volumes/abo-test-ram.
# Linux: uses /dev/shm, which is already RAM-backed.
#
# The disk is deliberately left mounted for reuse across runs. Remove it with:
#   hdiutil detach /Volumes/abo-test-ram
#
# DURABILITY: everything under the RAM disk vanishes at detach/reboot. Only
# ever point TEST scratch at it — never real data. This wrapper only sets
# TMPDIR for the child command; it does not touch any configured data path.

set -euo pipefail

[ $# -ge 1 ] || { echo "usage: $0 <command> [args...]" >&2; exit 2; }

RAM_MB="${ABO_RAMDISK_MB:-3072}"
MOUNT="/Volumes/abo-test-ram"

case "$(uname -s)" in
  Darwin)
    if [ ! -d "$MOUNT" ]; then
      sectors=$((RAM_MB * 2048)) # 512-byte sectors
      dev=$(hdiutil attach -nomount "ram://$sectors" | tr -d '[:space:]')
      diskutil erasevolume APFS "abo-test-ram" "$dev" >/dev/null
      echo "ramdisk: created ${RAM_MB}MB at $MOUNT ($dev)" >&2
    else
      echo "ramdisk: reusing $MOUNT" >&2
    fi
    export TMPDIR="$MOUNT"
    ;;
  Linux)
    export TMPDIR="/dev/shm"
    echo "ramdisk: using /dev/shm" >&2
    ;;
  *)
    echo "ramdisk: unsupported OS $(uname -s); running with default TMPDIR" >&2
    ;;
esac

exec "$@"
