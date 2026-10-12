#!/usr/bin/env bash
# file: scripts/deploy-match-unit-type.sh
# version: 1.0.0
# guid: 1d0e4dbc-b932-4379-a497-d08d32b6e0a9
# last-edited: 2026-10-12
#
# Make the service's Type match the binary about to run on HOST, BEFORE the
# restart. Usage: deploy-match-unit-type.sh HOST BINARY_PATH_ON_HOST
# BINARY --print-capabilities lists sd_notify: remove the Type=simple drop-in.
# Anything else (an older build says "unknown flag", or no answer): install it,
# so a binary that never sends READY=1 is not killed at TimeoutStartSec under
# the Type=notify unit. Then daemon-reload; the caller restarts. Uses sudo -n
# (passwordless rules in docs/system/deploy-and-gpu-ops.md).
# Expanding $bin, $dir and $dropin on this side is intended (SC2029).
# shellcheck disable=SC2029
set -euo pipefail
host="${1:?usage: deploy-match-unit-type.sh HOST BINARY}"
bin="${2:?usage: deploy-match-unit-type.sh HOST BINARY}"
dir=/etc/systemd/system/audiobook-organizer.service.d
dropin="$dir/zz-rollback-type-simple.conf"
caps=$(ssh "$host" "$bin --print-capabilities" 2>/dev/null) || caps=""
if grep -qx sd_notify <<<"$caps"; then
  echo "   $bin reports sd_notify: Type=notify (removing $dropin if present)"
  ssh "$host" "sudo -n rm -f $dropin"
else
  echo "   $bin does not report sd_notify: installing $dropin (Type=simple)"
  ssh "$host" "sudo -n mkdir -p $dir && printf '[Service]\\nType=simple\\n' | sudo -n tee $dropin >/dev/null"
fi
ssh "$host" "sudo -n systemctl daemon-reload"
