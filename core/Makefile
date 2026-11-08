# file: core/Makefile
# version: 1.6.0
# guid: 9ab46d35-36d2-4a7e-8b04-cddc8030d483
# last-edited: 2026-11-08

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
	errcheck-ratchet interface-width pprof-check mocks-check web-install web-test web-lint openapi-check openapi-regen \
	generate-api-client coverage-floor embed-check test-nightly version-check script-test \
	tools readiness apidiff-gate coverage coredoc-check vulncheck

## print-toolchain: print the pin and nothing else (one line, e.g. goX.Y.Z).
print-toolchain:
	@echo $(GOTOOLCHAIN)

## ci: core's merge gate, the parts that exist at this point of R1.
##
## 09 §9.4 R1's CI classification decides what runs here and what runs from the
## root: per-module gates (build vet test-short fmt-check tidy-check, and the two
## ratchets over core's own baselines) run in both modules, `fix-check` runs
## here only, the rest of the root's `make ci` is root only. A gate whose subject
## core does not have yet (mocks-check) prints one "not applicable" line and
## exits 0; it fails loudly the moment its subject appears without the real
## check being wired, so "not applicable" can not outlive its reason. (web-test
## was one until core/web arrived, R1-30, and coverage-floor until the storage
## move, R1-23.)
##
## 12 §12.3.11's full list also has vet-darwin vet-integration staticcheck
## replace-check; each joins this target in the PR that creates its subject or
## script (the darwin and integration vets run over core from the root's
## `make ci` already, R1-02). version-check joined with R1-06, with script-test,
## which runs the unit tests of core's own CI scripts.
ci: build vet test-short fmt-check fix-check tidy-check archtest exclude-check toolchain-check errcheck-ratchet interface-width pprof-check mocks-check openapi-check web-lint web-test coverage-floor embed-check version-check script-test
	@echo "core ci passed"

build:
	go build ./...

vet:
	go vet ./...

## test-short: the -short suite under -race, writing coverage.out for
## coverage-floor (one pass: -race and -coverprofile together).
test-short:
	go test ./... -short -race -count=1 -timeout 25m -coverprofile=coverage.out

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

## openapi-check: core/openapi/core.json is what core's own `hello openapi` emits
## (05-http-api §4.6). Runs before web-lint and web-test so a stale core.json fails
## core CI before the api-client generator reads it.
openapi-check:
	@sh scripts/ci/check_openapi_fresh.sh

## openapi-regen: rewrite core/openapi/core.json (the file the api-client generates from).
openapi-regen:
	go run ./examples/hello/cmd/hello openapi

## web-install: npm ci in core/web, exactly as locked (core/web's own lockfile).
web-install:
	cd web && npm ci --no-audit --no-fund

## web-lint: core/web's type check, its ESLint (with the import boundary of 07 §7.3.1:
## nothing in core/web imports from outside it) and the generated client's freshness.
web-lint: web-install
	cd web && npm run typecheck && npm run lint
	cd web && npm run generate:check -w @mediacore/api-client

## web-test: core/web's npm workspace tests (vitest in every workspace; the
## api-client's generated-client coverage check is among them), after npm ci.
web-test: web-install
	cd web && npm test --workspaces --if-present -- --run

## generate-api-client: regenerate @mediacore/api-client's src/generated/ from
## openapi/core.json (07 §7.8). `make -C core web-lint` fails when it is stale.
generate-api-client: web-install
	cd web && npm run generate -w @mediacore/api-client

## coverage-floor: core's coverage ratchet floor, from the storage move (R1-23;
## 12 §12.3 SC5, 09 §9.4 R1). The total of the coverage.out test-short wrote
## must not fall below .ci/coverage-floor.txt, so ci runs the suite once. The
## floor only rises: a PR that raises coverage raises it (ratchet-lower.yml's
## core entry, todo.d). SC5's 70 % / 60 % targets are the readiness report's,
## not this gate's. A missing profile, floor file or unreadable number fails.
coverage-floor:
	@if [ ! -f coverage.out ]; then echo "coverage-floor: coverage.out not found; run make test-short first"; exit 1; fi
	@if [ ! -f .ci/coverage-floor.txt ]; then echo "coverage-floor: .ci/coverage-floor.txt is missing"; exit 1; fi
	@total=$$(go tool cover -func=coverage.out | awk '$$1 == "total:" { sub(/%/, "", $$3); print $$3 }'); \
	floor=$$(tr -d '[:space:]' < .ci/coverage-floor.txt); \
	case "$$total" in ''|*[!0-9.]*) echo "coverage-floor: could not read the total coverage (got '$$total')"; exit 1;; esac; \
	case "$$floor" in ''|*[!0-9.]*) echo "coverage-floor: .ci/coverage-floor.txt is not a number (got '$$floor')"; exit 1;; esac; \
	if awk -v t="$$total" -v f="$$floor" 'BEGIN { exit !(t + 0 < f + 0) }'; then \
		echo "coverage-floor: core coverage $$total% is below the floor $$floor%"; exit 1; \
	fi; \
	echo "coverage-floor: core coverage $$total% meets the floor $$floor%"

