# file: core/Makefile
# version: 1.1.0
# guid: 9ab46d35-36d2-4a7e-8b04-cddc8030d483
# last-edited: 2026-11-06

# core's own Makefile (12 §12.3.11). It must run from an export of core/ alone
# (12 SC4) and, after the split, the core repo has nothing else, so every
# script a target here calls lives under core/scripts/ci/ and nothing reads a
# path above core/.

# The toolchain pin. It must equal the root Makefile's pin: the root guard
# scripts/check_toolchain_versions.py fails when they differ (12 SC10,
# 13 §13.2.2 item 2), and core's own toolchain-check reads it as the copy
# core/scripts/ci/check_core_toolchain.py compares the rest against. Commands
# and scripts never repeat the literal: they ask `make -s -C core print-toolchain`.
export GOTOOLCHAIN := go1.27.3

PYTHON ?= python3

.PHONY: print-toolchain ci build vet test-short fmt-check fix-check tidy-check archtest exclude-check toolchain-check \
	errcheck-ratchet interface-width pprof-check mocks-check web-test coverage-floor test-nightly

## print-toolchain: print the pin and nothing else (one line, e.g. goX.Y.Z).
print-toolchain:
	@echo $(GOTOOLCHAIN)

## ci: core's merge gate, the parts that exist at this point of R1.
##
## 09 §9.4 R1's CI classification decides what runs here and what runs from the
## root: per-module gates (build vet test-short fmt-check tidy-check, and the two
## ratchets over core's own baselines) run in both modules, `fix-check` runs
## here only, the rest of the root's `make ci` is root only. Gates whose subject
## core does not have yet (web-test, mocks-check, coverage-floor) print one
## "not applicable" line and exit 0; each one fails loudly the moment its
## subject appears without the real check being wired, so "not applicable" can
## not outlive its reason.
##
## 12 §12.3.11's full list also has vet-darwin vet-integration staticcheck
## replace-check version-check embed-check; each joins this target in the PR that
## creates its subject or script (the darwin and integration vets run over core
## from the root's `make ci` already, R1-02).
ci: build vet test-short fmt-check fix-check tidy-check archtest exclude-check toolchain-check errcheck-ratchet interface-width pprof-check mocks-check web-test coverage-floor
	@echo "core ci passed"

build:
	go build ./...

vet:
	go vet ./...

test-short:
	go test ./... -short -race -count=1 -timeout 25m

## test-nightly: everything -short skips (property tests and the rest), for
## nightly.yml's core job. Not part of ci.
test-nightly:
	go test ./... -race -count=1 -timeout 25m

## fmt-check: gofmt -l, excluding the npm trees (they ship .go files).
fmt-check:
	@unformatted=$$(gofmt -l . | sed 's|^\./||' | grep -Ev '^(web|examples/[^/]+/web)/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

## fix-check: go fix -diff prints nothing (13 §13.3). Core only: the root has
## no go fix gate (03 §3.2 item 4).
fix-check:
	go fix -diff ./...

tidy-check:
	go mod tidy -diff

## archtest: the R1 and R4 guards (TestNoAppRequire, TestCoreImportsOnlyCore,
## NoInit over ./..., the engine self-tests, and the R4 layer guard in
## internal/archtest_layers_test.go).
archtest:
	go test -count=1 ./archtest/... ./internal/

## exclude-check: the genproto exclude check in core mode (03 §3.2 item 1):
## the present/misplaced scan of core/ and the core-mode workspace probe, run in
## a throwaway copy under a temp dir. "not applicable" until core's own graph
## reaches the requirer (Pebble, later in R1).
exclude-check:
	$(PYTHON) scripts/ci/check_genproto_exclude.py

## toolchain-check: core's own copies of the pin only (core/Makefile,
## core/tools/*/go.mod, core-ci.yml's go-version). Parity with the root pin is
## the root guard's job (12 SC10).
toolchain-check:
	$(PYTHON) scripts/ci/check_core_toolchain.py

## errcheck-ratchet: errcheck findings in core against core/.errcheck-baseline
## (one-way; core's own copy of the root script, so an export of core/ runs it).
errcheck-ratchet:
	@bash scripts/ci/check-errcheck-ratchet.sh

## interface-width: interfacebloat findings in core against
## core/.interface-width-baseline (one-way, core's own copy).
interface-width:
	@bash scripts/ci/check-interface-width.sh

## pprof-check: vet and run the pprof-tagged tests (no other gate compiles with
## -tags pprof). A package with no Pprof test just passes `go test -run`.
pprof-check:
	go vet -tags pprof ./...
	go test -tags pprof -run Pprof -count=1 ./...
	@echo "pprof-check: OK"

## mocks-check: core's mockery config, from the first core interface with a mock.
## Not applicable until core/.mockery.yaml exists; if it appears this target must
## be wired to check it first, so it refuses rather than passing.
mocks-check:
	@if [ -e .mockery.yaml ]; then \
		echo "mocks-check: .mockery.yaml exists but this target does not check it; wire it (09 §9.4 R1: mocks-check is per module)"; exit 1; \
	fi
	@echo "mocks-check: not applicable (no core/.mockery.yaml yet)"

## web-test: core/web's npm workspace tests, from R1-30 which creates core/web.
## Not applicable until then; refuses if core/web appears before it is wired.
web-test:
	@if [ -e web ]; then \
		echo "web-test: core/web exists but this target does not test it; wire it (09 §9.4 R1: web-test is per npm workspace)"; exit 1; \
	fi
	@echo "web-test: not applicable (no core/web yet)"

## coverage-floor: core's coverage ratchet floor, from the storage move (R1-23).
## No floor yet; refuses if a floor file appears before it is enforced.
coverage-floor:
	@if [ -e .ci/coverage-floor.txt ]; then \
		echo "coverage-floor: .ci/coverage-floor.txt exists but this target does not enforce it; wire it"; exit 1; \
	fi
	@echo "coverage-floor: no floor yet"
