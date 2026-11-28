#!/usr/bin/env bash
# file: deploy/owner/service-stop.sh
# version: 0.1.0
# guid: 5691ef34-c12b-4a67-86ce-656676bedf2c
# last-edited: 2026-11-28
#
# R2 runbook, Step 0: stop ONLY the audiobook-organizer service, and wait until
# it is inactive.
#
# The owner runs:   bash ~/aorg-owner/service-stop.sh [--dry-run]
#
# It reads the unit name from /etc/audiobook-organizer/rollback.conf, looks at
# the unit's state, and:
#   - if it is already inactive, says so and does nothing (safe to run again);
#   - otherwise runs the ONE command the deploy user's sudoers drop-in already
#     grants, `sudo -n /usr/bin/systemctl stop <unit>` (no password, no new
#     sudoers line), and waits up to WAIT_SEC seconds until the unit is inactive.
#   --dry-run   look at the state and say what would run; run nothing.
#
# Exits non-zero on any failure.

set -euo pipefail

CONF=/etc/audiobook-organizer/rollback.conf
SYSTEMCTL=/usr/bin/systemctl
WAIT_SEC=120
POLL_SEC=2

DRY=0
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
  esac
done

say()  { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAILED: %s\n' "$*" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || fail "run this as yourself, not with sudo; only systemctl runs as root"

say "1/3 the unit"
[ -r "$CONF" ] || fail "$CONF is missing or unreadable"
UNIT=$(sed -n 's/^unit[[:space:]]*=[[:space:]]*//p' "$CONF" | tail -n 1)
[ -n "$UNIT" ] || fail "$CONF has no unit"
case "$UNIT" in
  *[!A-Za-z0-9@._-]*|-*|"") fail "$CONF value for unit has a character this script will not pass on" ;;
esac
case "$UNIT" in *.service) ;; *) fail "unit '$UNIT' is not a .service unit" ;; esac
active_state() { systemctl show -p ActiveState --value "$UNIT" 2>/dev/null || echo unreadable; }
STATE=$(active_state)
echo "    $UNIT is $STATE"
[ "$STATE" != "unreadable" ] || fail "cannot read the state of $UNIT; nothing was changed"

say "2/3 stop"
if [ "$STATE" = "inactive" ]; then
  echo "    already inactive: nothing to do"
  exit 0
fi
if [ "$DRY" -eq 1 ]; then
  say "dry run only: would run: sudo -n $SYSTEMCTL stop $UNIT and wait for inactive   (nothing was run)"
  exit 0
fi
sudo -n "$SYSTEMCTL" stop "$UNIT" || fail "systemctl stop exited non-zero; $UNIT is now $(active_state)"

say "3/3 waiting for inactive"
DEADLINE=$((SECONDS + WAIT_SEC))
while :; do
  STATE=$(active_state)
  if [ "$STATE" = "inactive" ]; then
    echo "    $UNIT is inactive"
    exit 0
  fi
  [ "$SECONDS" -lt "$DEADLINE" ] || fail "$UNIT is still $STATE after $WAIT_SEC seconds; wanted inactive"
  sleep "$POLL_SEC"
done
