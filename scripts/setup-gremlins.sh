#!/usr/bin/env bash
# file: scripts/setup-gremlins.sh
# version: 1.0.0
# guid: 8d1f6a02-5b74-4e39-a8c1-3f90e27b5d46
# last-edited: 2026-08-16
#
# Installs the pinned gremlins mutation-testing binary.
#
# Pinned rather than @latest on purpose: a mutation SCORE is only comparable
# against itself. A different gremlins version can generate a different mutant
# set from identical source, so an unpinned upgrade would move the score without
# anyone changing a line of code, and the number would stop meaning anything.

set -euo pipefail

GREMLINS_VERSION="v0.6.0"
MODULE="github.com/go-gremlins/gremlins/cmd/gremlins"

if command -v gremlins >/dev/null 2>&1; then
    have="$(gremlins version 2>/dev/null | head -1 || echo unknown)"
    echo "gremlins already installed: ${have}"
    echo "   (pinned version for this repo is ${GREMLINS_VERSION})"
    exit 0
fi

echo "Installing gremlins ${GREMLINS_VERSION}..."
go install "${MODULE}@${GREMLINS_VERSION}"

if ! command -v gremlins >/dev/null 2>&1; then
    echo "gremlins installed but not on PATH."
    echo "   Add \$(go env GOPATH)/bin to your PATH:"
    echo "   export PATH=\"\$(go env GOPATH)/bin:\$PATH\""
    exit 1
fi

echo "gremlins ${GREMLINS_VERSION} installed"
