<!-- file: docs/agent-tasks/platform-core/PC-12-07-logjournal-over-syslog-journalstore.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8db384d0-05eb-4ad8-b59e-e6e454ddb734 -->
<!-- last-edited: 2026-11-04 -->

# PC-12-07 — internal/logjournal over syslog with log.JournalStore (wired at the flip); the activity line parser, syslog writes and pruner removed in the same release

| Field | Value |
|---|---|
| ID | PC-12-07 |
| Tier | **T2 Sonnet/high** — a moved persistence path with one release coupling; the contract and HEAD's row shape are fixed by the spec |
| Wave | W12 (S5, release `S5r1`, dark until PC-12-08 wires it) |
| Depends on | PC-12-01 |
| Serialized | PC-12-01 → {PC-12-03, PC-12-04, PC-12-05, **PC-12-07**} → PC-12-02 |
| Trailer | `Stage-Release: S5 1` |
| Digest | `docs/plans/platform-core/digests/S5.md` §3 (`internal/logjournal` row), §5 rule 21, §6 row 6 |
| Owning spec | 05-logging §3 (`JournalStore`, `JournalQuery`, `JournalPage`), §5 ("Journal contract", "Native store", "ABO's store"), §10 tests, §11 step 1 (S5 bullet); 09 S5 "Data/keys"; 09 §9.2.1 row 6 |

## Goal

Land the system-log journal and both stores **without changing what ABO writes**:

- `log.JournalStore` (with `JournalQuery`, `JournalPage`) in `core/log`, and the exported suite `logtest.JournalStoreSuite`;
- `core/internal/logjournal`: the journal `Tap` (batches of ≤ 256 records or 1 s), the native store over
  `c/log/journal/<ULID>`, the prune task `core.log.journal.prune`, and its two settings sections;
- **ABO's `JournalStore`** over HEAD's `syslog:` rows (books code), round-tripping exactly HEAD's row shape.

ABO's current writers keep running in this PR. PC-12-08 (the flip, same release `S5r1`) hands books' store to the kernel
through `Manifest.LegacyStores.Logs` and, in that same PR, deletes the activity `Writer`'s line parsing and tap,
`StandardLogger`'s `syslog:` writes and `internal/logger/retention.go`, so each row has one writer and one pruner from
the first build that runs the journal (05-logging §5 "ABO's store", §11 step 1).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-07" -b feat/pc-12-07-logjournal origin/main
cd "$REPO/../audiobook-organizer-pc-12-07"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create:

