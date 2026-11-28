#!/usr/bin/env bash
# file: deploy/owner/r2-p6-reinstall.sh
# version: 0.1.0
# guid: 6b2f0c1e-7d43-4f8a-9a51-2e8c4b7d90a3
# last-edited: 2026-11-22
#
# R2 precondition P6: reinstall the rollback helper bundle so that
#   - offline_env names the state directory as ABO_CORE_PATHS_STATE_DIR (the
#     flipped binary refuses ABK_STATE_DIR, so `make rollback-restore` fails until
#     this runs), and
#   - export_dir exists, which the four new no-argument helpers need
#     (export-keyring, export-install-secrets, cutover-blockers-list,
#     cutover-blockers-apply).
#
# The owner runs:   bash ~/aorg-owner/r2-p6-reinstall.sh
# It asks for the sudo password once (the installer runs as root). Safe to run
# again: every step checks before it acts, and the installer is idempotent.
#
# Before the install it saves what P6 replaces, once, in ~/aorg-owner/p6-before/
# (mode 0700): the sha256 of the installed rollback_helper.py, a copy of
# rollback.conf, and the text of `sudo -n -l`. r2-p6-undo.sh puts the previous
# bundle back and refuses unless its checkout is the one whose helper has that
# hash. A rerun keeps the saved copy; it never overwrites it with the P6 state.
#
#   --dry-run   do everything except the sudo install (the installer's own
#               dry run); this is what the agents run before handing it over.
#
# Exits non-zero on any failure, before or after the install.

set -euo pipefail

PIN="__PIN__"   # the main commit that carries #5052 and the cutover-blockers PR
SRC="$HOME/aorg-owner/src"
EXPORT_DIR="$HOME/aorg-owner/export"
SAVED="$HOME/aorg-owner/p6-before"
LIBDIR=/usr/local/libexec/audiobook-organizer/rollback
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

# 1. The checkout the bundle is installed from, at exactly the pinned commit.
say "1/5 checkout at $PIN"
if [ ! -d "$SRC/.git" ]; then
  git clone -q --filter=blob:none "$REPO" "$SRC"
fi
git -C "$SRC" fetch -q origin main
git -C "$SRC" cat-file -e "$PIN^{commit}" || fail "commit $PIN is not on origin/main yet"
git -C "$SRC" merge-base --is-ancestor "$PIN" origin/main || fail "commit $PIN is not on origin/main"
git -C "$SRC" checkout -q --detach "$PIN"
[ "$(git -C "$SRC" rev-parse HEAD)" = "$(git -C "$SRC" rev-parse "$PIN^{commit}")" ] || fail "checkout is not at $PIN"
[ -z "$(git -C "$SRC" status --porcelain --untracked-files=no)" ] || fail "$SRC has local changes; it must match $PIN exactly"
echo "    $SRC is at $(git -C "$SRC" rev-parse --short HEAD)"

# 2. The export directory: yours, mode 0700, no symlink.
say "2/5 export directory $EXPORT_DIR"
if [ -L "$EXPORT_DIR" ]; then fail "$EXPORT_DIR is a symlink"; fi
if [ ! -e "$EXPORT_DIR" ]; then mkdir -m 700 "$EXPORT_DIR"; fi
[ -d "$EXPORT_DIR" ] || fail "$EXPORT_DIR is not a directory"
[ "$(stat -c %u "$EXPORT_DIR")" = "$(id -u)" ] || fail "$EXPORT_DIR is not owned by $(id -un)"
[ "$(stat -c %a "$EXPORT_DIR")" = "700" ] || chmod 700 "$EXPORT_DIR"
[ "$(stat -c %a "$EXPORT_DIR")" = "700" ] || fail "$EXPORT_DIR is not mode 0700"
echo "    ok: owned by $(id -un), mode 0700"

