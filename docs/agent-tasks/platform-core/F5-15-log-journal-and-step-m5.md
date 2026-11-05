<!-- file: docs/agent-tasks/platform-core/F5-15-log-journal-and-step-m5.md -->
<!-- version: 1.1.5 -->
<!-- guid: 2172729c-4a08-45a0-ad75-8cf11d81ee4d -->
<!-- last-edited: 2026-11-05 -->

# F5-15 — The log journal, dark: `internal/logjournal`, `log.JournalStore` native over `c/log/`, `log.Importer`, `log/logtest`, and books' step M5 (`syslog:` into `c/log/`)

| Field | Value |
|---|---|
| ID | F5-15 |
| Release / flip | R2, tasks flip F5 (dark foundation PR; M5 is registered by F5-18) |
| Wave | W30 |
| Tier | **T3** (Opus / high): a durable journal store with an idempotent importer and a one-way migration step of every system-log row |
| Depends on | F2-05 (core tasks surface, transitively F2-01..F2-04); F3-06 (`internal/books/cutover` and the step conventions M2 set), F3-06 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-15-log-journal-m5
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). `core/` is the nested module `github.com/falkcorp/mediacore`.

## Goal

Land, dark, the core system-log journal of 05-logging §5 and books' cutover step M5 that imports HEAD's
`syslog:` rows into it:

1. `core/log/journal.go`: the SDK types `JournalStore`, `Importer`, `ImportedRecord`, `JournalQuery`,
   `JournalPage` exactly as 05-logging §3 declares them (`log` is L0: they name only `log` and standard types).