- `core/log/journal.go` — `JournalStore`, `JournalQuery`, `JournalPage` exactly as 05-logging §3.
- `core/log/logtest/` — `JournalStoreSuite(t, newStore, fields)` (signature coined here; the suite name is the spec's):
  the store declares which fields its row shape holds and the suite round-trips exactly those.
- `core/internal/logjournal/` — `tap.go`, `native.go` (store over `c/log/journal/`), `prune.go` (the task), `settings.go`
  (`Settings`, `Schedules` exactly as 05-logging §5).

Also create (not in the inventory row; see Planner notes): ABO's store, `internal/books/store/legacy_journal.go`
(file name coined here), and its test `internal/books/store/legacy_journal_test.go`.

Modify:

- `internal/logger/` — no deletion in this PR. Only if needed, export one helper the books store reuses; keep behaviour.
- `internal/logger/retention.go` — **not deleted here**; add a comment line `// Deleted by PC-12-08 in the S5r1 flip
  (05-logging §11 step 1).` so the coupling is visible. (Deleting it before the journal is wired would leave intermediate
  builds with no `syslog:` pruner.)

## Re-verify greps (identical at `748fd787a` and the plan checkout; under `internal/database/` there, `internal/books/store/` at this wave's base)

```bash
S=internal/books/store
grep -n 'key := fmt.Sprintf("syslog:%s:%s", time.Now().Format(time.RFC3339Nano), source)' $S/pebble_store_activity.go   # expect 1 (HEAD :21)
grep -n "^func (p \*PebbleStore) AddSystemActivityLog\|^func (p \*PebbleStore) GetSystemActivityLogs\|^func (p \*PebbleStore) PruneSystemActivityLogs" $S/pebble_store_activity.go | wc -l   # expect 3
grep -n "type SystemActivityLog struct" $S/*.go | wc -l                         # expect 1 (HEAD store.go:675-682 row shape)
grep -n "AddSystemActivityLog(l.subsystem" internal/logger/standard.go          # expect 1 (StandardLogger's syslog write, HEAD :51)
grep -n "func PruneOldLogs\|store.PruneSystemActivityLogs" internal/logger/retention.go | wc -l   # expect 2
grep -n "^func (w \*Writer) Write(p \[\]byte)" internal/activity/writer.go      # expect 1 (line parser begins, HEAD :149)
grep -rn "logger.PruneOldLogs\|deps.PruneOldLogs" --include=*.go internal | grep -v _test | wc -l   # expect 2 (server_maintenance_deps.go, plugins/maintenance/cleanup.go)
test -d core/log && test -d core/tasks && echo ok                                # PC-03-01, PC-12-01; expect ok
grep -n "type Record struct" core/log/*.go | wc -l                               # expect 1 (05-logging §3 Record, landed with core/log)
test ! -e core/internal/logjournal && test ! -e core/log/journal.go && echo ok    # expect ok
```

## Steps

1. Write the tests first (below).
2. `core/log/journal.go`: copy 05-logging §3:
   `Append(ctx, recs []Record) error` (one durable batch per call), `Query(ctx, q JournalQuery) (JournalPage, error)`
   (newest first), `Prune(ctx, before time.Time, limit int) (deleted int, more bool, err error)`. `log` (L0) imports
   nothing new for it.
3. `logtest.JournalStoreSuite`: `Append` then `Query` round-trips level, module, message, time and attributes for the
   fields the store declares; filters (`MinLevel`, `Module`, `Since`/`Until`, `Text` case-insensitive substring) and
   cursors; `Prune` bounded, oldest first, `more` correct.
4. `core/internal/logjournal`:
   - `Tap` at `core.log.journal.min_level` (default info) handing records to `JournalStore.Append` in batches of at most
     256 records or 1 s, whichever comes first, never blocking the logger (bounded queue `queue_size`, drops counted);
   - native store: one record per `c/log/journal/<ULID>` (ULID from the record time), versioned JSON with attributes
     flattened to dotted keys with string, number or bool values; `Query` and `Prune` are prefix scans and range deletes;
   - prune task `core.log.journal.prune`: `tasks.Sequential("single range-delete per day bucket")`, scheduled by
     `tasks.ScheduleFromConfig("core.log.schedules.journal_prune")`, key declared with type `tasks.ScheduleConfig` and
     default `{"enabled":true,"every":"24h"}`; it calls `Prune` until `more` is false, by `retention_days`;
   - settings sections `core.log.journal` (`Settings{Enabled, MinLevel, RetentionDays, QueueSize}`) and
     `core.log.schedules` (`Schedules{JournalPrune tasks.ScheduleConfig}`).
5. ABO's store (`internal/books/store/legacy_journal.go`): implements `log.JournalStore` over HEAD's `syslog:` family:
   - `Append` writes each record as a row of exactly HEAD's shape (user ID, source, level, message, time; HEAD
     `store.go:675-682` @748fd787a, key `syslog:<RFC3339Nano>:<source>`): `source` = the record's subsystem, or its module
     when it has none; `user_id` = the record's `user_id` attribute; every other attribute, trace and span IDs are
     **not persisted** (A-2: no extension family, no extra member in the row);
   - `Query` returns each row as a `Record` (`Module` = the row's `source`; `Attrs` holds `user_id` when set); the
     `Module` filter matches `source`;
   - `Prune` reuses HEAD's chunked deletion (`pruneExpiredChunked`, HEAD `pebble_store_activity.go:428-434`) by
     `before`, bounded by `limit`.
   - Declared fields for the suite: time, level, source as `Module`, message, `user_id`.
6. Do **not** wire either store into the kernel or `Manifest.LegacyStores.Logs`, and do not delete any ABO writer or
   pruner (PC-12-08 does both). If `legacy.Stores.Logs` does not exist yet (PC-12-01 step 8), add the field here.
7. Acceptance; headers; `changelog.d/` fragment.

## Code contract (exact, 05-logging §3 and §5)

```go
type JournalStore interface {
	Append(ctx context.Context, recs []Record) error
	Query(ctx context.Context, q JournalQuery) (JournalPage, error)
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, more bool, err error)
}
type JournalQuery struct { MinLevel slog.Level; Module string; Since, Until time.Time; Text string; Cursor string; Limit int }
type JournalPage struct { Records []Record; Next string }
// logjournal
type Settings struct { Enabled bool `json:"enabled"`; MinLevel string `json:"min_level" schema:"enum=debug|info|warn|error"`; RetentionDays int `json:"retention_days" schema:"minimum=0"`; QueueSize int `json:"queue_size" reload:"restart" schema:"minimum=64"` }
type Schedules struct { JournalPrune tasks.ScheduleConfig `json:"journal_prune"` }
// legacy (06 §6.4.11)
Logs func(Handles) (log.JournalStore, error)
```

## Tests to write first (05-logging §10 names)

- `logtest.JournalStoreSuite` (exported contract) — run by the native store and by ABO's store with HEAD fixtures.
- `TestNativeJournalStore_RoundTrip` — the native store over `c/log/journal/` passes the suite.
- `TestJournal_BatchAndRetention` — batching bounds; the prune task calls `Prune` until `more` is false by
  `retention_days`; no `core/log` migration step or level stamp exists.
- `TestJournal_PruneScheduleKeyDeclared` — `core.log.schedules.journal_prune` is declared with type `tasks.ScheduleConfig`
  and accepted by `ScheduleFromConfig` at registration.
- `TestTap_NeverBlocks` (synctest: stalled journal store, 10k logs complete, drops counted) for the journal tap.
- `TestJournal_ReadsLegacySyslog` — moves into the ABO store's suite instance (`legacy_journal_test.go`): a HEAD-written
  `syslog:` row reads back as a `Record`; a record appended by the store is read by HEAD's `GetSystemActivityLogs`
  unchanged (row 6 of 09 §9.2.1: an older build reads journal rows as its own system log).

## Acceptance

```bash
(cd core && go build ./... && go vet ./log/... ./internal/logjournal/...)                  # exit 0
(cd core && go test -race ./log/... ./internal/logjournal/... -count=1)                     # all ok
(cd core && go test ./internal/logjournal/... -run 'TestNativeJournalStore_RoundTrip|TestJournal_BatchAndRetention|TestJournal_PruneScheduleKeyDeclared' -count=1 -v | grep -c '^--- PASS')   # expect 3
go test ./internal/books/store/ -run 'TestJournal_ReadsLegacySyslog|JournalStoreSuite' -count=1 -v | grep -c '^--- PASS'   # expect >= 2
grep -rn 'syslog:' core --include=*.go | wc -l                                              # expect 0 (no ABO key in core)
git diff --stat origin/main -- internal/activity internal/logger/standard.go internal/server internal/plugins | tail -1   # expect empty (no ABO writer removed yet)
make -C core ci            # exit 0
make ci                    # exit 0
```

## Out of scope

- Wiring `Manifest.LegacyStores.Logs`, deleting the activity line parser and tap, `StandardLogger`'s syslog writes,
  `internal/logger/retention.go` and its two callers (PC-12-08, same release).
- The native query route `GET /api/core/v1/system/logs`, the legacy `/api/v1/system/logs` alias and the live-tail topic
  `core.log.recorded.v1` (see Planner notes).
- `operationlog:` pruning (it moves to the run store's `Prune`, PC-12-05/PC-12-08).

## Rollback

Revert the PR. Nothing writes through the journal before PC-12-08.

## Repo rules

- File headers on every new or changed file; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S5 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`
  after required checks and OWNER-12's row; never squash.
- CLAUDE.md concurrency: the tap is one bounded queue and one flusher goroutine per App; no per-record goroutine.

## Planner notes

- The row puts the removal of `internal/logger/retention.go` in this task, but this task lands dark and the journal is
  wired only at the flip. Removing the pruner, the activity line parser and `StandardLogger`'s writes here would ship
  intermediate builds (an RC per push, 09 §9.1) with no `syslog:` writer or pruner. This brief moves every removal to
  PC-12-08; that PR also needs `internal/activity/writer.go`, `internal/logger/standard.go`, `internal/logger/retention.go`,
  `internal/server/server_maintenance_deps.go` and `internal/plugins/maintenance/cleanup.go`/`deps.go` (the two
  `PruneOldLogs` callers) in its file list.
- ABO's `JournalStore` (books code over `syslog:`) has no file in the row; this brief creates it under
  `internal/books/store/`.
- No 09 stage table names when `GET /api/core/v1/system/logs`, the legacy `/api/v1/system/logs` alias and the
  `core.log.recorded.v1` live tail land (the tail needs S6's hub). This brief leaves them out; the plan should assign them.
