# file: Makefile
# version: 2.15.1
# guid: c1d2e3f4-g5h6-7890-ijkl-m1234567890n
# last-edited: 2026-07-03

BINARY := audiobook-organizer
ROOT_DIR := $(shell git rev-parse --show-toplevel 2>/dev/null || pwd)
WEB_DIR := $(ROOT_DIR)/web
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo 'dev')
LDFLAGS := -X main.version=$(VERSION)
export GOEXPERIMENT := jsonv2

# Overridable deployment variables (set in Makefile.local or via environment)
DEPLOY_HOST ?=
DEPLOY_BIN  ?=
BACKUP_DIR  ?= $(CURDIR)/backups

# Include local overrides (not committed — see Makefile.local.example)
-include Makefile.local

.PHONY: all build build-api run run-api install clean help \
        web-install web-build web-dev web-test web-lint web-lint-memory \
        test test-short test-all test-all-short test-nightly test-frontend test-e2e test-e2e-demo \
        coverage coverage-check coverage-check-short ci \
        vet mocks mocks-check check-mock-fresh staticcheck oplint sdkguard \
        docker docker-run docker-stop \
        release-dry-run release-snapshot version \
        build-mtls-bridge build-mtls-bridge-windows \
        manual-smoke smoke-create-books smoke-run-demo \
        rollback

# Default: full build (frontend + backend with embed)
all: build

## help: Show available targets
help:
	@echo "Build:"
	@echo "  make build          - Full build: frontend + Go binary with embedded UI"
	@echo "  make build-api      - Backend only (no embedded frontend, for quick iteration)"
	@echo "  make build-bench    - Backend + bench tooling (dedup-bench experiments)"
	@echo "  make run            - Full build then serve"
	@echo "  make run-api        - Backend-only build then serve (API endpoints only)"
	@echo ""
	@echo "Frontend:"
	@echo "  make web-install    - Install npm dependencies"
	@echo "  make web-build      - Build frontend (outputs to web/dist)"
	@echo "  make web-dev        - Start Vite dev server"
	@echo "  make web-test       - Run frontend unit tests"
	@echo "  make web-lint       - Lint frontend code"
	@echo ""
	@echo "Testing:"
	@echo "  make test           - Run Go backend tests (full — includes slow prop tests, ~15 min)"
	@echo "  make test-short     - Run Go backend tests in -short mode (slow prop tests skipped, ~1 min)"
	@echo "  make test-all       - Run all tests: backend (full) + frontend"
	@echo "  make test-all-short - Run all tests: backend (-short) + frontend (for local ci)"
	@echo "  make test-nightly   - Run all tests including slow property tests (for nightly CI)"
	@echo "  make test-frontend  - Run frontend tests only"
	@echo "  make test-e2e       - Run Playwright E2E tests"
	@echo "  make coverage       - Generate coverage report"
	@echo "  make coverage-check - Verify 30% coverage threshold"
	@echo "  make sdkguard       - Assert pkg/plugin/sdk has no unexpected internal/ deps"
	@echo "  make ci             - Fast CI: short tests + coverage (prop tests skipped)"
	@echo ""
	@echo "Docker:"
	@echo "  make docker         - Build Docker image"
	@echo "  make docker-run     - Run with docker compose"
	@echo "  make docker-stop    - Stop docker compose"
	@echo ""
	@echo "Release:"
	@echo "  make version        - Show current version from git tags"
	@echo "  make release-dry-run - Test GoReleaser config without publishing"
	@echo "  make release-snapshot - Build snapshot release (no tag required)"
	@echo ""
	@echo "Setup:"
	@echo "  make install        - Install dependencies (npm)"
	@echo "  make clean          - Remove build artifacts"

## install: Install all dependencies
install: web-install

# --- Build targets ---
# The binary embeds the React frontend via //go:embed web/dist (build tag:
# embed_frontend). This requires web/dist to exist, so web-build runs first.
# Use build-api for quick backend iteration when you don't need the UI.

