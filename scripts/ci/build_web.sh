#!/bin/sh
# file: scripts/ci/build_web.sh
# version: 1.0.0
# guid: 9676792c-34c7-4786-bddb-8ed552c314e1
# last-edited: 2026-11-07
#
# The one entry point for web dependencies (platform-core 07 §7.3.2): `make web-deps`,
# every workflow, both Dockerfiles and the release pre-build scripts call it. POSIX sh:
# the Docker frontend stage is node:26-alpine and has no bash. It builds nothing in web/,
# sets no UI build ID, and leaves a caller's BUILD_ID alone. npm flags other than
# --no-audit/--no-fund come from the caller's environment (npm_config_ignore_scripts=true,
# npm_config_prefer_offline=true). Core packages build in this explicit order (one today).
# Usage: sh scripts/ci/build_web.sh deps
set -eu
if [ "$#" -ne 1 ] || [ "$1" != deps ]; then
  echo "usage: build_web.sh deps" >&2
  exit 2
fi
root=$(cd "$(dirname "$0")/../.." && pwd)
if [ -f "$root/core/web/package.json" ]; then
  cd "$root/core/web"
  npm ci --no-audit --no-fund
  npm run build -w @mediacore/api-client
fi
cd "$root/web"
npm ci --no-audit --no-fund
