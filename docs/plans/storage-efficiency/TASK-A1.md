<!-- file: docs/plans/storage-efficiency/TASK-A1.md -->
<!-- version: 1.2.0 -->
<!-- guid: 64eab7cd-1fed-411b-bff5-1946bf95ffa0 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A1: Export Pebble engine metrics to Prometheus

Wave W1. Start only after PR #3704 has merged to `main`: it edits
`internal/metrics/metrics.go`, which step 2 edits (plan P-1). Runs in
parallel with A2, A4, A5 and A6. Model: sonnet. Reviewer: code-reviewer.

## 1. Goal and why

**Goal.** Publish `db.Metrics()` for the main Pebble store and the
OpenLibrary store on the existing `/metrics` endpoint. Read the numbers at
scrape time. Change no store behaviour.

**Why.** Pebble runs on library defaults: an 8 MB block cache against a
50.6 GB store with 174,165,047 keys, no bloom filters, and one compaction at a
time (eval R1, R2, F2). A later task (A7) changes those settings one deploy at
a time, and each change needs a metric that shows whether it worked: cache hit
rate, read amplification, L0 sublevels, compaction debt, and write
amplification per level (the plan records it before and after each setting,
and the design defers `LBaseMaxBytes` and target file sizes until it is
measured). None of these is
exported today, so there is no baseline. The plan requires 24 hours of these
metrics before the first setting changes.

## 2. ⛔ START HERE (run this first)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
gh pr view 3704 --json state -q .state   # must print MERGED; if not, stop
git worktree add ../aorg-storage-a1-pebble-metrics -b feat/storage-a1-pebble-metrics origin/main
cd ../aorg-storage-a1-pebble-metrics
npm ci --prefix web
```

- Do NOT run `go work init`. It breaks the build in this repo.
- Do NOT spawn subagents.
- Never edit the primary checkout. Work only in the worktree above.
- Commit work in progress at least every 15 minutes, and push it to your own
  branch (step 10 gives the push form).
- Next, run the "already done if" check in `## Idempotency / Rollback` below.
  If it says the work exists, stop and report instead of redoing it.

## Idempotency / Rollback

Already done if both of these hold (run from the worktree root):

```bash
test -f internal/metrics/pebble_collector.go && echo "collector file present"
grep -c 'registerPebbleMetricsSources' internal/server/server.go   # prints 1 or more when done
```

Both present: stop and report "already done". Only one present: a previous
attempt stopped half way; finish the missing step instead of starting over.

Rollback: before the first push, remove the worktree and branch
(`git worktree remove ../aorg-storage-a1-pebble-metrics --force`, then
`git branch -D feat/storage-a1-pebble-metrics`), or `git reset --hard
origin/main` inside it. After the PR merges, `git revert` the merge; the task
adds files and one line in `server.go` and changes no stored data.

## 3. Read before editing

- `internal/database/compaction_stats.go`, the whole file. It maps
  `pebble.Metrics` fields to a struct. Copy its field paths. The comment at
  `:10-15` explains the dependency rule: `internal/database` is in the plugin
  SDK's dependency closure (`make sdkguard`).
- `internal/metrics/metrics.go:14-30` and `:303-314`. Naming convention
  (`Namespace: "audiobook_organizer"`) and the `Register()` once-guard.
- `internal/metafetch/openlibrary.go:71-88`. `OpenLibraryService.CompactionStats`
  reads the OL store under `svc.Mu`, because the delete and factory-reset
  handlers close the store and nil it while holding that lock. Your sampler
  must do the same.
- `internal/openlibrary/store.go:26-56`. `OLStore` and its `CompactionStats`.
- `internal/database/store_capability.go:58-107`. `AsCapability` walks the
  search-index decorator. A bare type assertion on `Server.Ops()` fails in
  prod.
- Pebble v2.1.7 source, `$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7/metrics.go:210-300`
  and `:450-475`. The `Metrics` struct, `MemTable`, and `WAL`.

## 4. Re-verify anchors

Run each command from the worktree root and confirm the expected hit before
editing. If a line number moved, use the new one. If a hit is gone, stop and
report it. The line numbers below were re-run on `origin/main` at
`373ba19d2`; every file:line cited in sections 3 and 5 comes from this list or
from that commit, and where they differ the grep result wins.

1. `grep -n 'func CollectCompactionStats' internal/database/compaction_stats.go`
   → `93:func CollectCompactionStats(db *pebble.DB) CompactionStats {`
2. `grep -n 'internal/database is in the plugin SDK' internal/database/compaction_stats.go`
   → `12:// other way: internal/database is in the plugin SDK's dependency closure`