## build: Full build with embedded frontend
build: web-build
	@echo "🔨 Building $(BINARY) with embedded frontend..."
	@go build -tags embed_frontend -ldflags="$(LDFLAGS)" -o $(BINARY) .
	@echo "✅ Built ./$(BINARY)"

## build-api: Backend-only build (no frontend, serves placeholder at /)
build-api:
	@echo "🔨 Building $(BINARY) (API only)..."
	@go build -ldflags="$(LDFLAGS)" -o $(BINARY) .
	@echo "✅ Built ./$(BINARY)"

## build-bench: Build with bench tooling (dedup-bench experiments)
build-bench:
	@echo "🔨 Building $(BINARY) with bench tooling..."
	@go build -tags bench -ldflags="$(LDFLAGS)" -o $(BINARY) .
	@echo "✅ Built ./$(BINARY) (bench mode)"

## build-linux: Cross-compile for Linux amd64 (requires: brew install filosottile/musl-cross/musl-cross)
build-linux: web-build
	@echo "🔨 Cross-compiling for Linux amd64..."
	@mkdir -p dist
	@CC=x86_64-linux-musl-gcc GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build \
		-tags "embed_frontend fts5 native_taglib" \
		-ldflags="-s -w -linkmode external -extldflags '-static' -X main.version=$(VERSION)" \
		-o dist/audiobook-organizer-linux-amd64 .
	@echo "✅ Built dist/audiobook-organizer-linux-amd64"

## run: Full build and serve
run: build
	@./$(BINARY) serve

## run-api: API-only build and serve
run-api: build-api
	@./$(BINARY) serve

# --- Frontend targets ---

## web-install: Install npm dependencies
web-install:
	@echo "📦 Installing frontend dependencies..."
	@cd $(WEB_DIR) && npm install
	@echo "✅ Dependencies installed"

## web-build: Build frontend (produces web/dist for embedding)
web-build: web-install
	@echo "🌐 Building frontend..."
	@cd $(WEB_DIR) && npm run build
	@echo "✅ Frontend built (web/dist)"

## web-dev: Start Vite dev server
web-dev:
	@cd $(WEB_DIR) && npm run dev

## web-test: Run frontend unit tests (single pass, no watch)
web-test:
	@echo "🧪 Running frontend tests..."
	@cd $(WEB_DIR) && npm run test -- --run
	@echo "✅ Frontend tests passed"

## web-lint: Lint frontend code
web-lint:
	@echo "🔍 Linting frontend..."
	@cd $(WEB_DIR) && npm run lint
	@echo "✅ Frontend lint passed"

## web-lint-memory: Scan for common memory leak patterns
web-lint-memory:
	@echo "🔍 Scanning for memory leaks..."
	@python3 scripts/check-memory-leaks.py
	@echo "✅ Memory leak scan complete"

# --- Testing targets ---

## test: Run Go backend tests (full — includes slow property tests)
## NOTE: -timeout 25m (vs the 10m default). internal/server alone runs ~421s
## WITHOUT -race; under -race it exceeds the 600s/package default and the run
## fails with "panic: test timed out after 10m0s". The package is large (heavy
## per-test setup: Pebble + migrations + op-registry workers). CI uses the
## -short variant which fits the default; this full target needs the headroom.
test: vet
	@echo "🧪 Running backend tests (full suite)..."
	@go test ./... -v -race -timeout 25m
	@echo "✅ Backend tests passed"

## test-short: Run Go backend tests in short mode — skips slow property
## tests (undo/playlist/dedup/etc.) that create per-iteration PebbleStores.
## Produces coverage.out as a side effect (consumed by coverage-check-short).
## Use for fast dev iteration and for sweep-style refactors where the
## primary gate is `go build ./...`. CI still runs the full suite.
test-short: vet
	@echo "🧪 Running backend tests (-short — slow prop tests skipped)..."
	@go test ./... -short -race
	@echo "✅ Short backend tests passed"
	@echo "📊 Generating coverage profile (separate run)..."
	@go test ./... -short -coverprofile=coverage.out -covermode=atomic >/dev/null 2>&1
	@echo "✅ Coverage profile generated"

