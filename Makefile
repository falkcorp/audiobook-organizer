# file: Makefile
# version: 2.20.0
# guid: c1d2e3f4-g5h6-7890-ijkl-m1234567890n
# last-edited: 2026-08-17

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
        vet mocks mocks-check staticcheck oplint sdkguard \
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
	@echo "  make test-short     - Run Go backend tests in -short mode + coverage (slow prop tests skipped, ~8 min)"
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
## fails with "panic: test timed out after 10m0s". The per-test setup (Pebble +
## migrations) is write-heavy, and on macOS that write cost dominates: same
## commit, 532s with a normal TMPDIR vs 33.7s with TMPDIR on a RAM disk (35.5s
## on Linux) — see TODO-SRVTIMEOUT. The package is not CPU-slow. CI uses the
## -short variant which fits the default; this full target needs the headroom.
test: vet
	@echo "🧪 Running backend tests (full suite)..."
	@go test ./... -v -race -timeout 25m
	@echo "✅ Backend tests passed"

## test-fast: Full backend suite with TMPDIR on a RAM disk (H111, opt-in).
## The per-test Pebble+migration setup is write-bound: measured 532s for
## internal/server on a normal macOS TMPDIR vs 33.7s on a RAM disk (~15.8x).
## Creates/reuses a 3GB RAM disk at /Volumes/abo-test-ram (macOS) or uses
## /dev/shm (Linux). Test artifacts on it vanish at detach/reboot — the
## wrapper only sets TMPDIR for the test run, it never touches data paths.
## Remove the disk afterwards with: hdiutil detach /Volumes/abo-test-ram
test-fast: vet
	@echo "🧪 Running backend tests (full suite, RAM-disk TMPDIR)..."
	@bash scripts/with-ramdisk-tmpdir.sh go test ./... -v -race -timeout 25m
	@echo "✅ Backend tests passed (RAM-disk TMPDIR)"

## test-fast-short: -short variant of test-fast.
test-fast-short: vet
	@echo "🧪 Running backend tests (-short, RAM-disk TMPDIR)..."
	@bash scripts/with-ramdisk-tmpdir.sh go test ./... -short -race -timeout 25m
	@echo "✅ Short backend tests passed (RAM-disk TMPDIR)"

## test-short: Run Go backend tests in short mode — skips slow property
## tests (undo/playlist/dedup/etc.) that create per-iteration PebbleStores.
## Produces coverage.out as a side effect (consumed by coverage-check-short).
## Use for fast dev iteration and for sweep-style refactors where the
## primary gate is `go build ./...`. CI still runs the full suite.
##
## ONE pass, with -race and -coverprofile together. This used to be two full
## runs of the suite — once with -race, then again with -coverprofile — which
## cost almost exactly double for no benefit. Measured on an idle machine
## 2026-08-16: -race alone 493s, -coverprofile alone 473s, both together 500s.
## Combining is 466s cheaper per invocation (966s -> 500s, -48%) and only 7s
## more than -race by itself. Coverage is byte-identical either way (47.0%,
## 81950 profile lines), so the coverage-check-short floor is unaffected.
## -covermode=atomic is required with -race and was already in use.
##
## The second run also discarded its output (>/dev/null 2>&1), so a failure
## that happened only under the coverage run produced a silent non-zero exit
## with nothing to read. One pass removes that failure mode for free.
test-short: vet
	@echo "🧪 Running backend tests (-short — slow prop tests skipped, with coverage)..."
	@go test ./... -short -race -coverprofile=coverage.out -covermode=atomic -timeout 25m
	@echo "✅ Short backend tests passed, coverage profile generated"

## vet: Run go vet across every package. Catches hand-written mock
## drift (the stubStore / PR #234 incident) before tests even compile.
vet:
	@echo "🔍 Running go vet..."
	@go vet ./...
	@echo "✅ go vet passed"

