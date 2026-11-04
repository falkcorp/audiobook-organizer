<!-- file: docs/agent-tasks/platform-core/PC-06-02-s2r3-flip-promregisterer.md -->
<!-- version: 1.0.0 -->
<!-- guid: 69d76b55-19cb-4852-bc7f-b75028a1bd40 -->
<!-- last-edited: 2026-11-04 -->

# PC-06-02 — S2 r3 flip: the telemetry facade's exporter is deleted, app.Options.PromRegisterer is set, the contract test reads Provider.Handler()

| Field | Value |
|---|---|
| ID | PC-06-02 |
| Tier | **T2 Sonnet/high** — one registry switch checked by a golden; the install order is fixed by the spec and every step has a test (see Planner notes on the size this brief gives it) |
| Wave | W06 (S2, release `S2r3`), serialized step 2 — the flip, last PR of W06 |
| Depends on | PC-06-01 |
| Trailer | `Stage-Release: S2 flip` |
| Required checks | 09 §9.2.1's table plus `compat-rehearsal` and `unit-lifecycle` |
| Spec | 09 §9.4 S2 "Moves" (install code deleted; `PromRegisterer`), "Flip PR" (r3), "Done when" (S2 r3 items); 05-metrics §3 (`Shared`, `NewShared`), §6 (who serves `/metrics` at r3; "What it scrapes"; `TestContractScrapesServedHandler`; `TestKernelFamiliesGathered`; the S2 r3 golden-diff row), §13; 05-logging §11 items 1 and 4; 06 §6.4.5 (`PromRegisterer`, `ServedMeterProvider` deleted in the r3 PR), §6.4.8 (`coretest.MetricsProvider`), §6.9 (kernel boot-phase series); 04 §8.1 S2 row (`PromRegisterer`) |
| Digest | `docs/plans/platform-core/digests/S2.md` §4.1, §5 rule 8, §6 "r3", §8 "r3" |

