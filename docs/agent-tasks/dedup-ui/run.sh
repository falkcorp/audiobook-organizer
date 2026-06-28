#!/usr/bin/env bash
# file: docs/agent-tasks/dedup-ui/run.sh
# version: 1.0.0
# guid: 82930415-6172-4809-9e41-769708192061
# last-edited: 2026-06-28
#
# Thin wrapper over ../run-sweep.sh for the dedup-ui workstream.
# All five tasks are independent — one wave.
#   ./run.sh            # set up all five
#   ./run.sh 01 03 05   # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"

cat <<'EOF'
dedup-ui: all five tasks independent (one wave, up to 5 parallel):
  01 bookdedup-row-redesign   02 metadata-compare-tab   03 manual-import-button
  04 label-review-panel       05 keyboard-shortcuts
Gate each with: cd web && npm run build && npm test
EOF

echo
exec "$HERE/../run-sweep.sh" "$WS" "$@"
