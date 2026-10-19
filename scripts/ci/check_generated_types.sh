#!/bin/bash
# file: scripts/ci/check_generated_types.sh
# version: 2.0.0
# guid: 2d8f4a60-b3c1-4e97-a15d-7e0c9b6f3a48
# last-edited: 2026-10-18
# Fail when web/src/types/generated differs from what tygo emits (07-F1): wipe,
# regenerate (so an orphan is caught), then ask git. Needs that directory clean:
# CI has it; locally commit the regenerated files first.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=web/src/types/generated
rm -f "$dir"/*.ts
go tool tygo generate
if [ -n "$(git status --porcelain -- "$dir")" ]; then
  echo "$dir is stale: run 'make generate-types' and commit the result" >&2
  git status --short -- "$dir" >&2
  exit 1
fi
echo "generated TypeScript types are fresh"
