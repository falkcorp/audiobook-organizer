<!-- file: docs/agent-tasks/platform-core/PC-13-14-t7a-schedule-conversion-scheduler-deleted.md -->
<!-- version: 1.0.0 -->
<!-- guid: 17a8e424-10d1-4ca0-b7ad-44828d358495 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-14 — T7a: schedule conversion: the 38 TaskDefinitions become ScheduleFromConfig, each deleting its ticker entry; the 15 never-run v2 crons deleted; internal/scheduler deleted

| Field | Value |
|---|---|
| ID | PC-13-14 |
| Tier | **T3 Opus/high** — every scheduled job changes scheduler, schedule keys move with aliases, and three definitions have no one-to-one target (see Design decisions) |
| Wave | W13 (stage S5, release `S5r5`; the PR that sets `release: S5r5`, so `compat-rehearsal` runs on it) |
| Depends on | OWNER-16 (the `S5r4` soak row; OWNER-13 recorded the Q-C decision) |
| Trailer | `Stage-Release: S5 5` on every commit |
| Spec | 05-tasks §5T.8.2 row T7; §5T.3.8 (the whole section: `ScheduleFromConfig`, `ScheduleConfig`, the key rule, "What replaces v2 cron strings", durable clock, rollback); §5T.6.2 "One writer per family; one scheduler per kind"; §5T.9 rows `TestOneSchedulerPerKind`, `TestScheduleParity_AllDefinitions`, `TestABOMetricsGatherNoDuplicateOrMissing`; §5T.11 (schedule families); 09 S5 rows "Moves and batches", "Config", "Done when"; 09 §9.2.5 (config aliases) and §9.2.9 (ledger); 14 Q-C; digest S5 §5 rules 11, 14, 15 |

## Gate

Merges only after OWNER-16 has merged a current `S5r4` row. This PR changes `release:` (`S5r4` → `S5r5`), so
`check_soak.py` refuses it until then; `compat-rehearsal` and `unit-lifecycle` (if its trigger list covers
`internal/scheduler`) run on it. Before starting, read the stage record: OWNER-13 decided 14 Q-C. If the decision is
anything other than the spec default ("delete the declarations"), **STOP** and report; this brief implements only
the default.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-14 -b pc-13-14-t7a-schedule-conversion origin/main
cd ../audiobook-organizer-pc-13-14
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

From this PR, **core's scheduler is the only scheduler**. Each of ABO's 38 `TaskDefinition`s becomes a `Schedule`
on its target kind, declared with `tasks.ScheduleFromConfig("modules.books.<subsystem>.schedules.<name>")` and a
default equal to today's interval, enable flag and on-start behaviour; each conversion deletes that definition's books
ticker entry in the same PR, so no kind ever has two schedulers. The `scheduled.*` and `maintenance.*` keys become
aliases of the new keys for the compatibility window. ABO's v3 loop and its `ReportScheduleTick` calls go. Then
`internal/scheduler` is deleted. This is T7's one behaviour change: schedule keys move (aliases for the window), and
the 15 v2 crons, which never ran, have no schedule (05-tasks §5T.8.2 T7).

## Exact files