## vet: Run go vet across every package. Catches hand-written mock
## drift (the stubStore / PR #234 incident) before tests even compile.
vet:
	@echo "🔍 Running go vet..."
	@go vet ./...
	@echo "✅ go vet passed"

## mocks: Regenerate mockery-managed mocks from .mockery.yaml.
## Run this after editing an interface listed in .mockery.yaml.
## Pinned mockery version: v3.7.1 (module github.com/vektra/mockery/v3).
## Install via scripts/setup-mockery.sh. Mockery v2.x cannot regenerate
## these mocks: it does not support merging multiple interfaces into one
## shared output file (e.g. internal/database/mocks/mock_store.go), so
## running an older/newer mockery binary here will silently corrupt or
## truncate that file. If `make mocks` produces a huge, repo-wide diff,
## you are running the wrong mockery version — check `mockery version`.
mocks:
	@echo "🎭 Regenerating mockery-managed mocks..."
	@mockery
	@echo "✅ Mocks regenerated"

## mocks-check: Verify committed mocks match what mockery would generate
## right now. Fails CI if someone edited an interface without re-running
## mockery. Pinned mockery version: v3.7.1 (see scripts/setup-mockery.sh).
## Backlog 5.9.
mocks-check:
	@echo "🎭 Checking that committed mocks are up to date..."
	@mockery --log-level warn
	@if ! git diff --quiet -- internal/*/mocks/ internal/ai/mock_*_test.go internal/metadata/mock_*_test.go; then \
		echo "❌ Committed mocks are stale. Run 'make mocks' and commit the result."; \
		git diff --stat -- internal/*/mocks/ internal/ai/mock_*_test.go internal/metadata/mock_*_test.go; \
		exit 1; \
	fi
	@echo "✅ Mocks are up to date"

## check-mock-fresh: Check that MockStore is up to date with the Store interface
check-mock-fresh:
	@echo "==> Checking mock freshness..."
	go generate ./internal/database/...
	git diff --exit-code internal/database/mocks/ || \
		(echo "ERROR: MockStore is stale. Run 'make generate' and commit." && exit 1)
	@echo "==> Mock is fresh."

## staticcheck: Run staticcheck (install: go install honnef.co/go/tools/cmd/staticcheck@latest)
staticcheck:
	@echo "==> Running staticcheck..."
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./... && echo "==> staticcheck passed."; \
	else \
		echo "==> staticcheck not installed, skipping."; \
	fi

## oplint: Run plugin import lint
oplint:
	@echo "🔍 Running plugin import lint..."
	@go run ./tools/cmd/oplint ./internal/plugins/...
	@echo "✅ Plugin import lint passed"

## reconcile-paths: Build the reconcile-paths dry-run CSV tool
reconcile-paths:
	@echo "Building reconcile-paths tool..."
	@go build -o bin/reconcile-paths ./tools/cmd/reconcile-paths/
	@echo "Built: bin/reconcile-paths"

## sdkguard: Assert pkg/plugin/sdk has no unexpected internal/ dependencies
sdkguard:
	@echo "🔍 Running SDK guard (asserting no new internal/ deps in pkg/plugin/sdk)..."
	@go run ./tools/cmd/sdkguard/main.go
	@echo "✅ SDK guard passed"

## test-all: Run all tests (backend full + frontend)
test-all: test web-test

## test-all-short: Run all tests with -short backend (prop tests skipped, ~1 min + frontend)
test-all-short: test-short web-test

## test-nightly: Run full suite including slow property tests (nightly CI only)
test-nightly: test web-test coverage-check

## test-frontend: Run frontend tests independently (alias for web-test)
test-frontend: web-test

## test-e2e: Run Playwright E2E tests (chromium + webkit only; excludes demo recording)
test-e2e:
	@echo "🧪 Running E2E tests..."
	@cd $(WEB_DIR) && npm run test:e2e
	@echo "✅ E2E tests passed"

