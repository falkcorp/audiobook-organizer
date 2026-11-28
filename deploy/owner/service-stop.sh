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
#     sudoers line), and waits until the unit is inactive. How long comes from the
#     unit, not from a constant: its TimeoutStopUSec (300 s for the start of this
#     service, which is Type=notify) plus MARGIN_SEC. `infinity` is read as
#     FALLBACK_SEC. WAIT_SEC, when set, replaces that wait.
#   --dry-run   look at the state and say what would run; run nothing.
#
# Exits non-zero on any failure.

set -euo pipefail

CONF=/etc/audiobook-organizer/rollback.conf
SYSTEMCTL=/usr/bin/systemctl
WAIT_SEC=
MARGIN_SEC=60
FALLBACK_SEC=900
MAX_WAIT_SEC=3600
POLL_SEC=2
PROP=TimeoutStopUSec

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
# systemd prints a timespan ("5min", "1min 30s", "250ms"); this gives whole seconds,
# rounded up, nothing for "infinity", and a non-zero status for anything else.
span_seconds() {
  awk -v t="$1" 'BEGIN {
    if (t == "infinity") exit 0
    n = split(t, p, " ")
    if (n == 0) exit 1
    total = 0
    for (i = 1; i <= n; i++) {
      if (!match(p[i], /^[0-9]+(\.[0-9]+)?/)) exit 1
      num = substr(p[i], 1, RLENGTH) + 0
      u = substr(p[i], RLENGTH + 1)
      if (u == "us") f = 0.000001
      else if (u == "ms") f = 0.001
      else if (u == "s") f = 1
      else if (u == "min") f = 60
      else if (u == "h") f = 3600
      else if (u == "d") f = 86400
      else if (u == "w") f = 604800
      else if (u == "month") f = 2629800
      else if (u == "y") f = 31557600
      else exit 1
      total += num * f
    }
    printf "%d\n", (total == int(total)) ? total : int(total) + 1
  }'
}
active_state() { systemctl show -p ActiveState --value "$UNIT" 2>/dev/null || echo unreadable; }
STATE=$(active_state)
echo "    $UNIT is $STATE"
[ "$STATE" != "unreadable" ] || fail "cannot read the state of $UNIT; nothing was changed"

say "2/3 stop"
if [ "$STATE" = "inactive" ]; then
  echo "    already inactive: nothing to do"
  exit 0
fi
raw=$(systemctl show -p "$PROP" --value "$UNIT" 2>/dev/null) || fail "cannot read $PROP of $UNIT; nothing was changed"
LIMIT=$(span_seconds "$raw") || fail "cannot read $PROP '$raw' of $UNIT; nothing was changed"
if [ -z "$LIMIT" ]; then
  LIMIT=$FALLBACK_SEC
  HOW="$PROP is infinity, so $FALLBACK_SEC s"
else
  HOW="$PROP $raw"
fi
if [ -z "$WAIT_SEC" ]; then
  WAIT_SEC=$((LIMIT + MARGIN_SEC))
  [ "$WAIT_SEC" -le "$MAX_WAIT_SEC" ] || WAIT_SEC=$MAX_WAIT_SEC
fi
if [ "$DRY" -eq 1 ]; then
  say "dry run only: would run: sudo -n $SYSTEMCTL stop $UNIT ($HOW) and wait up to $WAIT_SEC s for inactive   (nothing was run)"
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
