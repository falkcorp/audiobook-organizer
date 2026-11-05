<!-- file: docs/agent-tasks/platform-core/R1-09-core-leaves-version-entity-health.md -->
<!-- version: 1.1.3 -->
<!-- guid: f08cce47-0814-4819-8567-482be7830fb4 -->
<!-- last-edited: 2026-11-05 -->

# R1-09 — Core leaves: version 0.1.0, entity, coretest's injected log capture, and the flight recorder rewritten into core/health on a *slog.Logger

| Field | Value |
|---|---|
| ID | `R1-09` |
| Tier | **T1** (Sonnet / medium): small new leaves and one constructor rewrite |
| Release / wave | R1 / W10 |
| Depends on | R1-02, R1-07 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 04 §8.1 R1, §8.2 (mediacore/health row), §8.5 R1 (flight.go logger cut); 03 §3.3 (version, entity, coretest); 05-health §5H.10 Landing R1; 12 §12.4.3 (Version 0.1.0) |
| Branch | `agent/platform-core-r1-09-core-leaves-version-entity-health` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/version/`
- `core/entity/`
- `core/coretest/`
- `core/health/`
- `internal/diag/flight/`
- `internal/server/`: `flight_recorder.go`, `flight_recorder_test.go`, `server.go`
- `internal/server/handlers/`: `diagnostics_traces.go`, `diagnostics_traces_test.go`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`
- `core/internal/archtest_layers_test.go`: control edit only (step 5)

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-09" -b agent/platform-core-r1-09-core-leaves-version-entity-health origin/main
cd "$REPO/.worktrees/platform-core-r1-09"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Create four core leaves (04 §8.1 R1; 03 §3.3 rows `version`, `entity`, `coretest`, `health`; 05-health §5H.10
"Landing by release", R1 row; 12 §12.4.3):

| package | content in this PR |
|---|---|
| `core/version` | `const Version = "0.1.0"` and nothing else (03 §3.3 `version` row; 12 §12.4.3 "Initial value"). Never derived from `debug.ReadBuildInfo`. |
| `core/entity` | `type Ref struct{ Module, Kind, ID string }`, the one polymorphic entity reference (03 §3.3 `entity` row) |
| `core/coretest` | an injected-handler log capture for core tests (04 §8.1 R1: "an injected-handler log capture first, never `slog.SetDefault`, 03 R5") |
| `core/health` | the flight recorder, moved from `internal/diag/flight` and rewritten onto a `*slog.Logger` passed in (04 §8.5 R1 row `internal/diag/flight: flight.go`: "a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5)") |

old ==> new: `internal/diag/flight` ==> `github.com/falkcorp/mediacore/health`. Its three files move; ABO's five
importer files already import it under the alias `diagflight`, so only the import path changes there. The one
behaviour difference: the recorder's log lines go to the logger ABO passes instead of `internal/logger`'s
`flight-recorder: ` prefix (the subsystem becomes an attribute).

## Dependency check

R1-02 merged (`test -f .github/workflows/extraction-gates.yml`) **and R1-07 merged** (`test -d core/log`): this
PR's `health` imports `log` (step 4), the first in-core edge of the core guard's positive control. If either is
missing, STOP.

## Re-verify (expected at `f7ac5b11e`)

```bash
M=github.com/falkcorp/audiobook-organizer
ls internal/diag/flight                                   # expect: flight.go flight_pprof_test.go flight_test.go
git grep -l "\"$M/internal/diag/flight\"" -- '*.go'       # expect 5: internal/server/flight_recorder.go, flight_recorder_test.go,
                                                          #   server.go, handlers/diagnostics_traces.go, handlers/diagnostics_traces_test.go
git grep -n "diagflight \"$M/internal/diag/flight\"" -- '*.go' | wc -l   # expect 5 (all aliased diagflight)
grep -n 'flightLog' internal/diag/flight/flight.go | wc -l             # expect 13: the var (108), dropf (203), 11 call sites
grep -n '"github.com/falkcorp/audiobook-organizer/internal/logger"' internal/diag/flight/flight.go   # expect 1 (61)
grep -n '"internal/diag/flight"' internal/arch/layering_test.go         # expect 1 layerOf key (164 at f7ac5b11e)
grep -n 'internal/diag/flight' scripts/ci/short_test_weights.json | wc -l   # expect 0
ls core/version core/entity core/coretest core/health 2>&1 | grep -c 'No such file'   # expect 4
```

## Steps