Modify (inventory): `internal/scheduler/` (deleted), `internal/books/module.go`, `internal/config/aliases_gen.go`
(regenerated, never hand-edited), `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (the embedded copy),
`ci/stage.txt`. Also needed (Planner notes): the books package that declares the schedule keys in books' config
section, the 7 `internal/server` files and 3 `internal/server/handlers` packages (with mocks) that import
`internal/scheduler`, a new home for `scheduler.metadata-upgrade`'s registrar, the generator inputs of
`aliases_gen.go`, the moved parity test and golden, and `core/legacy` if the `TasksScheduleTicks` capability lives
there. One `changelog.d/` fragment.

## Re-verify greps (HEAD `f07f516be`; `internal/scheduler` and `internal/config` are identical at `748fd787a`)

```bash
grep -c '' internal/scheduler/testdata/cadence.golden                       # 38 (one line per TaskDefinition)
grep -n 'ts.maintenanceOrder = \[\]string{' internal/scheduler/scheduler.go  # 1 (:199; window order)
grep -n '^var taskV2DefIDs = map\[string\]string{' internal/scheduler/maintenance.go   # 1 (:154)
grep -n 'maintenance_window_last_run' internal/scheduler/maintenance.go     # 2 (:53 read, :71 write; YYYY-MM-DD)
grep -n 'SetDefault("scheduled\.' internal/config/config.go | wc -l        # 40 (from :2886)
grep -n 'SetDefault("maintenance\.' internal/config/config.go | head -3    # from :3003 (enabled, window_start, ...)
grep -n 'opsmetrics.Default().ScheduleMissed\|opsmetrics.Default().ScheduleLag' internal/scheduler/v3_schedule.go   # 2 at HEAD (:444,:447); 0 at your base (PC-12-08 made them ReportScheduleTick)
grep -rn 'ReportScheduleTick' --include=*.go internal | grep -v _test       # 0 at HEAD; >= 1 at your base, else STOP
grep -n 'func (r \*ExtraOpsRegistrar) RegisterMetadataUpgradeOp' internal/scheduler/extra_ops.go   # 1 (scheduler.metadata-upgrade lives here)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/scheduler"' --include=*.go . | grep -v '^./internal/scheduler/'
#   HEAD: 12 files: 7 in internal/server, 2 in internal/server/handlers (+1 mocks), 1 in internal/server/handlers/operations (+1 mocks)
grep -n 'func TestScheduleParity_AllDefinitions\|func TestOneSchedulerPerKind' internal/scheduler/*_test.go   # 2 (PC-12-04), else STOP
test -f internal/scheduler/testdata/schedule_parity.golden && echo present # present (PC-12-04), else STOP
grep -nE 'Schedule:\s*(new\(|&)' --include=*.go -r internal | grep -v _test | wc -l   # 15 at HEAD; 0 at your base (T5/T6 dropped them), else STOP
grep -rn 'ScheduleFromConfig' core/tasks/*.go | grep -v _test | head -2     # >= 1 (PC-12-01)
```

## Design decisions you must make (T3 latitude) and the invariants that bound them

1. **Key names.** The spec fixes the form `modules.books.<subsystem>.schedules.<name>` (segments
   `[a-z][a-z0-9_]{0,47}`, no hyphens; 05-tasks §5T.3.8 "The one key rule") and one example,
   `modules.books.scan.schedules.full_sweep`. Use that example for `library_scan_full`. For the rest, the rule coined
   here: `<subsystem>` = the first segment after `books.` of the target kind with hyphens as underscores (e.g.
   `books.dedup.author-scan` → `dedup`), `<name>` = HEAD's `TaskDefinition.Name` (already snake case). Put the full
   table (38 rows: task name, target kind, new key, alias keys, default) in the PR description and in the ledger
   rows of item 6.
2. **Defaults equal HEAD's.** Each key's default `ScheduleConfig` is today's effective config default
   (`internal/config/config.go` `SetDefault("scheduled.<x>.…")` from `:2886` and the hard-coded cadences of
   `tasks.go`): interval minutes → `Every`, `DailyAt` → `DailyAt`, enabled flag → `Enabled`, `RunOnStart` →
   `.OnStart()`, an `IsEnabled` gate that is not a schedule setting → `.When(...)`, window membership (index in
   `maintenanceOrder`, `scheduler.go:199`) → `Window: "nightly", Order: idx` (05-tasks §5T.3.8 last paragraphs).
   A duration `d` alias converts as `{enabled: d > 0, every: d}`, so "0 disables" stays true.
3. **No kind ever has two schedulers.** Each conversion and the deletion of its ticker entry land in the same PR
   (this one). `TestOneSchedulerPerKind` (ABO) proves it.
4. **Sequence parity.** `TestScheduleParity_AllDefinitions` keeps comparing the build's scheduling against HEAD's
   recorded sequence (`schedule_parity.golden`, generated from HEAD's ticker by PC-12-04 and never regenerated
   without an owner decision). Because `internal/scheduler` is deleted, **move the test and its golden first**, to
   `internal/books/schedule_parity_test.go` and `internal/books/testdata/schedule_parity.golden` (paths coined
   here), in a commit before any conversion, and make it drive core's scheduler with the books declarations under a
   fake clock (`testing/synctest`). It must show, for all 38 definitions in enabled and disabled config, the same
   enqueue / no-enqueue, start-run, window reachability and clock-advance sequence as the golden, with `transcode`
   and `series_normalize` never self-starting (09 S5 "Done when"). A difference you cannot remove is a **STOP**.
5. **Clocks continue across the swap.** Each converted kind's clock is books' `ClockStore` entry that the ticker
   already advanced (HEAD's key and encoding, `interval_clock.go`), read under the kind's `StoredKind`; the first poll
   after the upgrade does not fire a stampede and the first poll after a rollback does not double-fire
   (`TestClock_InPlace_RollbackNoStampedeNoDoubleFire`, 05-tasks §5T.9; R-test row "a schedule clock advanced on prev
   neither double-fires nor stampedes on cur"). `maintenance_window_last_run` keeps its `YYYY-MM-DD` codec as the
   window kind's clock (09 S5 "Moves and batches").
6. **Aliases and ledger.** Every `scheduled.*` (40 defaults at HEAD) and `maintenance.*` key that a converted
   schedule replaces becomes an alias row (window W) generated into `internal/config/aliases_gen.go` by the generator
   PC-09-03 ships (find it: `grep -rn 'aliases_gen.go' Makefile scripts tools internal/config | grep -v '^internal/config/aliases_gen.go'`),
   with a ledger row in `docs/compat-surfaces.md` and the embedded copy `internal/books/compat-surfaces.md`
   (`TestCompatLedgerMatchesCode` and the ledger-copy freshness check stay green). The worker count key becomes
   `core.tasks.workers` (09 S5 "Config"), if PC-12-08 did not already alias it.
7. **The three definitions with no one-to-one target.**
   - `transcode` enqueues nothing: delete it (05-tasks §5T.3.8).
   - `library_scan` and `library_scan_full` both enqueue `library.scan` (raw/06 §6.1 note (b)) and one kind holds one
     `Schedule`. Keep `library_scan` on `books.library.scan`; give the full sweep its own kind that runs the same body
     with the full-sweep params, under `modules.books.scan.schedules.full_sweep`. Coin the kind and record it; it has
     no `LegacyDefID` of its own, so its runs are dropped by an older build after a rollback (09 §9.2.1 row 5).
   - `batch_poller` calls `deps.PollBatches` directly. 05-tasks says it "becomes a schedule on an AI-owned task
     (05-ai)", but 05-ai's `core.ai.batch-poll` lands at S7. Until then make it a books task (kind coined) with
     `Every`-equivalent default `{enabled: true, every: 5m}` and `.OnStart()`, calling the same function; S7 replaces
     it. Polling cadence and on-start behaviour must equal HEAD's (the parity golden covers `batch_poller`).
8. **The 15 v2 crons.** At your base they are already gone (T5/T6 dropped each one; re-verify grep). Record the 15
   def IDs in the ledger as "cron declaration deleted, never ran, 14 Q-C" and in the release-note draft.
9. **Schedule metrics.** Delete the v3 loop and its `ReportScheduleTick` calls; core's scheduler records
   `ops.schedule_lag` and `ops.schedule_missed` for these kinds under the same `def_id` values (the `StoredKind`),
   so `TestABOMetricsGatherNoDuplicateOrMissing` (with the v3-loop case removed, 05-tasks §5T.11) still matches the HEAD
   golden. Delete the `legacy.TasksScheduleTicks` capability once it has no caller (digest S5 rule 11).
10. **`scheduler.metadata-upgrade`** is registered from `internal/scheduler/extra_ops.go`. Move that registrar,
    unchanged and still a v2 def, to `internal/server` (e.g. `internal/server/metadata_upgrade_op.go`) in its own
    import-path-only commit before deleting the package; PC-13-15 converts it.
11. **Operations routes stay books'.** `/api/v1/tasks` (and its aliases) and the three `/api/v1/maintenance-window/*`
    routes keep HEAD's bodies through `Deps.Tasks` (09 S5 "Endpoints"); `GET /api/core/v1/tasks/schedules` now lists
    the 38 converted schedules.

## Steps

1. Run the greps; read the Q-C decision; STOP on a mismatch.
2. Move the parity test and golden to `internal/books` (decision 4); it must pass unchanged against the current
   (ticker) wiring before you go on.
3. Move the `scheduler.metadata-upgrade` registrar out (decision 10).
4. Declare the 38 schedule keys in books' config section with their defaults (decisions 1, 2, 7); register each
   target kind's `Schedule` through `ScheduleFromConfig`, deleting its ticker entry and `taskV2DefIDs`/`pausableDefs`
   line in the same commit. Convert in a few commits, each green on `TestOneSchedulerPerKind` and the parity test.
5. Generate the aliases and ledger rows (decision 6).
6. Delete the v3 loop and `ReportScheduleTick` (decision 9); delete `internal/scheduler`; fix its importers.
7. `ci/stage.txt`: `release: S5r4` → `release: S5r5`.
8. Draft the release-note lines for the hand-off: the moved keys and their aliases, the deleted crons, the new
   full-sweep and batch-poll kinds (row 5).

## Code contract (05-tasks §5T.3.8, quoted)

```go
func ScheduleFromConfig(key string) *Schedule
type ScheduleConfig struct {
	Enabled bool            `json:"enabled"`
	Every   config.Duration `json:"every,omitempty"`
	DailyAt string          `json:"daily_at,omitempty"`
	Cron    string          `json:"cron,omitempty"`
	Window  string          `json:"window,omitempty"`
	Order   int             `json:"order,omitempty"`
}
func (s *Schedule) OnStart() *Schedule
func (s *Schedule) When(gate func() bool) *Schedule
func (s *Schedule) Params(p any) *Schedule
func (s *Schedule) Live() *Schedule
func (s *Schedule) Preview() *Schedule
func (s *Schedule) Missed(p MissedPolicy) *Schedule
type Window struct { Name string; Start, End int }
```

A scheduled writer must say `Live()` or `Preview()` (registration refuses otherwise, 5T.3.1); use the mode HEAD's
ticker enqueued with.

## Tests

- `TestScheduleParity_AllDefinitions` (moved, unchanged golden) and `TestOneSchedulerPerKind` (both ABO; names from
  05-tasks §5T.9).
- `TestABOMetricsGatherNoDuplicateOrMissing` with the v3-loop case removed.
- `TestClock_InPlace_RollbackNoStampedeNoDoubleFire` and `TestScheduleFromConfig_OwnerKeyAccepted` stay green.
- A config test (name coined: `TestScheduleAliasesResolve`) that sets each old `scheduled.*`/`maintenance.*` key in
  YAML and in env and reads the same effective `ScheduleConfig` from the new key.

## Acceptance

```bash
test ! -d internal/scheduler && echo deleted                                                           # deleted
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/scheduler"' --include=*.go . | wc -l       # 0
grep -rn 'ReportScheduleTick' --include=*.go . | wc -l                                                 # 0
grep -rhoE 'ScheduleFromConfig\("modules\.books\.[a-z0-9_]+\.schedules\.[a-z0-9_]+"\)' --include=*.go internal | sort -u | wc -l   # 37 (the 38 definitions minus transcode; full sweep and batch poll included)
go test ./internal/books/ -run 'TestScheduleParity_AllDefinitions|TestOneSchedulerPerKind|TestABOMetricsGatherNoDuplicateOrMissing' -count=1   # ok
go test ./internal/config/... -run 'TestScheduleAliasesResolve|TestCompatLedger' -count=1                # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1                       # ok
grep -n '^release:' ci/stage.txt                                                                        # release: S5r5
make ci && make -C core ci                                                                              # both exit 0
make compat-rehearsal                                                                                   # pass (required on this PR)
```

## STOP conditions

The Q-C decision is not the default; the parity test cannot match HEAD's golden; a converted kind's clock cannot be
read from the entry the ticker advanced; the `aliases_gen.go` generator is missing; `ReportScheduleTick` is absent
at your base (PC-12-08 not as specified).

## Out of scope

Server-package ops and `scheduler.metadata-upgrade`'s conversion (PC-13-15); turning any v2 cron into a real
schedule (14 Q-C); any parallelism change; core's AI batch-poll kind (S7).

## Rollback

Binary swap. The previous build ticks the 38 definitions itself from the clocks this build advanced (one clock per
kind, in HEAD's encoding), so no stampede and no double fire; old config keys are still read by it, and new keys set
only under their canonical names are ignored by it (release note). Runs of the two new kinds (full sweep, batch poll)
are dropped by its resume sweep (09 §9.2.1 row 5).

## Repo rules

File headers; conventional commits ending with `Stage-Release: S5 5` and
`Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds; `make ci` and `make -C core ci`; required
checks including `compat-rehearsal`; `gh pr merge <n> --rebase`, never squash, never `--admin`. CLAUDE.md
concurrency rules (no parallelism change); never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.

## Planner notes

- Files missing from the row: the 12 importer files of `internal/scheduler` under `internal/server` and
  `internal/server/handlers{,/operations}` (with mocks); a new `internal/server` file for the
  `scheduler.metadata-upgrade` registrar (otherwise deleting `internal/scheduler` deletes a v2 def PC-13-15 still
  needs); the books config declarations of the 38 keys; the generator input of `aliases_gen.go`; the moved parity test
  and golden under `internal/books`; and `core/legacy/tasks.go` if `legacy.TasksScheduleTicks` is deleted here.
  PC-13-15 also edits `internal/server` (ordered after this row).
- Spec gaps decided here under T3 latitude: (a) `library_scan_full` shares `library.scan` with `library_scan`, but a
  kind has one `Schedule`; (b) `batch_poller`'s target is S7's `core.ai.batch-poll`, which does not exist at S5r5; (c)
  the "migration ledger" for the 15 crons (05-tasks §5T.3.8) names no file; (d) only one of the 38 keys
  (`modules.books.scan.schedules.full_sweep`) is named by the spec. Each needs a spec line.
- 05-tasks §5T.3.8 says windows run as `core.window.<name>`, a core kind; HEAD's `maintenance.window` op ID would need
  `app.Options.LegacyTaskAliases`, whose row (PC-13-03) is SPEC-GATED. If the window kind needs that alias, this row
  depends on the mechanism half of PC-13-03.
- 05-tasks §5T.3.8 converts the 15 crons at T7, but no native field can hold a v2 cron, so T5/T6 already drop them
  (PC-13-04's and PC-13-07's recipe rule 7); T7 only records them.
