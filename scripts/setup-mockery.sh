#!/bin/bash
# file: scripts/setup-mockery.sh
# version: 1.6.0
# guid: c3d4e5f6-a7b8-9012-cdef-345678901abc
# last-edited: 2026-10-10

# Setup script for integrating mockery v3 into the project.
#
# PINNED VERSION: v3.8.0 (module github.com/vektra/mockery/v3), built from
# tools/mockery/go.mod, which also raises golang.org/x/tools to v0.51.0 so
# mockery can read go1.27.2's export data. CI and Woodpecker build it the
# same way (`go -C tools/mockery install tool`). Do NOT use @latest — it drifts silently (e.g. resolving to a newer mockery
# major/minor than CI expects) and produces spurious formatting-only diffs
# (interface{} -> any, receiver renames, etc.) that make `make mocks-check`
# fail locally even though CI is green. If you see a large, repo-wide mock
# diff after running `make mocks`, you are almost certainly running the
# wrong mockery version — check `mockery version` against the pin above.

set -euo pipefail

MOCKERY_VERSION="v3.8.0"

echo "🔧 Setting up mockery for improved test coverage..."

# Always (re)build from the tools module: a mockery from a plain
# `go install ...@v3.8.0` reports the same version but carries the old x/tools.
echo "📦 Building mockery ${MOCKERY_VERSION} from tools/mockery..."
go -C "$(git rev-parse --show-toplevel)/tools/mockery" install tool

echo "✅ Mockery is installed (expected version: ${MOCKERY_VERSION})"
mockery version

# Generate mocks using configuration
echo "🔨 Generating mocks for Store interface..."
mockery --config .mockery.yaml

echo ""
echo "✨ Setup complete!"
echo ""
echo "Generated mocks:"
ls -la internal/database/mocks/*.go 2>/dev/null || echo "  (check internal/database/mocks/)"
echo ""
echo "Next steps:"
echo "1. Review the generated mock in internal/database/mocks/"
echo "2. Update server tests to use the mock (see server_mockery_example_test.go.example)"
echo "3. Run: go test ./internal/server -v"
echo "4. Add 'make mocks' target to Makefile for CI/CD"
echo ""
echo "Expected coverage improvement: 66% → 85%+"
