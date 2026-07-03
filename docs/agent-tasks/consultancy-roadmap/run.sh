#!/usr/bin/env bash
# file: docs/agent-tasks/consultancy-roadmap/run.sh
# version: 1.0.0
# guid: 45e1a238-4e66-4de8-8f9b-6011f45243d4
# last-edited: 2026-07-03
#
# Thin wrapper over ../run-sweep.sh for the consultancy-roadmap workstream.
# See orchestration.md for wave order (many tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 04 12   # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
