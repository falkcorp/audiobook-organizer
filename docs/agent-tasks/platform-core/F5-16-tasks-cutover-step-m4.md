<!-- file: docs/agent-tasks/platform-core/F5-16-tasks-cutover-step-m4.md -->
<!-- version: 1.1.2 -->
<!-- guid: 8fcf5409-cd73-4f80-ab41-6d71c96ea327 -->
<!-- last-edited: 2026-11-05 -->

# F5-16 — Books' tasks step M4, built dark: runs, rows, marks, states, clocks, markers, buckets and stamps through `tasks.Importer`, with the generated kind-rename map applied to runs and the `act:*` families

| Field | Value |
|---|---|
| ID | F5-16 |
| Release / flip | R2, tasks flip F5 (dark foundation PR; M4 is registered by F5-18) |
| Wave | W29 |
| Tier | **T4** (Opus / xhigh–max): a one-way migration of every run, clock, marker and stamp, with a rename of every stored definition reference; a wrong row is lost task history or a re-applied repair |
| Depends on | F2-02 (`tasks.Importer`, `NativeImporter`, `taskstest.StoresSuite`), F3-06 (`internal/books/cutover` and its step conventions), F2-13 (the hand-built overlay of the R2 corpus) |
| Gate | Plan, then execute autonomously in a worktree with its own PR; the plan is reviewed before code because the step is one-way |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-16-tasks-cutover-m4
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). `core/` is the nested module `github.com/falkcorp/mediacore`.

## Goal

Build books' BootBlocking cutover step **M4** (03 §3.8; 05-tasks §5T.8.3) and the **kind-rename map**
(05-tasks §5T.8.2) it applies, both books code, both unregistered until the tasks flip F5-18 adds M4 to
`internal/books/cutover/steps.go`. M4 moves every HEAD ops family into `c/tasks/` through `tasks.Importer`,
deletes each source row in the batch that imported it, rewrites the HEAD def IDs stored in the books
activity families in place, and leaves every other row byte-identical. Core holds none of it (03 R1(c)).

## Spec to read first (in this order; this is the whole contract)

1. 05-tasks §5T.8.3 (order steps 1–4, the state table, clocks, markers, buckets, stamps, what is deleted,
   quarantine, counts, rollback). Read it twice.
2. 05-tasks §5T.8.2 "The kind-rename map" (sources, rules 1–6, the scheduler-name rule, the activity-family
   table: `act:audit:`/`act:batch:`/`act:change:`/`act:debug:`/`act:info:`/`act:system:`, `act:digest:`,
   `act:typ:`; the families that need nothing), the T4 table (rule 1 targets) and "Three core kinds whose
   owners flip after F5" (`batch_poller` → `core.ai.batch-poll`, whose clock M4 imports).
3. 05-tasks §5T.3.13 (`Importer`: `Run`, `RunData`, `Clock`, `Pause`, `StandDown`, `Debounce`, `Stamp`; `RunData`
   fields), §5T.3.8 "Durable clock" and "First boot after the cutover", §5T.3.6 (checkpoints: HEAD's bytes, the
   `v1` checkpoint), §5T.5 (quarantine, unknown kinds).
4. 05-storage §5S.3.13 (importer rules: create-only `Expect(key, nil)`, `ErrImportConflict`, `ErrImportRejected`
   → quarantine, no side effects, one batch per chunk), §5S.3.6 (`DataMigration`, `StepWriter`, cursors, crash
   matrix), §5S.9.1 rows 9, 13, 15 and the "Rewritten in place by M4" column (DATA-5).
5. 09 §9.2.3 (order; one batch per chunk), §9.3 item 1 (R0-written corpus, labelled overlay), §9.9 (bounded
   pool over disjoint key ranges).
6. 05-tasks §5T.9 rows `TestKindRenameMapCoversLedger` (F5-18 runs it on the flip build), `TestCutover_*`.

## Base state: re-verify before editing

