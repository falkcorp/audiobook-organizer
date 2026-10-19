#!/bin/bash
# file: scripts/ci/check_generated_types.sh
# version: 2.1.0
# guid: 2d8f4a60-b3c1-4e97-a15d-7e0c9b6f3a48
# last-edited: 2026-10-18
# Fail when web/src/types/generated differs from what tygo emits (07-F1): wipe,
# regenerate (catches orphans), ask git. Needs that dir clean; commit first locally.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=web/src/types/generated
rm -f "$dir"/*.ts
go -C tools/tygo install tool
bin=$(go env GOBIN); "${bin:-$(go env GOPATH)/bin}/tygo" generate
if [ -n "$(git status --porcelain -- "$dir")" ]; then
  echo "$dir is stale: run 'make generate-types' and commit the result" >&2
  git status --short -- "$dir" >&2
  exit 1
fi
echo "generated TypeScript types are fresh"
