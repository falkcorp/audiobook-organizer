#!/bin/sh
# file: core/scripts/ci/check_openapi_fresh.sh
# version: 1.0.0
# guid: 6f23147f-a4df-47e2-92b9-df8f00b6befd
# last-edited: 2026-11-08
#
# Fails when openapi/core.json differs from what core's own `hello openapi` emits
# (05-http-api §4.6, modelled on scripts/ci/check_generated_types.sh): it writes
# into a temp dir, so the tree is never touched, and diffs. A stale core.json
# therefore fails core CI before the api-client generator reads it. Run from
# anywhere; it finds core/ from its own path (nothing reads above core/).
set -eu
cd "$(dirname "$0")/../.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go run ./examples/hello/cmd/hello openapi --out "$tmp/core.json" >/dev/null
if ! diff -u openapi/core.json "$tmp/core.json" >&2; then
  echo "openapi/core.json is stale: run 'make -C core openapi-regen'" >&2
  exit 1
fi
echo "openapi/core.json is fresh"