```bash
# F2-02 / F3-06 / F2-13 outputs (0 hits on any = STOP)
grep -rn 'type Importer interface' core/tasks/*.go | wc -l                                   # 1
grep -rn 'RenameKind(ctx context.Context, b \*storage.Batch, from, to Kind' core/tasks/*.go | wc -l   # 1
grep -rn 'func NativeImporter\|NativeImporter(' core/tasks/*.go | grep -v _test | wc -l      # >=1 (F2-02's coined constructor)
grep -rn 'StoresSuite' core/tasks/taskstest/*.go | wc -l                                     # >=1
grep -rn 'type DataMigration struct' core/storage/datamigrate/*.go | wc -l                   # 1
ls internal/books/cutover/steps.go                                                           # exists (F3-06)
grep -n 'M4\|m4' internal/books/cutover/stepids.go | wc -l                                  # >=1 (F3-06 reserved M4's step ID), else STOP
test -f internal/books/testdata/r2corpus/manifest.json && echo manifest                      # manifest
grep -c 'opv2\|opv3\|operation:\|act:' internal/books/testdata/r2corpus/manifest.json        # >=1 (ops and activity rows in the corpus), else STOP
test ! -e internal/books/cutover/m4 && test ! -e internal/books/cutover/kindrename && echo absent   # absent
# HEAD's sources (f7ac5b11e; spec anchors are 748fd787a, see the note below)
grep -n 'func (p \*PebbleStore) ReconcileOpsV3WithV2' internal/database/pebble_store_ops_v3_reconcile.go | wc -l   # 1
grep -n 'ReconcileOpsV3WithV2(context.Background())' internal/database/store.go | wc -l     # 1 (HEAD's every-boot call, store.go:1868)
grep -c '' internal/server/testdata/op_ids.golden                                            # 288 lines (incl. comments)
grep -n '^var Tombstones = map\[string\]string{' internal/opscatalog/tombstones.go | wc -l   # 1
grep -n '^var taskV2DefIDs = map\[string\]string{' internal/scheduler/maintenance.go | wc -l  # 1
grep -n 'const intervalLastTickPrefix = "scheduled_task_last_tick."' internal/scheduler/interval_clock.go | wc -l   # 1
grep -n 'const fullSweepLastRunSetting = "library_scan_full_last_run"' internal/scheduler/full_sweep.go | wc -l     # 1
grep -n '"maintenance_window_last_run"' internal/scheduler/maintenance.go | wc -l           # 2
grep -n 'const pauseSettingKey = "registry.operations_paused"' internal/operations/registry/pause_gate.go | wc -l   # 1
grep -n 'const scanStandDownSettingKey = "registry.scan_standdown"' internal/operations/registry/scan_standdown.go | wc -l   # 1
grep -n 'PriorityLow    Priority = 0' internal/operations/registry/types.go | wc -l          # 1
grep -n 'details\["def_id"\] = r.defID' internal/operations/registry/reporter_db.go | wc -l  # 1 (the activity mirror's def ID, spec :848-870)
grep -n '"opv2:row:"\|"opv2:rowix:"\|"opv3:mark:"\|"opv3:rowmark:"' internal/database/keyfamilies.go | wc -l   # 4
```

Spec anchors at `748fd787a` that moved at `f7ac5b11e`: `store.go:1860-1870` is 1862-1868; `full_sweep.go:81,117`
are the uses of the constant at line 37; `reporter_db.go:848-870` holds `details["def_id"]` at 851. The others
match. The op-ID ledger is unchanged since `748fd787a`.

## Files

| Action | Path |
|---|---|
| create | `internal/books/cutover/kindrename/`: the map (`map_gen.go`, generated), its generator (`gen/`), `rules.go` (rules 1–6 as code the generator and the step share), a golden of the generated map (names coined here) |
| create | `internal/books/cutover/m4/`: the step(s), decoders over HEAD's row types, tests |
| create | `internal/books/testdata/r2corpus/m4/`: M4's expected answers over the corpus (counts, ID lists, the renamed activity rows), and any M4-specific overlay case the corpus manifest does not already list |
| create | one `changelog.d/` fragment |

Do not modify `internal/books/cutover/steps.go` (F5-18 registers M4), `internal/books/cutover/stepids.go` (F3-06's;
F5-18 adds its level constant there), `internal/database` (M4 imports its HEAD
readers; F5-18 moves the run-row store files into `internal/books/cutover/m4legacy/`), `internal/scheduler`,
`internal/opscatalog`, `core/`, `main.go`.

## The kind-rename map (05-tasks §5T.8.2)

Inputs, read by the generator at its commit: every line of `internal/server/testdata/op_ids.golden` (canonical
IDs and `FormerIDs` aliases), `opscatalog.Tombstones`, `taskV2DefIDs`, and the canonical→alias table HEAD's
registry exposes (`Aliases()`; read it the way `internal/server/op_id_aliases_test.go`'s `bootRegisteredOpIDs`
does, from a generator test, not from a production boot). Rules, first match wins:

