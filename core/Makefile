# file: core/Makefile
# version: 1.0.0
# guid: 9ab46d35-36d2-4a7e-8b04-cddc8030d483
# last-edited: 2026-11-05

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

.PHONY: print-toolchain ci build vet test-short fmt-check fix-check tidy-check archtest exclude-check toolchain-check

## print-toolchain: print the pin and nothing else (one line, e.g. goX.Y.Z).
print-toolchain:
	@echo $(GOTOOLCHAIN)

## ci: core's merge gate, the parts that exist at R1's first core PR.
##
## 12 §12.3.11's full list adds vet-darwin vet-integration staticcheck
## coverage-floor replace-check version-check errcheck-ratchet interface-width
## mocks-check pprof-check web-test embed-check; each joins this target in the
## PR that creates its subject or script (R1-03 owns the classification, R1-02
## the darwin and integration vets).
ci: build vet test-short fmt-check fix-check tidy-check archtest exclude-check toolchain-check
	@echo "core ci passed"

build:
	go build ./...

vet:
	go vet ./...

test-short:
	go test ./... -short -race -count=1 -timeout 25m

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
