<!-- file: docs/agent-tasks/platform-core/PC-12-05-books-taskstore-contract-writer.md -->
<!-- version: 1.0.0 -->
<!-- guid: 580d1b58-9d6e-42ea-91d7-c277301fa64c -->
<!-- last-edited: 2026-11-04 -->

# PC-12-05 — internal/books/taskstore: HEAD's ops store code with its dual write, legacy steps 62/65/66 bodies, clock, pause and stand-down stores; internal/books/contract and internal/books/writer

| Field | Value |
|---|---|
| ID | PC-12-05 |
| Tier | **T4 Opus/xhigh–max** — the run families' one writer; a capability assertion left on a moved method makes a legacy step log "nothing to do" and be recorded as applied without running (silent), and a second store instance splits `opsMu` |
| Wave | W12 (S5, release `S5r1`; behaviour-neutral, dark for `core/tasks`) |
| Depends on | PC-12-01 |
| Serialized | PC-12-01 → {PC-12-03, PC-12-04, **PC-12-05**, PC-12-07} → PC-12-02 |
| Trailer | `Stage-Release: S5 1` |
| Digest | `docs/plans/platform-core/digests/S5.md` §3 (taskstore, contract, writer rows), §5 rules 1–4, 14, 20 |
| Owning spec | 05-tasks §5T.6.2 (all of it), §5T.3.13, §5T.8.1 rows `internal/operations`, `pkg/ops/writer.go`, `internal/database` ops storage; 09 S5 "Mechanism", "Data/keys"; 04 §2.4, §4.5, §8.4 note 3, §8.6 gate E tables |

## Goal