1. **`core/version/version.go`** (new): `package version`, a doc comment citing 12 §12.4.3 (bumped only by the
   "core version" PR; CI keeps it ≥ the newest checkpoint), and `const Version = "0.1.0"`. Test
   `core/version/version_test.go`: `TestVersionIsSemver` (name coined here) asserts
   `Version` matches the regexp `^\d+\.\d+\.\d+$` (standard library only; core has no third-party import yet).
2. **`core/entity/entity.go`** (new): `package entity`,

   ```go
   // Ref is the one polymorphic reference to a module-owned entity (03 §3.3).
   type Ref struct {
       Module string
       Kind   string
       ID     string
   }
   ```

   with `func (r Ref) String() string` returning `Module + "/" + Kind + "/" + ID` (name coined here) and a test
   `TestRefString` (name coined here). No other identifier.
3. **`core/coretest/logcapture.go`** (new; all names coined here):

   ```go
   // LogCapture records the slog records written through Logger. It never touches slog.Default (03 R5).
   type LogCapture struct { /* mu sync.Mutex; recs []slog.Record; level slog.Level */ }
   func NewLogCapture(tb testing.TB) *LogCapture        // level Debug; tb.Cleanup is not needed (no global state)
   func (c *LogCapture) Logger() *slog.Logger           // slog.New(handler over c)
   func (c *LogCapture) Records() []slog.Record          // copies, in write order
   func (c *LogCapture) Messages() []string              // r.Message of each record
   ```

   The handler implements `slog.Handler` (`Enabled`, `Handle` with `r.Clone()` under the mutex, `WithAttrs`,
   `WithGroup` returning handlers that share the same capture). Test `core/coretest/logcapture_test.go`:
   `TestLogCapture_RecordsWithoutDefault` (name coined here) writes two records from two goroutines, checks both
   are captured and that `slog.Default()` is the same pointer before and after.
