#!/bin/sh
# file: scripts/ci/ensure_core_web.sh
# version: 1.0.0
# guid: 5d3c2f61-8a47-4b0e-9c15-6e0b7a2d41f8
# last-edited: 2026-11-08
#
# Builds @mediacore/api-client only when web/ would not find a fresh build of it.
# web/'s own build, typecheck and test scripts run this first: a caller that runs only
# `npm ci` in web/ (the reusable frontend job behind frontend-ci.yml cannot call
# build_web.sh) has no dist/ for the package's `types` and `default` exports to point
# at. Fresh means dist/ exists and no file it is built from is newer. This is not the
# dependency entry point (build_web.sh deps is, and stays under 20 lines); it never
# touches web/ and sets no BUILD_ID. POSIX sh, no bashisms.
# Usage: sh scripts/ci/ensure_core_web.sh
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
core="$root/core/web"
pkg="$core/packages/api-client"
[ -f "$core/package.json" ] || exit 0
if [ -f "$pkg/dist/index.js" ] && [ -f "$pkg/dist/index.d.ts" ] &&
  [ -z "$(find "$pkg" "$core/package-lock.json" \( -name node_modules -o -name dist \) -prune \
    -o -type f -newer "$pkg/dist/index.js" -print | head -n 1)" ]; then
  exit 0
fi
cd "$core"
if [ ! -f node_modules/.package-lock.json ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
  npm ci --no-audit --no-fund
fi
npm run build -w @mediacore/api-client