Create books' legacy run store `internal/books/taskstore`: **HEAD's own ops storage code moved unchanged in format**
(v3 record plus paired v2 row in one batch, HEAD's `opsMu`, HEAD's census row), the bodies of books' legacy steps 62,
65 and 66, the ops part of `InitializeStore` as `Recover`, and the clock, pause and stand-down codecs cut out of
`internal/scheduler` and `internal/operations/registry`. It implements `tasks.Stores`, `legacy.TasksRecoverer` and
`legacy.WriteLock`. Every existing ABO user of the ops families is rewired, behaviour-neutrally, to the **one**
taskstore instance. Create `internal/books/contract` and `internal/books/writer`. Nothing calls the `tasks.Stores`
face yet (PC-12-08 hands it to the kernel); the v2 engine keeps running on HEAD's methods, now on the taskstore type.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-05" -b feat/pc-12-05-books-taskstore origin/main
cd "$REPO/../audiobook-organizer-pc-12-05"
git rebase origin/main
make web-deps
git worktree list
```

Split the PR into commits that each build (`scripts/ci/check_commits_build.py`): (1) import-path-only moves with an
`old ==> new` list in the message (contract, writer); (2) the taskstore move and rewiring; (3) the `tasks.Stores` face
and its tests.

## Exact files

Create:

- `internal/books/taskstore/` — the ops storage code (step 3), `stores.go` (the `tasks.Stores` face, name coined here),
  `clock.go`, `pause.go`, `standdown.go`, `legacy_steps.go` (bodies of steps 62, 65, 66), `recover.go`.
- `internal/books/contract/` — receives `internal/operations/state.go`'s v1 checkpoint helpers (`OperationState`,
  `SaveCheckpoint`, `LoadCheckpoint`, `ClearState` and their store interfaces), `selection.go`, `types.go`.
- `internal/books/writer/` — the `opswriter` residue renamed (`writer.go`, `evidence.go`, `history.go`, `intent.go`,
  `evidence_journal.go`); `fence.go` is not moved (its names are aliases of `tasks` since PC-12-01; callers switch to
  `tasks.ErrFenced`, `tasks.WithFence`, `tasks.FenceFromContext`).
- `internal/books/legacy_steps_test.go` — `TestLegacyStepPreconditionsResolve`.

Modify: `internal/books/store/pebble_store_ops_v2.go`, `pebble_store_ops_v3.go`, `pebble_store_plan_legacy.go` and the
other ops files of `internal/books/store/` (moved out), `internal/books/store/` residue (exports per 04 §8.6 gate E),
`internal/operations/opswriter/` (emptied by the rename), `internal/operations/state.go`, `selection.go`, `types.go`
(moved), `internal/arch/layering_test.go` (`layerOf` entries for `internal/books/taskstore` and
`internal/books/contract`; the guard that the residue never imports taskstore).

Also modify (not in the inventory row; see Planner notes): the composition that builds the v2 registry, `childop`
and the books ticker; `internal/server/metadata_ops.go` (receives `BulkMetadataFetchParams`); every caller of a moved
method (re-verify grep 6).

## Re-verify greps

At `748fd787a` and the plan checkout these files are under `internal/database/`; PC-07-04 moved them to
`internal/books/store/` before this wave. Run against `internal/books/store/` at your base; the counts are the same.

```bash
S=internal/books/store
grep -n "opsMu  *sync.Mutex" $S/pebble_store.go                                   # 1. expect 1 (HEAD :227)
grep -n "^func (p \*PebbleStore) TransitionRunV3\|^func (p \*PebbleStore) UpdateOpProgressV2\|^func (p \*PebbleStore) CommitLedgerV3\|^func (p \*PebbleStore) ReplaceOpRowsPrefix\|^func (p \*PebbleStore) ReconcileOpsV3WithV2" $S/*.go | wc -l   # 2. expect 5
grep -n "^func (p \*PebbleStore) SweepHollowOperationsV2\|^func (p \*PebbleStore) MigrateOpsV2ToV3\|^func (p \*PebbleStore) StripLegacyPlanRows" $S/*.go | wc -l   # 3. expect 3 (steps 62, 65, 66)
grep -n "store\.(hollowOpsSweeper)\|AsCapability\[opsV3Migrator\]\|store\.(legacyPlanStripper)\|s\.(opsV3Reconciler)" $S/migrations.go $S/store.go | wc -l   # 4. expect 4 capability assertions to replace by direct calls
grep -n "Up: *migration062Up\|Up: *migration065Up\|Up: *migration066Up" $S/migrations.go | wc -l   # 5. expect 3
grep -rln "OpsV2Store\|OpsV3Store\|\.TransitionRunV3(\|\.UpdateOpProgressV2(\|\.CommitLedgerV3(\|\.ReplaceOpRowsPrefix(" --include=*.go internal cmd pkg | grep -v "^$S/" | grep -v _test | wc -l   # 6. expect 17 at 748fd787a (registry 9, server 3, maintenance plugins 2, opstest 1, testutil 1, repairs 1); report the list
ls internal/operations/opswriter/*.go | grep -v _test | wc -l                      # 7. expect 6
grep -c "^func SaveCheckpoint\|^func LoadCheckpoint\|^func ClearState\|^type OperationState struct\|^type BulkMetadataFetchParams struct" internal/operations/state.go   # 8. expect 5
grep -n 'const intervalLastTickPrefix\|const fullSweepLastRunSetting\|"maintenance_window_last_run"' internal/scheduler/*.go | grep -v _test | wc -l   # 9. expect 4
grep -n "pauseSettingKey\|type pauseMarker" internal/operations/registry/pause_gate.go | head -3   # 10. expect >= 2 (HEAD :52-60)
grep -n '"registry.scan_standdown"\|type scanStandDownMarker' internal/operations/registry/scan_standdown.go | head -3   # 11. expect >= 2 (HEAD :27, :106-110)
test -d core/tasks && test -f core/legacy/tasks.go && test -d core/legacy/legacytest && echo ok   # PC-12-01; expect ok
test ! -e internal/books/taskstore && echo ok                                       # expect ok
test -f scripts/ci/check_commits_build.py && echo ok                                # PC-01-04; expect ok
```

Counts that moved, or any zero: STOP and report.

## Invariants

1. **HEAD's code, HEAD's format.** Every run write commits the v3 record **and** its paired v2 row in one batch (HEAD
   `pebble_store_ops_v3.go:28-45`, owner decision D25); the census row `opv3:meta:counts` is rewritten from a read no
   other writer can overtake. No field is added to any legacy row; a `RunRecord`/`Clock` field HEAD has no place for is
   not stored and reads as zero; a record HEAD cannot hold is refused with `tasks.ErrStoreUnsupported` (05-tasks §5T.6.2
   "Fields HEAD's formats lack"; the table there lists `Stamp` subjects other than `books/books` and `books/book_files`,
   and stand-down markers for any key but the library scan or with more than one quiesced run).
2. **One lock, one instance.** `opsMu` moves onto the taskstore type; the taskstore constructor memoises and returns the
   same instance to every caller (the P4c call, books' `Register`, opsbridge, `childop`, the books ticker). The
   staging methods take **no** mutex: each checks `storage.LocksHeld(b, opsMu)` and refuses with
   `legacy.ErrRunsNotLocked`. `WriteLocks([]string{legacy.LockRuns})` returns `opsMu`. `Prune` and `Recover` commit
   their own chunks through `storage.CommitLocked` with `opsMu` per chunk. `ReplaceRows` is HEAD's
   `ReplaceOpRowsPrefix` unchanged: no lock, own chunked commits, never inside `WriteRuns`, exempt from
   `ErrRunsNotLocked` (05-tasks §5T.6.2 "ReplaceOpRowsPrefix's lock rule").
3. **HEAD's paths keep HEAD's locking until the flip.** The HEAD-shaped methods the v2 engine still calls
   (`TransitionRunV3`, `UpdateOpProgressV2`, `CommitLedgerV3`, …) keep taking `opsMu` themselves exactly as at HEAD.
   No `tasks.Stores` staging method calls one of them (it would deadlock under a `WriteRuns` hold).
4. **Direction.** `internal/books/taskstore` imports the store residue; the residue never imports taskstore (ABO guard
   assertion in `internal/arch/layering_test.go`). Taskstore imports `internal/operations/state` (stays in ABO) and maps
   `tasks.RunState` ↔ `state.V3` by 05-tasks §5T.6.2's "Run states" table, never by string equality; keeps HEAD's
   in-store legality check (`state.InferEvent`/`state.Next`).
5. **Legacy steps reach their bodies.** Steps 62, 65, 66 keep their order in the residue's step list and call funcs that
   `internal/books` wires from taskstore; every capability assertion on a moved method (grep 4) becomes a direct call;
   `InitializeStore`'s ops reconcile becomes `Recover`, called from books' start hook until PC-12-08 hands it to the
   executor.
6. **Clock codecs.** `ClockStore` serves: HEAD's v3 `SchedState` for v3 kinds; the settings clocks
   (`scheduled_task_last_tick.<task>` RFC3339; `library_scan_full_last_run` RFC3339) by `taskV2DefIDs`; and
   `maintenance_window_last_run` with HEAD's **date codec** read as local midnight
   (`time.ParseInLocation("2006-01-02", v, time.Local)`) and written as `next.LastRun.In(time.Local).Format("2006-01-02")`.
   `Advance` compares the clock it read under the hold and returns `tasks.ErrClockConflict` on a stale one.
   `taskV2DefIDs` becomes books data passed to taskstore and the ticker; the tick logic stays in `internal/scheduler`.
7. **Behaviour-neutral.** HEAD's ops tests move with taskstore and stay green; every v2-engine test stays green; no stored
   byte changes.

## Steps

1. Read 05-tasks §5T.6.2 end to end and 04 §8.6's gate E tables (the residue-file table and the unexported-name route
   table): they list exactly which names the residue exports (`RecoverPebbleClosed`, `PrefixEnd`, `PrefixUpperBound`,
   `OpChangeKey`, the version-source adapter), which move onto the taskstore type (`opsMu`, `opsLogSeq`,
   `opsV2MirrorOff`, `opsV2TimelineClosed`, `opsV2TimelineTrustMu`, `opsV2TimelineTrusted`), and that taskstore matches
   `storage.ErrClosed` instead of HEAD's `errStoreClosing`.
2. Commit 1 (import-path-only): move `state.go`'s v1 checkpoint helpers, `selection.go`, `types.go` to
   `internal/books/contract`; move `BulkMetadataFetchParams` to `internal/server` (its only user, `metadata_ops.go`);
   rename the `opswriter` residue to `internal/books/writer`; rewrite imports. Message lists `old ==> new`.
3. Commit 2: move HEAD's ops storage files (v2, v3, v3 sub-files, op rows, the v1 history reader, timeline, wait,
   status lookup, `iface_ops*.go` types the code needs, `pebble_store_plan_legacy.go`) and their tests into taskstore;
   build the taskstore type over books' `storage.LegacyRange` handle; rewire every caller (grep 6) and the composition
   so the v2 registry, `childop`, the repairs plan store and the ticker get the one instance.
4. Commit 3: the `tasks.Stores` face over the moved code (05-tasks §5T.6.2 interface table): `RunStore` (priority Low/
   Normal/High ↔ 0/1/2, `PrioritySystem` ↔ 3; `NewID` = HEAD's op-ID generator; `List` merges v3, v2-fallback and v1
   history as plain terminal `RunRecord`s; `Delete` = HEAD's discard key set), `ClockStore` (invariant 6), `PauseStore`
   (HEAD's `pauseSettingKey` and `pauseMarker`), `StandDownStore` (HEAD's `registry.scan_standdown` and
   `scanStandDownMarker` codec `{holder_op_id, scan_op_id, lease_expiry_unix_nano}`, blank = cleared). Compile-time
   assertions: `var _ legacy.TasksRecoverer = (*taskstore.RunStore)(nil)`, `var _ legacy.WriteLock = (*taskstore.RunStore)(nil)`
   (type name per your package; keep the assertion form).
5. `TestOpsFamiliesWrittenOnlyByTaskstore` (ABO archtest): AST check that every runtime write of the ops, clock, pause
   and stand-down families is inside taskstore, except books' legacy steps and the v1 carve-outs of 05-tasks §5T.6.2.
6. Acceptance; headers; `changelog.d/` fragment.

## Code contract

`tasks.Stores`, `RunStore`, `ClockStore`, `PauseStore`, `StandDownStore` exactly as 05-tasks §5T.3.13 (digest S5 §4
quotes them). From `legacy`: `TasksRecoverer{ Recover(ctx) error }`, `WriteLock{ WriteLocks(names []string) []storage.Lock }`,
`LockRuns = "tasks.runs"`, `ErrRunsNotLocked`. From `storage`: `CommitLocked`, `LocksHeld`, `ErrClosed`.

## Tests to write first (row done-tests; all names from the spec)

- `TestLegacyStepPreconditionsResolve` (in `internal/books`) — every legacy step, 62, 65 and 66 included, reaches its
  body for the production store type; none can log "nothing to do".
- The guard that the residue never imports taskstore (in `internal/arch/layering_test.go`).
- `TestOpsFamiliesWrittenOnlyByTaskstore`.
- `taskstest.StoresSuite` on the legacy store with HEAD fixtures (name the test function `TestTaskstore_StoresSuite`,
  coined here), including the `TZ`-west-of-UTC date-codec case of invariant 6 (`Advance` at 23:30 local, `Get`, due check
  the same day → not due).
- `legacytest.TasksRunStoreSuite` over books' store; `TestLegacyRunStoreRecovers` is its `TasksRecoverer` case.
- `TestLegacyRunLockSpansCommit`, `TestLegacyReplaceRowsTakesNoRunLock`, `TestLegacyRunTransitionVsCancelOneWins`,
  `TestLegacyProgressVsCancelKeepsState` (synctest; 05-tasks §5T.9 wording).
- `TestStandDown_MarkerAtBootDefersQuiescedRun` (ABO twin over HEAD's `registry.scan_standdown` row with a HEAD-written
  fixture).
- Also from 05-tasks §5T.9, owned here: `TestLegacyStoreRefusesNonHEADRecords`.

## Acceptance

```bash
go build ./... && go vet ./internal/books/... ./internal/operations/...          # exit 0
go test -race ./internal/books/taskstore/... ./internal/books/... -count=1       # every package ok, no DATA RACE
go test ./internal/books/ -run 'TestLegacyStepPreconditionsResolve' -count=1 -v | grep -c '^--- PASS'   # expect 1
go test ./internal/arch/... -count=1                                              # ok (layering, residue guard)
go test ./internal/operations/... ./internal/scheduler/... ./internal/repairs/... ./internal/plugins/maintenance/... -count=1   # ok: v2 engine unchanged
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/books/taskstore"' internal/books/store --include=*.go | wc -l   # expect 0
grep -n "store\.(hollowOpsSweeper)\|AsCapability\[opsV3Migrator\]\|store\.(legacyPlanStripper)\|s\.(opsV3Reconciler)" internal/books/store/*.go | wc -l   # expect 0
grep -rn "sync.Mutex" internal/books/taskstore/stores.go internal/books/taskstore/clock.go internal/books/taskstore/pause.go internal/books/taskstore/standdown.go | wc -l   # expect 0 (staging takes no mutex)
python3 scripts/ci/check_commits_build.py origin/main..HEAD                       # every commit builds
make ci                    # exit 0
make -C core ci            # exit 0 (unchanged core)
```

## Out of scope

- Handing the store to the kernel, `legacy.Stores.Tasks`, the books ticker enqueuing through `Deps.Tasks`, deleting the
  v2 engine's metric calls (PC-12-08).
- Deleting the step 62/65/66 code or `MigrateOpsV2ToV3`/`SweepHollowOperationsV2`/`StripLegacyPlanRows` (T8, PC-14-01).
- Any v1 carve-out writer (T2/T5/T7).

## STOP conditions

- A moved method is reached through an interface assertion you cannot turn into a direct call without importing taskstore
  from the residue.
- Any HEAD ops test needs its assertion changed to pass after the move.
- Rewiring needs a second taskstore instance anywhere (tests excepted).

## Rollback

Revert the PR (binary swap). No stored format changes; the previous build reads every row it wrote and every row this
build wrote, because both are HEAD's code.

## Repo rules

- File headers on every new or changed file (moved files keep their guid and bump the version); `changelog.d/` fragment
  without a header.
- Commits end with `Stage-Release: S5 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; move commits are
  `refactor(...)` with the `old ==> new` list; `gh pr merge <n> --rebase` after required checks and OWNER-12's row;
  never squash.
- Feature PRs touching `internal/books/store` ops files are held while this PR is open (announce it in the stage record).

## Planner notes

- The row mixes import-path-only moves (`contract`, `writer`) with behaviour work; the ground rules want move PRs apart
  from behaviour PRs. This brief keeps them as separate commits that each build; splitting into two PRs is cleaner.
- Files missing from the row: the composition that constructs the v2 registry, `childop` and the books ticker (they must
  receive the one taskstore instance, 05-tasks §5T.6.2 "One store instance per App"); `internal/server/metadata_ops.go`
  (receives `BulkMetadataFetchParams`, 05-tasks §5T.8.1); and every caller of a moved ops method (17 non-test files
  at `748fd787a`, in `internal/operations/registry`, `internal/server`, `internal/plugins/maintenance`, `pkg/ops/opstest`,
  `internal/testutil`, `internal/repairs`). `internal/operations/registry/` is also in PC-12-08's set; the order
  PC-12-05 → PC-12-08 already serializes them.
