<!-- file: docs/agent-tasks/platform-core/PC-03-04-core-metrics-provider-construction.md -->
<!-- version: 1.0.0 -->
<!-- guid: 98d35248-0bbf-4dae-867d-fcd7f6036720 -->
<!-- last-edited: 2026-11-04 -->

# PC-03-04 — `core/metrics` provider construction with namespace and scope options; the ABO telemetry façade keeps the global installs; `internal/telemetrypolicy`

| Field | Value |
|---|---|
| ID | PC-03-04 |
| Tier | **T3 Opus/high.** The provider construction moves into core while every series name, type, label set, scope and `le` value must stay byte-identical; a mismatch drops dashboards and alerts silently. Design latitude inside `core/metrics`; the invariants and tests below are fixed. |
| Wave | W03 (S1, release `S1`) |
| Depends on | PC-03-01 (`core/log`, for `*log.Redactor` per 05-metrics §3) |
| Also requires | PC-02-07 merged (it adds `head_series.golden`, `head_series_identity.golden` and `TestHEADSeriesStillPresent`, which this PR moves and keeps green) |
| Trailer | `Stage-Release: S1 1` |
| Digest | `docs/plans/platform-core/digests/S1.md` §3 (row `metrics`, "Stay in ABO"), §4.1, §5 rule 6, §7 |
| Spec | 09 S1 row "Moves" (the `internal/telemetry` sentence) and "Done when"; 05-metrics §3 (`Options`, `OTLPMetrics`, `OTLPTracing`, `Provider`, `New`, `Shared`), §6 (the stage table: "S1 … empty"; `TestHEADSeriesStillPresent`; `TestHistogramBucketsPinned`; the smoke scrape), §13 item 1 (what moves, what stays); 09 §9.2.7 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-03-04" -b platform-core/pc-03-04-core-metrics origin/main
cd "$REPO/../audiobook-organizer-pc-03-04"
git rebase origin/main
git worktree list
make web-deps
export GOTOOLCHAIN=$(make -s -C core print-toolchain)
```

Preconditions: `ls core/log` (PC-03-01) and `ls internal/telemetry/contract/head_series_test.go` (PC-02-07). Else STOP.

## Exact files

Create: `core/metrics/provider.go`, `core/metrics/endpoint.go`, `core/metrics/resource.go`, `core/metrics/provider_test.go`,
`internal/telemetrypolicy/` (by `git mv` of `internal/telemetry/contract/` and `internal/telemetry/ratchet_test.go`).
Modify: `internal/telemetry/telemetry.go`, `internal/telemetry/endpoint.go`, `internal/telemetry/resource.go`,
`internal/telemetry/contract/` (moved), `internal/telemetry/ratchet_test.go` (moved); also `core/go.mod`,
`core/go.sum`, root `go.mod`/`go.sum` (tidy) and `internal/arch/layering_test.go` (Planner notes).

## Re-verify (expected values at the planning checkout `51a2c2acd`)

```bash
grep -n 'otel.SetTracerProvider(tp)' internal/telemetry/telemetry.go               # 1 hit (line 287)
grep -n 'otel.SetMeterProvider(meterProvider)' internal/telemetry/telemetry.go     # 1 hit (line 461)
grep -n 'otel.SetErrorHandler' internal/telemetry/errorhandler.go                  # 1 hit (line 121)
grep -n 'var setOTelLogger = otel.SetLogger' internal/telemetry/sdklog.go          # 1 hit (line 39)
grep -n 'func newMeterProvider' internal/telemetry/telemetry.go                    # 1 hit (line 377)
grep -n 'prometheus.WithoutScopeInfo()' internal/telemetry/telemetry.go            # 1 hit
grep -n 'func parseOTLPEndpoint' internal/telemetry/endpoint.go                    # 1 hit
grep -n 'func NewResourceWithEnvironment' internal/telemetry/resource.go           # 1 hit
grep -n 'func HistogramBuckets\|func Views' internal/telemetry/views.go            # 2 hits
ls internal/telemetry/contract/testdata                                             # head_series.golden head_series_identity.golden histogram_buckets.golden series.golden series_reserved.txt
grep -n 'const repoRoot = "../../.."' internal/telemetry/contract/declared_families_test.go   # 1 hit
grep -n '"\.\./\.\./\.\./deploy/grafana' internal/telemetry/contract/series_contract_test.go  # 1 hit
grep -n '"internal/telemetry/contract"' internal/arch/layering_test.go             # 1 hit
grep -lE 'otel\.(GetMeterProvider|Meter|Tracer|GetTracerProvider)\(|telemetry\.Meter\(' internal/ai/telemetry.go internal/aidispatch/metrics.go internal/database/memdb_metrics.go internal/dedup/engine.go internal/operations/registry/worker.go internal/opsmetrics/opsmetrics.go | wc -l   # 6 (the six packages that create instruments through the globals, 05-metrics §13 item 1)
```

05-metrics cites `telemetry.go:287,461`, `errorhandler.go:121`, `sdklog.go:39`, `resource.go:36-50`, `views.go:37-80` at
`748fd787a`; they are unchanged at `51a2c2acd`. A miss: STOP.

## What moves, what stays (05-metrics §13 item 1; 09 S1 "Moves")

- **To `core/metrics` (re-implemented, not moved verbatim):** provider construction (`newMeterProvider`,
  `newOTLPReader`, `initTracing`'s provider build), the endpoint validator (`parseOTLPEndpoint` and its helpers),
  the exporter wiring and the resource (`NewResourceWithEnvironment`), with namespace and scope as constructor
  options. Core **returns** providers and installs no global (03 R5).
- **Stays in `internal/telemetry` permanently:** `attr.go`, `views.go` (`Views()`, `HistogramBuckets()`),
  `meter.go` (`Meter`, `MeterFrom`, `SetVersion`, `Version`), `config.go` (`LoadConfig`, `WithMetricsOTLP`,
  `WithEnvironment`, `ParseMetricsInterval`, `Config`).
- **Stays in the façade until S2:** every process-global install — `otel.SetTracerProvider`,
  `otel.SetMeterProvider`, `otel.SetErrorHandler`, `otel.SetLogger`, the propagator, and the default-registry
  Prometheus exporter — so the six global-reading packages keep their series. `InitOTEL`'s signature and
  behaviour are unchanged for its callers.
- **To `internal/telemetrypolicy`:** `git mv internal/telemetry/contract internal/telemetrypolicy`, then
  `git mv internal/telemetry/ratchet_test.go internal/telemetrypolicy/ratchet_test.go`; package clauses `contract`
  and `telemetry` become `telemetrypolicy`; `repoRoot` becomes `"../.."`; the dashboard path loses one `../`; every
  header's `file:` line follows; `layerOf`'s key `internal/telemetry/contract` becomes `internal/telemetrypolicy`.
  Tests only; the package has no non-test file. If `ratchet_test.go` uses an unexported `telemetry` identifier,
  copy that constant into the test (STOP if it needs a function).

## Contract (05-metrics §3, copied; implement this subset at S1)

```go
package metrics // github.com/falkcorp/mediacore/metrics, L0; imports only client_golang, OTel API/SDK/exporters,
                // otlptranslator and <MOD>/log (05-metrics §3 "Imports")