3. `go list -f '{{join .Imports "\n"}}' ./internal/database | grep -n 'internal/metrics$'`
   → one line naming `internal/metrics`. `internal/database` ALREADY imports
   `internal/metrics`, so `internal/metrics` must never import
   `internal/database` (that would be an import cycle).
4. `grep -n 'internal/metrics$' tools/cmd/sdkguard/internal-deps.txt`
   → `26:github.com/falkcorp/audiobook-organizer/internal/metrics`. Both
   packages are already in the SDK closure. Adding code to them adds no new
   closure entry, as long as you import nothing new from `internal/`.
5. `grep -n 'func Register()' internal/metrics/metrics.go` → `303:func Register() {`
6. `grep -n 'metrics.Register()' internal/server/server.go` → `554:	metrics.Register()`
7. `grep -n 'func (svc \*OpenLibraryService) CompactionStats' internal/metafetch/openlibrary.go`
   → `78:`
8. `grep -n 'func (s \*OLStore) CompactionStats' internal/openlibrary/store.go` → `55:`
9. `grep -n 'func AsCapability' internal/database/store_capability.go` → `90:`
10. `grep -n 's.olService = ol' internal/server/registry_wire.go` → `399:`
11. `P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7; grep -n '^func (d \*DB) Metrics() \*Metrics' $P/db.go`
    → `2068:func (d *DB) Metrics() *Metrics {`
12. `grep -n 'BlockCache CacheMetrics\|Filter FilterMetrics\|Levels \[numLevels\]LevelMetrics\|^	MemTable struct\|^	WAL struct' $P/metrics.go`
    → `211`, `283`, `285`, `287`, `454`.
13. `grep -n '^func (m \*Metrics) ReadAmp\|^func (m \*Metrics) DiskSpaceUsage' $P/metrics.go`
    → `514` (DiskSpaceUsage), `556` (ReadAmp).
14. `grep -n '^	TableBytesIn uint64\|^	TableBytesFlushed uint64\|^	TableBytesCompacted uint64\|^	BlobBytesFlushed uint64\|^	BlobBytesCompacted uint64\|^func (m \*LevelMetrics) WriteAmp' $P/metrics.go`
    → `76`, `89`, `93`, `112`, `116`, `192`. `WriteAmp()` is
    (TableBytesFlushed + TableBytesCompacted + BlobBytesFlushed +
    BlobBytesCompacted) / TableBytesIn, and 0 when TableBytesIn is 0. For L0,
    TableBytesIn is the bytes written to the WAL.

15. `grep -n 'server.container = regContainer' internal/server/server.go`
    → `689:	server.container = regContainer` (step 7 inserts after this line).
16. `grep -n 'func recoverPebbleClosed(op string, errp \*error)' internal/database/pebble_store_ops_v2.go`
    → `1103:`. It is a `defer` helper that takes a pointer to the named error
    return.
17. `grep -n 'ticker := time.NewTicker(5 \* time.Second)' internal/server/server_lifecycle.go`
    → `298:`. This is the ticker precedent the decision in section 5 declines
    to copy (the block runs `:298-345`).
18. `grep -n 'func NewAIScanStoreFromDB\|prefix: "aiscan:", owned: false' internal/database/ai_scan_store.go`
    → `128:` and `129:`. The shared-DB flag that step 8 cites.
19. `grep -n 'Namespace: "audiobook_organizer"' internal/metrics/metrics.go | head -1`
    → `19:` (the naming convention).
21. `grep -n 'prometheus.MustRegister(operationStarted' internal/metrics/metrics.go`
    → `305:` (the first `MustRegister` call that step 2 edits).