## mutate: Run mutation testing on ONE package (PKG=./internal/scanner/).
## Mutation testing answers the question a green suite cannot: would these
## tests actually FAIL if the code were wrong? It edits the source (flipping
## conditionals, removing statements), reruns the tests, and reports mutants
## that SURVIVED -- each survivor is a change no test noticed.
##
## PKG IS REQUIRED AND DELIBERATELY NOT DEFAULTED TO ./... . gremlins runs the
## package's whole test suite once per mutant; several suites here take 30s+
## (internal/server has hit its 600s timeout), so a repo-wide run is measured in
## hours, not minutes. Scope it to what you changed.
##
##   make mutate PKG=./internal/scanner/
##   make mutate PKG=./internal/server/handlers/abs/
##
## ⚠️ RUN FROM A WORKTREE, NOT THE PRIMARY CHECKOUT. gremlins copies the whole
## module directory once per worker. This module root is ~34GB because
## .worktrees/ sits inside it, so the default worker count projects to 340GB+ of
## copies -- that is how a --dry-run filled a 926GB volume on 2026-08-16. From a
## worktree the same copy is 1.8GB. scripts/run-mutation.sh REFUSES to run from
## the primary checkout, budgets the disk before starting, and kills the run if
## free space crosses the floor.
##
## Tunables (env): MUTATE_WORKERS (default 2), MUTATE_MIN_FREE_GB (60),
## MUTATE_FLOOR_GB (20).
##
## Install the pinned binary first: bash scripts/setup-gremlins.sh
mutate:
	@if [ -z "$(PKG)" ]; then \
		echo "PKG is required, e.g. make mutate PKG=./internal/scanner/"; \
		echo "   (repo-wide mutation runs take hours -- scope it to what you changed)"; \
		exit 2; \
	fi
	@command -v gremlins >/dev/null 2>&1 || { \
		echo "gremlins not installed. Run: bash scripts/setup-gremlins.sh"; \
		exit 1; \
	}
	@PKG=$(PKG) bash scripts/run-mutation.sh

## mutate-dry: List the mutants gremlins WOULD generate for PKG, without
## running any tests. Use it to size a run before committing to it, or to check
## that a package has mutable code at all.
##
## NOT free on disk: --dry-run skips the TEST RUNS, not the working-copy staging,
## so it goes through the same disk guard as a full run.
mutate-dry:
	@if [ -z "$(PKG)" ]; then \
		echo "PKG is required, e.g. make mutate-dry PKG=./internal/scanner/"; \
		exit 2; \
	fi
	@command -v gremlins >/dev/null 2>&1 || { \
		echo "gremlins not installed. Run: bash scripts/setup-gremlins.sh"; \
		exit 1; \
	}
	@PKG=$(PKG) bash scripts/run-mutation.sh --dry-run

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
	@if ! git diff --quiet -- ':(glob)internal/**/mocks/**' internal/ai/mock_*_test.go internal/metadata/mock_*_test.go; then \
		echo "❌ Committed mocks are stale. Run 'make mocks' and commit the result."; \
		git diff --stat -- ':(glob)internal/**/mocks/**' internal/ai/mock_*_test.go internal/metadata/mock_*_test.go; \
		exit 1; \
	fi
	@echo "✅ Mocks are up to date"

## (removed 2026-08-17) check-mock-fresh — deleted, not repaired. It claimed to
## catch a stale MockStore and could not: it ran `go generate
## ./internal/database/...` in a repo with ZERO //go:generate directives (mocks
## come from .mockery.yaml), so the regeneration was a no-op and the following
## `git diff --exit-code internal/database/mocks/` only ever detected a dirty
## worktree. Measured: add a method to the Store interface and leave the mock
## alone — the exact drift the docstring named — and it printed "Mock is fresh"
## and exited 0. Its own error message told you to run `make generate`, a target
## that does not exist. Coverage lost by removing it: none. Store/mock
## divergence is a COMPILE error via the assertions at
## internal/database/iface_assert.go:12 and internal/database/mock_store.go:30,
## `vet` (a test-short prerequisite) runs over every package, and `mocks-check`
## below regenerates from .mockery.yaml and diffs. All three go red on that same
## mutation. Do not reintroduce this target; add cases to mocks-check instead.

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

