<!-- file: docs/agent-tasks/platform-core/F5-03-generic-ops-become-core-kinds.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f5272da-4bda-4339-a93b-a8c82404a125 -->
<!-- last-edited: 2026-11-05 -->

# F5-03 — Conversion group T4: HEAD's generic ops become core kinds, dark

`core.storage.compact` with the books half `books.maintenance.aux-store-compact`, `core.storage.census`,
`core.log.journal.prune`, `core.window.nightly` and the new `core.tasks.prune`.

| Field | Value |
|---|---|
| ID | F5-03 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W27 |
| Tier | **T3** (Opus / high): core kinds over core state only; a wrong owner lets a core kind reach a module writer, and two of the owners sit at layers that cannot import `tasks` |
| Depends on | F5-15 (the journal store `core.log.journal.prune` prunes), F2-05 (transitively F2-01..F2-04) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

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
registrations (05-tasks §5T.8.2 "Three core kinds whose owners flip after F5"); this PR does **not** touch
them. This PR lands, dark (no kernel registers them in ABO before F5-18):

| HEAD def ID | Core kind | Owner | What it covers (05-tasks §5T.8.2 table) |
|---|---|---|---|
| `maintenance.db-optimize` | `core.storage.compact` | core storage (layer rule below) | full compaction of the main store through `storage.Admin.Compact`; `ConcurrencyKey` `core.storage.exclusive`; schedule `core.storage.schedules.compact`, nightly-window member |
| (same op, AI-scan and OpenLibrary stores) | `books.maintenance.aux-store-compact` (new books kind, no imported runs) | `internal/plugins/maintenance/db.go` | HEAD's steps 2 and 3 of `runDBOptimizeSteps` |
| `maintenance.db-census-exact` | `core.storage.census` | core storage, `storage.Admin.Census` (05-storage §5S.9.1) | exact per-family count into `c/storage/census/`; HEAD's `book_ver:` special case stays books code behind the census hook |
| `maintenance.purge-old-logs` | `core.log.journal.prune` | `core/internal/logjournal` (05-logging §5) | the journal by `core.log.journal.retention_days`; HEAD's `operationlog:` half is run-log history, covered by `core.tasks.prune` |
| `maintenance.window` | `core.window.nightly` | `core/internal/scheduler` (05-tasks §5T.3.8) | the window run; members are kinds with `ScheduleConfig.Window = "nightly"` |
| (operation-record half of `maintenance.retention-and-hygiene`) | `core.tasks.prune` | `core/tasks`, the executor (05-tasks §5T.6.1, §5T.10) | terminal-run retention by `core.tasks.history_retention` through `RunStore.Prune` |

## Spec to read first

1. 05-tasks §5T.8.2 (T4 row; "HEAD's generic ops: which become core kinds"; the three late-owner kinds),
   §5T.3.8 (`ScheduleFromConfig`, the key rule, `Window`), §5T.3.12 (`TestCoreRun_NeverCallsModuleWriter`),
   §5T.6.1 (`RunStore.Prune`), §5T.10 (`core.tasks.history_retention`, `core.tasks.schedules.prune`,
   `core.tasks.windows.nightly.*`).
2. 05-storage §5S.4 "Task kinds" (`core.storage.compact`, `core.storage.census`, both `tasks.Sequential("one
   store-wide pass")`, key `core.storage.exclusive`), §5S.9.1 (`storage.Admin.Census`).
3. 05-logging §5 "Journal contract" (`core.log.journal.prune`: "`tasks.Parallelism` sequential with reason
   'single range-delete per day bucket', scheduled by `tasks.ScheduleFromConfig("core.log.schedules.journal_prune")`",
   default `{"enabled":true,"every":"24h"}`), §10 (`TestJournal_BatchAndRetention`,
   `TestJournal_PruneScheduleKeyDeclared`).
4. 03 §3.5 R4 (core layers: `storage` is **L1**, `config` L3, `tasks` L4; "a package may import only its own
   or lower layers"; an `internal/*` package that bridges capabilities takes its highest import's layer).
5. 05-config's rename table rows `scheduled.db_optimize` (seeds both `modules.books.scheduler.schedules.db_optimize`
   and `core.storage.schedules.compact`).

