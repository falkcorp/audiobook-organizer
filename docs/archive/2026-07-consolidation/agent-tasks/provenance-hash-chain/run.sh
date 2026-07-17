#!/usr/bin/env bash
# file: docs/agent-tasks/provenance-hash-chain/run.sh
# version: 1.0.0
# guid: db951fd2-71f6-7971-a6d3-570373b1f44a
# last-edited: 2026-07-01
#
# Thin wrapper over ../run-sweep.sh for the provenance-hash-chain workstream.
# See orchestration.md for wave order (some tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 03      # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