## test-everything: Every test surface in ONE run, continuing past failures, ending in a matrix
##                  (local pre-PR sweep; replaces the retired scripts/run-all-tests.sh)
.PHONY: test-everything
test-everything:
	@echo "🧪 Running every test surface: backend, frontend, e2e."
	@echo "   A failing surface does NOT stop the run — the matrix at the end is the verdict."
	@rc_go=0; rc_web=0; rc_e2e=0; \
	echo ""; echo "────── 1/3  backend (go test) ──────"; \
	$(MAKE) --no-print-directory test || rc_go=$$?; \
	echo ""; echo "────── 2/3  frontend (vitest) ──────"; \
	$(MAKE) --no-print-directory web-test || rc_web=$$?; \
	echo ""; echo "────── 3/3  e2e (playwright) ──────"; \
	if command -v lsof >/dev/null 2>&1; then \
	  stale=$$(lsof -ti :8484 2>/dev/null || true); \
	  if [ -n "$$stale" ]; then \
	    echo "⚠️  Killing stale server on :8484 (pids: $$stale)."; \
	    echo "    playwright.config.ts sets reuseExistingServer outside CI, so a leftover"; \
	    echo "    server would make the e2e verdict describe a DIFFERENT build than this one."; \
	    kill -9 $$stale 2>/dev/null || true; \
	  fi; \
	else \
	  echo "ℹ️  lsof unavailable — cannot check for a stale :8484 server."; \
	fi; \
	$(MAKE) --no-print-directory test-e2e || rc_e2e=$$?; \
	echo ""; \
	echo "╭──────────────────────────────────────────╮"; \
	echo "│  test-everything summary                 │"; \
	echo "├──────────────────────────────────────────┤"; \
	[ $$rc_go  -eq 0 ] && echo "│  backend  (go test)      ✅ PASS         │" || echo "│  backend  (go test)      ❌ FAIL (rc=$$rc_go)   │"; \
	[ $$rc_web -eq 0 ] && echo "│  frontend (vitest)       ✅ PASS         │" || echo "│  frontend (vitest)       ❌ FAIL (rc=$$rc_web)   │"; \
	[ $$rc_e2e -eq 0 ] && echo "│  e2e      (playwright)   ✅ PASS         │" || echo "│  e2e      (playwright)   ❌ FAIL (rc=$$rc_e2e)   │"; \
	echo "╰──────────────────────────────────────────╯"; \
	failed=0; \
	for rc in $$rc_go $$rc_web $$rc_e2e; do [ $$rc -eq 0 ] || failed=$$((failed + 1)); done; \
	if [ $$failed -ne 0 ]; then \
	  echo "❌ $$failed of 3 surfaces FAILED."; \
	  exit 1; \
	fi; \
	echo "✅ All 3 surfaces passed."

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
#
# -timeout 25m on every `go test ./...` here is not decoration. Go's default
# is 10m PER PACKAGE, and internal/server alone takes ~500s on an idle Mac.
#
# That ~500s is a macOS TEMP-FILESYSTEM cost, not the package being slow —
# measured 2026-08-10 on the same commit: 532s with a normal TMPDIR, 33.7s with
# TMPDIR on a RAM disk, and 35.5s on Linux. The Mac used ~61s of CPU across
# 538s of wall clock, i.e. it was blocked on writes, not computing. So a local
# `TMPDIR=/Volumes/<ramdisk> make test` is ~16x faster. The timeout below stays
# regardless: it is the guard for CI and for anyone without a RAM disk.
# Running packages in parallel (which `./...` does) makes them contend, and
# a contended run tips past 600s and dies with "panic: test timed out" —
# naming whichever test happened to be running, which looks like a real
# failure in an unrelated test and sends you debugging the wrong thing.
# Observed 2026-08-09. The other three ./... targets already had it; these
# two did not.
coverage:
	@echo "📊 Generating coverage report..."
	@go test ./... -coverprofile=coverage.out -covermode=atomic -timeout 25m
	@go tool cover -html=coverage.out -o coverage.html
	@echo ""
	@echo "Coverage summary:"
	@go tool cover -func=coverage.out | grep total | awk '{printf "  Total: %s\n", $$3}'
	@echo ""
	@echo "📄 Detailed report: coverage.html"

## coverage-check: Verify coverage meets 30% threshold (full suite)
coverage-check:
	@echo "🎯 Checking coverage threshold..."
	@go test ./... -coverprofile=coverage.out -covermode=atomic -timeout 25m >/dev/null 2>&1
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
ci: mocks-check staticcheck sdkguard test-all-short coverage-check-short
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

# Wave 0 of the silent-failure sweep. See .golangci.yml for why this enables
# exactly one linter, and docs/audits/2026-08-11-silent-failure-error-discards.md
# for the population it measures. Deliberately NOT wired into `make ci`: 922
# findings is a backlog to burn down over waves 4-13, not a gate to fail on today.
#
# 2026-08-18: .golangci.yml now also carries interfacebloat + nolintlint (the
# interface-width gate). Both targets below therefore pass --enable-only, which
# OVERRIDES the config's enable list. Without it a bare `golangci-lint run`
# reports errcheck+interfacebloat together and the attributability this file's
# header exists to protect is gone. Do not drop the selector.
.PHONY: lint-errcheck lint-errcheck-full lint-width lint-width-full
lint-errcheck: ## Run the Wave 0 errcheck config (exclusions applied)
	golangci-lint run --enable-only errcheck ./...

lint-errcheck-full: ## Same, with a count — use this to verify a new exclusion actually matched
	@golangci-lint run --enable-only errcheck ./... 2>&1 | tail -3

lint-width: ## Interface-width gate (interfacebloat + nolintlint), same selector CI uses
	golangci-lint run --enable-only interfacebloat,nolintlint ./...

lint-width-full: ## Same, with a count — compare against .github/interface-width-baseline.json
	@golangci-lint run --enable-only interfacebloat,nolintlint ./... 2>&1 | tail -3