## The layer rule you must resolve (read before step 1)

05-storage §5S.4 says "`core/storage` declares two core kinds" and "a `tasks.ScheduleConfig` that storage
declares in its own section". Package `storage` is L1 and `tasks` is L4 (03 §3.5 R4, normative), so code in
`core/storage` can neither call `tasks.Task` nor declare a field of type `tasks.ScheduleConfig`. 03 wins:
declare the two storage kinds and storage's schedule section in an internal **bridging** package that may
import `storage` and `tasks`, layer L4 by 03 R4's bridging rule, owned by storage and wired by the kernel —
`core/internal/storagetasks/` (name coined here) — and keep the kind names, key name and section path
exactly as 05-storage gives them (`core.storage.compact`, `core.storage.census`,
`core.storage.schedules.compact`). The inventory's `core/storage/kinds.go` is replaced by this package (Planner
notes). If at your base `core/storage` already imports `tasks` (it must not), STOP and report.

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
# Has F2-04 already landed the window kind? (decides step 5)
grep -rn '"core.window.nightly"\|core\.window\.' core/internal/scheduler/*.go | grep -v _test | wc -l   # 0 or >=1: record which
# HEAD's sources (f7ac5b11e)
grep -n 'ID: *"maintenance.db-optimize"' internal/plugins/maintenance/db.go | wc -l       # 1
grep -n 'func runDBOptimizeSteps' internal/plugins/maintenance/db.go | wc -l              # 1
grep -n 'dbCensusExactOpID' internal/plugins/maintenance/db_census_exact.go | wc -l       # >=1
grep -n 'ID: "maintenance.purge-old-logs"' internal/plugins/maintenance/cleanup.go | wc -l   # 1
grep -n '"maintenance.window"' internal/server/scheduler_maintenance_window_op.go | wc -l  # >=1
grep -rn 'maintenance.retention-and-hygiene\|retention-and-hygiene' internal/maintenance/jobs/retention_and_hygiene.go | wc -l   # >=1
```

Any hit on the `go list -deps ./storage/` line is a STOP.

## Files

| Action | Path |
|---|---|
| create | `core/internal/storagetasks/` (name coined here; replaces the row's `core/storage/kinds.go`): `kinds.go` (`core.storage.compact`, `core.storage.census`), `settings.go` (the `core.storage.schedules` section with `compact` of type `tasks.ScheduleConfig`), tests |
| create or extend | `core/internal/scheduler/window.go`: `core.window.nightly` (only what F2-04 did not already land) |
| create | `core/internal/executor/prune.go`: `core.tasks.prune` |
| create | `core/internal/logjournal/prune.go`: `core.log.journal.prune` and the `core.log.schedules` section if F5-15 did not declare it |
| modify | `internal/plugins/maintenance/db.go`: the new books kind `books.maintenance.aux-store-compact` (native shell only; HEAD's `maintenance.db-optimize` def unchanged) |
| modify | `internal/plugins/maintenance/db_census_exact.go`: the books census hook for `book_ver:` (only if `storage.Admin.Census` takes a hook at your base; else STOP for this item) |
| create | `core/internal/archtest_layers_test.go` entry for `internal/storagetasks` if that file lists internal packages by name |
| create | one `changelog.d/` fragment |

Do not modify `internal/plugins/maintenance/cleanup.go` (F5-08 owns its other defs; `purge-old-logs` gets no
books kind), `throttle_reset.go` or `internal/server/diagnostics_ops.go` (the two late-owner kinds are F2's),
`internal/server/scheduler_maintenance_window_op.go` (deleted by F5-18), `internal/maintenance/jobs/` (F5-01),
`main.go` or any kernel registration.

## Code contract (names from the spec unless marked)

```go
// core/internal/storagetasks (package name coined here)
const KindCompact tasks.Kind = "core.storage.compact"
const KindCensus  tasks.Kind = "core.storage.census"
const KeyStorageExclusive tasks.Key = "core.storage.exclusive"   // 05-tasks §5T.3.4: compaction and backup share it

type Schedules struct { // section core.storage.schedules
	Compact tasks.ScheduleConfig `json:"compact"` // tasks.ScheduleFromConfig("core.storage.schedules.compact"); default: HEAD's db_optimize task (nightly window member)
}

// both: tasks.Sequential("one store-wide pass"), ConcurrencyKey KeyStorageExclusive (05-storage §5S.4)
func Kinds(admin storage.Admin /* or the narrow Compact/Census surface at your base */) []tasks.Definition

