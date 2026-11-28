#!/usr/bin/env bash
# file: deploy/owner/cutover-blockers-list.sh
# version: 0.1.0
# guid: 3d7a91c4-52e8-4b06-9f1d-c8a2e60b7415
# last-edited: 2026-11-22
#
# R2 runbook, Step 0 (Path B): list the identity rows the cutover cannot convert.
#
# The owner runs:   bash ~/aorg-owner/cutover-blockers-list.sh
#
# It runs the root-owned helper `cutover-blockers-list` (a fixed sudoers line, no
# arguments, no password), which runs `storage cutover-blockers list --out ...`
# offline as the service user and leaves the list in your export directory. The
# script then moves the files into ~/aorg-owner/blockers/<UTC stamp>/ (mode 0700),
# checks each one's size and sha256 against what root reported, deletes the copies
# in the export directory, and prints the list.
#
# The service must ALREADY be stopped. This script never stops or starts it, and
# it refuses before the sudo call if the unit is not inactive.
#
# Files it leaves (mode 0600):
#   aorg-cutover-blockers-<stamp>.txt   keys, steps and reasons; no values
#   aorg-cutover-blockers-<stamp>.json  only when something blocks: the rows WITH
#                                       their values, which can be credential
#                                       hashes. Keep it off shared places and
#                                       delete it once the rows are fixed.
#
# Safe to run again: it only reads, and each run writes a new directory.
#   --dry-run   do everything except the sudo call.
#
# Exits non-zero on any failure.

set -euo pipefail

CONF=/etc/audiobook-organizer/rollback.conf
LIBDIR=/usr/local/libexec/audiobook-organizer/rollback
OUT_ROOT="$HOME/aorg-owner/blockers"
STUB=cutover-blockers-list

DRY=0
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
  esac
done