1. a HEAD def ID in the T4 table → that core kind (`maintenance.db-optimize` → `core.storage.compact`,
   `maintenance.db-census-exact` → `core.storage.census`, `maintenance.purge-old-logs` → `core.log.journal.prune`,
   `maintenance.throttle-reset` → `core.integrations.release-holds`, `diagnostics.export` → `core.diagnostics.export`,
   `maintenance.window` → `core.window.nightly`);
2. `repairs.plan` / `repairs.apply` → `books.repairs.<fixer_id>` from the run's `fixer_id` param; a plan run as
   `ModePreview`, an apply run as `ModeLive` with `PlanRef` = the plan run HEAD's params name (a run-level rule:
   the map holds a marker, the step applies it per run);
3. a `FormerIDs` alias → where its canonical ID maps;
4. every other registered HEAD def ID `<id>` → `books.<id>` (the wrapped `maintenance.<job>` IDs included), with
   one run-level split: a `library.scan` run whose params are the full sweep's (`force_update` and
   `include_root_dir` both true, the params HEAD's `library_scan_full` task enqueues, whoever started the run) →
   `books.library.scan-full`; every other `library.scan` run → `books.library.scan` (a run-level rule like rule 2:
   the map holds a marker, the step applies it per run);
5. a tombstoned ID, `operations.backfill-legacy-status` and any other well-formed ID found in a store →
   `books.retired.<id>`: history only, never registered (05-tasks §5T.5's unknown-kind row: the run is kept; the
   first boot's resume sweep drops a non-terminal one with reason `unknown_kind`);
6. an ID that fails the kind grammar → its run is quarantined.

Scheduler task names map through `taskV2DefIDs`, then rule 1 or 4 (`batch_poller` → `core.ai.batch-poll`;
`transcode` fires nothing and its clock is dropped), with one exception: `library_scan_full`, which `taskV2DefIDs`
points at `library.scan`, maps to `books.library.scan-full` (F5-13's kind), so its clock and its
`scheduled.library_scan_full` entry reach that kind and not `books.library.scan` (05-tasks §5T.8.2, the paragraph
after rule 6). `TestKindRenameMapCoversLedger` counts the ledger line `library.scan` once, mapped to
`books.library.scan`; the run-level split is checked by `TestCutover_TasksFixtureFromR0`.

The map is regenerated by `go generate ./internal/books/cutover/kindrename/` and a test fails when the
generated file differs from a fresh run (name coined here: `TestKindRenameMap_UpToDate`). F5-18 regenerates
it at the flip commit and runs the spec's `TestKindRenameMapCoversLedger` against the registered kinds.

## What M4 does (05-tasks §5T.8.3; your design must keep every rule)

1. Runs HEAD's `ReconcileOpsV3WithV2` **once**, so every v3 record agrees with its v2 row before decoding.
2. In chunks, each chunk **one batch** holding the `Importer` calls and the deletes of exactly the source rows
   it imported, with a persisted cursor per family:
   - **runs**: v3 records, falling back to the v2 row for a run without a projection; with trails, intents,
     items, log, checkpoints, chunk ledgers, item-key snapshots, and the Fixer rows and marks
     (`opv2:row:` read with `opv2:rowix:` → `c/tasks/row/`; `opv3:mark:` and `opv3:rowmark:` → `c/tasks/mark/`).
     HEAD's v1 history (`operation:` with its `operationlog:` lines, results, summaries, statuses) → terminal
     runs with their log pages. Every run keeps its HEAD op ID; its kind goes through the map; `Priority`
     0/1/2 → Low/Normal/High. A v1 checkpoint of an open run becomes that run's checkpoint named `v1`; one with
     no open run is deleted.
   - **states** by 05-tasks §5T.8.3's table, exactly (copy the table into a Go table and test every row).
   - **clocks**: v3 `SchedState` (`LastDue`, `LastFire`, `Missed`, `GateClosedAt`); the settings clocks under
     `scheduled_task_last_tick.` (RFC3339) as `LastDue` of the kind each task maps to, and
     `library_scan_full_last_run` as `LastDue` of `books.library.scan-full`;
     `maintenance_window_last_run` (`YYYY-MM-DD`) as `LastDue` = `LastRun` = **local** midnight
     (`time.ParseInLocation(..., time.Local)`); `ScheduleHash` empty.
   - **markers and buckets**: the pause marker → `Pause`; `registry.scan_standdown` → one `StandDown` for the
     converted scan kind's key; v2 debounce buckets → `Debounce`.
   - **stamps**: HEAD's completion and dependency-revision stamps for books and book-file subjects → `Stamp`
     with subject `books/books` or `books/book_files` and the renamed kind (library-scale).
   - **activity families, rewritten in place** (not imported): in `act:audit:`, `act:batch:`, `act:change:`,
     `act:debug:`, `act:info:`, `act:system:` an entry's `type` when it is a HEAD def ID, its `details.def_id` and
     its `def:<id>` tag; in `act:digest:` each item's `type` and `def:<id>` tag; `act:typ:` entries keyed by a HEAD
     def ID re-keyed to the kind (delete and put in one batch, with its primary row). `act:op:`, `act:src:`,
     `act:bk:`, `act:lvl:` untouched.
3. Not imported, deleted: watchdog strikes, `opv3:meta:counts`, the v2 and v3 definition rows.
4. Of the shared `setting:` family **only** the run keys above move; every other `setting:` row is
   byte-identical before and after.

A row the step cannot decode is quarantined and reported, never dropped. Every run, clock and stamp imported
is counted and checked against the source in `Verify`.

## Concurrency (09 §9.9; CLAUDE.md)

M4 is library-scale (stamps per book, activity rows, run logs). Split it into one or more
`datamigrate.DataMigration`s (all `Cutover: true`, BootBlocking, in one ordered block before M5) with an
explicit `tasks.Parallelism` each and workers over **disjoint** key ranges:

- runs and all their data shard by run ID (a run's `RunData` must be imported in the run's batch or a later one,
  05-tasks §5T.3.13), so no two workers touch one run;
- stamps shard by subject ID; activity rewrites by primary-row key range, with an `act:typ:` re-key staged in
  the batch of its primary row's shard;
- shared counters (quarantine counts, verify tallies) are per shard and merged after, or held behind one lock.

Say in a comment, per step, why its partition can never let two workers write one row.

## Level from the caller (order M2; F3-04 "Flip mechanism" item 2)

Every M4 `DataMigration` is `Breaking`, so each needs `RaisesLevel > 0` (05-storage §5S.3.6), and the block must run
`After` the level step of the flip that registers it. Neither exists yet: F5-18 adds its books level constant and
level-step ID to `internal/books/cutover/stepids.go` and its level step to `steps.go`. So M4's exported constructor
(name coined here, for example `m4.New(imp tasks.Importer, raisesLevel int, after []storage.StepRef)
[]datamigrate.DataMigration`) takes `RaisesLevel` and the `After` level-step ref from its caller. Every step it returns
gets that `RaisesLevel`. The first step runs `After` the passed ref, and each later step runs `After` the one before it.
F5-18 passes its own constant and level step. This row reads no level constant and adds none. The tests pass F3's level
as the fixture: the books level constant and level-step ID F3-06 put in `stepids.go`.

## Open spec gaps you must not paper over (STOP and report if hit)

- A HEAD row family the corpus holds that 05-storage §5S.9.1 does not list as imported, rewritten or deleted.

## TDD: tests to write first (spec names, copied exactly)

| Test (05-tasks §5T.9) | Proves |
|---|---|
| `TestCutover_TasksFixtureFromR0` **(ABO)** | over the R0-written store with queued, waiting, running, stopping (each reason), interrupted, quiesced, awaiting-decision and terminal runs of every shape, v1 history, checkpoints, ledgers, rows, marks, pages, clocks, the pause and stand-down markers, debounce buckets and stamps: after M4 every source row is gone or quarantined, every run is present with HEAD's ID, the renamed kind and §5T.8.3's state, every count and cross-reference matches the source, a full-sweep `library.scan` run is present under `books.library.scan-full` and any other under `books.library.scan` (the first-boot resume sweep half runs in F5-18, where kinds are registered) |
| `TestCutover_KindReferencesRewritten` **(ABO)** | every activity-family row that stored a HEAD def ID names the renamed kind in `type`, `details.def_id` and its `def:` tag; `act:typ:` holds the re-keyed entry and not the old one; a scan of the corpus finds no HEAD def ID in any other family outside the retired claims; `Requires` stamps are under the renamed kind |
| `TestCutover_RepairsRowsAndMarksImported` **(ABO)** | an R0 store with a repairs plan run awaiting decision and an apply run interrupted mid-way: after M4 no `opv2:row:`, `opv2:rowix:`, `opv3:mark:` or `opv3:rowmark:` row is left; the plan run holds every row it held; the apply run's marks are exactly HEAD's (the "applies only the unmarked rows" half needs the registered Fixer kinds: F5-18 extends this test) |
| `TestCutover_SharedSettingFamilyOnlyRunKeysMove` **(ABO)** | every `setting:` row other than the moved run keys is byte-identical before and after |
| `TestCutover_UndecodableRowQuarantined` **(ABO)** | a corrupt ops row from the hand-built overlay is copied under `c/storage/quarantine/`, deleted at its source in the same batch, counted; the step completes |
| `TestCutover_ResumableCrashMatrix` **(ABO)** | `CrashClone` at every chunk boundary and inside a chunk: the resumed step imports each row exactly once |

Add (names coined here): `TestKindRenameMap_UpToDate`; `TestKindRenameMap_RulesTable` (one case per rule 1–6,
the `library.scan` run-level split, the scheduler-name rule with its `library_scan_full` exception, `batch_poller`,
`transcode`); `TestM4_StateTable` (every row of §5T.8.3's state table);
`TestM4_WindowClockLocalMidnight` (with `TZ` west of UTC: a run at 23:30 local on day D imports `LastDue` =
local midnight of D); `TestM4_ShardsDisjoint` (`-race`).

## Steps

1. Base checks; STOP on any mismatch. M4's step ID is the one F3-06 reserved in
   `internal/books/cutover/stepids.go`; never coin another. Write a short design note in the PR description first (the step split,
   the shard keys, the batch layout per family) and get it reviewed before code (the Gate line).
2. `kindrename`: rules, generator, generated map, golden, the two map tests.
3. Decoders over HEAD's row types (import `internal/database` and `internal/operations/state` read-only;
   05-tasks §5T.8.1: `state` is read only by this step).
4. The step(s), family by family, each with its crash-matrix cell; then `Verify`.
5. The spec tests over the corpus; `internal/books/testdata/r2corpus/m4/` answers.
6. PR description: the families table (read / imported / rewritten / deleted / quarantined counts from the
   corpus), the map's size and rule tallies.

## Acceptance (commands and expected output)

```bash
go build ./... && go vet ./internal/books/cutover/...                                   # exit 0
go generate ./internal/books/cutover/kindrename/ && git diff --exit-code internal/books/cutover/kindrename/   # exit 0 (map up to date)
go test ./internal/books/cutover/kindrename/ ./internal/books/cutover/m4/ -race -count=1   # ok x2
go test ./internal/books/cutover/m4/ -race -count=1 -v -run 'TestCutover_(TasksFixtureFromR0|KindReferencesRewritten|RepairsRowsAndMarksImported|SharedSettingFamilyOnlyRunKeysMove|UndecodableRowQuarantined|ResumableCrashMatrix)$' | grep -c '^--- PASS'   # 6
git grep -n 'cutover/m4' -- internal/books/cutover/steps.go main.go | wc -l            # 0 (unregistered until F5-18)
git grep -n 'falkcorp/audiobook-organizer' -- core/ | wc -l                             # 0 (core holds no ABO name)
make ci                                                                                 # exit 0
```

## Out of scope

Registering M4, adding the flip's level step (which raises M7's target) and replacing M8 (F5-18, 03 §3.8: each flip
that adds a step raises M7's target in the same PR); the level constant and IDs in `stepids.go` (F5-18); moving HEAD's run-row store files into `internal/books/cutover/m4legacy/` (F5-18);
`TestKindRenameMapCoversLedger`, `TestCutover_CheckpointsResume` and the resume-sweep halves (F5-18, they need
the registered kinds); `TestCutover_NoCatchUpStorm` (F5-14); M5 (F5-15).

## Rollback

Unregistered code: revert the PR. After F5-18 ships, M4's rollback is 05-tasks §5T.8.3's: the offline restore of
the pre-migration checkpoint (or the owner's off-host archive) plus the pre-freeze release's binary; runs started
after the upgrade are lost.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task.
- File headers on every created file (the generated map carries one too; the generator writes it);
  `changelog.d/` fragment without a header.
- Conventional commit `feat(books/cutover): tasks step M4 and the kind-rename map (dark)`, body ending
  `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency: bounded pools, disjoint shards, explicit `Parallelism`, the partition argument in a
  comment (the "auto-merge must not double-merge" rule applies: never two workers on one run).
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.