// core/internal/logjournal/prune.go
// core.log.journal.prune: tasks.Sequential("single range-delete per day bucket"),
// Schedule tasks.ScheduleFromConfig("core.log.schedules.journal_prune"), default {"enabled":true,"every":"24h"};
// calls JournalStore.Prune(ctx, now - retention_days, limit) until more == false or the attempt's budget ends;
// retention_days 0 = keep (05-logging §8).
type Schedules struct { // section core.log.schedules (05-logging §5), if F5-15 did not declare it
	JournalPrune tasks.ScheduleConfig `json:"journal_prune"`
}

// core/internal/executor/prune.go
// core.tasks.prune: RunStore.Prune(ctx, now - core.tasks.history_retention, limit) in chunks until 0 deleted;
// terminal runs only (the store enforces it); Schedule tasks.ScheduleFromConfig("core.tasks.schedules.prune"),
// default {"enabled": true, "every": "24h"} (05-tasks §5T.10).

// core/internal/scheduler/window.go
// core.window.nightly: one run per window opening; runs the member kinds (ScheduleConfig.Window == "nightly")
// in Order; hours from core.tasks.windows.nightly.start/.end (defaults 1 / 4), enabled from .enabled.
```

`books.maintenance.aux-store-compact` (in `db.go`): a books Task kind over HEAD's steps 2 and 3 of
`runDBOptimizeSteps` (the AI-scan and OpenLibrary stores), shared-core style (HEAD's `maintenance.db-optimize`
v2 def unchanged and still compacting all three stores until F5-18), `tasks.Sequential("one store-wide pass
per auxiliary store")`, `Writes(tasks.Res("books", "aux_stores"))` (resource kind name coined here), no
`ConcurrencyKey` beyond `Key(Kind)`, its constructor takes `sched *tasks.Schedule` and sets `Common.Schedule` from it
(the conversion briefs' schedule hook): F5-14 declares the books key
`modules.books.scheduler.schedules.db_optimize` (a nightly member) and F5-18 passes it in.

## Invariants and the tests that prove them (names coined here unless marked)

1. **A core kind never reaches a module writer** (05-tasks §5T.3.12; 03 R1): `TestCoreRun_NeverCallsModuleWriter`
   (F2's archtest, spec name) passes with the five new core kinds in its package set; add them to its input
   if it takes an explicit list.
2. **Layers hold** (03 §3.5 R4): `cd core && go list -deps ./storage/` contains no `mediacore/tasks`;
   `core/internal/archtest_layers_test.go` passes.
3. **Schedule keys are valid** (05-tasks §5T.3.8 key rule): `TestScheduleFromConfig_OwnerKeyAccepted` style
   test for `core.storage.schedules.compact`, `core.log.schedules.journal_prune`, `core.tasks.schedules.prune`:
   each is declared with type `tasks.ScheduleConfig` by its owner and accepted at registration; the spec's
   `TestJournal_PruneScheduleKeyDeclared` (05-logging §10) covers the journal one.
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
8. **Window**: `TestWindowNightly_MembersInOrderOncePerOpening` (synctest, fake zone): members run in `Order`,
   once per opening, none outside `[start, end)`; a window that ran on local day D does not run again on D.
9. **Books half parity**: `TestAuxStoreCompact_CoversHEADSteps2And3`: the books kind compacts exactly the two
   auxiliary stores HEAD's op compacts, and nothing of the main store.
10. Every kind passes `taskstest.Conformance` (one test per owner package).

## Steps

1. Base checks; record whether F2-04 already declared `core.window.nightly` and whether F5-15 already declared
   `core.log.schedules`. Only add what is missing; never redeclare a key (a duplicate fails registration).
2. Write the tests of the invariants first.
3. `core/internal/storagetasks/`, then `logjournal/prune.go`, `executor/prune.go`, `scheduler/window.go` (if
   needed), each with `taskstest.Conformance`.
4. The books half in `db.go` and the census hook in `db_census_exact.go`.
5. PR description: the six HEAD IDs with their targets (05-tasks' table), the two late-owner kinds noted as
   F2's, the layer decision (`core/internal/storagetasks`), and which keys or kinds were already present.

## Acceptance (commands and expected output)

```bash
cd core
go build ./... && go vet ./internal/storagetasks/ ./internal/logjournal/ ./internal/executor/ ./internal/scheduler/   # exit 0
go test ./internal/storagetasks/ ./internal/logjournal/ ./internal/executor/ ./internal/scheduler/ -race -count=1     # ok x4
go list -deps ./storage/ | grep -c 'mediacore/tasks$'                                                                   # 0
grep -rhoE '"core\.(storage\.compact|storage\.census|log\.journal\.prune|window\.nightly|tasks\.prune)"' . | sort -u | wc -l   # 5
make ci                                                                                                                 # exit 0 (core's)
cd ..
go test ./internal/plugins/maintenance/ -run 'TestAuxStoreCompact_|DBOptimize|Census' -count=1   # ok
make ci                                                                                         # exit 0
git grep -n '"core.storage.compact"\|"core.tasks.prune"' -- main.go internal/server/ | wc -l     # 0 (dark: nothing registers them in ABO)
```

## STOP conditions

- `core/storage` would need to import `tasks` (the layer rule).
- `storage.Admin` at your base has no `Compact`/`Census` surface, or `Census` takes no hook for the books
  `book_ver:` case (report; do not move books code into core).
- A schedule key or section is already declared by another package with a different type.
- `TestCoreRun_NeverCallsModuleWriter` fails for a core kind (a wrong owner).

## Out of scope

`core.integrations.release-holds` and `core.diagnostics.export` (F2's minimal owner registrations); registering
anything in ABO (F5-18); deleting HEAD's four maintenance-plugin defs and the two server defs (F5-18); the
books half's schedule and the window membership of books kinds (F5-14); `cleanup.go`'s other seven defs (F5-08).

## Idempotency / rollback

If the five kind names already resolve in `core/` and the tests pass, run the acceptance instead. Rollback =
revert; nothing in ABO registers these kinds before F5-18.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task.
- File headers on every created or modified file; `changelog.d/` fragment without a header.
- Conventional commit `feat(core): generic ops as core kinds: storage compact and census, journal and run prune, nightly window (dark)`,
  body ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency: every prune and census loop is chunked and bounded; `Sequential` with its reason as the
  spec gives it (one store-wide pass), never an unbounded fan-out.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **Layer contradiction in the spec.** 05-storage §5S.4 places the two storage kinds and the
  `core.storage.schedules.compact` key "in `core/storage`", but 03 §3.5 R4 (normative) puts `storage` at L1
  and `tasks` at L4, so `core/storage` cannot import `tasks`. The brief follows 03 with a coined bridging
  package `core/internal/storagetasks`; the row's file `core/storage/kinds.go` should be replaced by it and
  05-storage amended.
- **`core.window.nightly` is claimed twice**: F2-04's title lands it with the fresh scheduler, and this row lists
  `core/internal/scheduler/window.go` for it. The brief makes F5-03 add only what F2-04 left out; the lead
  should pick one owner.
- **`core.log.schedules`**: 09's F2 row registers every core config section dark from F2, while 05-logging
  §5 has `logjournal` (F5-15) declare it. The brief declares it here only if neither did.
- **`TestJournal_BatchAndRetention`** spans F5-15 (batching) and this PR (the prune task); each PR adds its
  cases to the one spec-named test.
- The inventory's `internal/plugins/maintenance/<T4 file group>` resolves to `db.go` and
  `db_census_exact.go` (and no edit to `cleanup.go` or `throttle_reset.go`).
- `retention_and_hygiene`'s operation-record half becomes `core.tasks.prune`; the job itself must stop pruning
  run history when `core.tasks.prune` is registered (F5-18), or both prune it.
