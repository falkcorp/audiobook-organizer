<!-- file: docs/agent-tasks/platform-core/F5-03-generic-ops-become-core-kinds.md -->
<!-- version: 1.1.1 -->
<!-- guid: 2f5272da-4bda-4339-a93b-a8c82404a125 -->
<!-- last-edited: 2026-11-05 -->

# F5-03 — Conversion group T4: HEAD's generic ops become core kinds, dark

`core.storage.compact` and `core.storage.census` in the new `core/internal/storagetasks`, with the books half
`books.maintenance.aux-store-compact`; `core.log.journal.prune`; and the new `core.tasks.prune`.

| Field | Value |
|---|---|
| ID | F5-03 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W31 |
| Tier | **T3** (Opus / high): core kinds over core state only, in a new L4 package; a wrong owner lets a core kind reach a module writer |
| Depends on | F5-15 (the journal store `core.log.journal.prune` prunes, and `logjournal.Schedules`), F2-05 (transitively F2-01..F2-04), F2-16 (`Registrar.Tasks`, `coretest`'s task cells), F2-17 (`tasklint`) |
| Gate | Plan, then execute autonomously in a worktree with its own PR; inside an announced move window (its 04 keys move code out of `internal/plugins/maintenance`; 09 §9.2.10) |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-03-generic-core-kinds
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). Core code lives in the nested module `core/`
(`github.com/falkcorp/mediacore`); run `make -C core ci` and the root `make ci`.

## Goal

05-tasks §5T.8.2's T4 row turns the HEAD definitions whose every read and write after R2 is core-owned
state into **core** kinds, each declared in its owner's package, plus the new `core.tasks.prune`
("6 defs → 6 core kinds, plus the new `core.tasks.prune`"; gate "the owner file's tests"). Two of the six,
`core.integrations.release-holds` and `core.diagnostics.export`, already exist as F2's minimal owner
registrations (05-tasks §5T.8.2 "Three core kinds whose owners flip after F5"), and a third, `core.window.nightly`,
is declared by `core/tasks` at F2 with the scheduler (F2-04; 05-tasks §5T.8.2 table: "the one owner"); this PR
does **not** touch them. This PR lands, dark (no kernel registers them in ABO before F5-18):

| HEAD def ID | Core kind | Owner | What it covers (05-tasks §5T.8.2 table) |
|---|---|---|---|
| `maintenance.db-optimize` | `core.storage.compact` | `core/internal/storagetasks` over `storage.Admin.Compact` | full compaction of the main store through `storage.Admin.Compact`; `ConcurrencyKey` `core.storage.exclusive`; schedule `core.storage.schedules.compact`, nightly-window member |
| (same op, AI-scan and OpenLibrary stores) | `books.maintenance.aux-store-compact` (new books kind, no imported runs) | `internal/plugins/maintenance/db.go` | HEAD's steps 2 and 3 of `runDBOptimizeSteps` |
| `maintenance.db-census-exact` | `core.storage.census` | `core/internal/storagetasks` over `storage.Admin.Census` (05-storage §5S.9.1) | exact per-family count into `c/storage/census/`; HEAD's `book_ver:` special case stays books code behind the census hook |
| `maintenance.purge-old-logs` | `core.log.journal.prune` | `core/internal/logjournal` (05-logging §5) | the journal by `core.log.journal.retention_days`; HEAD's `operationlog:` half is run-log history, covered by `core.tasks.prune` |
| (operation-record half of `maintenance.retention-and-hygiene`) | `core.tasks.prune` | `core/tasks`, the executor (05-tasks §5T.6.1, §5T.10) | terminal-run retention by `core.tasks.history_retention` through `RunStore.Prune` |

## Spec to read first

1. 05-tasks §5T.8.2 (T4 row: "the conversions of `core.storage.compact` and `core.storage.census` create
   `core/internal/storagetasks`, dark"; "HEAD's generic ops: which become core kinds"; the three late-owner kinds;
   conversion rules 1–4), §5T.8.4 (`tasklint`),
   §5T.3.8 (`ScheduleFromConfig`, the key rule, `Window`), §5T.3.12 (`TestCoreRun_NeverCallsModuleWriter`),
   §5T.6.1 (`RunStore.Prune`), §5T.10 (`core.tasks.history_retention`, `core.tasks.schedules.prune`,
   `core.tasks.windows.nightly.*`).
