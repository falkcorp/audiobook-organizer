#!/usr/bin/env bash
# file: docs/agent-tasks/run-sweep.sh
# version: 1.4.0
# guid: 9e0f1a2b-4c3d-4e60-af71-2b3c4d5e6f70
# last-edited: 2026-08-12
#
# Portable coordinator helper: for each requested TASK in a workstream, create an
# isolated git worktree branched off a fresh origin/main and emit a ready-to-paste
# agent prompt file. Tool-agnostic — it does NOT call any specific AI CLI; you
# paste the generated prompt into whatever agent you use.
#
# Usage:
#   ./run-sweep.sh <workstream> [TASK-id ...]
#   ./run-sweep.sh transcription-matching            # all tasks in the workstream
#   ./run-sweep.sh transcription-matching 01 05      # only TASK-01 and TASK-05
#
# It NEVER pushes or merges. After agents finish, you (the coordinator) gate on
# `go build ./... && go test`, then push/PR/merge, then rebase siblings.
# See ORCHESTRATION.md.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Repo root = three levels up from docs/agent-tasks/run-sweep.sh
REPO="$(cd "$HERE/../.." && pwd)"
WORKSTREAM="${1:-}"

if [[ -z "$WORKSTREAM" || ! -d "$HERE/$WORKSTREAM" ]]; then
  echo "usage: $0 <workstream> [TASK-id ...]" >&2
  echo "workstreams:" >&2
  find "$HERE" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -exec basename {} \; |
    sed 's/^/  /' >&2
  exit 1
fi
shift || true

# Resolve the task files: explicit ids, or all TASK-*.md in the workstream.
declare -a TASK_FILES
if [[ $# -gt 0 ]]; then
  for id in "$@"; do
    f=$(find "$HERE/$WORKSTREAM" -maxdepth 1 -name "TASK-${id}-*.md" | head -1)
    [[ -n "$f" ]] || { echo "no TASK-${id}-*.md in $WORKSTREAM" >&2; exit 1; }
    TASK_FILES+=("$f")
  done
else
  while IFS= read -r f; do TASK_FILES+=("$f"); done \
    < <(find "$HERE/$WORKSTREAM" -maxdepth 1 -name 'TASK-*.md' | sort)
fi

# A workstream with no TASK-*.md files must be an ERROR, not a quiet success.
#
# Four of the ten live packages (community-fingerprint-index, workflow-system,
# responses-api-migration, error-correction-2026-07) hold their work in
# AWAIT-APPROVAL.md, HOLD-STATUS.md or a TASKS.md with inline tasks — not in
# TASK-*.md files. For those, the loop below simply iterated zero times and the
# script went on to print "Next steps (coordinator)" as though it had done its
# job. `set -e` cannot catch this: iterating an empty list is not a failure, and
# every individual command succeeded.
#
# The failure mode was therefore indistinguishable from "this workstream has
# nothing to do" — the operator saw a clean run and no worktrees, and had no way
# to tell whether that meant done or unparseable.
if [[ ${#TASK_FILES[@]} -eq 0 ]]; then
  {
    echo "ERROR: no TASK-*.md files in workstream '$WORKSTREAM' — nothing to drive."
    echo
    echo "This script only understands workstreams built from TASK-*.md briefs."
    echo "'$WORKSTREAM' contains:"
    find "$HERE/$WORKSTREAM" -maxdepth 1 -type f -name '*.md' -exec basename {} \; |
      sort | sed 's/^/  /'
    echo
    for alt in AWAIT-APPROVAL.md HOLD-STATUS.md TASKS.md; do
      if [[ -f "$HERE/$WORKSTREAM/$alt" ]]; then
        case "$alt" in
          AWAIT-APPROVAL.md)
            echo "  ⇒ $alt: this workstream is gated on a human approval that has not been"
            echo "    given. Read it and resolve the gate before sweeping." ;;
          HOLD-STATUS.md)
            echo "  ⇒ $alt: this workstream is on an explicit hold. Read it and confirm the"
            echo "    hold is lifted before sweeping." ;;
          TASKS.md)
            echo "  ⇒ $alt: tasks are inline in a single file rather than one brief per file."
            echo "    Run them by hand, or split them into TASK-NN-*.md first." ;;
        esac
      fi
    done
    echo
    echo "If you expected briefs here, check the workstream name for a typo."
  } >&2
  exit 2
fi

git -C "$REPO" fetch origin -q

PROMPT_DIR="$HERE/.prompts"
mkdir -p "$PROMPT_DIR"

for task in "${TASK_FILES[@]}"; do
  base="$(basename "$task" .md)"            # e.g. TASK-01-search-path-hints
  slug="${WORKSTREAM}-${base#TASK-*-}"      # e.g. transcription-matching-search-path-hints
  slug="$(echo "$slug" | tr -cs 'a-zA-Z0-9-' '-' | sed 's/-\+/-/g;s/^-//;s/-$//')"
  branch="agent/${slug}"
  wt="$REPO/.worktrees/${slug}"

  if git -C "$REPO" worktree list --porcelain | grep -q "worktree $wt$"; then
    echo "• worktree exists: $wt (skipping create)"
  elif git -C "$REPO" show-ref --verify --quiet "refs/heads/$branch"; then
    git -C "$REPO" worktree add "$wt" "$branch" >/dev/null
    echo "• created worktree $wt on existing $branch"
  else
    git -C "$REPO" worktree add "$wt" -b "$branch" origin/main >/dev/null
    echo "• created worktree $wt on $branch"
  fi
  git -C "$wt" rebase origin/main -q || true

  prompt="$PROMPT_DIR/${slug}.agent-prompt.txt"
  {
    echo "You are an autonomous coding agent. Execute the task below EXACTLY."
    echo "Work ONLY inside this worktree: $wt"
    echo "Your branch ($branch) is already created and rebased on origin/main."
    echo "Do NOT run git push, gh, or merge — when finished, STOP and report a"
    echo "summary plus the exact files you changed. Stop immediately and report if"
    echo "any acceptance criterion fails."
    echo
    echo "===== TASK ($base) ====="
    cat "$task"
  } > "$prompt"
  echo "  → prompt: $prompt"
done

echo
echo "Next steps (coordinator):"
echo "  1. Paste each .agent-prompt.txt into a separate agent session."
echo "  2. When an agent reports done:  cd <worktree> && go build ./... && go test ./<pkgs>/ -count=1"
echo "  3. If green:  git -C <worktree> push -u origin <branch> && gh pr create --fill && gh pr merge <n> --rebase"
echo "  4. After each merge, rebase open siblings:  for wt in $REPO/.worktrees/*; do git -C \"\$wt\" fetch origin main -q && git -C \"\$wt\" rebase origin/main; done"
echo "  5. Clean up merged worktrees:  git -C $REPO worktree remove <worktree>"