**Gate.** Open and make green any time; merge only after OWNER-05's `S2r2` row is current and PC-06-01 has merged.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-06-02 -b feat/pc-06-02-s2r3-flip origin/main
cd ../audiobook-organizer-pc-06-02 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| modify | `internal/telemetry/telemetry.go` (delete the install code and the exporter on the default registry) |
| modify | `main.go` |
| modify | `internal/telemetrypolicy/` (the contract test scrapes `Provider.Handler()` built by `coretest.MetricsProvider`) |
| modify | `ci/stage.txt` |
| also touched (see Planner notes) | `cmd/root.go` (delete `setupFileLogging`, call `app.InstallProcessDefaults`), `internal/server/server.go` (delete the second `slog.SetDefault` and `log.SetOutput(aw)`), `internal/telemetry/sdklog.go` and `errorhandler.go` (installs deleted; the copies live in `core/app` since PC-06-01), `internal/metrics/metrics.go` (`Register` through the `*metrics.Shared`), `internal/books/module.go` (the activity `Logging.Tap`), `core/app/options.go` (delete `ServedMeterProvider`), `core/coretest/` (`MetricsProvider`), `core/metrics/` (`Shared`, if absent), `core/kernel/` (`TestKernelFamiliesGathered`) |
| create | `changelog.d/<date>_pc_06_02_s2r3_flip.md` (headerless; release-note lines below) |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
grep -n 'otel.SetTracerProvider(tp)\|otel.SetMeterProvider(meterProvider)' internal/telemetry/telemetry.go     # 2 (~287, ~461)
grep -n 'prometheus.New(append(\[\]prometheus.Option{prometheus.WithoutScopeInfo()}' internal/telemetry/telemetry.go   # 1 (~378)
grep -n 'var setOTelLogger = otel.SetLogger' internal/telemetry/sdklog.go                                     # 1
grep -n 'otel.SetErrorHandler(newRateLimitedErrorHandler' internal/telemetry/errorhandler.go                 # 1
grep -n '^func Register()' internal/metrics/metrics.go                                                        # 1 (~300)
grep -n '^func setupFileLogging' cmd/root.go                                                                  # 1
grep -n 'log.SetOutput(aw)\|slog.SetDefault(slog.New(handler))' internal/server/server.go                    # 2
grep -n 'inScopePrefixes' $(git ls-files | grep -E 'series_contract_test\.go$') | head -2                    # >= 1
grep -n 'promhttp.Handler()' $(git ls-files | grep -E 'series_contract_test\.go$') | wc -l                   # >= 1 (HEAD's scrape, replaced here)
grep -n 'type Shared struct\|func NewShared' core/metrics/*.go | wc -l                                        # 2 if present (Step 0)
grep -n 'func MetricsProvider' core/coretest/*.go | wc -l                                                     # 1 if present (Step 0)
grep -n 'ServedMeterProvider' core/app/*.go | wc -l                                                           # >= 1 (deleted here)
grep -n 'InstallProcessDefaults' core/app/process_defaults.go | head -1                                       # exists (PC-06-01)
grep -E '^(shipped_stage|release):' ci/stage.txt                                                              # S2r2 / S2r2
```

**STOP and report** if `ci/stage.txt` does not name `S2r2`, or PC-06-01's helper is absent.

## Steps

0. **Core prerequisites.** If `metrics.Shared`/`NewShared` (05-metrics §3) or `coretest.MetricsProvider(t, opts)`
   (06 §6.4.8: boots one zero-module `coretest` harness with `opts` and returns that boot's Provider) are missing,
   add them exactly as specified, with tests.
1. **One registry from here to S8** (05-metrics §6 table, row "S2 r3–S7"): ABO passes
   `PromRegisterer: metrics.NewShared(prometheus.DefaultRegisterer)`; ABO's `metrics.Register` registers its legacy
   client_golang families through that `*metrics.Shared`; the App provider's Prometheus exporter registers on it; gin's
   legacy `/metrics` route stays (same path, same compression exclusion, same any-credential check) and its handler
   becomes the App's `Provider.Handler()`. Delete `app.Options.ServedMeterProvider` and every use (06 §6.4.5: "S2 r1–r2
   only; deleted in the S2 r3 PR"); stage 2 now records into the App's provider.
2. **Delete the façade's install code** (`internal/telemetry`): `InitOTEL`'s `otel.SetTracerProvider`,
   `otel.SetMeterProvider`, the exporter on the default registry, `otel.SetLogger` (`sdklog.go`), `otel.SetErrorHandler`
   (`errorhandler.go`). The books telemetry vocabulary (attribute keys, views, `Meter(name)`, `SetVersion`/`Version`,
   `HistogramBuckets()`) stays at its path permanently (05-metrics §13).
3. **ABO installs the process defaults** — after `app.New` returns, `serve` calls
   `restore, err := app.InstallProcessDefaults(a)` (a failure fails boot) before `a.Run`. In the same PR, delete
   `setupFileLogging` (`cmd/root.go:698-723`) and its three call sites, and the second `slog.SetDefault` plus
   `log.SetOutput(aw)` (`internal/server/server.go:1134-1155`) — nothing else (05-logging §11 item 1).
4. **Activity feed unchanged** — the activity `Writer`, which received slog text through that second default, is fed
   by a books `Logging.Tap` at info that renders each record with a `slog.TextHandler` into it, so its line parsing and
   its `system` entries are unchanged (05-logging §11 item 1). Register the tap from `books.Register` (or the books
   start hook that starts the writer).
5. **File output** — ABO sets `core.log.output=file` with the default `core.log.file.path` (`logs/abo.log` under the
   state directory), as 05-logging §11 item 1 requires. The mechanism is a spec gap (see Planner notes): if your
   base has a reviewed way for the books module or `app.Options` to default a core key, use it; otherwise **STOP and
   report** before merging, because without it ABO stops writing a log file.
6. **Contract test onto the served handler** (`internal/telemetrypolicy`, 05-metrics §6 "What it scrapes"): build the
   Provider with `coretest.MetricsProvider(t, app.Options{MetricNamespace: "audiobook_organizer", MetricPrefix:
   "audiobook_organizer", PromRegisterer: <fresh *metrics.Shared over a fresh registry>})` and scrape its `Handler()`,
   never `promhttp.Handler()`. Add **no** prefix to `inScopePrefixes`. Add the seeding row for the kernel's own families
   (the zero-module boot inside `MetricsProvider`) and their golden lines (`audiobook_organizer_kernel_boot_phase_seconds`
   and any other kernel family of S2). One request through ABO's production engine to a sampled `:id` legacy route must
   record gin's template (`c.FullPath()`, for example `/api/v1/audiobooks/:id`) as `http_route` through otelgin.
7. **`ci/stage.txt`**: `shipped_stage: S2r3`, `release: S2r3`.
8. **Release-note lines** (changelog fragment): the log file moves under the state directory
   (`<state dir>/logs/abo.log`; the old working-directory `logs/audiobook-organizer-<date>.log` is no longer written);
   `log_level` now sets the handler level and takes effect without a restart; `/metrics` gains the kernel's own
   `audiobook_organizer_kernel_*` families.

## Code contract

```go
// core/metrics (05-metrics §3)
type Shared struct{ /* unexported */ }
func NewShared(reg prometheus.Registerer) *Shared // reg must also be a prometheus.Gatherer
func (s *Shared) Register(c prometheus.Collector) error
func (s *Shared) MustRegister(cs ...prometheus.Collector)

// core/coretest (06 §6.4.8)
func MetricsProvider(t testing.TB, opts app.Options) *metrics.Provider

