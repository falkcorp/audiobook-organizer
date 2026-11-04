<!-- file: docs/agent-tasks/platform-core/PC-06-01-logging-stages-process-defaults.md -->
<!-- version: 1.0.0 -->
<!-- guid: 78b99470-3760-475b-90a8-623521dad951 -->
<!-- last-edited: 2026-11-04 -->

# PC-06-01 — 05-logging stages 1-2: app.InstallProcessDefaults, file output, the second SetDefault removed, the books shim over core handles

| Field | Value |
|---|---|
| ID | PC-06-01 |
| Tier | **T2 Sonnet/high** — logging behaviour changes owner; the helper and the shim are specified, but the order of installs relative to the telemetry façade decides whether series stay scraped |
| Wave | W06 (S2, release `S2r3`), serialized step 1 |
| Depends on | OWNER-05 (the `S2r2` soak row) |
| Trailer | `Stage-Release: S2 3` |
| Spec | 05-logging L3 and L6 ("Decisions at a glance"), §3 "Process defaults" (the `InstallProcessDefaults` contract), §3 `Logging`/`Settings`/`OpenRotatingFile`, §8 (`core.log.*` keys), §10 (`TestNoGlobals`, `TestInstallProcessDefaults_RestoreAndExclusive`, `TestModuleLevels_Live`, `TestRotatingFile_RotatesAndCaps`), §11 items 1, 2, 4, 6; 09 §9.4 S2 "Logging"; 09 §9.4.1 S2 r3 row; 05-metrics §6 table (who serves `/metrics` at S2 r1–r2 versus r3) |
| Digest | `docs/plans/platform-core/digests/S2.md` §6 "r3" |

**Gate.** Open and make green any time; merge only after OWNER-05's `S2r2` row is in `ci/soak.txt`.