2. 05-storage §5S.4 "Task kinds" (`core/internal/storagetasks`, L4, "imports `storage` and `tasks`, calls only
   `storage.Admin`, and is wired by the kernel"; `core.storage.compact`, `core.storage.census`, both
   `tasks.Sequential("one store-wide pass")`, key `core.storage.exclusive`; the schedule key
   `core.storage.schedules.compact` "declared at F2 by the kernel's core-section list … `storagetasks` only passes
   it to `tasks.ScheduleFromConfig`"), §5S.9.1 (`storage.Admin.Census`).
3. 05-logging §5 "Journal contract" (`core.log.journal.prune`: "`tasks.Parallelism` sequential with reason
   'single range-delete per day bucket', scheduled by `tasks.ScheduleFromConfig("core.log.schedules.journal_prune")`",
   default `{"enabled":true,"every":"24h"}`), §10 (`TestJournal_BatchAndRetention`,
   `TestJournal_PruneScheduleKeyDeclared`).
4. 03 §3.5 R4 (core layers: `storage` is **L1**, `tasks` L4; "a package may import only its own or lower
   layers"); 03 §3.3's `internal/...` row names `internal/storagetasks`.
6. 04 §8.6 and the `internal/plugins/maintenance → mediacore/internal/storagetasks` rows: the store-wide halves of
   `db.go` and `db_census_exact.go` are **re-implemented** in `storagetasks`, and `db_census_exact_test.go` and
   `db_optimize_heartbeat_test.go`, which test those halves, are rewritten with them as `storagetasks` tests.
5. 05-config's rename table rows `scheduled.db_optimize` (seeds both `modules.books.scheduler.schedules.db_optimize`
   and `core.storage.schedules.compact`).

## The owner package and the exemption list

`core/storage` is L1 and cannot import `tasks` (03 §3.5 R4), so the two storage kinds live in
`core/internal/storagetasks/` (05-storage §5S.4; 05-tasks §5T.8.2 T4 row), a new L4 package that imports `storage`
and `tasks`, calls only `storage.Admin`, and is wired by the kernel from F5-18. It declares no config section:
`core.storage.schedules.compact` is declared at F2 by the kernel's core-section list, and `storagetasks` only passes
the key to `tasks.ScheduleFromConfig`. Likewise `core.log.schedules.journal_prune` is declared from F2 (F2-09's
section list, typed by F5-15's `logjournal.Schedules`) and `core.tasks.schedules.prune` by `core/tasks` (05-tasks
§5T.10). If `core/storage` already imports `tasks` at your base (it must not), STOP and report.

This row's kinds are **core's**, so it has **no** `tasklint` exemption entry file (plan §(l) item 5). The files
that still hold HEAD's T4 definitions (`db.go`, `db_census_exact.go`, and `cleanup.go`'s `maintenance.purge-old-logs`)
stay listed in F5-18's entry until F5-18 deletes those definitions; do not touch any file under
`internal/books/testdata/tasklint_exempt.d/`. The one books kind this PR adds, `books.maintenance.aux-store-compact`,
still follows 05-tasks conversion rules 2 and 3: one shared body with HEAD's `maintenance.db-optimize`, which reaches
the two auxiliary stores only through what its shell hands it (the native shell: the stores its owner declares in
the constructor's dependency argument; HEAD's shell: the stores HEAD's body reads today).

## Base state: re-verify before editing

```bash
# F2 and F5-15 outputs (0 hits on any = STOP)
grep -rn 'func Task\[P, R any\]' core/tasks/*.go | grep -v _test | wc -l                  # 1
grep -rn 'func ScheduleFromConfig(key string) \*Schedule' core/tasks/*.go | wc -l         # 1
grep -rn 'type ScheduleConfig struct' core/tasks/*.go | wc -l                             # 1
grep -rn 'Prune(ctx context.Context, before time.Time, limit int) (deleted int, err error)' core/tasks/*.go | wc -l   # 1 (RunStore.Prune)
grep -rn 'history_retention' core/tasks/ core/internal/ | grep -v _test | wc -l           # >=1 (the core.tasks section, F2)
grep -rn 'type JournalStore interface' core/log/*.go | wc -l                              # 1 (F5-15)
ls core/internal/logjournal/*.go | grep -v _test | wc -l                                  # >=1 (F5-15)
grep -rn 'Census' core/storage/*.go | grep -v _test | grep -c 'func'                      # >=1 (storage.Admin.Census, R1)
grep -rn 'Compact' core/storage/*.go | grep -v _test | grep -c 'func'                     # >=1 (storage.Admin.Compact, R1)
(cd core && go list -deps ./storage/) | grep -c 'mediacore/tasks$'                     # 0 (must stay 0)
# The window kind and the schedule keys are declared at F2 (F2-04, F2-09) and the journal's Schedules type by F5-15
grep -rn '"core.window.nightly"' core/ --include=*.go | grep -v _test | wc -l             # >=1 (F2-04; never redeclare it)
grep -rn 'core.storage.schedules.compact\|"compact"' core/ --include=*.go | grep -v _test | wc -l   # >=1 (the key is declared; this PR only passes it)
grep -rn 'type Schedules struct' core/internal/logjournal/*.go | wc -l                    # 1 (F5-15)
grep -rn 'core.tasks.schedules.prune\|schedules.prune' core/ --include=*.go | grep -v _test | wc -l   # >=1 (core/tasks' key)
test ! -e core/internal/storagetasks && echo absent                                       # absent
# F2-16 / F2-17
test -f core/coretest/tasks.go && echo present                                            # present (F2-16: coretest's task cells)
test -d core/archtest/tasklint && echo tasklint                                           # tasklint (F2-17)
# HEAD's sources (f7ac5b11e)
grep -n 'ID: *"maintenance.db-optimize"' internal/plugins/maintenance/db.go | wc -l       # 1
grep -n 'func runDBOptimizeSteps' internal/plugins/maintenance/db.go | wc -l              # 1
grep -n 'dbCensusExactOpID' internal/plugins/maintenance/db_census_exact.go | wc -l       # >=1
grep -n 'ID: "maintenance.purge-old-logs"' internal/plugins/maintenance/cleanup.go | wc -l   # 1
grep -n 'func TestDBCensusExact\|func Test' internal/plugins/maintenance/db_census_exact_test.go | wc -l   # >=1
test -f internal/plugins/maintenance/db_optimize_heartbeat_test.go && echo present          # present
grep -rn 'maintenance.retention-and-hygiene\|retention-and-hygiene' internal/maintenance/jobs/retention_and_hygiene.go | wc -l   # >=1
```

Any hit on the `go list -deps ./storage/` line is a STOP.

## Files

| Action | Path |
|---|---|
| create | `core/internal/storagetasks/` (05-storage §5S.4): `kinds.go` (`core.storage.compact`, `core.storage.census`), `kinds_test.go`, and the store-wide test cases of HEAD's `db_census_exact_test.go` and `db_optimize_heartbeat_test.go` rewritten against `storage.Admin` (04 §8.6) |
| create | `core/internal/executor/prune.go`: `core.tasks.prune` |
| create | `core/internal/logjournal/prune.go`: `core.log.journal.prune` (its schedule key and `Schedules` type exist; this file only uses them) |
| modify | `internal/plugins/maintenance/db.go`: the new books kind `books.maintenance.aux-store-compact` (one shared body with HEAD's steps 2 and 3, conversion rules 2 and 3; HEAD's `maintenance.db-optimize` def unchanged in behaviour) |
| modify | `internal/plugins/maintenance/db_census_exact.go`: the books census hook for `book_ver:` (only if `storage.Admin.Census` takes a hook at your base; else STOP for this item) |
| modify | `internal/plugins/maintenance/db_census_exact_test.go`, `db_optimize_heartbeat_test.go`: only if the census hook or the shared body changes what they call; HEAD's cases stay and stay green until F5-18 deletes the HEAD definitions |
| create | one `changelog.d/` fragment |

Do not modify `internal/plugins/maintenance/cleanup.go` (F5-08 owns its other defs; `purge-old-logs` gets no
books kind), `throttle_reset.go` or `internal/server/diagnostics_ops.go` (the two late-owner kinds are F2's),
`core/internal/scheduler/` and `internal/server/scheduler_maintenance_window_op.go` (the window kind is F2-04's; the
HEAD op is deleted by F5-18), `internal/maintenance/jobs/` (F5-01), any file under
`internal/books/testdata/tasklint_exempt.d/`, `core/archtest`'s layer table (R1-01's `layerOf` already carries 03
R4's table), `main.go` or any kernel registration.

## Code contract (names from the spec unless marked)

```go
// core/internal/storagetasks (05-storage §5S.4)
const KindCompact tasks.Kind = "core.storage.compact"
const KindCensus  tasks.Kind = "core.storage.census"
const KeyStorageExclusive tasks.Key = "core.storage.exclusive"   // 05-tasks §5T.3.4: compaction and backup share it

// core.storage.compact's Schedule: tasks.ScheduleFromConfig("core.storage.schedules.compact") (the key is declared
// at F2 by the kernel's core-section list; this package only passes it); a nightly-window member.

// both: tasks.Sequential("one store-wide pass"), ConcurrencyKey KeyStorageExclusive (05-storage §5S.4)
func Kinds(admin storage.Admin /* or the narrow Compact/Census surface at your base */) []tasks.Definition

// core/internal/logjournal/prune.go
// core.log.journal.prune: tasks.Sequential("single range-delete per day bucket"),
// Schedule tasks.ScheduleFromConfig("core.log.schedules.journal_prune"), default {"enabled":true,"every":"24h"};
// calls JournalStore.Prune(ctx, now - retention_days, limit) until more == false or the attempt's budget ends;
// retention_days 0 = keep (05-logging §8). logjournal.Schedules (F5-15) types the key; do not redeclare it.

// core/internal/executor/prune.go
// core.tasks.prune: RunStore.Prune(ctx, now - core.tasks.history_retention, limit) in chunks until 0 deleted;
// terminal runs only (the store enforces it); Schedule tasks.ScheduleFromConfig("core.tasks.schedules.prune"),
// default {"enabled": true, "every": "24h"} (05-tasks §5T.10).
```

`books.maintenance.aux-store-compact` (in `db.go`): a books Task kind over HEAD's steps 2 and 3 of
`runDBOptimizeSteps` (the AI-scan and OpenLibrary stores), shared-core style (HEAD's `maintenance.db-optimize`
v2 def unchanged and still compacting all three stores until F5-18), `tasks.Sequential("one store-wide pass
per auxiliary store")`, `Writes(tasks.Res("books", "aux_stores"))` (resource kind name coined here), no
`ConcurrencyKey` beyond `Key(Kind)`; its constructor takes the owner-declared stores (conversion rule 3) and
`sched *tasks.Schedule`, and sets `Common.Schedule` from it (05-tasks §5T.8.2 conversion rule 4): F5-14 declares the books key
`modules.books.scheduler.schedules.db_optimize` (a nightly member) and F5-18 passes it in.

## Invariants and the tests that prove them (names coined here unless marked)

1. **A core kind never reaches a module writer** (05-tasks §5T.3.12; 03 R1): `TestCoreRun_NeverCallsModuleWriter`
   (F2's archtest, spec name) passes with the four new core kinds in its package set; add them to its input
   if it takes an explicit list.
2. **Layers hold** (03 §3.5 R4): `cd core && go list -deps ./storage/` contains no `mediacore/tasks`; core's
   layer test passes with `internal/storagetasks` at L4.
3. **Schedule keys are valid** (05-tasks §5T.3.8 key rule): each kind's `ScheduleFromConfig` key
   (`core.storage.schedules.compact`, `core.log.schedules.journal_prune`, `core.tasks.schedules.prune`) is already
   declared with type `tasks.ScheduleConfig` and is accepted at registration (`TestCoreKinds_ScheduleKeysDeclared`,
   name coined here); the spec's `TestJournal_PruneScheduleKeyDeclared` (05-logging §10, F5-15's) covers the journal
   one.
4. **Compaction excludes backup**: `TestStorageKinds_CompactHoldsExclusiveKey`: `core.storage.compact` declares
   `ConcurrencyKey` `core.storage.exclusive`, `Sequential`, `ReadOnly`/`Writes` on core storage only.
5. **Census parity**: `TestStorageKinds_CensusMatchesHEAD`: on the R0 fixture corpus (or HEAD's
   `db_census_exact` test store), `core.storage.census`'s per-family counts equal HEAD's
   `maintenance.db-census-exact` result, `book_ver:` included through the books hook.
6. **Journal retention**: the retention cases of `TestJournal_BatchAndRetention` (05-logging §10, spec name):
   the prune task calls `JournalStore.Prune` until `more` is false, by `retention_days`; 0 keeps everything.
7. **Run retention**: `TestTasksPrune_TerminalOnlyByRetention`: only terminal runs older than
   `core.tasks.history_retention` are deleted; an imported terminal run (from the R0 fixture via
   `taskstest`'s importer) ages out the same way; a running run is never touched.
8. **Books half parity**: `TestAuxStoreCompact_CoversHEADSteps2And3`: the books kind compacts exactly the two
   auxiliary stores HEAD's op compacts, and nothing of the main store, reaching them only through the stores its
   constructor is handed (conversion rule 3).
9. Every kind passes `taskstest.Conformance` (one test per owner package).

## Steps

1. Base checks; STOP on a mismatch. Never declare a schedule key or a section: they exist (a duplicate fails
   registration).
2. Write the tests of the invariants first.
3. `core/internal/storagetasks/` (with the rewritten store-wide cases of the two HEAD test files), then
   `logjournal/prune.go`, `executor/prune.go`, each with `taskstest.Conformance`.
4. The books half in `db.go` and the census hook in `db_census_exact.go`.
5. PR description: the HEAD IDs with their targets (05-tasks' table), the two late-owner kinds and the window kind
   noted as F2's, the 04 rows moved (`internal/plugins/maintenance → mediacore/internal/storagetasks`, re-implemented),
   and "no exemption entry file (core kinds)".

## Acceptance (commands and expected output)

```bash
cd core
go build ./... && go vet ./internal/storagetasks/ ./internal/logjournal/ ./internal/executor/   # exit 0
go test ./internal/storagetasks/ ./internal/logjournal/ ./internal/executor/ -race -count=1     # ok x3
go list -deps ./storage/ | grep -c 'mediacore/tasks$'                                                                   # 0
grep -rhoE '"core\.(storage\.compact|storage\.census|log\.journal\.prune|tasks\.prune)"' internal/ | sort -u | wc -l   # 4
make ci                                                                                                                 # exit 0 (core's)
cd ..
go test ./internal/plugins/maintenance/ -run 'TestAuxStoreCompact_|DBOptimize|Census' -count=1   # ok
make ci                                                                                         # exit 0
git grep -n '"core.storage.compact"\|"core.tasks.prune"' -- main.go internal/server/ | wc -l     # 0 (dark: nothing registers them in ABO)
git diff --name-only origin/main -- internal/books/testdata/tasklint_exempt.d/ | wc -l          # 0 (no entry file: core kinds)
```

## STOP conditions

- `core/storage` would need to import `tasks` (the layer rule).
- `storage.Admin` at your base has no `Compact`/`Census` surface, or `Census` takes no hook for the books
  `book_ver:` case (report; do not move books code into core).
- A schedule key this PR passes to `ScheduleFromConfig` is not declared at your base, or is declared with a type
  other than `tasks.ScheduleConfig` (report; do not declare it here).
- `TestCoreRun_NeverCallsModuleWriter` fails for a core kind (a wrong owner).

## Out of scope

`core.integrations.release-holds` and `core.diagnostics.export` (F2's minimal owner registrations);
`core.window.nightly` (F2-04); registering
anything in ABO (F5-18); deleting HEAD's four maintenance-plugin defs and the two server defs (F5-18); the
books half's schedule and the window membership of books kinds (F5-14); `cleanup.go`'s other seven defs (F5-08).

## Idempotency / rollback

If the four kind names already resolve in `core/` and the tests pass, run the acceptance instead. Rollback =
revert; nothing in ABO registers these kinds before F5-18.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task; the move window announced one working day
  ahead (09 §9.2.10).
- File headers on every created or modified file; `changelog.d/` fragment without a header.
- Conventional commit `feat(core): generic ops as core kinds: storage compact and census, journal and run prune (dark)`,
  body ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency: every prune and census loop is chunked and bounded; `Sequential` with its reason as the
  spec gives it (one store-wide pass), never an unbounded fan-out.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.
