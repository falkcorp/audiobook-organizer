#!/usr/bin/env bash
# file: scripts/ci/push_bot_branch.sh
# version: 1.0.0
# guid: 8c4e1b79-3a52-4d06-9b8f-e2d7a05c6143
# last-edited: 2026-10-12
#
# Usage: push_bot_branch.sh <remote-url-or-name> <branch>
#
# Force-push HEAD to <branch> on <remote>, leased against the branch's CURRENT
# remote sha (read with ls-remote; empty when the branch does not exist yet).
# A bare `--force-with-lease` against a URL has no remote-tracking ref to
# compare with, so git rejects it as "stale info" whenever the branch already
# exists -- which is exactly the state a week-old unmerged bot PR leaves behind.
set -euo pipefail

remote=${1:?remote}
branch=${2:?branch}

sha=$(git ls-remote --heads "$remote" "refs/heads/$branch" | cut -f1)
git push --force-with-lease="refs/heads/$branch:$sha" "$remote" "HEAD:refs/heads/$branch"