## test-e2e-demo: Run demo recording tests (opt-in; requires live media content)
test-e2e-demo:
	@echo "🎬 Running demo recording tests (chromium-record project)..."
	@cd $(WEB_DIR) && npm run test:e2e:demo
	@echo "✅ Demo recording tests complete"

## manual-smoke: Run all manual smoke scripts against a running server
manual-smoke: smoke-create-books smoke-run-demo

## smoke-create-books: Create test audiobook fixtures on the running server
smoke-create-books:
	@echo "📚 Creating test audiobook fixtures..."
	@bash scripts/create-test-audiobooks.sh

## smoke-run-demo: Run the demo recording script (requires running server with media)
smoke-run-demo:
	@echo "🎬 Running demo recording script..."
	@bash scripts/run_demo_recording.sh

## coverage: Generate coverage report
coverage:
	@echo "📊 Generating coverage report..."
	@go test ./... -coverprofile=coverage.out -covermode=atomic
	@go tool cover -html=coverage.out -o coverage.html
	@echo ""
	@echo "Coverage summary:"
	@go tool cover -func=coverage.out | grep total | awk '{printf "  Total: %s\n", $$3}'
	@echo ""
	@echo "📄 Detailed report: coverage.html"

## coverage-check: Verify coverage meets 30% threshold (full suite)
coverage-check:
	@echo "🎯 Checking coverage threshold..."
	@go test ./... -coverprofile=coverage.out -covermode=atomic >/dev/null 2>&1
	@coverage=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	echo "Coverage: $$coverage%"; \
	if [ $$(echo "$$coverage < 30" | bc -l) -eq 1 ]; then \
		echo "❌ Coverage $$coverage% is below 30% threshold"; \
		exit 1; \
	fi; \
	echo "✅ Coverage $$coverage% meets 30% threshold"

## coverage-check-short: Verify coverage using pre-existing coverage.out (produced by test-short).
## Fails if coverage.out is missing or if coverage drops below the committed floor file (.ci/coverage-floor.txt).
## The floor file can only be raised by a human; this ensures we don't silently erode coverage over time.
coverage-check-short:
	@echo "🎯 Checking coverage threshold (-short)..."
	@if [ ! -f coverage.out ]; then \
		echo "❌ coverage.out not found. Run 'make test-short' first."; \
		exit 1; \
	fi
	@echo "Per-package coverage:"
	@go tool cover -func=coverage.out | grep -v total
	@coverage=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	echo ""; \
	echo "Total coverage: $$coverage%"; \
	floor_file=".ci/coverage-floor.txt"; \
	if [ ! -f "$$floor_file" ]; then \
		echo "❌ $$floor_file not found. Coverage floor file must be committed to the repo."; \
		exit 1; \
	fi; \
	floor=$$(cat $$floor_file); \
	if [ $$(echo "$$coverage < $$floor" | bc -l) -eq 1 ]; then \
		echo "❌ Coverage $$coverage% is below committed floor $$floor%"; \
		exit 1; \
	fi; \
	last_file=".ci/coverage-last.txt"; \
	if [ -f "$$last_file" ]; then \
		last=$$(cat $$last_file); \
		if [ $$(echo "$$coverage < $$last" | bc -l) -eq 1 ]; then \
			echo "⚠️  WARN: Coverage dropped from $$last% to $$coverage%"; \
		fi; \
	fi; \
	mkdir -p .ci; \
	echo "$$coverage" > "$$last_file"; \
	echo "✅ Coverage $$coverage% meets floor $$floor%"

## ci: Fast CI check (short tests — prop tests skipped; use test-nightly for full suite)
ci: mocks-check check-mock-fresh staticcheck sdkguard test-all-short coverage-check-short
	@echo "✅ All CI checks passed!"

## build-mtls-bridge: Build the mTLS bridge binary (macOS)
build-mtls-bridge:
	@echo "Building mtls-bridge..."
	@go build -ldflags="$(LDFLAGS)" -o mtls-bridge ./cmd/mtls-bridge

