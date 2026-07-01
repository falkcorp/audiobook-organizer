#!/usr/bin/env bash
# file: docs/agent-tasks/ai-responses-migration/run.sh
# version: 1.0.0
# guid: f8707923-532b-60ea-2276-acd7a825c02a
# last-edited: 2026-07-01
#
# Thin wrapper over ../run-sweep.sh for the ai-responses-migration workstream.
# See orchestration.md for wave order (some tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 03      # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