// core/app (06 §6.4.5): PromRegisterer *metrics.Shared — set by ABO from this PR; ServedMeterProvider deleted
```

## TDD tests to write first

- `TestContractScrapesServedHandler` (05-metrics §6) — the handler under test is the one the legacy `/metrics` route
  serves at this stage.
- The seeded series contract test — golden diff **adds only** the kernel's `audiobook_organizer_*` kernel families and
  renames none; `TestHEADSeriesStillPresent` and `TestHistogramBucketsPinned` green.
- `TestKernelFamiliesGathered` (05-metrics §12; a `kernel` test in core) — one zero-module `coretest` boot with
  `PromRegisterer` set to a fresh registry: the scrape carries `<namespace>_kernel_boot_phase_seconds` (from S2 r3).
- `TestS2r3_SampledIDRouteRecordsGinTemplate` (coined) — the `:id` route's series carries gin's template as
  `http_route`.
- `TestLogLevelChangeAppliesAtRuntime_ABO` (coined; "level change applies", 09 S2 r3) — `PUT /api/v1/config` with
  `log_level=debug` makes a debug record appear without a restart.
- `TestInstallProcessDefaults_RestoreAndExclusive` stays green; the activity feed's existing tests stay green
  unchanged (the tap feeds the same lines).

## Acceptance

```bash
grep -rn 'otel.SetMeterProvider\|otel.SetTracerProvider\|otel.SetLogger\|otel.SetErrorHandler\|otel.SetTextMapPropagator' --include=*.go internal | grep -v _test | wc -l   # 0
grep -n '^func setupFileLogging' cmd/root.go | wc -l                                       # 0
grep -n 'log.SetOutput(aw)' internal/server/server.go | wc -l                              # 0
grep -rn 'ServedMeterProvider' --include=*.go . | wc -l                                    # 0
grep -n 'InstallProcessDefaults' cmd/root.go | wc -l                                       # 1
POL=$(dirname $(git ls-files | grep -E 'series_contract_test\.go$')); go test ./$POL/ -count=1   # ok
git diff origin/main -- $(git ls-files | grep -E '(^|/)series\.golden$') | grep '^-[^-]' | wc -l   # 0 (no line removed or renamed)
git diff origin/main -- $(git ls-files | grep -E '(^|/)series\.golden$') | grep '^+[^+]'          # only audiobook_organizer_kernel_* lines
(cd core && go test ./kernel/ -run TestKernelFamiliesGathered -count=1)                    # ok
grep -E '^(shipped_stage|release):' ci/stage.txt                                           # shipped_stage: S2r3 / release: S2r3
make ci                                                                                    # exit 0
make -C core ci                                                                            # exit 0
#   on the PR: compat-rehearsal and unit-lifecycle green; docker-image green with the credentialed smoke scrape
```

## Invariants and STOP conditions

- Exactly one registry is scraped, and the series-name set never drops (05-metrics §6). A missing HEAD family, a
  duplicate series or a `/metrics` 500 → STOP.
- Two exporters never share one registry: the façade's exporter is deleted in this same PR that sets `PromRegisterer`.
- Core never calls the seven globals outside `app.InstallProcessDefaults` (`TestNoGlobals`).
- The activity feed's entries are unchanged; the activity `Writer`'s line parsing, `StandardLogger`'s system-log writes
  and HEAD's pruner stay until the S5 `logjournal` release.
- No stored format changes; rollback is a binary swap.
- CLAUDE.md concurrency: no library-scale loop.

## Out of scope

Moving the legacy families to the private registry and the kernel's `Infra` `/metrics` mount (S8); `logjournal` (S5);
any change to the perimeter (W05) or storage (S2b).

## Rollback

Binary swap to the S2r2 build. The older build writes its log to HEAD's working-directory path again and serves
`/metrics` from the façade's exporter; nothing stored changes.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every changed file; headerless changelog fragment;
conventional commit `feat(s2): S2 r3 flip — one registry via PromRegisterer; process defaults from core` ending with
`Stage-Release: S2 flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase` once every
required check (`compat-rehearsal`, `unit-lifecycle` included) is green on an up-to-date branch; never squash; never
`--admin`.

## Planner notes

- This brief carries the ABO half of 05-logging stage 1 (calling `app.InstallProcessDefaults`, deleting
  `setupFileLogging`, the second `slog.SetDefault` and `log.SetOutput(aw)`, the activity tap) that the inventory gives
  to PC-06-01, because the spec ties those installs to the PR that deletes the façade's exporter and sets
  `PromRegisterer` (see PC-06-01's notes). The row's files therefore miss `cmd/root.go`, `internal/server/server.go`,
  `internal/telemetry/sdklog.go`, `internal/telemetry/errorhandler.go`, `internal/metrics/metrics.go`,
  `internal/books/module.go`, `core/app/options.go`, `core/coretest/`, `core/metrics/` and `core/kernel/`.
- With that scope the tier T2 is low; consider T3 (Opus/high) for the install order and the metric path.
- Spec gap: how ABO defaults `core.log.output=file` (05-logging §11: "set by the books module's defaults") is not
  specified by any S2 API; step 5 stops on it.
