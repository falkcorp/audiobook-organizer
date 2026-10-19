#!/bin/bash
# file: scripts/ci/check_generated_types.sh
# version: 3.0.0
# guid: 2d8f4a60-b3c1-4e97-a15d-7e0c9b6f3a48
# last-edited: 2026-10-18
# Fail when web/src/types/generated differs from what tygo emits (07-F1). Generates
# into a temp dir (the tree is never touched) and diffs: stale, missing and orphan
# files fail; a correct uncommitted regeneration passes; any error exits non-zero.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
dir=web/src/types/generated
go -C tools/tygo install tool
bin=$(go env GOBIN)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
sed "/output_path/s#$dir/#$tmp/out/#" tygo.yaml > "$tmp/tygo.yaml"
"${bin:-$(go env GOPATH)/bin}/tygo" generate --config "$tmp/tygo.yaml"
diff -r "$tmp/out" "$dir" >&2 || { echo "$dir is stale: run 'make generate-types'" >&2; exit 1; }
echo "generated TypeScript types are fresh"
