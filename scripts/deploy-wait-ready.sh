#!/usr/bin/env bash
# file: scripts/deploy-wait-ready.sh
# version: 2.1.0
# guid: 08d3f99b-10a7-4a18-b6b6-c092d9177c08
# last-edited: 2026-11-09
#
# Wait for a restarted server to report "ready":true on /health.
# Usage: deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT [PREVIOUS_INVOCATION]]
# With SSH_HOST it also reads the systemd unit over ssh: activating is
# progress (TIMEOUT counts from when the unit leaves it), failed, inactive or
# a rising NRestarts fails at once, and exit 78 (the binary refused the store)
# is reported by name. With PREVIOUS_INVOCATION (the unit's InvocationID read
# before the restart) it never calls the process that was already running
# ready: a restart that never ran fails as "not restarted". Without SSH_HOST (Docker, launchd) it polls the URL
# only, for at least DEPLOY_READY_URL_TIMEOUT (default 1800 s).
# Exit: 0 ready, 1 not ready, 2 bad answer, 78 refused the store.
# The logic is Python (repo rule: shell only for small scripts); the details
# are in scripts/deploy_wait_ready.py.
set -euo pipefail
exec python3 "$(dirname "$0")/deploy_wait_ready.py" "$@"