**What lands here, and what waits for the flip.** 05-logging §11 item 4 and 05-metrics §6 tie ABO's *call* of
`app.InstallProcessDefaults` to the release that deletes the façade's installs and sets `app.Options.PromRegisterer`:
while `PromRegisterer` is nil (r1–r2) the App's providers use a private, unserved registry, so pointing the OTel
globals at them before the flip would move every global-reading instrument off the scraped registry. This PR therefore
lands everything that is safe on its own (the core helper, file output and live levels in core, the books shim), and
**PC-06-02 makes ABO call the helper** and deletes `setupFileLogging`, the second `slog.SetDefault` and
`log.SetOutput(aw)` in the same PR as the façade's installs (see Planner notes).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-06-01 -b feat/pc-06-01-process-defaults origin/main
cd ../audiobook-organizer-pc-06-01 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| create | `core/app/process_defaults.go` |
| create | `core/app/process_defaults_test.go` |
| create | `internal/logger/shim.go` (in the row's `internal/logger/`; the stage-2 shim `FromSlog`) and its test |
| modify | `cmd/root.go`, `main.go` — **no change in this PR** (their edits move to PC-06-02; see Planner notes) |
| also touched (see Planner notes) | `core/log/` (pipeline file output and live levels, if absent at your base), the `TestNoGlobals` archtest exception list |
| create | `changelog.d/<date>_pc_06_01_process_defaults.md` (headerless) |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
grep -n '^func setupFileLogging' cmd/root.go                                          # 1 (~698)
grep -n 'setupFileLogging()' cmd/root.go | wc -l                                       # 4: 3 call sites (~108, ~201, ~260) + the definition (~698)
grep -n 'log.SetOutput(aw)\|slog.SetDefault(slog.New(handler))' internal/server/server.go   # 2 (~1139, ~1154)
grep -n 'var setOTelLogger = otel.SetLogger' internal/telemetry/sdklog.go              # 1 (~39)
grep -n 'otel.SetErrorHandler(newRateLimitedErrorHandler' internal/telemetry/errorhandler.go   # 1 (~121)
grep -n 'otel.SetTracerProvider(tp)\|otel.SetMeterProvider(meterProvider)' internal/telemetry/telemetry.go   # 2 (~287, ~461)
grep -n '^func New(subsystem string) \*StandardLogger' internal/logger/standard.go     # 1 (~25; logs through slog.Default() in logger.go's logToStdout)
grep -n 'func New(o Options) (\*Logging, error)\|func OpenRotatingFile\|func (l \*Logging) SetLevels' core/log/*.go | wc -l   # 3 if core/log has the pipeline; else 0 (Step 0)
grep -rn 'TestNoGlobals' core/archtest/ | wc -l                                        # >= 1 if PC-01-03 landed it
ls core/app/app.go core/app/options.go 2>/dev/null                                      # exist (W04/W05)
```

## Steps

0. **Core logging pipeline present?** The helper calls `a.Logging().Root()` and the file output needs
   `log.Settings.Output`, `log.FileSettings` and `log.OpenRotatingFile` (05-logging §3). If grep 8 returns 0, build
   those members of `core/log` exactly as 05-logging §3 specifies (with `TestRotatingFile_RotatesAndCaps` and
   `TestModuleLevels_Live`), and record in the PR that this row's files did not list them.
1. **`core/app/process_defaults.go`** — exactly 05-logging §3:
   ```go
   // InstallProcessDefaults points the process-wide defaults at this App's pipeline and telemetry:
   //   slog.SetDefault(a.Logging().Root())   // also redirects the stdlib "log" package
   //   otel.SetLogger(valueDroppingSink)
   //   otel.SetErrorHandler(rateLimited)
   //   otel.SetMeterProvider(a.metrics.MeterProvider())
   //   otel.SetTracerProvider(a.metrics.TracerProvider())
   //   otel.SetTextMapPropagator(a.metrics.Propagator())
   // restore() puts the previous values back (tests). A second App in the same process gets ErrProcessDefaultsTaken.
   // It never touches the Prometheus default registry.
   func InstallProcessDefaults(a *App) (restore func(), err error)
   var ErrProcessDefaultsTaken = errors.New(...)
   ```
   The value-dropping sink is ABO's `sdkLogSink` (`internal/telemetry/sdklog.go:50-110`) **copied** and re-targeted
   at `Logging.Handler()` (keep message and key names, drop values; never `logr.FromSlogHandler`). The error handler
   is a copy of `internal/telemetry/errorhandler.go`'s class-keyed rate limiter logging through the App's pipeline.
   ABO's originals stay until PC-06-02 deletes them.
2. **`TestNoGlobals`** — its exception list names exactly the seven calls of step 1, allowed only inside
   `app.InstallProcessDefaults` (and `main` packages).
3. **File output in core** (05-logging §8): `core.log.output` `stderr|stdout|file`; with `file`, the pipeline writes
   through `log.OpenRotatingFile(root, core.log.file.path, max_size_mb, max_files)` under an `os.Root` at the state
   directory (default path `logs/<app-id>.log`, so `logs/abo.log` for ABO) and tees to stderr while
   `core.log.file.tee_stderr` is true.
4. **Live levels** — `core.log.level` and `core.log.levels` are hot-reloaded: a config republish calls
   `Logging.SetLevels` (05-logging §11 item 6: "`log_level` / `--log-level` map to `core.log.level`, which now actually
   sets the handler level").
5. **Books shim (stage 2)** — `internal/logger` gains `func FromSlog(l *slog.Logger) Logger` (05-logging §11 item 2), in a new file `internal/logger/shim.go` (name coined here):
   printf-style methods format, then log with `subsystem` as an **attribute** (so per-subsystem levels work);
   progress/changes/cancel keep their current behaviour. Package-level `logger.New(...)` (`standard.go`) keeps logging through
   `slog.Default()` (`logger.go`'s `logToStdout`), so ABO's output is unchanged until PC-06-02 installs the defaults.
6. Headers; changelog fragment: "Internal: core gains `app.InstallProcessDefaults`, file output and live log levels;
   ABO's logger gains a shim over core handles. No change to ABO's logs in this PR."

## Code contract

```go
// core/app (05-logging §3; 06 §6.4.10 S2 row)
func InstallProcessDefaults(a *App) (restore func(), err error)
var ErrProcessDefaultsTaken error // 05-logging §6: "InstallProcessDefaults twice → ErrProcessDefaultsTaken"

// internal/logger (05-logging §11 item 2)
func FromSlog(l *slog.Logger) Logger
```

## TDD tests to write first

- `TestInstallProcessDefaults_RestoreAndExclusive` (05-logging §10) — after install, `otel.GetMeterProvider()`,
  `otel.GetTracerProvider()`, `otel.GetTextMapPropagator()` return the App's; an instrument created through the global
  appears in the App's scrape; `slog.Default()` logs through the pipeline; `restore()` puts all seven back; a second
  install errors; the OTel sink drops values.
- `TestNoGlobals` with the exception list of step 2.
- `TestLogLevelChangeAppliesAtRuntime` (coined; the row's "a level change applies at runtime (new test)") — a
  `coretest` boot of hello with `core.log.level=info`; a debug record is dropped; a config change to `debug` (through
  the store's republish path) makes the next debug record appear, with no restart.
- `TestLogFileUnderStateDir` (coined) — `core.log.output=file` writes `logs/<app-id>.log` under the state directory
  through `os.Root`; a `..` path is refused.
- `TestFromSlog_SubsystemIsAttribute` (coined, `internal/logger`).
- Keep green: every existing `internal/logger` and `internal/telemetry` test, the series contract test (no change).

## Acceptance

```bash
(cd core && go test ./app/ ./log/ ./archtest/ -count=1)                                   # ok each
go test ./internal/logger/... ./internal/telemetry/... -count=1                           # ok
grep -rn 'InstallProcessDefaults' --include=*.go . | grep -v '^./core/' | grep -v _test | wc -l   # 0 (ABO does not call it yet)
grep -rn 'slog.SetDefault\|otel.SetMeterProvider\|otel.SetTracerProvider' --include=*.go core | grep -v _test | grep -v 'core/app/process_defaults.go' | wc -l   # 0
make -C core ci                                                                           # exit 0
make ci                                                                                   # exit 0
```

## Out of scope

ABO calling the helper, deleting `setupFileLogging` (`cmd/root.go:698-723`), the second `slog.SetDefault` and
`log.SetOutput(aw)` (`internal/server/server.go:1134-1155`), the activity `Writer`'s books `Logging.Tap`, deleting the
façade's installs and exporter, `PromRegisterer` (all PC-06-02); the activity `Writer`'s line parsing,
`StandardLogger`'s system-log writes and HEAD's pruner (stay until the S5 release that starts `logjournal`).

## Rollback

Revert the PR; ABO's logging is unchanged by it.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every new or changed file; headerless changelog
fragment; conventional commit `feat(log): app.InstallProcessDefaults, file output, live levels; books logger shim`
ending with `Stage-Release: S2 3` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
never squash; never `--admin` past a required check. No library-scale loop (CLAUDE.md concurrency).

## Planner notes

- Row split moved: the row gives this PR ABO's switch to `app.InstallProcessDefaults` and the deletion of
  `setupFileLogging` and the second `slog.SetDefault`. 05-logging §11 item 4 ("At S2 r3 that helper takes over every
  install the façade did ... and the façade's install code ... is deleted") and 05-metrics §6 (the App's registry is
  private and unserved until `PromRegisterer` is set) require those to land in the PR that sets `PromRegisterer` and
  deletes the façade's exporter, PC-06-02. This brief leaves `cmd/root.go` and `main.go` unchanged and moves those
  edits to PC-06-02, whose row then also needs `cmd/root.go`, `internal/server/server.go` and the activity tap.
- Conflict with the spec: the row's done-check "the log file path matches HEAD's" contradicts 05-logging §11 item 1
  ("path change release-noted; the old cwd-relative path is not reproduced") and 09 §9.4.1's S2 r3 row ("log file
  path" is a listed behaviour change). This brief follows the spec: the file lives under the state directory.
- Spec gap: 05-logging §11 says `core.log.file.path` is "set by the books module's defaults", but no S2 API lets a
  module or `app.Options` default a core key, and ABO also needs `core.log.output=file` (core default `stderr`) to keep
  writing a file at all. PC-06-02 needs that mechanism; it is not specified.
- `core/log`'s pipeline members (`Logging`, `Settings.Output`, `OpenRotatingFile`, `SetLevels`) are not in any W03–W06
  row's files; Step 0 builds them here if they are missing.
