#!/usr/bin/env bash
# file: docs/agent-tasks/dedup-hardening/run.sh
# version: 1.0.0
# guid: 24196b53-c4c0-32be-ab51-18b12f8a973a
# last-edited: 2026-07-01
#
# Thin wrapper over ../run-sweep.sh for the dedup-hardening workstream.
# See orchestration.md for wave order (some tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 03      # subset
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
exec "$HERE/../run-sweep.sh" "$WS" "$@"
