#!/usr/bin/env bash
# file: docs/agent-tasks/dedup-intro-falsepositive/run.sh
# version: 1.0.0
# guid: 0a1b2c3d-8304-4021-9c63-9e1f30417293
# last-edited: 2026-06-28
#
# Thin wrapper over ../run-sweep.sh for the dedup-intro-falsepositive workstream.
#   ./run.sh         # show waves, set up wave 1 (TASK-01 investigation)
#   ./run.sh 02 03 04
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"

cat <<'EOF'
dedup-intro-falsepositive waves:
  Wave 1: 01 investigate (read-only; merge FINDINGS.md first)
  Wave 2 (parallel): 02 skip-short-clip, 03 title-blocklist, 04 isbn-gate
EOF

if [[ $# -gt 0 ]]; then
  exec "$HERE/../run-sweep.sh" "$WS" "$@"
fi
echo; echo "Setting up Wave 1 (01 investigate)…"; echo
exec "$HERE/../run-sweep.sh" "$WS" 01