## embed-check: every embed pattern of core resolves to a file, except a pattern
## naming a build output listed in scripts/ci/embed_build_outputs.txt (empty until
## hello's UI lands, 07 §7.6.3) that this tree has not built. core builds no
## image, so there is no .dockerignore or Dockerfile comparison here; the root's
## `make embed-check` judges core's packages against the Docker context too.
embed-check:
	$(PYTHON) scripts/ci/check_embed_outputs.py

## version-check: version.Version is at or above the newest recorded checkpoint,
## hello's MinCoreVersion is at or below it, and every @mediacore/* npm package
## carries it (12 §12.4.3). Fetches refs/core-checkpoints/* from origin when the
## tree has that remote; an export of core/ alone has none ("not applicable").
version-check:
	$(PYTHON) scripts/ci/check_core_version.py

## script-test: the unit tests of core's own CI scripts (core/scripts/ci/test_*.py).
## Nothing else discovers them: the root's python tests sweep scripts/ and
## .github/workflows/scripts/ only.
script-test:
	$(PYTHON) -m unittest discover -s scripts/ci -p 'test_*.py'

## tools: build the pinned tool binaries into core/bin (ignored by core/.gitignore),
## from the nested modules under tools/ (12 §12.3.11): apidiff and gorelease (SC1),
## govulncheck (SC9) and staticcheck (pinned like the root's tools/staticcheck;
## scripts/ci/check_module_skew.py compares the two). Nothing is written outside core/.
TOOLS_BIN := $(CURDIR)/bin

tools:
	GOBIN=$(TOOLS_BIN) go -C tools/apidiff install tool
	GOBIN=$(TOOLS_BIN) go -C tools/govulncheck install tool
	GOBIN=$(TOOLS_BIN) go -C tools/staticcheck install tool

## readiness: the split-readiness measurements that are reports, never merge gates
## (12 §12.3.11): SC1 apidiff-gate, SC5 coverage thresholds, SC6 coredoc-check,
## SC9 vulncheck. It writes only under $(READINESS_OUT): one JSON result per
## criterion, then readiness.json and readiness.md with the date and the commit.
## It exits 0 whatever the verdicts (each script runs with --report, so a criterion
## that is not met is recorded, not failed) and fails only when a script crashes.
## The root-side nightly job folds the report into
## docs/plans/platform-core/split-readiness.md through a PR (split-readiness.yml).
##
## SC1_FIRST_CHECKPOINT is the first checkpoint that counts (release R2's, 12 SC1,
## e.g. v0.5.0): earlier checkpoints are reported but start no count. Unset, SC1
## cannot be met yet.
READINESS_OUT ?= $(CURDIR)/.ci/readiness
SC1_FIRST_CHECKPOINT ?=

readiness: apidiff-gate coverage coredoc-check vulncheck
	$(PYTHON) scripts/ci/readiness_report.py --out $(READINESS_OUT)

## apidiff-gate: SC1 over the newest four refs/core-checkpoints/* (core_api_gate.py).
apidiff-gate: tools
	$(PYTHON) scripts/ci/core_api_gate.py --apidiff $(TOOLS_BIN)/apidiff \
		--first-counting '$(SC1_FIRST_CHECKPOINT)' --out $(READINESS_OUT) --report

## coverage: SC5, go test -short -coverprofile over core, then the thresholds
## (total 70 %, every SDK package 60 %; core_coverage_by_package.py). The tests run
## here without -race: this measures coverage, test-short gates the merge.
coverage:
	@mkdir -p $(READINESS_OUT)
	@status=0; \
	go test -short -count=1 -coverprofile=$(READINESS_OUT)/core.out ./... > $(READINESS_OUT)/core-test.log 2>&1 || status=$$?; \
	$(PYTHON) scripts/ci/core_coverage_by_package.py $(READINESS_OUT)/core.out --min 60 \
		--exclude 'kernel/|internal/|examples/' --test-status $$status --out $(READINESS_OUT) --report

## coredoc-check: SC6, the generated reference matches core's registrations
## (not applicable until the coredoc generator lands; coredoc_check.py).
coredoc-check:
	$(PYTHON) scripts/ci/coredoc_check.py --out $(READINESS_OUT) --report

## vulncheck: SC9, govulncheck ./... with the pinned binary (core_vulncheck.py).
vulncheck: tools
	$(PYTHON) scripts/ci/core_vulncheck.py --out $(READINESS_OUT) --report
