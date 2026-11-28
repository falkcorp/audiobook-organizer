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
# again after a FAILURE: every step checks before it acts, and the installer is
# idempotent. After a success it refuses (the save is retired, nothing to put back).
#
#   --dry-run   do everything except the sudo install (the installer's own dry run).
#
# Why PIN below is NOT the commit P6 installed: the installer renders the bundle of
# the checkout it runs from, so the previous bundle can only be put back from the
# commit that bundle came from. That is the commit whose bundle was installed on the
# host when P6 ran, which is not necessarily the commit deployed before P6. The
# helper accepts HOME=, ABK_* and ABO_* names in offline_env and refuses only
# ABK_STATE_DIR together with an ABO_ state directory, so the value is passed
# explicitly (`HOME=<state_dir> ABK_STATE_DIR=<state_dir>`).
#
# What it checks, so a wrong PIN cannot pass for the right one: r2-p6-reinstall.sh
# saved the pre-P6 state in ~/aorg-owner/p6-before/ (the sha256 of the installed
# rollback_helper.py, rollback.conf, sudo -l). This script refuses, before any sudo and
# under --dry-run too, unless the helper in the PIN checkout hashes to the saved
# value. After the install it diffs rollback.conf against the saved copy and fails on
# any difference, and it fails when `sudo -n -l` prints nothing or no restore-offline
# line (an empty listing proves nothing). On success the save is renamed to
# p6-before.undone-<UTC stamp>, so a second P6 saves a fresh one.
#
# The cost of this undo: ABK_STATE_DIR is back, which the flipped binary refuses, so
# `make rollback-restore` fails again on a flipped host until P6 is run again.
#
# Exits non-zero on any failure, before or after the install.

set -euo pipefail

PIN="__PIN__"   # the main commit whose bundle was installed on the host when P6 ran
SRC="$HOME/aorg-owner/src"
SAVED="$HOME/aorg-owner/p6-before"
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
# The checkout must be the commit whose bundle P6 replaced: its helper must hash to
# the value r2-p6-reinstall.sh saved before it installed anything.
[ -e "$SAVED/complete" ] || fail "no saved pre-P6 state in $SAVED: r2-p6-reinstall.sh saves it before its install, so there is nothing here to put back"
WANT_SHA=$(cat "$SAVED/helper.sha256")
[ "$WANT_SHA" != "absent" ] || fail "no rollback helper was installed before P6 (the saved hash is 'absent'), so there is no previous bundle to put back"
GOT_SHA=$(sha256sum -- "$SRC/deploy/rollback-helpers/rollback_helper.py" | cut -d' ' -f1)
[ "$GOT_SHA" = "$WANT_SHA" ] || fail "the helper in $PIN (sha256 $GOT_SHA) is not the one installed before P6 (sha256 $WANT_SHA). PIN must be the commit whose bundle was installed when P6 ran. Nothing was changed."
echo "    ok: the helper in this checkout is the one P6 replaced (sha256 $WANT_SHA)"

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
# An empty or partial listing proves nothing: the host's own grants must be in it.
if ! printf '%s\n' "$grants" | grep -q "/usr/local/libexec/audiobook-organizer/rollback/restore-offline"; then
  echo "    CANNOT TELL: 'sudo -n -l' printed no restore-offline line, so the listing is empty or incomplete and cannot show that the four helpers are no longer granted"; bad=1
fi
for s in "${STUBS[@]}"; do
  if printf '%s\n' "$grants" | grep -q "/usr/local/libexec/audiobook-organizer/rollback/$s \"\""; then
    echo "    STILL GRANTED: sudoers line for $s"; bad=1
  else
    echo "    ok: sudoers no longer grants $s"
  fi
done
if diff -u "$SAVED/rollback.conf" "$CONF"; then
  echo "    ok: rollback.conf is what it was before P6"
else
  echo "    WRONG: rollback.conf above differs from the copy saved before P6 ($SAVED/rollback.conf)"; bad=1
fi
echo "    $(systemctl show -p DropInPaths "$UNIT")"
[ "$bad" -eq 0 ] || fail "the install ran but a check above did not pass; send this output to the agents"
mv -- "$SAVED" "$SAVED.undone-$(date -u +%Y%m%dT%H%M%SZ)"
say "done: P6 is undone (ABK_STATE_DIR is back; make rollback-restore fails on a flipped host until P6 is run again)"
