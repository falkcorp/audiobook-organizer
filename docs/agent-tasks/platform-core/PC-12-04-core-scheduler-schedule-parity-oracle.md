<!-- file: docs/agent-tasks/platform-core/PC-12-04-core-scheduler-schedule-parity-oracle.md -->
<!-- version: 1.0.0 -->
<!-- guid: 33bd958e-8bad-4c02-b083-641f3c8dacfd -->
<!-- last-edited: 2026-11-04 -->

# PC-12-04 — Core scheduler re-implementation and the schedule-parity oracle TestScheduleParity_AllDefinitions

| Field | Value |
|---|---|
| ID | PC-12-04 |
| Tier | **T3 Opus/high** — 38 definitions must keep HEAD's enqueue and clock sequence; a wrong clock rule either stampedes on boot or silently stops a schedule |
| Wave | W12 (S5, release `S5r1`, dark) |
| Depends on | PC-12-01 |
| Serialized | PC-12-01 → {PC-12-03, **PC-12-04**, PC-12-05, PC-12-07} → PC-12-02 |
| Trailer | `Stage-Release: S5 1` |
| Digest | `docs/plans/platform-core/digests/S5.md` §5 rules 11, 13, 14; §7 |
| Owning spec | 05-tasks §5T.3.8 (the one schedule mechanism), §5T.6.2 "One writer per family; one scheduler per kind", §5T.8.1 scheduler row, §5T.9 rows `TestScheduleParity_AllDefinitions`, `TestOneSchedulerPerKind`, `TestSchedule_*`, `TestScheduleFromConfig_*`; §5T.11 item 2; 09 S5 "Moves and batches", "Done when" |

## Goal

1. Write `core/internal/scheduler`, a **re-implementation** (no file of `internal/scheduler` moves) of the one schedule
   mechanism of 05-tasks §5T.3.8 against `tasks.Schedule`, `tasks.ClockStore` and `WriteRuns`. It polls only kinds that
   declare a `Schedule`; until T7 no adapted kind declares one, so it ticks nothing of ABO's.
2. Land the HEAD oracle `TestScheduleParity_AllDefinitions` (ABO, root CI, fake clock) **before** PC-12-08 rewires the
   books ticker, so the flip is checked against HEAD's recorded sequence.
3. Land `TestOneSchedulerPerKind` (its first half; PC-12-08 completes it).

ABO's `internal/scheduler` keeps ticking the 38 `TaskDefinition`s unchanged in this PR.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-04" -b feat/pc-12-04-core-scheduler origin/main
cd "$REPO/../audiobook-organizer-pc-12-04"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create:

- `core/internal/scheduler/` — the core scheduler (one goroutine; `core.tasks.schedule_poll`, default 1 m).
- `internal/scheduler/schedule_parity_test.go` — `TestScheduleParity_AllDefinitions` and `TestOneSchedulerPerKind`.
- `internal/scheduler/testdata/schedule_parity.golden` — HEAD's recorded sequence (file name coined here).

Modify:

- `core/legacy/tasks.go` — confirm `TasksScheduleTicks` and `ErrScheduleTickRefused` are declared exactly as 05-tasks
  §5T.3.13 spells them (PC-12-01 creates the file); add them here only if missing. No other change.

## Re-verify greps (identical at `748fd787a` and the plan checkout)

```bash
grep -c "ts.registerTask(" internal/scheduler/tasks.go                                    # expect 38 (the TaskDefinitions)
wc -l < internal/scheduler/testdata/cadence.golden                                         # expect 38
grep -n 'const intervalLastTickPrefix = "scheduled_task_last_tick."' internal/scheduler/interval_clock.go   # expect 1 (line ~50)
grep -n "const intervalPollInterval = time.Minute" internal/scheduler/interval_clock.go     # expect 1 (line ~56)
grep -n 'const fullSweepLastRunSetting = "library_scan_full_last_run"' internal/scheduler/full_sweep.go     # expect 1 (line ~37)
grep -c '"maintenance_window_last_run"' internal/scheduler/maintenance.go                   # expect 2 (lines ~53, ~71)
grep -n "^var taskV2DefIDs = map\[string\]string{" internal/scheduler/maintenance.go         # expect 1 (line ~154)
grep -c "opsmetrics.Default().ScheduleMissed\|opsmetrics.Default().ScheduleLag" internal/scheduler/v3_schedule.go   # expect 2 (~444, ~447)
grep -n 'Name: *"transcode"\|Name: *"series_normalize"' internal/scheduler/tasks.go          # expect 2 (manual-only, ~529, ~687)
grep -n '"audiobook_organizer.ops.schedule_lag": *{1, 5, 15, 60, 300, 900, 3600, 21600, 86400}' internal/telemetry/views.go   # expect 1
test -d core/tasks && test -f core/legacy/tasks.go && echo ok                               # PC-12-01; expect ok
test ! -e core/internal/scheduler && echo ok                                                # expect ok
```