20. `grep -n 'Cheap: Metrics() reads in-memory' internal/database/compaction_stats.go`
    → `91:` (the comment step 1's decision cites as "`compaction_stats.go:91-92`").

Pebble v2.1.7 field paths, verified. `BlockCache` is `cache.Metrics`
(`internal/cache/cache.go:23`).

| Value | Field path |
|---|---|
| block cache bytes / blocks / hits / misses | `m.BlockCache.Size`, `.Count`, `.Hits`, `.Misses` (all int64) |
| bloom filter hits / misses | `m.Filter.Hits`, `m.Filter.Misses` (int64) |
| read amplification | `m.ReadAmp()` (int) |
| L0 files / sublevels | `m.Levels[0].TablesCount` (int64), `m.Levels[0].Sublevels` (int32) |
| compactions total | `m.Compact.Count` (int64) |
| compaction debt | `m.Compact.EstimatedDebt` (uint64) |
| compactions in progress, bytes | `m.Compact.NumInProgress`, `m.Compact.InProgressBytes` (int64) |
| memtable bytes / count | `m.MemTable.Size` (uint64), `m.MemTable.Count` (int64) |
| WAL bytes / physical bytes / files | `m.WAL.Size`, `m.WAL.PhysicalSize` (uint64), `m.WAL.Files` (int64) |
| disk usage | `m.DiskSpaceUsage()` (uint64) |
| per-level bytes / files | `m.Levels[i].TablesSize`, `m.Levels[i].TablesCount` (int64), i = 0..6 |
| per-level bytes in | `m.Levels[i].TableBytesIn` (uint64) |
| per-level bytes flushed | `m.Levels[i].TableBytesFlushed + m.Levels[i].BlobBytesFlushed` (uint64) |
| per-level bytes compacted | `m.Levels[i].TableBytesCompacted + m.Levels[i].BlobBytesCompacted` (uint64) |
| per-level write amplification | `m.Levels[i].WriteAmp()` (float64, cumulative since open) |

## 5. Steps

Decision: **a custom `prometheus.Collector` that reads the sources at scrape
time.** The design left the choice between scrape-time and a ticker to the
precedent in `internal/metrics`. That package has no custom collector; its
gauges are set by a ticker in `internal/server/server_lifecycle.go:298-345`.
Do not follow that precedent here, for three reasons:

- A5 edits `server_lifecycle.go` in the same wave.
- Several values are cumulative counters (hits, misses, compactions). A ticker
  would have to republish them as gauges, which is the wrong Prometheus type.
- `Metrics()` is cheap: it reads in-memory counters
  (`compaction_stats.go:91-92`).

1. **`internal/metrics/pebble_collector.go` (new).** Import only the standard
   library and `github.com/prometheus/client_golang/prometheus`.
   - `type PebbleSample struct` with these fields: `BlockCacheBytes`,
     `BlockCacheBlocks`, `BlockCacheHits`, `BlockCacheMisses`, `FilterHits`,
     `FilterMisses`, `ReadAmp`, `L0Files`, `L0Sublevels`, `Compactions`,
     `CompactionDebtBytes`, `CompactionsInProgress`,
     `CompactionInProgressBytes`, `MemTableBytes`, `MemTables`, `WALBytes`,
     `WALPhysicalBytes`, `WALFiles`, `DiskUsageBytes`, `LevelBytes [7]float64`,
     `LevelFiles [7]float64`, `LevelBytesIn [7]float64`,
     `LevelBytesFlushed [7]float64`, `LevelBytesCompacted [7]float64`,
     `LevelWriteAmp [7]float64`. Every field is `float64` (the arrays too), so
     the collector passes values straight to `MustNewConstMetric` with no
     conversions.
   - `type PebbleSource func() (PebbleSample, bool)`. `ok == false` means "no
     database right now" (closed, removed, not opened). The collector then
     emits nothing for that store. It never emits zeros for it.
   - `func SetPebbleSource(store string, src PebbleSource)`. Store the source
     in a `map[string]PebbleSource` under a `sync.RWMutex`. Passing `nil`
     deletes the entry. Replacing an existing entry is allowed and expected:
     `NewServer` runs many times inside one test binary.
   - `pebbleCollector` implements `Describe` and `Collect`. In `Collect`, copy
     the map under the read lock, sort the store names, and call each source.
     Wrap each source call in `func() { defer recover() ... }()`, so a
     panicking source drops only its own store. Emit with
     `prometheus.MustNewConstMetric`.
   - Metric names. Every name carries `Namespace: "audiobook_organizer"`, and
     every metric has the label `store`. The per-level metrics also carry
     `level` ("0" to "6").
     - Gauges: `pebble_block_cache_bytes`, `pebble_block_cache_blocks`,
       `pebble_read_amplification`, `pebble_l0_files`, `pebble_l0_sublevels`,
       `pebble_compaction_debt_bytes`, `pebble_compactions_in_progress`,
       `pebble_compaction_in_progress_bytes`, `pebble_memtable_bytes`,
       `pebble_memtables`, `pebble_wal_bytes`, `pebble_wal_physical_bytes`,
       `pebble_wal_files`, `pebble_disk_usage_bytes`, `pebble_level_bytes`,
       `pebble_level_files`, `pebble_level_write_amplification`
       (cumulative since the store opened).
     - Counters (`prometheus.CounterValue`): `pebble_block_cache_hits_total`,
       `pebble_block_cache_misses_total`, `pebble_filter_hits_total`,
       `pebble_filter_misses_total`, `pebble_compactions_total`,
       `pebble_level_bytes_in_total`, `pebble_level_bytes_flushed_total`,
       `pebble_level_bytes_compacted_total`. The three per-level counters are
       what the plan's write-amplification readings use: over a window, a
       level's write amplification is (rate of flushed + rate of compacted) /
       rate of bytes in, and the whole store's is the sum over levels of
       flushed + compacted divided by L0's bytes in. Say this in their help
       text.
     - Help text says where the value comes from, for example "Pebble
       Metrics().BlockCache.Hits, cumulative since the store opened".
   - Store label values are exactly `main` and `openlibrary`. Export them as
     constants `PebbleStoreMain` and `PebbleStoreOpenLibrary`.
2. **`internal/metrics/metrics.go`.** Add the package-level collector value
   `pebbleCollectorInstance` to the first `prometheus.MustRegister(...)` call
   in `Register()` (`:305`). It stays inside the existing `registerOnce`, so
   it registers once per process.
3. **`internal/database/pebble_metrics_export.go` (new).**
   - `func PebbleSampleFromDB(db *pebble.DB) (s metrics.PebbleSample, ok bool)`.
     Return `false` for a nil db. Recover a panic whose value is an error
     matching `errors.Is(err, pebble.ErrClosed)`, and return `false`. Re-panic
     anything else, the same way `recoverPebbleClosed` does
     (`pebble_store_ops_v2.go:1103`). Fill every field from the table in
     section 4.
   - `type PebbleMetricsSampler interface { PebbleMetricsSample() (metrics.PebbleSample, bool) }`.
   - `func (p *PebbleStore) PebbleMetricsSample() (metrics.PebbleSample, bool) { return PebbleSampleFromDB(p.db) }`.
     The method goes in this new file. Do NOT edit `pebble_store.go` (A4 owns
     it in this wave).
   - `var _ PebbleMetricsSampler = (*PebbleStore)(nil)`.
4. **`internal/openlibrary/store.go`.** Add
   `func (s *OLStore) PebbleMetricsSample() (metrics.PebbleSample, bool) { return database.PebbleSampleFromDB(s.db) }`
   below `CompactionStats` (`:55`). Do not touch `NewOLStore` or `Optimize`.
5. **`internal/metafetch/openlibrary.go`.** Add
   `func (svc *OpenLibraryService) PebbleMetricsSample() (metrics.PebbleSample, bool)`.
   Copy the shape of `CompactionStats` (`:78`): take `svc.Mu`, return `false`
   when `svc.OLStore == nil`, otherwise delegate.
6. **`internal/server/pebble_metrics_sources.go` (new).** Add
   `func (s *Server) registerPebbleMetricsSources()`. It calls:
   - `metrics.SetPebbleSource(metrics.PebbleStoreMain, func() (metrics.PebbleSample, bool) { ... })`.
     The closure resolves `database.AsCapability[database.PebbleMetricsSampler](s.Ops())`
     on every call, and returns `false` when that fails.
   - `metrics.SetPebbleSource(metrics.PebbleStoreOpenLibrary, ...)`. The
     closure returns `false` when `s.olService == nil`, otherwise
     `s.olService.PebbleMetricsSample()`.
7. **`internal/server/server.go`.** Add one line,
   `server.registerPebbleMetricsSources()`, right after
   `server.container = regContainer` (`:689`, anchor 15). Nothing else in this file.
8. Do NOT export the AI-scan store. On prod it shares the main DB
   (`ai_scan_store.go:129`, `NewAIScanStoreFromDB`), so its numbers would
   duplicate `store="main"`.
9. Note for A7, written as a comment above the cache fields in
   `pebble_collector.go`: once A7 shares one block cache between `main` and
   `openlibrary`, the two `pebble_block_cache_*` series report the same
   cache. Do not sum them.

## 6. Do not touch

- `internal/server/server_lifecycle.go` (A5), `internal/database/pebble_store.go`
  (A4), `internal/database/pebble_store_ops_v2.go` (A5),
  `internal/server/handlers/diagnostics.go` and `wire_media_routes.go` (A2),
  `internal/operations/registry/` (A6).
- `database.Store`, any `internal/database/iface_*.go`,
  `internal/database/mocks/`. Do not widen an interface. Do not regenerate
  mocks.
- Store options (cache size, bloom filters). That is A7.
- `internal/database/compaction_stats.go`. Reuse its field paths. Do not
  change it.

## 7. Tests

- `internal/metrics/pebble_collector_test.go`:
  - `TestPebbleCollector_EmitsAllSeriesPerStore`. Register a fake source for
    `main` that returns fixed values. Gather from a fresh
    `prometheus.NewRegistry()` that holds only the collector. Assert:
    - every metric name listed in step 1 is present with `store="main"`;
    - the counters have type COUNTER;
    - `pebble_level_bytes` has 7 series, and so does each of the three
      per-level byte counters and `pebble_level_write_amplification`.
  - `TestPebbleCollector_SourceNotOKEmitsNothing`. A source that returns
    `false`. Assert zero series for that store.
  - `TestPebbleCollector_PanickingSourceIsolated`. One source panics, one
    works. Assert the working store's series are present and `Gather` returns
    no error.
  - `TestPebbleCollector_SetPebbleSourceReplacesAndNilDeletes`.
- `internal/database/pebble_metrics_export_test.go`:
  - `TestPebbleSampleFromDB_RealStore`. Use `NewPebbleStoreInMemory`, write
    1,000 keys, call `p.db.Flush()`, sample. Assert `ok`,
    `DiskUsageBytes > 0`, `MemTables >= 1`, that the sum of `LevelFiles`
    is `>= 1`, and that `LevelBytesFlushed[0] > 0` and `LevelBytesIn[0] > 0`.
  - `TestPebbleSampleFromDB_ClosedStore`. Close the store, then sample. Assert
    `ok == false` and no panic.
  - `TestPebbleSampleFromDB_Nil`. Assert `ok == false`.
- Existing tests must still pass. `internal/metrics/metrics_test.go` checks
  that `Register()` registers its collectors. Run it.
- Anti-over-suppression: N/A. A1 only reads and exports; it rejects, skips or
  hides nothing. The one path that emits nothing (a source returning `false`)
  is bounded by `TestPebbleCollector_EmitsAllSeriesPerStore`, which fails if a
  healthy source emits no series, and by `TestPebbleSampleFromDB_RealStore`,
  which fails if a live store samples as `ok == false`.

## 8. Verify

```bash
go build ./...
go vet ./internal/metrics/... ./internal/database/... ./internal/openlibrary/... ./internal/metafetch/... ./internal/server/...
go test -race -count=1 ./internal/metrics/...
go test -race -count=1 -run 'PebbleSample' ./internal/database/
go test -race -count=1 ./internal/openlibrary/... ./internal/metafetch/...
go test -race -count=1 ./internal/server/
make sdkguard
make lint-errcheck-ratchet
make lint-width
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required. Run it anyway if `make lint-width` reports anything.

Manual check: `make run-api`, then
`curl -sk https://localhost:8484/metrics | grep audiobook_organizer_pebble_ | head`.
The `store="main"` series must appear.

## 9. Deliverables

- Version header on every new file (Go header format: `// file:`,
  `// version: 1.0.0`, `// guid:` from `uuidgen | tr A-Z a-z`,
  `// last-edited:`). Bump the patch or minor version and `last-edited` on
  every edited file: `metrics.go`, `openlibrary/store.go`,
  `metafetch/openlibrary.go`, `server.go`.
- Changelog fragment `changelog.d/<YYYYMMDD>_storage_a1_pebble_metrics.md`,
  with NO header. Category `### Added`. One `####` entry naming the metrics
  and the `store` label. Never use a `#` or `##` heading.
- No internal host addresses and no API-key-shaped strings in any committed file. Check
  with `git diff origin/main --stat` and
  `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"`, which must print
  nothing.
- Commit with exactly `feat(metrics): export Pebble engine metrics for main and OpenLibrary stores`
  (`<type>(<scope>): ...` form), and end the message with:

  ```
  Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- Push by explicit sha (zsh):
  `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/feat/storage-a1-pebble-metrics"`.
- Open a PR with `gh pr create --base main --head feat/storage-a1-pebble-metrics`.
  End the body with the generated-by line the session provides. Do NOT merge.

## 10. Exit criteria and report

Exit criteria, each checkable:

- [ ] `/metrics` on a local run shows `audiobook_organizer_pebble_block_cache_hits_total{store="main"}`
      and `audiobook_organizer_pebble_level_bytes_compacted_total{store="main",level="6"}`.
- [ ] All tests in section 7 exist and pass with `-race`.
- [ ] `make sdkguard`, `make lint-errcheck-ratchet` and `make lint-width` pass.
- [ ] `git diff origin/main --name-only` lists only the files in steps 1-7,
      the tests, and the fragment.
- [ ] PR open, not merged.

Report format:

```
TASK-A1 report
head sha: <sha>
PR: <url>
files changed: <list>
tests: <name> PASS (<time>) ... ; packages: <pkg> ok (<time>) ...
make sdkguard / lint-errcheck-ratchet / lint-width: <result each>
not done / deviations: <list or "none">
```
