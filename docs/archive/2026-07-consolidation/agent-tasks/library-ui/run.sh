#!/usr/bin/env bash
# file: docs/agent-tasks/library-ui/run.sh
# version: 1.0.0
# guid: d73afdf0-9573-44d4-9bd5-25bfe7b71add
# last-edited: 2026-07-01
#
# Thin wrapper over ../run-sweep.sh for the library-ui workstream.
# See orchestration.md for wave order (TASK-02/03/04 share Library.tsx and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 04      # wave 1 (parallel)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
