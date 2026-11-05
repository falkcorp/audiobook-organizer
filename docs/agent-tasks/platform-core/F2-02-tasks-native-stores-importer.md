<!-- file: docs/agent-tasks/platform-core/F2-02-tasks-native-stores-importer.md -->
<!-- version: 1.1.0 -->
<!-- guid: c6364d76-4e03-4695-8c99-5d7af9d23802 -->
<!-- last-edited: 2026-11-05 -->

# F2-02 — `tasks.Stores` native over `c/tasks/`, `tasks.Importer`, and `taskstest.StoresSuite` with its importer cases

| Field | Value |
|---|---|
| ID | F2-02 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W24 |
| Tier | **T3** (Opus / high): durable stores with commit-time preconditions and an exactly-once importer, proven by a conformance suite; the key layout and invariants are fixed, the internals are not |
| Depends on | F2-01 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-02-native-stores
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md). Core tests run from inside `core/`.

## Goal

Implement the one store format of core's task system: `tasks.NativeStores(ks storage.Keyspace) tasks.Stores` over
the core keyspace `c/tasks/` (runs, queue, indexes, pages, checkpoints, chunk ledger, Fixer rows and marks, debounce
buckets, strikes, clocks, the pause marker, stand-down markers, `Requires` stamps), the `tasks.Importer` that an app's
cutover step uses once (ABO: books' M4 at F5), and the conformance suite `taskstest.StoresSuite` with its importer
cases. Dark: no kernel wiring, no ABO caller (05-tasks §5T.6.1: "dark in ABO until the tasks flip F5").

## Spec to read first

1. 05-tasks §5T.3.13 (the interfaces, `RunRecord`, `Expect`, `Importer`, `RunData`, sentinels; copy exactly).
2. 05-tasks §5T.6 and §5T.6.1 (the key table and the Serialized rule), §5T.7 (one build-and-commit path,
   conflict vs lost precondition, closed-ledger rule), §5T.5 rows on `Transition`/`Insert`, other writes,
   `ReplaceRows`, `Discard`.
3. 05-tasks §5T.9 rows: `taskstest.StoresSuite`, `TestProgressVsCancelKeepsState`,
   `TestLedgerCommitAfterTerminalRefused`, `TestConcurrentTransitionsOfDistinctRunsAllCommit`,
   `TestCancelCommitsUnderProgressTicks`, `TestPruneUnderConcurrentCompletions`, `TestTransitionVsCancelOneWins`,
   `TestFixerMarksLandDuringQuiesce`, `TestNativeStores_RoundTrip`, `TestPerRunSeq_NoReissueAfterCrash`, the
   importer cases row.
4. 05-storage §5S.3.2 (Batch, `Bind`, `Expect`, `GetForUpdate`, `RetryOnConflict`, participant binding), §5S.3.3
   (codecs: values are `codec.JSON[T](1)`), §5S.3.4 (`DeleteRange` outside a checked range only), §5S.3.11 (suite
   naming rule `<pkg>test.<Interface>Suite`, `TestNative<Interface>_RoundTrip`).
5. 06 §6.4.8 (store conformance suites: SDK, no non-test core package imports them).

## Base state: re-verify before editing

```bash
grep -rn 'type RunStore interface' core/tasks/              # 1 hit (F2-01), else STOP
grep -rn 'type Stores struct' core/tasks/                   # 1 hit, else STOP
grep -rn 'func NativeStores' core/tasks/ | wc -l            # 0 (this PR adds it); 1 = already applied, see Idempotency
grep -rn 'type Importer interface' core/tasks/ | wc -l      # 0 (this PR adds it)
grep -rn 'func (b \*Batch) GetForUpdate' core/storage/      # 1 hit (R1-20), else STOP
grep -rn 'func (b \*Batch) Expect' core/storage/            # 1 hit, else STOP
grep -rn 'func RetryOnConflict' core/storage/               # 1 hit, else STOP
grep -rn 'CrashClone' core/ | head -3                       # >=1 (the crash helper R1-20/R1-21 use), else STOP
grep -rn 'Serialized' core/storage/*.go | head -3           # >=1 (sub-prefix Serialized declaration), else STOP
```

HEAD reference (read, never import): `internal/database/iface_ops_v3.go` (the v3 records whose fields `RunEvent`,
`ItemOutcome`, `LogLine`, `Chunk`, `DebounceBucket`, `Strike` mirror), `internal/database/pebble_store_ops_v3.go`
(closed-ledger rule near the `SaveChunk` path), `internal/database/iface_op_rows.go` (`ReplaceOpRowsPrefix`
contract). After R1-26 these live under `internal/books/store/`; locate them with
`git ls-files | grep -E 'iface_ops_v3.go|pebble_store_ops_v3.go|iface_op_rows.go'` (expect 3 paths).

## Files

Create:

- `core/internal/taskstore/` — key layout and staging over `storage` (all `c/tasks/` keys of §5T.6.1, index
  maintenance, the Serialized guard keys).
- `core/tasks/importer.go` — `type Importer interface`, `RunData`, and the importer constructor.
- `core/tasks/native.go` — `func NativeStores(ks storage.Keyspace) Stores` and `NativeImporter`. The spec fixes
  `NativeStores` in package `tasks` (05-tasks §5T.3.13); keeping it in its own file of package `tasks`, over an
  `internal/taskstore` that does not import `tasks`, avoids an import cycle.
- `core/tasks/taskstest/storesuite.go` — `func StoresSuite(t *testing.T, newStores func(*testing.T) tasks.Stores)`
  plus the importer cases (see below for how the suite gets an importer).
- Tests: `core/internal/taskstore/*_test.go`, `core/tasks/native_test.go`.

**Import-cycle rule.** `core/internal/taskstore` must **not** import `core/tasks` if `tasks.NativeStores` calls into
it. Two acceptable shapes: (a) `taskstore` owns key encoding and raw staging over `[]byte` values and `tasks`
encodes the typed records; or (b) everything typed lives in `core/tasks/native*.go` and `taskstore` holds only key
helpers. Pick one, state it in the PR body. A cycle fails `go build`, so the compiler is the check.

Do not touch `core/kernel/`, `internal/**`, `go.mod`.

## Code contract (fixed by the spec)

- `func NativeStores(ks storage.Keyspace) Stores` (§5T.3.13) — `Runs`, `Clocks`, `Pause`, `StandDown` all
  non-nil.
- `type Importer interface { Run; RunData; Clock; Pause; StandDown; Debounce; Stamp; RenameKind }` with the
  signatures of §5T.3.13, `type RunData struct` as §5T.3.13.
- The importer constructor is not named by the spec. Add `func NativeImporter(ks storage.Keyspace) Importer`
  (name coined here) beside `NativeStores`, so the kernel can hand it to a cutover step in `module.Importers`
  (06 §6.4.2) at the flip.
- Key table: exactly §5T.6.1 (`c/tasks/run/<run_id>`, `q/<priority>/<enqueued>/<run_id>` with `PrioritySystem`
  sorting first, `ix/kind/…`, `ix/state/…`, `ix/done/…`, `page/<run_id>/{evt,intent,item,log}/…`,
  `ckpt/…`, `chunk/…`, `row/…`, `mark/…`, `debounce/<kind>/<bucket>`, `strike/…`, `sched/<kind>`, `pause`,
  `standdown/<key>`, `req/…`, `req-rev/…`) plus `def/<kind>` and `idem/<kind>/<key>` of §5T.6 (declare the
  prefixes; their writers are the kernel's and `Client.Start`'s, not this PR's). Values `codec.JSON[T](1)`.
- Serialized sub-prefixes are exactly the guard keys: `run/`, `debounce/`, `sched/`, `idem/` (§5T.6.1). No other
  family is Serialized.
- `NewID` returns a ULID. `Counts` counts keys under each `ix/state/<state>/` prefix (no census row).
- Every write method that takes `b` **stages** into the caller's batch and never commits. `Insert` stages
  `Expect(absent)` on `run/<id>`; `Transition`, `Progress`, `SaveCheckpoint`, `SaveChunk`, `MarkRow`, `Strike`,
  `AppendEvent`, `AppendIntent`, `PutItem`, `AppendLog`, `Delete` stage `GetForUpdate` on `run/<id>`; `Debounce`
  on its bucket; `ClockStore.Advance` checks the stored clock equals `prev` (absent when `had == false`).
  `Insert`/`Transition` return `ErrRunConflict` at staging when the read no longer matches `want`.
- `Prune` and `ReplaceRows` commit their own bounded batches (maintenance class); `Prune` deletes only terminal runs,
  the guard key by point `Delete` under `Expect`, the other families by `DeleteRange`; `ReplaceRows` refuses an
  empty prefix, clears then writes in bounded batches, each carrying the guard check and refused once terminal.
- Importer: `Run` stages under `Expect(absent)` (second import of one ID → `ErrRunConflict`); a run recorded as
  running or stopping is stored as `StateInterrupted`; `RunData` only on a run imported in this or an earlier
  batch; `Clock` stores an empty `ScheduleHash`; `RenameKind` rewrites runs and their kind index, clock,
  definition snapshot and stamps chunk by chunk and is idempotent.
- Concurrency: every store is goroutine-safe; no package-level mutable state (R5). Library-scale iteration
  (`Prune`, `ReplaceRows`, `RenameKind`) is bounded by `limit`/batch size; no unbounded fan-out (CLAUDE.md).

## TDD: tests to write first (names copied from 05-tasks §5T.9)

`taskstest.StoresSuite(t, newStores)` runs, as subtests, at least:

- CAS transitions over every executor edge (one subtest per edge of §5T.3.10's `RunState` list)
- `TestProgressVsCancelKeepsState` (`synctest`)
- `TestLedgerCommitAfterTerminalRefused` (`synctest`)
- `TestConcurrentTransitionsOfDistinctRunsAllCommit` (`synctest`; `Counts` exact)
- `TestCancelCommitsUnderProgressTicks` (`synctest`)
- `TestPruneUnderConcurrentCompletions` (`synctest`)
- `TestTransitionVsCancelOneWins` (`synctest`; also two `ClockStore.Advance` on one clock)
- `TestFixerMarksLandDuringQuiesce` (`synctest`)
- a discarded batch leaves nothing (every staging method), `Prune` and `ReplaceRows` excepted
- one row per run; per-run `Seq` advances; terminal-only `Prune`
- the importer cases (§5T.9 importer row): imported run keeps ID, kind, state and `Seq`; second import →
  `ErrRunConflict`; `RunData` lands only on an imported run; `CrashClone` at every point of an import batch leaves
  the chunk wholly imported or wholly not; `RenameKind` rewrites runs, clock, snapshot and stamps and is idempotent

The suite's signature is fixed as `StoresSuite(t, func(*testing.T) tasks.Stores)` (§5T.3.13). It needs an
`Importer` over the **same** keyspace for the importer cases: the suite discovers it by type assertion on the
returned `Stores.Runs` to an exported interface `interface{ Importer() tasks.Importer }` (name coined here), which
`NativeStores` implements; an implementation that does not satisfy it skips the importer cases with `t.Skip` and a
message. State this in the suite's doc comment.

Also, in `core/tasks/native_test.go`:

- `TestNativeStores_RoundTrip` — every `RunStore`, `ClockStore`, `PauseStore`, `StandDownStore` method over
  `c/tasks/` (the spec row's `hello.count` half needs hello to register the kind on a `coretest` App, which is
  F2-16's wiring; F2-16 adds that cell), and `StoresSuite(t, …NativeStores…)` over core's
  in-memory storage.
- `TestPerRunSeq_NoReissueAfterCrash` (`CrashClone` between row write and commit).

## Steps

1. Base-state greps; plan into the PR body.
2. Write `storesuite.go` and the two tests; they fail.
3. Implement `taskstore` key helpers and staging; then `NativeStores` and `NativeImporter`.
4. Make every suite case pass under `-race`.
5. Headers, changelog fragment, acceptance.

## Acceptance

```bash
cd core
go build ./...                                                                       # exit 0
go vet ./tasks/... ./internal/taskstore/...                                          # exit 0
go test ./tasks/... ./internal/taskstore/... -race -count=1                          # all ok
go test ./tasks/ -run 'TestNativeStores_RoundTrip|TestPerRunSeq_NoReissueAfterCrash' -race -count=1 -v | grep -c '^--- PASS'   # 2
go test ./tasks/ -run 'TestNativeStores_RoundTrip' -race -count=1 -v | grep -E 'TestProgressVsCancelKeepsState|TestLedgerCommitAfterTerminalRefused|TestConcurrentTransitionsOfDistinctRunsAllCommit|TestCancelCommitsUnderProgressTicks|TestPruneUnderConcurrentCompletions|TestTransitionVsCancelOneWins|TestFixerMarksLandDuringQuiesce' | grep -c -- '--- PASS'   # 7
go list -deps ./tasks/ | grep -c 'mediacore/tasks/taskstest'                         # 0 (no non-test core package imports the suite)
cd ..
make -C core ci                                                                      # exit 0
make ci                                                                              # exit 0
git grep -n 'mediacore/tasks"' -- ':!core/' | wc -l                                  # 0 (still dark)
```

## Out of scope

The executor and `WriteRuns` (F2-03); the scheduler (F2-04); the kernel's start rule and boot wiring of the stores
(F2-16); `module.Importers` wiring for a cutover step (the F5 flip); books' M4 (F5-16); any ABO file.

## Idempotency / rollback

If `func NativeStores` exists in `core/tasks/`, run the acceptance instead. Rollback = revert the PR; nothing outside
`core/tasks` and `core/internal/taskstore` depends on it yet.

## Repo rules

File headers on every new file; conventional commit (`feat(core/tasks): native stores, importer and StoresSuite (dark)`)
with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; a `changelog.d/` fragment; CLAUDE.md
concurrency rules. No 09 commit trailer exists.
