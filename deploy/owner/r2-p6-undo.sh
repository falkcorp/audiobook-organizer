#!/usr/bin/env bash
# file: deploy/owner/r2-p6-undo.sh
# version: 0.1.0
# guid: 9c4e7b12-0a85-4d36-b7f9-5e1d3a8c6f20
# last-edited: 2026-11-22
#
# Undo of R2 precondition P6 (r2-p6-reinstall.sh): put the PREVIOUS rollback helper
# bundle back, so the host's rollback.conf and sudoers drop-in are what they were
# before the P6 reinstall:
#   - offline_env goes back to `HOME=<state_dir> ABK_STATE_DIR=<state_dir>`,
#   - export_dir leaves rollback.conf (no --export-dir),
#   - the four export and cutover-blockers helpers are no longer granted in sudoers
#     (their stubs stay on disk, ungranted), and
#   - the export directory is removed when it is empty.
#
# The owner runs:   bash ~/aorg-owner/r2-p6-undo.sh
# It asks for the sudo password once (the installer runs as root). Safe to run
# again: every step checks before it acts, and the installer is idempotent.
#
#   --dry-run   do everything except the sudo install (the installer's own dry run).
#
# Why PIN below is the PREVIOUS commit, not the one P6 installed: the installer
# renders the bundle of the checkout it runs from. The new bundle's helper accepts
# only ABO_* names in offline_env; the previous bundle's helper accepts only HOME=,
# ABK_STATE_DIR= and ABK_* and refuses any ABO_* token. So the previous value can
# only be installed by the previous commit's installer, and the previous commit's
# installer does not check the value, which is why this script passes it explicitly.
# PIN is the main commit that was deployed before the one P6 used.
#
# The cost of this undo: ABK_STATE_DIR is back, which the flipped binary refuses, so
# `make rollback-restore` fails again on a flipped host until P6 is run again.
#
# Exits non-zero on any failure, before or after the install.

set -euo pipefail

PIN="__PIN__"   # the main commit whose bundle was installed before P6
SRC="$HOME/aorg-owner/src"
CONF=/etc/audiobook-organizer/rollback.conf
UNIT_NAME=audiobook-organizer.service
REPO=https://github.com/falkcorp/audiobook-organizer.git
STUBS=(export-keyring export-install-secrets cutover-blockers-list cutover-blockers-apply)

DRY=0
case "${1:-}" in
  "") ;;
  --dry-run) DRY=1 ;;
  *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
esac

