#!/usr/bin/env bash
# file: scripts/deploy-preflight.sh
# version: 1.0.0
# guid: 3f0d9a5c-7b2e-4c81-9e6a-0d5b1c2f8a47
# last-edited: 2026-09-11
#
# Deploy pre-flight: refuse to ship unless HEAD is EXACTLY the remote main.
#
# The `deploy` and `deploy-debug` targets in Makefile.local(.example) call this
# before cross-compiling. Until 2026-09-11 (todo.d CI-01) the check they ran
# inline was
#
#     git merge-base --is-ancestor origin/main HEAD
#
# which only fails when HEAD is MISSING commits from origin/main. It passes
# cleanly when HEAD is AHEAD of origin/main -- local commits that were never
# pushed, never reviewed, never run through CI -- which is the exact incident
# class the guard exists to prevent. TODO.md has always documented the intended
# precondition as bidirectional:
#
#     git rev-list --left-right --count HEAD...origin/main   ==   0 0
#
# This script is that precondition, in one place, with a test
# (scripts/tests/test_deploy_preflight.py) so the two Makefile targets cannot
# drift back to the one-sided form.
#
# Usage: bash scripts/deploy-preflight.sh [REMOTE/BRANCH]   (default origin/main)
# Exit 0 only when ahead == 0 and behind == 0.

set -eu

ref="${1:-origin/main}"
remote="${ref%%/*}"
branch="${ref#*/}"

if [ "$remote" = "$ref" ] || [ -z "$branch" ]; then
  echo "deploy-preflight: expected REMOTE/BRANCH, got '$ref'" >&2
  exit 2
fi

echo "→ Pre-flight: checking HEAD is exactly $ref (nothing unpushed, nothing unpulled)..."
git fetch --quiet "$remote" "$branch"

# `rev-list --left-right --count A...B` prints "<only in A><TAB><only in B>".
counts=$(git rev-list --left-right --count "HEAD...$ref")
ahead=${counts%%[[:space:]]*}
behind=${counts##*[[:space:]]}

if [ "$ahead" != "0" ] || [ "$behind" != "0" ]; then
  echo "❌ HEAD is $ahead commit(s) ahead of and $behind commit(s) behind $ref." >&2
  if [ "$ahead" != "0" ]; then
    echo "   Unpushed commits never went through review or CI; push them, merge the PR, and pull main." >&2
  fi
  if [ "$behind" != "0" ]; then
    echo "   Pull first so the build includes everything already on $ref." >&2
  fi
  exit 1
fi

echo "✅ HEAD == $ref ($(git rev-parse --short HEAD))."
