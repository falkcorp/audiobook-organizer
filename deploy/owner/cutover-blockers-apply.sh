#!/usr/bin/env bash
# file: deploy/owner/cutover-blockers-apply.sh (copied to ~/aorg-owner/ on the server)
# version: 0.1.0
# guid: e5b02a68-91c3-4d7f-a8e4-16f7d3c92b50
# last-edited: 2026-11-22
#
# R2 runbook, Step 0 (Path B): fix or delete ONE identity row the cutover cannot
# convert.
#
# The owner runs:   bash ~/aorg-owner/cutover-blockers-apply.sh [REQUEST_FILE]
#
# REQUEST_FILE is a JSON file of yours (mode 0600, at most 16 MiB):
#     {"action":"fix","key":"<lowercase hex>","value":"<base64>"}
#     {"action":"delete","key":"<lowercase hex>"}
# Without an argument the script uses the request already waiting in your export
# directory (a rerun after a failure leaves it there).
#
# It puts the request in your export directory under the fixed name
# aorg-cutover-blockers-request.json (never replacing a different one), then runs
# the root-owned helper `cutover-blockers-apply` (a fixed sudoers line, no
# arguments, no password). The helper runs `storage cutover-blockers fix` or
# `delete --confirm` offline as the service user, writes what it printed to a
# result file, and deletes the request when the command succeeded. The script
# moves the result to ~/aorg-owner/blockers/<UTC stamp>/ (mode 0700), checks it,
# and prints it.
#
# `delete` removes the row and the rows that go with it. It cannot be undone
# except by `make rollback-restore`.
#
# The service must ALREADY be stopped. This script never stops or starts it, and
# it refuses before the sudo call if the unit is not inactive.
#
# Safe to run again: a request that is already in place with the same contents is
# reused; a different one is never overwritten. After a FAILED apply the request
# stays where it is and the change may or may not have happened: run
# cutover-blockers-list.sh to see what is left before trying again.
#   --dry-run   do everything except putting the request in place and the sudo
#               call.
#
# Exits non-zero on any failure.

set -euo pipefail

CONF=/etc/audiobook-organizer/rollback.conf
LIBDIR=/usr/local/libexec/audiobook-organizer/rollback
OUT_ROOT="$HOME/aorg-owner/blockers"
STUB=cutover-blockers-apply
REQUEST_NAME=aorg-cutover-blockers-request.json
LIMIT=$((16 * 1024 * 1024))

DRY=0
REQ=
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    -*) echo "usage: $0 [--dry-run] [REQUEST_FILE]" >&2; exit 2 ;;
    *) [ -z "$REQ" ] || { echo "usage: $0 [--dry-run] [REQUEST_FILE]" >&2; exit 2; }; REQ=$a ;;
  esac
done

