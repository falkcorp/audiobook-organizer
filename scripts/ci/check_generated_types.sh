#!/bin/bash
# file: scripts/ci/check_generated_types.sh
# version: 1.0.0
# guid: 2d8f4a60-b3c1-4e97-a15d-7e0c9b6f3a48
# last-edited: 2026-10-18
# Fail when web/src/types/generated is stale against the Go structs (07-F1).
# Regenerates in place and compares a content digest of the directory before and
# after, so it catches a stale file, a missing file and an orphan alike, in CI
# and in a dirty local tree. Fix: `make generate-types` and commit.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=web/src/types/generated
digest() { find "$dir" -type f 2>/dev/null | LC_ALL=C sort | xargs shasum 2>/dev/null || true; }
before=$(digest)
go tool tygo generate
after=$(digest)
if [ "$before" != "$after" ]; then
  echo "generated TypeScript types are stale; run 'make generate-types' and commit $dir" >&2
  diff <(echo "$before") <(echo "$after") >&2 || true
  exit 1
fi
echo "generated TypeScript types are fresh"
