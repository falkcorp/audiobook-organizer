#!/usr/bin/env bash
# file: docs/agent-tasks/transcription-matching/run.sh
# version: 1.0.0
# guid: 94b5c61d-8203-44c6-9d72-4d5e6f708192
# last-edited: 2026-06-28
#
# Thin wrapper over ../run-sweep.sh for the transcription-matching workstream.
# Prints the dependency wave order, then delegates worktree+prompt creation.
#   ./run.sh            # show waves, set up wave 1 (TASK-01, TASK-05)
#   ./run.sh 02 03      # set up specific tasks by id
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"

cat <<'EOF'
transcription-matching waves (respect Depends on:):
  Wave 1 (parallel): 01 search-path-hints, 05 dedup-tiebreaker
  Wave 2 (parallel): 02 apply-auto-confirm, 03 upgrade-confidence
  Wave 3 (after 02): 04 batch-auto-match
EOF

if [[ $# -gt 0 ]]; then
  exec "$HERE/../run-sweep.sh" "$WS" "$@"
fi
echo; echo "Setting up Wave 1 (01, 05)…"; echo
exec "$HERE/../run-sweep.sh" "$WS" 01 05
