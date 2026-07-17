#!/usr/bin/env bash
# file: docs/agent-tasks/dedup-dataset/run.sh
# version: 1.0.0
# guid: 272a901d-f964-c475-085d-a18c86a709ee
# last-edited: 2026-07-01
#
# Thin wrapper over ../run-sweep.sh for the dedup-dataset workstream.
# See orchestration.md for wave order (some tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 03      # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