say()  { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAILED: %s\n' "$*" >&2; exit 1; }
sha_of() { sha256sum -- "$1" | cut -d' ' -f1; }

[ "$(id -u)" -ne 0 ] || fail "run this as yourself, not with sudo; the helper is the only thing that runs as root"

# 1. The host's values.
say "1/4 values from $CONF"
[ -r "$CONF" ] || fail "$CONF is missing or unreadable"
val() { sed -n "s/^$1[[:space:]]*=[[:space:]]*//p" "$CONF" | tail -n 1; }
EXPORT_DIR=$(val export_dir)
UNIT=$(val unit)
[ -n "$EXPORT_DIR" ] || fail "$CONF has no export_dir; run the P6 reinstall first (bash ~/aorg-owner/r2-p6-reinstall.sh)"
[ -n "$UNIT" ] || fail "$CONF has no unit"
for v in EXPORT_DIR UNIT; do
  case "${!v}" in *[[:space:]\'\"\;\$\`\\]*) fail "$CONF value for ${v} has a character this script will not pass on";; esac
done
[ -x "$LIBDIR/$STUB" ] || fail "$LIBDIR/$STUB is not installed; run the P6 reinstall first"
if [ -L "$EXPORT_DIR" ] || [ ! -d "$EXPORT_DIR" ]; then fail "$EXPORT_DIR is not a directory (or is a symlink)"; fi
[ "$(stat -c %u "$EXPORT_DIR")" = "$(id -u)" ] || fail "$EXPORT_DIR is not owned by $(id -un)"
[ "$(stat -c %a "$EXPORT_DIR")" = "700" ] || fail "$EXPORT_DIR is not mode 0700"
echo "    export directory $EXPORT_DIR ok; unit $UNIT"

# 2. The service must already be stopped.
say "2/4 the service is stopped"
STATE=$(systemctl show -p ActiveState --value "$UNIT" 2>/dev/null) || STATE="unreadable"
[ "$STATE" = "inactive" ] || fail "$UNIT is '$STATE', not 'inactive'. This script never stops or starts it. Nothing was run."
echo "    $UNIT is inactive"

if [ "$DRY" -eq 1 ]; then
  say "dry run only: would run: sudo -n $LIBDIR/$STUB   (nothing was run)"
  exit 0
fi

# 3. The helper.
say "3/4 listing the blocking rows (the helper runs as root)"
RC=0
OUT=$(sudo -n "$LIBDIR/$STUB") || RC=$?
[ "$RC" -eq 0 ] || fail "the helper exited $RC (its message is above). Nothing was delivered."

LIST_NAME=; LIST_SIZE=; LIST_SHA=; ROWS_NAME=; ROWS_SIZE=; ROWS_SHA=; ROWS_ABSENT=0
re='^EXPORTED ([A-Za-z0-9._-]+) ([0-9]{1,8}) ([0-9a-f]{64})$'
list_re='^aorg-cutover-blockers-[0-9]{8}T[0-9]{6}Z\.txt$'
rows_re='^aorg-cutover-blockers-[0-9]{8}T[0-9]{6}Z\.json$'
while IFS= read -r line; do
  [ -n "$line" ] || continue
  if [[ $line =~ $re ]]; then
    name=${BASH_REMATCH[1]}; size=${BASH_REMATCH[2]}; sum=${BASH_REMATCH[3]}
    if [[ $name =~ $list_re ]] && [ -z "$LIST_NAME" ]; then
      LIST_NAME=$name; LIST_SIZE=$size; LIST_SHA=$sum
    elif [[ $name =~ $rows_re ]] && [ -z "$ROWS_NAME" ]; then
      ROWS_NAME=$name; ROWS_SIZE=$size; ROWS_SHA=$sum
    else
      fail "the helper printed a file name this script does not accept: $name. Look in $EXPORT_DIR."
    fi
  elif [ "$line" = "ABSENT rows" ] && [ "$ROWS_ABSENT" -eq 0 ]; then
    ROWS_ABSENT=1
  else
    fail "the helper printed a line this script does not recognise. Look in $EXPORT_DIR."
  fi
done <<<"$OUT"
[ -n "$LIST_NAME" ] || fail "the helper did not report the list. Look in $EXPORT_DIR."
if [ -n "$ROWS_NAME" ] && [ "$ROWS_ABSENT" -eq 1 ]; then fail "the helper reported the rows both present and absent. Look in $EXPORT_DIR."; fi
if [ -z "$ROWS_NAME" ] && [ "$ROWS_ABSENT" -eq 0 ]; then fail "the helper reported the rows neither present nor absent. Look in $EXPORT_DIR."; fi

# 4. Move the files out of the export directory, checked.
say "4/4 moving the files to $OUT_ROOT"
[ -e "$OUT_ROOT" ] || mkdir -m 700 "$OUT_ROOT"
[ -d "$OUT_ROOT" ] && [ ! -L "$OUT_ROOT" ] || fail "$OUT_ROOT is not a directory"
# mktemp -d makes a new directory of mode 0700 with a unique name, so a rerun never collides.
DEST=$(mktemp -d "$OUT_ROOT/$(date -u +%Y%m%dT%H%M%SZ).XXXXXX") || fail "cannot make a directory under $OUT_ROOT"
MOVED=()
copy_checked() { # name size sha
  local src="$EXPORT_DIR/$1"
  [ -f "$src" ] && [ ! -L "$src" ] || fail "$src is not a regular file. The files stay in $EXPORT_DIR."
  [ "$(stat -c %u "$src")" = "$(id -u)" ] || fail "$src is not owned by $(id -un). The files stay in $EXPORT_DIR."
  [ "$(stat -c %s "$src")" = "$2" ] && [ "$(sha_of "$src")" = "$3" ] || fail "$src is not the file the helper wrote (size or sha256 differ). The files stay in $EXPORT_DIR."
  install -m 600 -- "$src" "$DEST/$1"
  [ "$(stat -c %s "$DEST/$1")" = "$2" ] && [ "$(sha_of "$DEST/$1")" = "$3" ] || fail "the copy $DEST/$1 does not match. The files stay in $EXPORT_DIR."
  MOVED+=("$src")
}
copy_checked "$LIST_NAME" "$LIST_SIZE" "$LIST_SHA"
[ -z "$ROWS_NAME" ] || copy_checked "$ROWS_NAME" "$ROWS_SIZE" "$ROWS_SHA"
for f in "${MOVED[@]}"; do rm -f -- "$f"; done

echo "    $DEST/$LIST_NAME  ($LIST_SIZE bytes)"
if [ -n "$ROWS_NAME" ]; then
  echo "    $DEST/$ROWS_NAME  ($ROWS_SIZE bytes; holds the rows' VALUES: keep it private)"
fi
say "the list"
cat -- "$DEST/$LIST_NAME"
say "done"