4. **`core/health`: move the flight recorder.**

   ```bash
   mkdir -p core/health
   git mv internal/diag/flight/flight.go core/health/flight.go
   git mv internal/diag/flight/flight_test.go core/health/flight_test.go
   git mv internal/diag/flight/flight_pprof_test.go core/health/flight_pprof_test.go
   sed -i 's/^package flight$/package health/' core/health/*.go
   ```

   Then in `core/health/flight.go`, the logger cut (and nothing else):
   - delete the import of `internal/logger` and the package-level `var flightLog = logger.New("flight-recorder")`;
   - add `Logger *slog.Logger` to `Config` (doc: "Logger receives the recorder's log lines; nil discards them.")
     and `log *slog.Logger` to `Recorder`;
   - in `New`: `log: cfg.Logger`, then `if r.log == nil { r.log = slog.New(slog.DiscardHandler) }`, and
     `dropf: r.infof` (set after `r` exists);
   - add

     ```go
     func (r *Recorder) logf(level slog.Level, format string, args ...any) {
         r.log.Log(context.Background(), level, corelog.Sanitize(fmt.Sprintf(format, args...)))
     }
     func (r *Recorder) infof(format string, args ...any) { r.logf(slog.LevelInfo, format, args...) }
     ```

     with the import `corelog "github.com/falkcorp/mediacore/log"`. `Sanitize` keeps HEAD's behaviour:
     `internal/logger` sanitized every line before printing it. This is an interim: the target is the log
     handler's own escaping of control characters (05-logging §3; `TestHandler_EscapesControlChars`), and R1-36,
     which builds that handler, deletes `Sanitize` and re-points this call (`core/health/flight.go` is in R1-36's
     files, not this row's). If R1-36 has already merged at your base (`git grep -n 'TestHandler_EscapesControlChars' -- core/log`),
     do not call `Sanitize` at all: pass `fmt.Sprintf(format, args...)` straight to `r.log.Log`. Either way this
     row adds no other `core/log` caller. While the `Sanitize` call exists it is the core guard's first real
     in-core edge, `health → log` (03 §3.5.1 item 2, "once core has leaves").
   - replace each `flightLog.Info(` with `r.logf(slog.LevelInfo, ` and each `flightLog.Warn(` with
     `r.logf(slog.LevelWarn, ` (11 call sites; every one is inside a `*Recorder` method at `f7ac5b11e`).

   The tests' own `r.dropf = func(...)` overrides (`flight_test.go:416,457`) keep working unchanged.
5. **Core guard control.** In core's layering guard (created by R1-01 under `core/internal/`; find it with
   `git grep -ln 'archtest.Layering' -- core`), set the in-module positive control to the edge
   `health → log` (03 §3.5.1 item 2: the PR that creates the first real edge names it; this is the only edit this
   row makes to the file, and when R1-36 removes the `Sanitize` call it names the replacement edge, the rule of that
   item; if R1-36 merged first and no `health → log` import exists, name the first real in-core edge your base has), and give the four new
   packages their R4 layers: `version`, `entity`, `log` (L0); `health` (L4); `coretest` is test-support (−1 per
   03 §3.5.1 item 1's explicit mapping). If R1-01's guard has no control slot to set, STOP and report.
6. **ABO importers.** Rewrite the import path (alias kept) in the five files:

   ```bash
   M=github.com/falkcorp/audiobook-organizer
   git grep -l "\"$M/internal/diag/flight\"" -- '*.go' | xargs sed -i "s#diagflight \"$M/internal/diag/flight\"#diagflight \"github.com/falkcorp/mediacore/health\"#"
   gofmt -w internal/server/flight_recorder.go internal/server/flight_recorder_test.go internal/server/server.go internal/server/handlers/diagnostics_traces.go internal/server/handlers/diagnostics_traces_test.go
   ```

   In `internal/server/flight_recorder.go` (`newFlightRecorder`), pass the logger:
   `diagflight.New(diagflight.Config{Dir: dir, Logger: slog.Default().With("subsystem", "flight-recorder")})`
   (ABO is the process owner; reading `slog.Default()` there is allowed, 03 R5).
7. `internal/arch/layering_test.go`: delete the `"internal/diag/flight"` key; add nothing. `internal/diag` keeps
   any other package. Remove the empty `internal/diag/flight` directory.
8. `go mod tidy`, `go -C core mod tidy`: expect no change (standard library only). `go -C core fix ./...` on the
   moved files, same commit.
9. Header bump on every touched file; moved files get their new `file:` path. Headerless fragment
   `changelog.d/<today>_core_leaves_health.md` (`### Changed`: "Internal: core leaves version, entity, coretest
   and health (the runtime flight recorder) created; flight-recorder log lines carry `subsystem=flight-recorder`
   instead of a text prefix."). Commit: `feat(core): version, entity, coretest log capture, flight recorder in core/health`.

## Tests

- Moved: `core/health/flight_test.go` and `flight_pprof_test.go` (the pprof one under `-tags pprof`;
  09 §9.4 R1 classification row `mocks-check`, `pprof-check`: "`internal/diag/flight`'s tests move to `mediacore/health`").
- New (names coined here): `TestVersionIsSemver`, `TestRefString`, `TestLogCapture_RecordsWithoutDefault`,
  and `TestRecorderLogsThroughInjectedLogger` in `core/health/flight_test.go`: build a recorder with
  `Config{Dir: t.TempDir(), Logger: coretest.NewLogCapture(t).Logger()}`, `Start` it, `Stop` it, and assert the
  capture holds "flight recorder started" and "flight recorder stopped" messages (skip if `trace.FlightRecorder`
  start fails on the host, exactly as the existing tests do).

## Code contract

As listed in Goal: `version.Version`, `entity.Ref` with `String()`, `coretest.LogCapture`/`NewLogCapture`/`Logger`/`Records`/`Messages`, `health.Config.Logger *slog.Logger` (the flight recorder's other exported names unchanged, package `health`).

## Acceptance

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -n "\"$M/internal/diag/flight\"" -- '*.go' | wc -l                        # expect 0
git grep -n 'internal/logger\|slog.SetDefault\|flightLog' -- core/health | wc -l    # expect 0
git grep -n 'const Version = "0.1.0"' -- core/version | wc -l                       # expect 1
go build ./... && go -C core build ./...                                             # expect exit 0
go -C core test ./version/ ./entity/ ./coretest/ ./health/ -count=1                  # expect ok x4
go -C core vet -tags pprof ./health/ && go -C core test -tags pprof -run Pprof -count=1 ./health/   # expect ok
go test ./internal/server/ -run Flight -count=1 && go test ./internal/server/handlers/ -run Trace -count=1   # expect ok
make ci && make -C core ci                                                            # expect exit 0 for both
```

## Out of scope

The rest of `health` (F2 readiness core, F8 everything else; 05-health §5H.10). `Supervisor` (R1-13/R1-16).
`scripts/ci/fixture_weights.json` keeps its `internal/diag/flight` measurement rows (stale balance data, never a
failure; the recalibration job rewrites the file).

## Rollback

Revert the commit. No stored data changes.

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