A different count or a zero: STOP and report.

## Steps

1. **Oracle first.** In `schedule_parity_test.go`, build `NewTaskScheduler` with the test deps pattern of
   `task_reachability_test.go` (`reachabilityTestDeps`), replacing the enqueue sink, the settings store and the v3
   store with in-memory recorders. Run it inside `testing/synctest.Test` (its bubble fakes `time.Now`, so no production
   seam is needed) over a fixed virtual span (coin it; at least 8 days so weekly intervals fire). For each of the 38
   definitions, in two configs (enabled, disabled), record the ordered events: enqueue (def ID, params digest) or
   no-enqueue at each due check, start-run, maintenance-window reachability, and every clock write (key, value). Write
   them to `testdata/schedule_parity.golden` behind `UPDATE_SCHEDULE_PARITY_GOLDEN=1` (mirror `TestCadenceGolden`'s
   update switch). Commit the golden generated from this unchanged HEAD ticker.
2. The test then asserts that the scheduler pair the build wires (in this PR: HEAD's ticker unchanged plus the core
   scheduler, which polls no adapted kind) reproduces the golden exactly, and that `transcode` and `series_normalize`
   never start themselves. PC-12-08 must keep it green after rewiring the ticker onto `Deps.Tasks` and `ClockStore`;
   the golden is never regenerated after this PR without an owner decision (say so in a comment at its top).