## build-mtls-bridge-windows: Cross-compile mTLS bridge for Windows amd64
build-mtls-bridge-windows:
	@echo "Building mtls-bridge.exe for Windows..."
	@GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o mtls-bridge.exe ./cmd/mtls-bridge

## clean: Remove build artifacts
clean:
	@echo "🧹 Cleaning..."
	@rm -f $(BINARY) coverage.out coverage.html
	@echo "✅ Clean complete"

# --- Docker targets ---

## docker: Build Docker image
docker:
	@echo "🐳 Building Docker image..."
	@docker build --build-arg APP_VERSION=$(VERSION) -t audiobook-organizer:latest .
	@echo "✅ Docker image built: audiobook-organizer:latest"

## docker-run: Run with docker compose
docker-run:
	@echo "🐳 Starting with docker compose..."
	@APP_VERSION=$(VERSION) docker compose up -d
	@echo "✅ Running at http://localhost:8484"

## docker-stop: Stop docker compose
docker-stop:
	@docker compose down
	@echo "✅ Stopped"

# --- Release targets ---

## version: Show current version from git tags
version:
	@echo "Version: $(VERSION)"

## release-dry-run: Test GoReleaser config without publishing
release-dry-run: web-build
	@echo "Testing GoReleaser configuration..."
	@goreleaser check
	@goreleaser release --snapshot --clean --skip=publish
	@echo "Dry run complete. Artifacts in dist/"

## release-snapshot: Build snapshot release (no tag required)
release-snapshot: web-build
	@echo "Building snapshot release..."
	@goreleaser release --snapshot --clean
	@echo "Snapshot built. Artifacts in dist/"

## backup: Create timestamped backup of all prod data directories (requires DEPLOY_HOST)
.PHONY: backup
backup:
	@[ -n "$(DEPLOY_HOST)" ] || (echo "ERROR: DEPLOY_HOST is not set. Add it to Makefile.local or export it."; exit 1)
	@mkdir -p $(BACKUP_DIR)
	@echo "→ Creating backup on $(DEPLOY_HOST)..."
	@STAMP=$$(date +%Y%m%d-%H%M%S); \
		ssh $(DEPLOY_HOST) "tar -czf /tmp/aobackup-$$STAMP.tar.gz \
			-C /var/lib/audiobook-organizer \
			audiobooks.pebble activity.nutsdb embeddings.db 2>/dev/null || true"; \
		scp $(DEPLOY_HOST):/tmp/aobackup-$$STAMP.tar.gz $(BACKUP_DIR)/; \
		ssh $(DEPLOY_HOST) "rm -f /tmp/aobackup-$$STAMP.tar.gz"; \
		echo "✅ Backup saved to $(BACKUP_DIR)/aobackup-$$STAMP.tar.gz"

## rollback: Swap in the previous deployed binary and restart (requires DEPLOY_HOST, DEPLOY_BIN)
.PHONY: rollback
rollback:
	@[ -n "$(DEPLOY_HOST)" ] || (echo "ERROR: DEPLOY_HOST is not set. Add it to Makefile.local or export it."; exit 1)
	@[ -n "$(DEPLOY_BIN)" ] || (echo "ERROR: DEPLOY_BIN is not set. Add it to Makefile.local or export it."; exit 1)
	@echo "→ Rolling back $(DEPLOY_BIN) on $(DEPLOY_HOST)..."
	@ssh $(DEPLOY_HOST) 'test -f $(DEPLOY_BIN).prev' || (echo "ERROR: no $(DEPLOY_BIN).prev found on $(DEPLOY_HOST) — nothing to roll back to."; exit 1)
	ssh $(DEPLOY_HOST) 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).rolled-back && \
	  sudo cp $(DEPLOY_BIN).prev $(DEPLOY_BIN) && \
	  sudo systemctl restart audiobook-organizer.service'
	@echo "✅ Rolled back $(DEPLOY_BIN) to previous version and restarted."

# Quick aliases
.PHONY: t c b v
t: test
c: coverage
b: build
v: version
