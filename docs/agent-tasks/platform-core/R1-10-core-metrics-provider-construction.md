<!-- file: docs/agent-tasks/platform-core/R1-10-core-metrics-provider-construction.md -->
<!-- version: 1.1.0 -->
<!-- guid: 1308b2a6-0800-4e0a-99b8-026da89fd137 -->
<!-- last-edited: 2026-11-05 -->

# R1-10 — core/metrics provider construction (namespace and scope options), internal/telemetry/contract renamed internal/telemetrypolicy, and the dashboard-reference test with promtool

| Field | Value |
|---|---|
| ID | `R1-10` |
| Tier | **T3** (Opus / high): provider construction moves while every served series must keep its meaning; the test gates every later rename |
| Release / wave | R1 / W07 |
| Depends on | R1-02 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-metrics §13 item 1, §6, §12 (TestDeployReferencedSeriesServed, TestPromRules_Promtool); 09 §9.2.7; 09 §9.2.1 (extraction-gates: the dashboard-reference test); 04 §2.4 (internal/telemetrypolicy) |
| Branch | `agent/platform-core-r1-10-core-metrics-provider-construction` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/metrics/`
- `core/`: `go.mod`, `go.sum`
- `go.sum`
- `go.mod`
- `Makefile`
- `internal/telemetry/`: `telemetry.go`, `endpoint.go`, `resource.go`, `ratchet_test.go`
- `internal/telemetry/contract/`
- `internal/telemetrypolicy/`
- `.github/dependabot.yml`
- `core/archtest/thirdparty_allow.txt`
- `.github/workflows/extraction-gates.yml`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-10" -b agent/platform-core-r1-10-core-metrics-provider-construction origin/main
cd "$REPO/.worktrees/platform-core-r1-10"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Move ABO's telemetry **provider construction** into core `metrics` (05-metrics §13 item 1, "to core `metrics` in
R1": provider construction, the endpoint validator, the exporter wiring and the resource, with the namespace and
the scope as constructor options; core only **returns** providers, 03 R5), rename HEAD's series contract package
`internal/telemetry/contract` to `internal/telemetrypolicy` (04 §2.4), and land the **dashboard-reference test**
`TestDeployReferencedSeriesServed` with `promtool test rules` as an `extraction-gates` job (09 §9.2.1; 09 §9.2.7;
05-metrics §12). That test gates every later metric rename in R1 and R2.

**The process-global installs stay in ABO's façade** (`internal/telemetry`: `otel.SetTracerProvider`,
`telemetry.go:287`; `otel.SetMeterProvider`, `:461`; `otel.SetErrorHandler`, `errorhandler.go:121`;
`otel.SetLogger`, `sdklog.go:39`; the propagator; the default-registry exporter) until R1-15 replaces them with
`app.InstallProcessDefaults` and the App's private registry (05-metrics §13 item 1; 04 §8.1 R1). Every served
series keeps its name, labels and meaning in this PR.

## Contract (05-metrics §3, minimum surface for R1-10)

`package metrics` (`core/metrics`, L0): `Options`, `OTLPMetrics`, `OTLPTracing`, `Provider` with `New`,
`MeterProvider`, `TracerProvider`, `Propagator`, `Registerer`, `Gatherer`, `Handler`, `Shutdown`, `Core`, `Module`;
`Scope` with `Prefix`, `Meter`, `Tracer`, `Propagator`, `Allowed`, `View`; `ErrInstrumentName`, `ErrScopeTaken`,
`ErrFamilyTaken`; the exporter construction exactly as 05-metrics §3 states (`prometheus.WithRegisterer(reg)`,
`WithoutScopeInfo()`, `WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes)`, plus the Go and
process collectors on the private registry), the one-owner-per-family wrapper of §3, the instrument rules of §3.
**Minimum-surface rule (04 §8 intro):** `Options.Redactor *log.Redactor` needs 05-logging §3's `log.Redactor`,
which R1-32 creates (wave W08, after this task; it also adds the field to `Options`, the redacting span exporter
and `TestTrace_NoApikeyInSpanAttributes`). Declare `Options`, `OTLPMetrics` and `OTLPTracing` in their own file,
`core/metrics/options.go` (R1-32's row names that file), without the `Redactor` field and with a doc comment saying
R1-32 adds it. No other §3 member may be dropped.

## Invariants and the tests that prove them

- Two Providers are isolated (`TestTwoProvidersIsolated`); the Go and process collectors are on a fresh Provider's
  private registry (`TestGoAndProcessCollectorsPresent`); bad names fail (`TestScope_RejectsBadNames`); namespaces
  (`TestNamespace_CoreScopesUseAppNamespace`); one owner per family (`TestFamilyTaken_ModuleAndCoreShareNamespace`);
  advice buckets (`TestAdviceBucketsOutsideScope`); resource (`TestResource_ServiceNameAndSDKDefaults`); module
  namespaces (`TestModuleNamespace_SingleAndMulti`, `TestModuleNamespace_StableUnderEnablement`); translation
  pinned (`TestTranslationStrategyPinned`); bad OTLP endpoint degrades (`TestOTLPBadEndpointDegrades`); flush on
  shutdown (`TestShutdownFlushes`). All names are 05-metrics §12's; write them in `core/metrics` first. Tests that
  need pieces landing later are owned by the rows that bring them, not by this row: `TestNoGlobals` and
  `TestKernelFamiliesGathered` by R1-13, `TestFamilyTaken_ThroughGlobalProvider` (and `TestContractScrapesServedHandler`)
  by R1-15, `TestStage2_ServerAddressIgnoresHost` and `TestHandler_NoDoubleGzip` by R1-17,
  `TestTrace_NoApikeyInSpanAttributes` by R1-32. Do not write them here.
- ABO's served series do not change: HEAD's `TestSeriesContract`, `TestDeclaredFamiliesInGolden`,
  `TestReservedAISeries`, `TestAIFamiliesOnScrape` (moved into `internal/telemetrypolicy`) pass unchanged against
  their goldens; `internal/telemetry/ratchet_test.go`'s client_golang ratchet stays at its baseline.
- `TestDeployReferencedSeriesServed` (spec name; 05-metrics §12 paragraph after the table; the
  "dashboard-reference test" of 09 §9.2.1): reads every series name that `deploy/grafana/dashboards/*.json` and
  `deploy/prometheus/*.yml` reference (PromQL selectors in panel `expr`s and rule `expr`s; recording-rule outputs
  defined in `recording-rules.yml` count as served when their inputs are) and fails unless each is served by the
  scrape the contract test builds. Lives in `internal/telemetrypolicy`.
- `TestPromRules_Promtool` (HEAD's, `internal/opsmetrics/promrules_test.go:116`) runs `promtool test rules`
  in the new `extraction-gates` job with `promtool` installed (pinned release, checksum verified); it must not skip
  there. Leave the test where it is (its file is not in this row).

## Dependency check

R1-02 merged (`test -f .github/workflows/extraction-gates.yml && echo ok`).

## Re-verify (expected at `f7ac5b11e`)

```bash
M=github.com/falkcorp/audiobook-organizer
wc -l internal/telemetry/telemetry.go internal/telemetry/endpoint.go internal/telemetry/resource.go   # expect 466, 184, 60
grep -n 'otel.SetTracerProvider\|otel.SetMeterProvider' internal/telemetry/telemetry.go               # expect :287 and :461
ls internal/telemetry/contract                                       # expect declared_families_test.go series_contract_test.go testdata
git grep -l "\"$M/internal/telemetry/contract\"" -- '*.go' | wc -l   # expect 0 (test-only package)
git grep -l "\"$M/internal/telemetry\"" -- '*.go' | wc -l            # expect 18 importer files (they keep the façade path)
git grep -n 'func TestPromRules_Promtool' -- '*.go'                  # expect internal/opsmetrics/promrules_test.go:116
ls deploy/grafana/dashboards deploy/prometheus                        # expect 2 dashboards; alert-rules.yml, alert-rules.test.yml, recording-rules.yml, scrape-config.yml, README.md
grep -n '"internal/telemetry/contract"\|"internal/telemetry"' internal/arch/layering_test.go   # expect both keys
```

## Steps (outline; the design inside `core/metrics` is yours within 05-metrics §3)

1. Write the 05-metrics §12 tests listed above in `core/metrics` (red), then implement `core/metrics` by
   re-implementing (04: "re-implemented from `internal/telemetry`'s `telemetry.go`, `endpoint.go`, `resource.go`",
   not a verbatim move) the provider, exporter wiring, endpoint validation and resource with namespace and scope as
   options. Core's `go.mod` gains the OTel, client_golang and `otlptranslator` requirements at **the same versions
   as the root** (`check_module_skew.py` must pass); run root `go mod tidy` in the same PR (03 §3.2 item 4). Add the
   third-party paths to `TestCoreImportsOnlyCore`'s one allowlist file (`core/archtest/thirdparty_allow.txt`). Pin
   `go.opentelemetry.io/otel/exporters/prometheus` at the version 13 §13.5.2 names (v0.69.0; it is the root's
   version too): add `--pin go.opentelemetry.io/otel/exporters/prometheus@v0.69.0` to root `make ci`'s
   `check_module_skew.py` call (`Makefile`), a Dependabot `ignore` for it (`.github/dependabot.yml`) and its path to
   the third-party allowlist `core/archtest/thirdparty_allow.txt`; root `go.mod` and `go.sum` change only as
   `go mod tidy` requires.
2. ABO façade: `internal/telemetry`'s `telemetry.go`, `endpoint.go`, `resource.go` build their provider through
   `metrics.New` and keep doing the global installs (ABO-side) exactly as today, so every caller and series is
   unchanged. `attr.go`, `views.go`, `meter.go`, `SetVersion`/`Version`, `config.go` stay ABO (05-metrics §13 item 1).
   Delete code in the three files that `core/metrics` now owns; do not leave two implementations.
3. `git mv internal/telemetry/contract internal/telemetrypolicy` (package name `telemetrypolicy`); update
   `layerOf` (rename the key) and any reference in `ci/gates.txt` or scripts.
4. Write `TestDeployReferencedSeriesServed` in `internal/telemetrypolicy` (red if you delete a served family in a
   scratch edit; green on the tree).
5. `extraction-gates.yml`: add job `dashboard-reference` (`timeout-minutes` ≤ 30; `needs: changes`, skip-as-success
   `if:`) that installs `promtool` and runs `go test ./internal/telemetrypolicy/ -run 'TestDeployReferencedSeriesServed' -count=1`
   and `go test ./internal/opsmetrics/ -run TestPromRules_Promtool -count=1`; add it to the terminal job's `needs`.
6. `internal/arch/layering_test.go`: rename the `contract` key; `internal/telemetry` keeps its key (its computed
   layer now includes the mapped core import, layer −1).
7. Header bumps; fragment `changelog.d/<today>_core_metrics_provider.md` (`### Changed`: "Internal: telemetry
   provider construction moved into the core module; every metric keeps its name."). Commit(s):
   `feat(core): metrics provider construction`, `refactor(telemetry): rename contract to telemetrypolicy`,
   `test(telemetry): dashboard-reference test in extraction-gates`.

## Acceptance

```bash
go -C core test ./metrics/ -count=1 -race                  # expect ok
go test ./internal/telemetry/... ./internal/telemetrypolicy/ -count=1   # expect ok (series contract unchanged)
go test ./internal/telemetrypolicy/ -run TestDeployReferencedSeriesServed -count=1 -v   # expect PASS
promtool --version && go test ./internal/opsmetrics/ -run TestPromRules_Promtool -count=1 -v   # expect PASS, not SKIP
python3 scripts/ci/check_module_skew.py                     # expect exit 0
grep -l 'go.opentelemetry.io/otel/exporters/prometheus' core/archtest/thirdparty_allow.txt .github/dependabot.yml Makefile | wc -l   # expect 3
git grep -n 'otel.Set\|prometheus.DefaultRegisterer\|MustRegister' -- core | wc -l   # expect 0 (03 R5)
make ci && make -C core ci                                  # expect exit 0
```

## STOP conditions

- A served series would change name, labels or type: stop; that is a rename, which needs its dashboard and rule
  edits and a release-note line in the same PR (09 §9.2.7), and none is planned here.
- `go build ./...` at the root reports `ambiguous import` after core gains the OTLP exporters: re-run R1-01's
  `genproto-exclude-root`; do not add an exclude to the root `go.mod`.

## Out of scope

`app.InstallProcessDefaults`, the App's private registry as the served one, `Provider.Registerer()` for ABO's
client_golang families, the legacy gin `/metrics` serving `Provider.Handler()` (R1-15). The HTTP metric changes
(R1-17).

## Rollback

Revert the PR; the façade returns to building its own provider. No stored data changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