3. `TestOneSchedulerPerKind` (ABO): the set of kinds with a core `Schedule` and the set of def IDs the books ticker
   drives (`taskV2DefIDs` values plus the v3 loop's driven defs) are disjoint. The second half of 05-tasks §5T.9's row
   ("the ABO scheduler's clock writes go through `ClockStore`") is added by PC-12-08; leave a `t.Skip` stub named in
   the test with that reason, or assert it as an AST check that is expected to fail only after the flip (pick the stub).
4. **Core scheduler** (`core/internal/scheduler`), from 05-tasks §5T.3.8:
   - one goroutine; poll every `core.tasks.schedule_poll`; only kinds that declare a `Schedule`; a held kind is not polled;
   - a due fire is **one batch** inside `WriteRuns`: `ClockStore.Advance(prev, had, next)` + `RunStore.Insert` of the
     queued run; `ErrClockConflict` means another poll won (no fire); a clock read error skips this poll and counts
     `schedule_errors`, never "due now";
   - first sight seeds `LastDue = now` and does not fire; the clock advances on every due check whether or not the gate
     enqueued; an empty `ScheduleHash` derives the next instant from `LastDue` and the current `ScheduleConfig`;
   - a fire folds into an active run of the same kind and mode; `Missed`: `Coalesce` (count) or `SkipMissed`;
   - `DailyAt`/`Cron` in `core.tasks.timezone`: a skipped wall time fires at the first instant after, a repeated one
     once;
   - `ScheduleFromConfig(key)` validates the key form `<owner>.<subsystem>.schedules.<name>` (segments
     `[a-z][a-z0-9_]{0,47}`), the declared type `tasks.ScheduleConfig`, one key per kind; `Every` is refused on every
     kind except `core.outbox.relay`;
   - records `ops.schedule_lag` (now − `dueAt`, when set) and `ops.schedule_missed` (`folded`, when > 0) on scope
     `<ns>.ops`, `def_id` = the kind's `StoredKind`; `ops.schedule_lag` declares the boundaries
     `1, 5, 15, 60, 300, 900, 3600, 21600, 86400` through `Scope.View`;
   - exposes the path the kernel's `Deps.Tasks` client uses to implement `legacy.TasksScheduleTicks.ReportScheduleTick`
     (05-tasks §5T.11 item 2): it records lag and missed on the same instruments for a kind of the calling module that
     declares no core `Schedule`, and returns `legacy.ErrScheduleTickRefused` otherwise. Wiring that capability onto
     the client is kernel work done in PC-12-02 or PC-12-08; this package only provides the function.
5. Write the core tests below; run acceptance; headers; `changelog.d/` fragment.

## Code contract (exact, from 05-tasks §5T.3.8 and §5T.3.13)

```go
func ScheduleFromConfig(key string) *Schedule
func Every(d time.Duration) *Schedule
type ScheduleConfig struct {
	Enabled bool            `json:"enabled"`
	Every   config.Duration `json:"every,omitempty"`
	DailyAt string          `json:"daily_at,omitempty"`
	Cron    string          `json:"cron,omitempty"`
	Window  string          `json:"window,omitempty"`
	Order   int             `json:"order,omitempty"`
}
type MissedPolicy uint8 // Coalesce, SkipMissed
type ClockStore interface {
	Get(ctx context.Context, k StoredKind) (Clock, bool, error)
	Advance(ctx context.Context, b *storage.Batch, k StoredKind, prev Clock, had bool, next Clock) error
}
// legacy
type TasksScheduleTicks interface { ReportScheduleTick(ctx context.Context, kind tasks.Kind, dueAt time.Time, folded int) error }
var ErrScheduleTickRefused = errors.New("legacy: schedule tick for a kind the caller does not tick")
```

## Tests to write first

ABO (root `make ci`):

- `TestScheduleParity_AllDefinitions` (05-tasks §5T.9, OPS8-4, DATA5-8) — 38 definitions × {enabled, disabled}, golden
  from HEAD.
- `TestOneSchedulerPerKind` (first half, as step 3).

Core (`make -C core ci`; names from 05-tasks §5T.9):

- `TestSchedule_EnqueueAndClockOneBatch` (crash injection between clock and enqueue: both or neither),
  `TestSchedule_NoBootStampede`, `TestSchedule_CoalesceMissed`, `TestSchedule_DSTSpringForwardFallBack` (synctest, fake
  zone).
- `TestScheduleFromConfig_UndeclaredKeyRefused`, `TestScheduleFromConfig_ForeignNamespaceRefused`,
  `TestScheduleFromConfig_OwnerKeyAccepted`, `TestScheduleFromConfig_HotReload`, `TestEvery_RefusedExceptRelay`.
- `TestReportScheduleTick_RecordsLagAndMissed` (name coined here): a reported tick for an unscheduled kind of the
  caller records one lag sample and the folded count; a kind with a core `Schedule` returns
  `legacy.ErrScheduleTickRefused`.

## Acceptance

```bash
go test ./internal/scheduler/ -run 'TestScheduleParity_AllDefinitions|TestOneSchedulerPerKind' -count=1 -v | grep -c '^--- PASS'   # expect 2
go test ./internal/scheduler/... -count=1                     # expect ok (TestCadenceGolden and every HEAD test unchanged)
grep -c . internal/scheduler/testdata/schedule_parity.golden  # report the line count in the PR description; expect > 76
(cd core && go test -race ./internal/scheduler/... -count=1)  # expect ok
(cd core && go test ./internal/scheduler/... -run 'TestSchedule_|TestScheduleFromConfig_|TestEvery_' -count=1 -v | grep -c '^--- PASS')   # expect >= 9
grep -rn 'scheduled_task_last_tick\|maintenance_window_last_run\|library_scan_full_last_run\|internal/scheduler"' core --include=*.go | wc -l   # expect 0 (no ABO key in core)
make -C core ci            # exit 0
make ci                    # exit 0
git diff --stat origin/main -- internal/scheduler | grep -v '_test.go\|testdata' | tail -1   # expect empty: no ticker change here
```

## Out of scope

- Rewiring the books ticker onto `Deps.Tasks`/`ClockStore`, deleting the v3 loop's `opsmetrics` calls, cutting the
  clock codecs into taskstore (PC-12-05 and PC-12-08).
- T7's conversion of the 38 definitions to `ScheduleFromConfig` and the 15 never-run v2 crons (PC-13-14).
- `GET /schedules` routes (PC-12-06).

## STOP conditions

- The HEAD ticker cannot be driven inside a `synctest` bubble without a production change (for example a real-time
  goroutine outside the bubble): STOP and report which call blocks; do not add a seam to `internal/scheduler` in this PR.
- The golden differs between two consecutive runs (non-determinism): STOP.

## Rollback

Revert the PR. The core scheduler ticks no ABO kind and ABO's ticker is untouched.

## Repo rules

- File headers on every new or changed file (the golden is data: no header, exactly as `cadence.golden`); the
  `changelog.d/` fragment has no header.
- Commits end with `Stage-Release: S5 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `feat(tasks): ...`;
  `gh pr merge <n> --rebase` after required checks and OWNER-12's row; never squash.
- CLAUDE.md concurrency: one scheduler goroutine for all schedules (05-tasks §5T.7); no per-kind goroutines.