2. `core/internal/logjournal/`: the native store over `c/log/journal/<ULID>` (one versioned JSON record per key,
   ULIDs from the record time, attributes flattened to dotted keys), the importer over it, the journal `Tap`
   at `core.log.journal.min_level` handing records to `Append` in batches (≤ 256 records or 1 s), and the two
   types the `core.log.journal` and `core.log.schedules` sections name (`Settings` and `Schedules` of 05-logging §5).
   The sections themselves are registered dark from F2 by F2-09's section list (with F2-04's `journal_prune` key);
   this PR adds the types and **swaps** those declarations in `core/kernel/core_sections.go` to `logjournal.Settings`
   and `logjournal.Schedules`, so one type is registered (05-logging §8: "`logjournal.Settings` and
   `logjournal.Schedules` are registered dark from F2").
3. `core/log/logtest/`: `logtest.JournalStoreSuite` (the exported contract, with its importer cases).
4. `internal/books/cutover/m5/`: books' BootBlocking cutover step M5, a `datamigrate.DataMigration` that decodes
   `syslog:` rows in chunks and calls `log.Importer.Import`, then deletes the chunk's source rows.

Nothing is wired: no kernel starts the tap, no step list includes M5, no route serves the journal. The tasks
flip F5-18 registers M5 in `internal/books/cutover/steps.go`, starts the journal and deletes HEAD's writers
and pruner (05-logging §11 item 1, R2 part). The prune kind `core.log.journal.prune` is F5-03's, the query route
`GET /api/core/v1/system/logs` F5-19's and the live tail `core.log.recorded.v1` F6-05's.

## Spec to read first (in this order)

1. 05-logging §3 (the `Record`, `JournalStore`, `Importer`, `ImportedRecord`, `JournalQuery`, `JournalPage`,
   `OtherUserPredicate` declarations: copy them exactly), §5 (journal contract, native store, "ABO's rows:
   imported once (M5)"), §7 (concurrency), §8 (config keys), §10 (tests), §11 item 1 (R2 part).
2. 05-storage §5S.3.13 (the cutover: importer rules, the `log.Importer` exception — it commits its own batch and
   is idempotent —, quarantine, `Verify`), §5S.3.6 (`DataMigration`, `StepWriter`, chunks, cursors, crash
   matrix), §5S.9.1 (the `syslog:` family row).
3. 03 §3.8 M5 and the step order (M1, M7, M2, M3, M4, **M5**, M6, M8); 03 §3.3 "Store interfaces and importers".
4. 09 §9.2.3 (one batch per chunk; no row in both forms), §9.3 item 1 (the R0-written corpus and the labelled
   hand-built overlay), §9.9 (a library-scale step is a `DataMigration` with its own `tasks.Parallelism` and a
   bounded pool over disjoint key ranges).
5. 06 §6.4.2 (`module.Manifest.Cutover func(module.Importers) []datamigrate.DataMigration`; `Importers.Log`
   lands at F5, §6.4.10).

## Base state: re-verify before editing

```bash
# R1/F2/F3 outputs (0 hits on any = STOP)
test -d core/log && grep -rn 'type Record struct' core/log/*.go | wc -l                # 1 (R1's core/log)
grep -rn 'func (l \*Logging) Tap(' core/log/*.go | wc -l                                  # 1
grep -rn 'type OtherUserPredicate func' core/log/*.go | wc -l                             # 0 or 1: if 1, do not redeclare it
grep -rn 'type DataMigration struct' core/storage/datamigrate/*.go | wc -l                # 1 (F2)
grep -rn 'func (w \*StepWriter) Quarantine(' core/storage/datamigrate/*.go | wc -l        # 1
grep -rn 'Cutover func(module.Importers)\|Cutover  *func(' core/module/*.go | wc -l       # >=1 (F3)
grep -rn 'type Importers struct' core/module/*.go | wc -l                                 # 1
ls internal/books/cutover/ | head                                                          # exists (F3-06): m2/ (or equivalent), steps.go
test -d internal/books/testdata/r2corpus && echo corpus                                    # corpus (F2-12/F2-13)
grep -rn 'syslog' internal/books/testdata/r2corpus/manifest.json | wc -l                  # >=1 (the corrupt syslog: overlay row, 09 §9.3), else STOP
# This PR's targets are absent
test ! -e core/internal/logjournal && test ! -e core/log/journal.go && test ! -e internal/books/cutover/m5 && echo absent   # absent
# The core.log.journal and core.log.schedules sections are registered dark from F2 (F2-09's section list); never declare them twice
grep -rn 'core.log.journal\|core.log.schedules' core/kernel/core_sections.go core/config/ | grep -v _test | wc -l   # >=2 (F2-09), else STOP
# M5's step ID is reserved by F3-06
grep -n 'M5\|m5\|syslog' internal/books/cutover/stepids.go | wc -l                     # >=1 (F3-06), else STOP
# HEAD's source rows (f7ac5b11e)
grep -n 'key := fmt.Sprintf("syslog:%s:%s", time.Now().Format(time.RFC3339Nano), source)' internal/database/pebble_store_activity.go | wc -l   # 1
grep -n 'type SystemActivityLog struct' internal/database/store.go | wc -l                # 1 (ID, UserID, Source, Level, Message, CreatedAt)
grep -n '"syslog:"' internal/database/keyfamilies.go | wc -l                              # 1
```

Spec anchors: 05-logging cites `pebble_store_activity.go:20-37` and `store.go:675-682` at `748fd787a`; at
`f7ac5b11e` they are lines 21 (the key format) and 675-682 (the struct), unchanged.

## Files

| Action | Path |
|---|---|
| create | `core/log/journal.go` (the SDK types) |
| create | `core/log/journal_test.go` (tests beside the SDK types; inventory row F5-15) |
| create | `core/log/logtest/` (`JournalStoreSuite`) |
| modify | `core/kernel/logmetrics.go` and `core/kernel/testdata/log/series.golden` (R1-18's bridge: add `mediacore_log_journal_write_errors_total` (counter) and `mediacore_log_journal_lag_seconds` (gauge) from 05-logging §9, read from the journal tap's counters; add their lines to the golden, asserted by `metricstest.AssertSeries`) |
| create | `core/internal/logjournal/` (`store.go` native store, `importer.go`, `tap.go`, `settings.go` with the `Settings` and `Schedules` types, tests); **not** `prune.go` (F5-03), `tail.go` (F6-05) |
| modify | `core/kernel/core_sections.go`: the `core.log.journal.*` declaration (F2-09's) and the `core.log.schedules.journal_prune` declaration (F2-04's) now name `logjournal.Settings` and `logjournal.Schedules`; the kernel's own struct types for the two sections are deleted. Touch no other section or key |
| create | `internal/books/cutover/m5/` (the step, its tests, its fixture glue) |
| create | one `changelog.d/` fragment |

Do not modify `internal/books/cutover/steps.go` (F5-18 registers M5), `internal/books/cutover/stepids.go` (F3-06's),
`core/kernel/core_sections_test.go` (F2-09's), `core/kernel/core_sections_schedule_test.go` (F2-04's), `internal/activity`, `internal/logger`, `internal/database`, `main.go` or any
kernel wiring.

## Code contract

`core/log/journal.go`: copy 05-logging §3's `JournalStore`, `Importer`, `ImportedRecord`, `JournalQuery`,
`JournalPage` (and `OtherUserPredicate` if R1 did not land it) verbatim, with their doc comments. `JournalStore`
is the one store interface that commits its own batch (05-logging §3; 05-storage §5S.3.13 "One exception").

`core/internal/logjournal` (L4: it imports `storage`; `Schedules` names `tasks.ScheduleConfig`, and F5-03's prune
kind imports `tasks` too):

```go
type Settings struct { // section core.log.journal (05-logging §5)
	Enabled       bool   `json:"enabled"`
	MinLevel      string `json:"min_level" schema:"enum=debug|info|warn|error"`
	RetentionDays int    `json:"retention_days" schema:"minimum=0"`
	QueueSize     int    `json:"queue_size" reload:"restart" schema:"minimum=64"`
}

type Schedules struct { // section core.log.schedules (05-logging §5)
	JournalPrune tasks.ScheduleConfig `json:"journal_prune"` // tasks.ScheduleFromConfig("core.log.schedules.journal_prune"), default {"enabled":true,"every":"24h"}
}
// F2-09 (and F2-04, for journal_prune) registered both sections dark in core/kernel/core_sections.go with the kernel's
// own struct types. This PR swaps those declarations to these two types (same paths, JSON tags, defaults and reload
// tags), so only one Go type is registered per section. F2-09's TestCoreSections_EveryOwnerKeyDeclared and F2-04's
// TestCoreSections_ScheduleKeysDeclared compare leaf paths by reflection, so both stay green unedited. A field whose
// path, default or tag differs between the two declarations: the owner table (05-logging §8) wins; report it.
// Native store, importer and tap constructors: names coined here (e.g. NewStore, NewImporter, StartTap),
// taking the c/log/ keyspace handle the kernel will pass at F5-18.
```

Rules:

- **Keys.** `c/log/journal/<ULID>`, the ULID's time part from `Record.Time`, so `Query` (newest first) and
  `Prune` (oldest first, ≤ `limit` per call, one bounded batch, `more` reported) are prefix scans and range
  deletes.
- **Importer idempotency.** `Import` derives each record's key from its `Time` and `SourceID` only (the ULID's
  random part is a hash of `SourceID`), so a re-import after a crash writes the same keys and adds nothing
  (05-logging §3). One durable batch per call.
- **Tap.** `Logging.Tap("core.log.journal", minLevel, queueSize, fn)` with `fn` batching into `Append` (≤ 256 or
  1 s); the tap never blocks the logger (drops counted; R1-36 owns `TestTap_NeverBlocks`, which covers the tap
  mechanism, so this row does not).
- **Records are already redacted** when they reach the store (05-logging §3).

`internal/books/cutover/m5`: a `datamigrate.DataMigration` built from `module.Importers.Log`:

| Field | Value |
|---|---|
| `ID` | M5's step ID as F3-06 reserved it in `internal/books/cutover/stepids.go` (stable forever; never coin another) |
| `Owner` | books |
| `Source` | the retired claim range of `syslog:` |
| `Parallelism` | `tasks.Fixed(n, "<reason>")` or `tasks.CPU()` over disjoint key-range shards of `syslog:` (by the RFC3339Nano time prefix), chosen and justified in the PR (09 §9.9; CLAUDE.md): the importer commits its own batch per call, so workers own disjoint ranges and never share a row |
| `Mode`, `Compat`, `Cutover` | BootBlocking, Breaking, `Cutover: true` (05-storage §5S.3.13) |
| `RaisesLevel` | the books level the caller passes (see below); never a constant read here |
| `After` | the level-step ref the caller passes (see below) |
| `Step` | per chunk: decode each row's JSON as HEAD's `SystemActivityLog`; build `ImportedRecord{Record{Time: CreatedAt, Level: <HEAD level string → slog.Level>, Module: "books", Subsystem: Source, Message: Message, Attrs: user_id when non-empty}, SourceID: <the row's key>}`; `Importer.Import` the chunk; then `StepWriter.Delete` each imported source key; a row that does not decode (or a level string HEAD never wrote) → `StepWriter.Quarantine(key, raw, reason)` |
| `Verify` | imported + quarantined = rows read; no `syslog:` key left |

**Level from the caller** (order M2; F3-04 "Flip mechanism" item 2). M5 is `Breaking`, so it needs `RaisesLevel > 0`
(05-storage §5S.3.6), and it must run `After` the level step of the flip that registers it. Neither exists yet: F5-18
adds its books level constant and level-step ID to `stepids.go` and its level step to `steps.go`. So M5's exported
constructor (name coined here, for example `m5.New(imp log.Importer, raisesLevel int, after []storage.StepRef)`) takes
`RaisesLevel` and the `After` level-step ref from its caller. F5-18 passes its own constant and level step. This row
reads no level constant and adds none. The tests pass F3's level as the fixture: the books level constant and
level-step ID F3-06 put in `stepids.go`.

Ordering inside a chunk: import first, delete second. A crash between them re-imports the same keys (idempotent)
and then deletes; a crash after the delete commit leaves nothing to redo. The deletes and the shard cursor
commit in the chunk's batch; the journal records commit in the importer's own batch first.

## TDD: tests to write first (spec names, copied exactly)

| Test | Where | Proves (05-logging §10) |
|---|---|---|
| `logtest.JournalStoreSuite` | `core/log/logtest` | `Append` then `Query` round-trips level, module, subsystem, message, time, attributes; filters and cursors; `Prune` bounded and oldest first; importer cases: importing the same `ImportedRecord`s twice leaves one record each; imported records sort by their own `Time` among native ones |
| `TestNativeJournalStore_RoundTrip` | `core/internal/logjournal` | the native store over `c/log/journal/` passes the suite |
| `TestJournal_BatchAndRetention` (batching half; F5-03 adds the prune-task half) | `core/internal/logjournal` | batching bounds (256 records or 1 s); "no `core/log` migration step or level stamp exists" |
| `TestJournal_PruneScheduleKeyDeclared` | `core/internal/logjournal` | `core.log.schedules.journal_prune` is declared with type `tasks.ScheduleConfig` in `logjournal.Schedules` and accepted by `ScheduleFromConfig` at registration |
| `TestCutover_SyslogImported` **(ABO)** | `internal/books/cutover/m5` | over the store written by the R0 binary (with the corrupt row of 09 §9.3's labelled overlay): M5 leaves no `syslog:` row, imports one journal record per row with time, level, source as `Subsystem`, message and `user_id`, quarantines the corrupt row, and resumes after a crash at any chunk point without duplicates |

Add (names coined here): `TestM5_CrashMatrixNoDuplicates` (`CrashClone` at every chunk boundary and between
import and delete; each source row imported exactly once) and `TestM5_ParallelShardsDisjoint` (`-race`; no two
workers import the same key).

## Steps

1. Base checks; STOP on any mismatch.
2. `core/log/journal.go` and `logtest.JournalStoreSuite` (failing against nothing).
3. The native store and importer; `TestNativeJournalStore_RoundTrip` green.
4. The tap, `Settings` and `Schedules`; `TestJournal_BatchAndRetention`'s batching cases (F5-03 adds its prune
   cases); `TestJournal_PruneScheduleKeyDeclared` (05-logging §10) for the `Schedules` type.
5. M5 with its tests over the R2 corpus; the crash matrix.
6. PR description: the record mapping table above, the parallelism choice and reason, counts from the corpus
   (rows read / imported / quarantined).

## Acceptance (commands and expected output)

```bash
cd core
go build ./... && go vet ./log/... ./internal/logjournal/                          # exit 0
go test ./log/... ./internal/logjournal/ -race -count=1                            # ok
go test ./internal/logjournal/ -run 'TestNativeJournalStore_RoundTrip|TestJournal_BatchAndRetention|TestJournal_PruneScheduleKeyDeclared' -count=1 -v | grep -c '^--- PASS'   # 3
go list -deps ./log/ | grep -c 'mediacore/storage'                                 # 0 (log stays L0)
go test ./kernel/ -run 'TestCoreSections_' -count=1                                # ok (F2-09's and F2-04's section tests, unedited)
grep -cE 'logjournal\.(Settings|Schedules)' kernel/core_sections.go                # >=2 (the swapped declarations)
make ci                                                                            # exit 0 (core's)
cd ..
go test ./internal/books/cutover/m5/ -race -count=1 -run 'TestCutover_SyslogImported|TestM5_' -v | grep -c '^--- PASS'   # 3
git grep -n 'cutover/m5' -- internal/books/cutover/steps.go main.go | wc -l       # 0 (unregistered until F5-18)
make ci                                                                            # exit 0
```

## STOP conditions

- `core/log` would need to import `storage` (L0 rule) — redesign inside `logjournal`, never in `log`.
- The R2 corpus has no `syslog:` rows or no corrupt overlay row (`TestCutover_SyslogImported` cannot be honest).
- `module.Importers` has no place for `Log` and the kernel shape at your base cannot take one without editing
  kernel wiring (F5-18's files): report.

## Out of scope

`core.log.journal.prune` (F5-03); registering M5, starting the tap, deleting `StandardLogger`'s system-log
writes, the activity `Writer`'s line parsing and `internal/logger/retention.go` (F5-18); the query route
`GET /api/core/v1/system/logs` with `TestJournal_RequiresSystemManage`'s route half and
`TestJournal_OtherUsersLinesNeedUsersView` (F5-19); the live tail topic `core.log.recorded.v1` and the test's event
half (F6-05).

## Idempotency / rollback

If `core/internal/logjournal/store.go` exists and the suite passes, run the acceptance instead. Rollback = revert;
nothing is wired, no stored row changes before F5-18.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task.
- File headers on every created file; `changelog.d/` fragment without a header.
- Conventional commit `feat(core/log): native system-log journal, importer and books step M5 (dark)`, body
  ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency: M5 is a library-scale loop (one row per system-log line): bounded pool, disjoint shards,
  explicit `Parallelism`, `ctx` honoured between chunks (09 §9.9).
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **`TestJournal_PruneScheduleKeyDeclared`** (05-logging §10) has no owner in the plan's gate table. The row gives
  F5-15 the `logjournal.Schedules` type the test checks, so this brief writes it here; the gate table should
  list it under F5-15.
