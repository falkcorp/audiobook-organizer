#!/usr/bin/env bash
# file: docs/agent-tasks/logging-slog/run.sh
# version: 1.0.0
# guid: cf2819a8-c37a-f1ac-e664-d6ecfb487ded
# last-edited: 2026-07-01
#
# Thin wrapper over ../run-sweep.sh for the logging-slog workstream.
# See orchestration.md for wave order (some tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 03      # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