# 3. The host's values, read from the installed rollback.conf.
say "3/5 values from $CONF"
[ -r "$CONF" ] || fail "$CONF is missing or unreadable"
val() { sed -n "s/^$1[[:space:]]*=[[:space:]]*//p" "$CONF" | tail -n 1; }
BIN=$(val bin); DB=$(val db); STATE=$(val state_dir); UNIT=$(val unit)
SVC=$(val service_user); ARCH=$(val archive_dir); SBIN=$(val stage_bin); SLC=$(val stage_local_conf)
CKPT=$(val checkpoint_dir); SGRP=$(val service_group)
for v in BIN DB STATE UNIT SVC ARCH SBIN SLC; do
  [ -n "${!v}" ] || fail "$CONF has no value for ${v}"
  case "${!v}" in *[[:space:]\'\"\;\$\`]*) fail "$CONF value for ${v} has a character this script will not pass on";; esac
done
[ "$UNIT" = "$UNIT_NAME" ] || echo "    note: unit is $UNIT"
ARGS="--deploy-user $(id -un) --service-user $SVC --state-dir $STATE --archive-dir $ARCH"
ARGS="$ARGS --stage-bin $SBIN --stage-local-conf $SLC"
[ -z "$CKPT" ] || ARGS="$ARGS --checkpoint-dir $CKPT"
[ -z "$SGRP" ] || ARGS="$ARGS --service-group $SGRP"
ARGS="$ARGS --offline-env 'HOME=$STATE ABO_CORE_PATHS_STATE_DIR=$STATE' --export-dir $EXPORT_DIR"
echo "    bin=$BIN db=$DB unit=$UNIT service_user=$SVC state_dir=$STATE"

# 4. The installer's own dry run: shows every change, changes nothing.
say "4/5 installer dry run (no sudo)"
make -C "$SRC" --no-print-directory install-rollback-helpers REPLACE_CONF=1 \
  DEPLOY_BIN="$BIN" DEPLOY_DB="$DB" DEPLOY_UNIT="$UNIT" ROLLBACK_INSTALL_ARGS="$ARGS"

if [ "$DRY" -eq 1 ]; then
  if [ -e "$SAVED/complete" ]; then
    echo "    the pre-P6 state is already saved in $SAVED; a real run keeps it"
  else
    echo "    a real run saves the pre-P6 state in $SAVED first (helper hash, rollback.conf, sudo -l)"
  fi
  say "dry run only: nothing was installed"
  exit 0
fi

# The pre-P6 state, saved once: what r2-p6-undo.sh checks its checkout against and
# diffs the restored rollback.conf with. An earlier save is never overwritten.
say "saving the pre-P6 state in $SAVED"
if [ -e "$SAVED/complete" ]; then
  echo "    already saved by an earlier run: kept"
else
  [ ! -e "$SAVED" ] && [ ! -L "$SAVED" ] || fail "$SAVED exists but is not a complete save; look at it, move it away, and run this again"
  TMPSAVE=$(mktemp -d "$SAVED.tmp.XXXXXX")
  if [ -r "$LIBDIR/rollback_helper.py" ]; then
    sha256sum -- "$LIBDIR/rollback_helper.py" | cut -d' ' -f1 > "$TMPSAVE/helper.sha256"
  else
    echo absent > "$TMPSAVE/helper.sha256"
  fi
  [ -s "$TMPSAVE/helper.sha256" ] || fail "could not hash the installed helper"
  install -m 600 -- "$CONF" "$TMPSAVE/rollback.conf"
  (sudo -n -l 2>/dev/null || true) > "$TMPSAVE/sudo-l.txt"
  chmod 600 "$TMPSAVE/helper.sha256" "$TMPSAVE/sudo-l.txt"
  touch "$TMPSAVE/complete"
  mv -- "$TMPSAVE" "$SAVED"
  echo "    saved: helper sha256 $(cat "$SAVED/helper.sha256"), rollback.conf, sudo -l"
fi

# 5. The install (sudo), then proof it took.
say "5/5 install (asks for your sudo password)"
make -C "$SRC" --no-print-directory install-rollback-helpers APPLY=1 REPLACE_CONF=1 \
  DEPLOY_BIN="$BIN" DEPLOY_DB="$DB" DEPLOY_UNIT="$UNIT" ROLLBACK_INSTALL_ARGS="$ARGS"

say "checks"
bad=0
if grep -q "^export_dir = $EXPORT_DIR\$" "$CONF"; then
  echo "    ok: rollback.conf has export_dir = $EXPORT_DIR"
else
  echo "    MISSING: export_dir in $CONF"; bad=1
fi
if grep -q "^offline_env = .*ABO_CORE_PATHS_STATE_DIR=$STATE" "$CONF" && ! grep -q "^offline_env = .*ABK_STATE_DIR" "$CONF"; then
  echo "    ok: offline_env names ABO_CORE_PATHS_STATE_DIR only"
else
  echo "    WRONG: offline_env in $CONF:"; grep '^offline_env' "$CONF" || true; bad=1
fi
grants=$(sudo -n -l 2>/dev/null || true)
for s in "${STUBS[@]}"; do
  if printf '%s\n' "$grants" | grep -q "/usr/local/libexec/audiobook-organizer/rollback/$s \"\""; then
    echo "    ok: sudoers grants $s with no arguments"
  else
    echo "    MISSING: sudoers line for $s"; bad=1
  fi
done
echo "    $(systemctl show -p DropInPaths "$UNIT")"
[ "$bad" -eq 0 ] || fail "the install ran but a check above did not pass; send this output to the agents"
say "done: P6 is in place"