say()  { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAILED: %s\n' "$*" >&2; exit 1; }

[ "$PIN" != "__PIN__" ] || fail "this copy has no pinned commit; ask for the current script"
[ "$(id -u)" -ne 0 ] || fail "run this as yourself, not with sudo; it asks for sudo when it needs it"

# 1. The checkout the previous bundle is installed from, at exactly the pinned commit.
say "1/5 checkout at $PIN"
if [ ! -d "$SRC/.git" ]; then
  git clone -q --filter=blob:none "$REPO" "$SRC"
fi
git -C "$SRC" fetch -q origin main
git -C "$SRC" cat-file -e "$PIN^{commit}" || fail "commit $PIN is not on origin/main"
git -C "$SRC" merge-base --is-ancestor "$PIN" origin/main || fail "commit $PIN is not on origin/main"
git -C "$SRC" checkout -q --detach "$PIN"
[ "$(git -C "$SRC" rev-parse HEAD)" = "$(git -C "$SRC" rev-parse "$PIN^{commit}")" ] || fail "checkout is not at $PIN"
[ -z "$(git -C "$SRC" status --porcelain --untracked-files=no)" ] || fail "$SRC has local changes; it must match $PIN exactly"
echo "    $SRC is at $(git -C "$SRC" rev-parse --short HEAD)"

# 2. The host's values, read from the installed rollback.conf (the P6 one).
say "2/5 values from $CONF"
[ -r "$CONF" ] || fail "$CONF is missing or unreadable"
val() { sed -n "s/^$1[[:space:]]*=[[:space:]]*//p" "$CONF" | tail -n 1; }
BIN=$(val bin); DB=$(val db); STATE=$(val state_dir); UNIT=$(val unit)
SVC=$(val service_user); ARCH=$(val archive_dir); SBIN=$(val stage_bin); SLC=$(val stage_local_conf)
CKPT=$(val checkpoint_dir); SGRP=$(val service_group); EXPORT_DIR=$(val export_dir)
for v in BIN DB STATE UNIT SVC ARCH SBIN SLC; do
  [ -n "${!v}" ] || fail "$CONF has no value for ${v}"
  case "${!v}" in *[[:space:]\'\"\;\$\`]*) fail "$CONF value for ${v} has a character this script will not pass on";; esac
done
case "$EXPORT_DIR" in *[[:space:]\'\"\;\$\`]*) fail "$CONF value for export_dir has a character this script will not pass on";; esac
[ "$UNIT" = "$UNIT_NAME" ] || echo "    note: unit is $UNIT"
# No --export-dir, and the previous offline_env, passed explicitly.
ARGS="--deploy-user $(id -un) --service-user $SVC --state-dir $STATE --archive-dir $ARCH"
ARGS="$ARGS --stage-bin $SBIN --stage-local-conf $SLC"
[ -z "$CKPT" ] || ARGS="$ARGS --checkpoint-dir $CKPT"
[ -z "$SGRP" ] || ARGS="$ARGS --service-group $SGRP"
ARGS="$ARGS --offline-env 'HOME=$STATE ABK_STATE_DIR=$STATE'"
echo "    bin=$BIN db=$DB unit=$UNIT service_user=$SVC state_dir=$STATE export_dir=${EXPORT_DIR:-none}"

# 3. The installer's own dry run: shows every change, changes nothing.
say "3/5 installer dry run (no sudo)"
make -C "$SRC" --no-print-directory install-rollback-helpers REPLACE_CONF=1 \
  DEPLOY_BIN="$BIN" DEPLOY_DB="$DB" DEPLOY_UNIT="$UNIT" ROLLBACK_INSTALL_ARGS="$ARGS"

if [ "$DRY" -eq 1 ]; then
  say "dry run only: nothing was installed"
  exit 0
fi

# 4. The install (sudo).
say "4/5 install (asks for your sudo password)"
make -C "$SRC" --no-print-directory install-rollback-helpers APPLY=1 REPLACE_CONF=1 \
  DEPLOY_BIN="$BIN" DEPLOY_DB="$DB" DEPLOY_UNIT="$UNIT" ROLLBACK_INSTALL_ARGS="$ARGS"

# 5. The export directory, then proof the undo took.
say "5/5 export directory and checks"
if [ -n "$EXPORT_DIR" ] && [ -d "$EXPORT_DIR" ] && [ ! -L "$EXPORT_DIR" ]; then
  if rmdir "$EXPORT_DIR" 2>/dev/null; then
    echo "    removed the empty export directory $EXPORT_DIR"
  else
    echo "    note: $EXPORT_DIR is not empty, so it was left in place; look at what is in it"
  fi
fi
bad=0
if grep -q "^export_dir" "$CONF"; then
  echo "    STILL THERE: export_dir in $CONF"; bad=1
else
  echo "    ok: rollback.conf has no export_dir"
fi
if grep -q "^offline_env = .*ABK_STATE_DIR=$STATE" "$CONF" && ! grep -q "^offline_env = .*ABO_" "$CONF"; then
  echo "    ok: offline_env names ABK_STATE_DIR only"
else
  echo "    WRONG: offline_env in $CONF:"; grep '^offline_env' "$CONF" || true; bad=1
fi
grants=$(sudo -n -l 2>/dev/null || true)
for s in "${STUBS[@]}"; do
  if printf '%s\n' "$grants" | grep -q "/usr/local/libexec/audiobook-organizer/rollback/$s \"\""; then
    echo "    STILL GRANTED: sudoers line for $s"; bad=1
  else
    echo "    ok: sudoers no longer grants $s"
  fi
done
echo "    $(systemctl show -p DropInPaths "$UNIT")"
[ "$bad" -eq 0 ] || fail "the install ran but a check above did not pass; send this output to the agents"
say "done: P6 is undone (ABK_STATE_DIR is back; make rollback-restore fails on a flipped host until P6 is run again)"