say()  { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAILED: %s\n' "$*" >&2; exit 1; }
sha_of() { sha256sum -- "$1" | cut -d' ' -f1; }
# A regular file of yours, no symlink, no group or other access, 1 byte to 16 MiB.
check_request() {
  local f=$1 size
  [ -f "$f" ] && [ ! -L "$f" ] || fail "$f is not a regular file (a symlink is refused)"
  [ "$(stat -c %u "$f")" = "$(id -u)" ] || fail "$f is not owned by $(id -un)"
  [ "$(stat -c %a "$f")" = "600" ] || fail "$f must be mode 0600: it holds a value"
  size=$(stat -c %s "$f")
  { [ "$size" -ge 1 ] && [ "$size" -le "$LIMIT" ]; } || fail "$f is $size bytes; it must be 1 to $LIMIT"
}

[ "$(id -u)" -ne 0 ] || fail "run this as yourself, not with sudo; the helper is the only thing that runs as root"

# 1. The host's values.
say "1/5 values from $CONF"
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
IN_PLACE="$EXPORT_DIR/$REQUEST_NAME"
echo "    export directory $EXPORT_DIR ok; unit $UNIT"

# 2. The service must already be stopped.
say "2/5 the service is stopped"
STATE=$(systemctl show -p ActiveState --value "$UNIT" 2>/dev/null) || STATE="unreadable"
[ "$STATE" = "inactive" ] || fail "$UNIT is '$STATE', not 'inactive'. This script never stops or starts it. Nothing was run."
echo "    $UNIT is inactive"

# 3. The request.
say "3/5 the request"
if [ -n "$REQ" ]; then
  check_request "$REQ"
  WANT=$(sha_of "$REQ")
  if [ -e "$IN_PLACE" ] || [ -L "$IN_PLACE" ]; then
    check_request "$IN_PLACE"
    [ "$(sha_of "$IN_PLACE")" = "$WANT" ] || fail "a different request is already in $EXPORT_DIR ($REQUEST_NAME), left by an earlier run. Nothing was changed. Ask the agents to look at it and remove it (it needs no sudo)."
    echo "    the same request is already in place; reusing it"
  elif [ "$DRY" -eq 1 ]; then
    echo "    $REQ is a valid request file; a dry run does not copy it"
  else
    TMP="$EXPORT_DIR/.request.$$"
    install -m 600 -- "$REQ" "$TMP"
    # A hard link is the no-replace step: it fails if the fixed name appeared meanwhile.
    ln -- "$TMP" "$IN_PLACE" || { rm -f -- "$TMP"; fail "could not place the request as $IN_PLACE"; }
    rm -f -- "$TMP"
    [ "$(stat -c %s "$IN_PLACE")" = "$(stat -c %s "$REQ")" ] && [ "$(sha_of "$IN_PLACE")" = "$WANT" ] || fail "the request in $EXPORT_DIR is not the file you gave. Nothing was run."
    echo "    $REQ placed as $IN_PLACE"
  fi
else
  [ -e "$IN_PLACE" ] || fail "no request given and none waiting in $EXPORT_DIR. Pass the request file as the one argument."
  check_request "$IN_PLACE"
  echo "    using the request already in $IN_PLACE"
fi

if [ "$DRY" -eq 1 ]; then
  say "dry run only: would run: sudo -n $LIBDIR/$STUB   (nothing was run)"
  exit 0
fi

# 4. The helper.
say "4/5 applying (the helper runs as root)"
RC=0
OUT=$(sudo -n "$LIBDIR/$STUB") || RC=$?

RESULT_NAME=; RESULT_SIZE=; RESULT_SHA=; REMOVED=0
re='^EXPORTED ([A-Za-z0-9._-]+) ([0-9]{1,8}) ([0-9a-f]{64})$'
res_re='^aorg-cutover-blockers-result-[0-9]{8}T[0-9]{6}Z\.txt$'
while IFS= read -r line; do
  [ -n "$line" ] || continue
  if [[ $line =~ $re ]]; then
    name=${BASH_REMATCH[1]}; size=${BASH_REMATCH[2]}; sum=${BASH_REMATCH[3]}
    if [[ $name =~ $res_re ]] && [ -z "$RESULT_NAME" ]; then
      RESULT_NAME=$name; RESULT_SIZE=$size; RESULT_SHA=$sum
    else
      echo "    (the helper printed a file name this script does not accept: $name; ignored)" >&2
    fi
  elif [ "$line" = "REQUEST-REMOVED $REQUEST_NAME" ] && [ "$REMOVED" -eq 0 ]; then
    REMOVED=1
  else
    echo "    (the helper printed a line this script does not recognise; ignored)" >&2
  fi
done <<<"$OUT"

# 5. The result.
say "5/5 the result"
if [ -n "$RESULT_NAME" ]; then
  [ -e "$OUT_ROOT" ] || mkdir -m 700 "$OUT_ROOT"
  [ -d "$OUT_ROOT" ] && [ ! -L "$OUT_ROOT" ] || fail "$OUT_ROOT is not a directory"
  # mktemp -d makes a new directory of mode 0700 with a unique name, so a rerun never collides.
  DEST=$(mktemp -d "$OUT_ROOT/$(date -u +%Y%m%dT%H%M%SZ).XXXXXX") || fail "cannot make a directory under $OUT_ROOT"
  SRC="$EXPORT_DIR/$RESULT_NAME"
  [ -f "$SRC" ] && [ ! -L "$SRC" ] && [ "$(stat -c %u "$SRC")" = "$(id -u)" ] \
    && [ "$(stat -c %s "$SRC")" = "$RESULT_SIZE" ] && [ "$(sha_of "$SRC")" = "$RESULT_SHA" ] \
    || fail "$SRC is not the file the helper wrote. It stays in $EXPORT_DIR."
  install -m 600 -- "$SRC" "$DEST/$RESULT_NAME"
  [ "$(sha_of "$DEST/$RESULT_NAME")" = "$RESULT_SHA" ] || fail "the copy $DEST/$RESULT_NAME does not match. It stays in $EXPORT_DIR."
  rm -f -- "$SRC"
  echo "    $DEST/$RESULT_NAME"
  say "what the command printed"
  cat -- "$DEST/$RESULT_NAME"
fi

if [ "$RC" -ne 0 ]; then
  echo >&2
  echo "FAILED: the helper exited $RC (its message is above)." >&2
  echo "The change MAY have been applied. The request is still in $EXPORT_DIR; it is only removed after a success." >&2
  echo "Run cutover-blockers-list.sh to see what is left before you try again." >&2
  exit 1
fi
[ "$REMOVED" -eq 1 ] || fail "the helper succeeded but did not report removing the request; look in $EXPORT_DIR"
[ ! -e "$IN_PLACE" ] || fail "the helper reported removing the request but $IN_PLACE is still there"
say "done: applied; the request was removed from $EXPORT_DIR. Delete your own copy of the request file (it holds the value), then run cutover-blockers-list.sh to see what is left."
