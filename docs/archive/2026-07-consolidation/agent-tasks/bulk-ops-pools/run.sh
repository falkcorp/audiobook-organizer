#!/usr/bin/env bash
# file: docs/agent-tasks/bulk-ops-pools/run.sh
# version: 1.0.0
# guid: 834c309e-03f7-4805-a6f9-be67c727c7ec
# last-edited: 2026-07-05
#
# Thin wrapper for the bulk-ops-pools workstream. See orchestration.md for wave order
# (some tasks share files and must serialize).
#   ./run.sh            # print task list + set up worktrees
#   ./run.sh 01 03      # subset (two-digit task numbers)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"
echo "Workstream: $WS — see orchestration.md for wave order before running tasks in parallel."
if [ -x "$HERE/../run-sweep.sh" ]; then
  exec "$HERE/../run-sweep.sh" "$WS" "$@"
fi
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
BRANCH_BASE="origin/main"
for NN in "$@"; do
  BRIEF=$(ls "$HERE"/TASK-"$NN"-*.md 2>/dev/null | head -1) || { echo "no brief TASK-$NN"; continue; }
  SLUG=$(basename "$BRIEF" .md | sed 's/^TASK-[0-9]*-//')
  git -C "$REPO" worktree add "$REPO/.worktrees/$WS-$SLUG" -b "agent/$WS-$SLUG" "$BRANCH_BASE" 2>/dev/null || true
  {
    echo "You are an autonomous coding agent. Execute this task exactly. Do not skip the START HERE setup. Stop and report if any acceptance criterion fails."
    echo; cat "$BRIEF"
  } > "$HERE/TASK-$NN.agent-prompt.txt"
  echo "prepared TASK-$NN -> worktree .worktrees/$WS-$SLUG + TASK-$NN.agent-prompt.txt"
done
if [ $# -eq 0 ]; then ls "$HERE"/TASK-*.md; fi
