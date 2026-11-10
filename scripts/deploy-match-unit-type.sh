#!/usr/bin/env bash
# file: scripts/deploy-match-unit-type.sh
# version: 2.0.0
# guid: 1d0e4dbc-b932-4379-a497-d08d32b6e0a9
# last-edited: 2026-11-09
#
# Make the service's Type match the binary about to run on HOST, BEFORE the
# restart. Usage: deploy-match-unit-type.sh HOST BINARY_PATH_ON_HOST
# BINARY --print-capabilities lists sd_notify: remove the Type=simple drop-in.
# Anything else (an older build says "unknown flag", or no answer): install it,
# so a binary that never sends READY=1 is not killed at TimeoutStartSec under
# the Type=notify unit. The caller restarts. The drop-in is written by the
# root-owned unit-type helper, which takes only `simple` or `notify`, writes
# fixed content and daemon-reloads: a sudo rule for `tee` into the unit's
# drop-in directory would let the deploy user write any unit setting
# (User=root, ExecStartPre=) and so be root (docs/system/deploy-and-gpu-ops.md,
# "Rollback helpers").
# Expanding $bin and $helper on this side is intended (SC2029).
# shellcheck disable=SC2029
set -euo pipefail
host="${1:?usage: deploy-match-unit-type.sh HOST BINARY}"
bin="${2:?usage: deploy-match-unit-type.sh HOST BINARY}"
helper=/usr/local/libexec/audiobook-organizer/rollback/unit-type
# Fail fast instead of hanging on an unreachable host or a password prompt.
sshq=(ssh -o ConnectTimeout=10 -o BatchMode=yes "$host")
caps=$("${sshq[@]}" "$bin --print-capabilities" 2>/dev/null) || caps=""
if grep -qx sd_notify <<<"$caps"; then
  echo "   $bin reports sd_notify: Type=notify (removing the Type=simple drop-in if present)"
  "${sshq[@]}" "sudo -n $helper notify"
else
  echo "   $bin does not report sd_notify: installing the Type=simple drop-in"
  "${sshq[@]}" "sudo -n $helper simple"
fi