type Options struct {
	AppID, ServiceName string
	MetricViews        map[string][]float64
	MetricNamespace    string // default "mediacore"; ABO: "audiobook_organizer"
	MetricPrefix       string // default AppID; ABO: "audiobook_organizer"
	Modules            []string
	ServiceVersion, InstanceID, Environment string
	Prometheus         bool
	OTLP               *OTLPMetrics
	Tracing            *OTLPTracing
	Redactor           *log.Redactor
	CardinalityLimit   int
	PromRegisterer     prometheus.Registerer
	Logger             *slog.Logger
}
type OTLPMetrics struct{ Endpoint string; Insecure bool; Interval time.Duration }
type OTLPTracing struct{ Endpoint string; Insecure bool; SampleRatio float64 }
type Provider struct{ /* unexported */ }
func New(ctx context.Context, o Options) (*Provider, error)
func (p *Provider) MeterProvider() metric.MeterProvider
func (p *Provider) TracerProvider() trace.TracerProvider
func (p *Provider) Propagator() propagation.TextMapPropagator
func (p *Provider) Registerer() prometheus.Registerer
func (p *Provider) Gatherer() prometheus.Gatherer
func (p *Provider) Handler() http.Handler
func (p *Provider) Shutdown(ctx context.Context) error
type Shared struct{ /* unexported */ }
func NewShared(reg prometheus.Registerer) *Shared
```

If `core/log` has no `Redactor` type at your base (at `51a2c2acd` `internal/logging` has none), omit the `Redactor`
field and say so in the PR (it arrives with 05-logging's stages); do not invent a `log.Redactor`. The `Scope` API of 05-metrics §3
lands with its first user (S2); do not build it here.

## Invariants (each one is a named test that must pass)

1. **Zero golden diff** against the S0 build: `TestSeriesContract` (now in `internal/telemetrypolicy`) passes with
   `testdata/series.golden` unchanged (`git diff origin/main -- '*series.golden'` is empty). 05-metrics §6 stage
   table: "S1 … empty".
2. **HEAD oracle:** `TestHEADSeriesStillPresent` passes against `testdata/head_series.golden`, unchanged.
3. **Buckets:** `TestHistogramBucketsPinned` passes against `testdata/histogram_buckets.golden`, unchanged; ABO's
   façade passes `telemetry.HistogramBuckets()` as `Options.MetricViews`, installed when the `MeterProvider` is
   built (09 S1 "Moves"; MOD10-6 in the footnotes of the digest).
4. **One exporter, one registry:** the default registry carries exactly one Prometheus exporter (no duplicate
   `target_info`), and the Go and process collectors are not registered twice; `promhttp.Handler()` still serves.
5. **Resource identity:** `service.name` is the façade's configured name (`audiobook-organizer` in production),
   merged with `resource.Default()` as HEAD does; `target_info`'s label names equal `head_series_identity.golden`'s.
6. **No global in core:** `grep -rn 'otel.Set\|SetDefault\|prometheus.DefaultRegisterer\b' core/metrics/*.go | grep -v _test`
   prints nothing; the façade passes the default registerer in.
7. **Every other check of the package** (`TestOTLPMetrics_Env*`, `TestNewMeterProvider_*`, `TestInitOTEL_*`,
   `TestRedactUserinfo`, the endpoint fuzz tests) stays green, moved to `core/metrics` where it tests moved code.

## TDD tests to write first (`core/metrics/provider_test.go`, names coined here)

`TestNew_ReturnsProvidersAndInstallsNoGlobal` (after `New`, `otel.GetMeterProvider()` is still the noop),
`TestNew_MetricViewsInstalledAtBuild` (an instrument named in `MetricViews` exports exactly those `le` values),
`TestNew_NamespaceAndPrefixDefaults` (empty → `mediacore` and `AppID`), `TestNew_PromRegistererSkipsCollectors`,
`TestResource_ServiceNameAndSDKDefaults` (05-metrics §12 names this one), `TestParseOTLPEndpoint_Forms` (moved).

## Steps

1. Re-verify. Run the series tests once on the base and keep the output.
2. Write the failing core tests; implement `core/metrics` (`resource.go`, `endpoint.go`, `provider.go`); add the
   OTel and client_golang requires to `core/go.mod` at **the root's exact versions** (`go list -m` in the root);
   `go mod tidy` in `core/` and in the root; `python3 scripts/ci/check_module_skew.py` must pass.
3. Rewire `internal/telemetry/telemetry.go`, `endpoint.go`, `resource.go` onto `metrics.New`, keeping every global
   install in the façade and `InitOTEL`'s contract.
4. Move the policy tests (above). Run the whole `internal/telemetrypolicy` suite, `internal/telemetry`, the six
   global-reading packages' tests and `go -C core test ./metrics/`.
5. Headers, `changelog.d/` fragment ("no metric, scope or bucket changes").

## Acceptance

```bash
go build ./... && (cd core && go build ./...) && echo built                         # built
go test ./internal/telemetrypolicy/ -count=1 -run 'TestSeriesContract|TestHEADSeriesStillPresent|TestHistogramBucketsPinned|TestDeclaredFamiliesInGolden' -v 2>&1 | grep -cE '^--- PASS'   # 4
go test ./internal/telemetry/ ./internal/telemetrypolicy/ -count=1                  # ok (both)
go -C core test ./metrics/ -count=1 -race                                           # ok  github.com/falkcorp/mediacore/metrics
git diff -M --numstat origin/main -- '*.golden' | awk '$1!=0 || $2!=0' | wc -l              # 0 (goldens moved as pure renames, no line changed)
python3 scripts/ci/check_module_skew.py; echo "exit=$?"                             # exit=0
make -C core ci; echo "exit=$?"                                                     # exit=0
make ci; echo "exit=$?"                                                             # exit=0
```

Smoke scrape (09 S1 "Done when"; recorded by the owner at OWNER-03 on the reference host): a credentialed
`GET /metrics` answers 200 with a non-empty family list containing `process_resident_memory_bytes` and `go_goroutines`.
In the PR, show the same against a local `make run-api` server.

## STOP conditions

- Any golden must change for a test to pass.
- `core/metrics` would need an `internal/...` import of ABO, a global install, or an ABO name (03 R1; the bucket
  table is numbers keyed by names the app supplies).
- The core module would require a dependency version different from the root's (module skew).

## Out of scope

`app.InstallProcessDefaults` and the façade's install deletion (S2 r3); the `Scope` API; `PromRegisterer` as the
App provider's bridge (S2 r3); renaming or aliasing any series (A-5: ABO keeps its names).

## Rollback

Revert the PR; no stored data; series identical by invariant 1.

## Repo rules

- Worktree only; never `go work init`. Headers on every changed file; goldens take none.
- Every commit carries `Stage-Release: S1 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
  The two `git mv`s go in their own commit before the content changes, so git records them as renames.
- A `core/go.mod` change carries the root `go mod tidy` result in the same PR (09 S0 "Module coupling").
- `gh pr merge <n> --rebase` after `core-ci`, `extraction-gates` and the other required checks pass.

## Planner notes

- The inventory row omits `core/go.mod`/`core/go.sum` and the root `go.mod`/`go.sum` (core gains the OTel and
  client_golang requires), and `internal/arch/layering_test.go` (its `internal/telemetry/contract` key).
- The inventory gives PC-03-04 only PC-03-01 as a dependency; it moves the files PC-02-07 creates, so PC-02-07 must
  have merged (it has, through OWNER-02's dependency list, so no change is needed).
- If PC-03-03 lands first, the core guard's positive control already exists; this row adds the edge
  `metrics → log` and must not remove the control.
