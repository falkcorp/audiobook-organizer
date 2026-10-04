<!-- file: docs/plans/storage-efficiency/TASK-A7.md -->
<!-- version: 1.0.0 -->
<!-- guid: cb342533-a38a-4e11-a78c-49b2f54cecaf -->
<!-- last-edited: 2026-10-03 -->

# TASK-A7: Pebble store-open settings from the environment; shared block cache

Wave W3. Start only after A1 (Pebble metrics), A3 (db-health census) and A4
(format stamp) have merged to `main`. A3 and A7 both edit
`internal/database/pebble_store.go` and `ai_scan_store.go`, so they cannot
run in parallel. Model: sonnet. Reviewer: code-reviewer.

## 1. Goal and why

**Goal.**
- Read the Pebble open options for the main store from environment variables
  (`AORG_PEBBLE_*`): block cache size, bloom filter bits, memtable size,
  compaction concurrency, and parallel manual compaction.
- When a cache size is set, the main and OpenLibrary stores share one block
  cache.
- Log the effective values at every open.
- With no variable set, behaviour is exactly today's. This PR changes nothing
  on prod until the operator sets a variable.

**Why.** Every Pebble knob is at its library default (eval R2): an 8 MB block
cache against 50.6 GB, a 4 MB memtable, no bloom filters, and one compaction
at a time. The manual full compaction runs serially (`parallelize=false`,
`pebble_store.go:5276`), so a full compaction takes 28.5 minutes at
31-52 MiB/s on an NVMe (R3, F2).

The settings must come from the environment: the app's saved settings live
inside the store they would configure (design 8, `pebble_store.go:392-397`).

The operator applies one setting per deploy, each with the A1 metric it
should move:
- block cache 4 GB → `pebble_block_cache_hits_total` ratio;
- bloom 10 → `pebble_filter_hits_total`;
- memtable 64 MB → `pebble_l0_sublevels`;
- compaction 1-4 with parallel manual compaction →
  `pebble_compaction_debt_bytes` and the db-optimize duration.

## 2. Setup

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git log --oneline origin/main | grep -m3 -iE 'pebble engine metrics|db-health|storage_format'   # expect A1, A3, A4; if any is missing, stop
git worktree add ../aorg-storage-a7-pebble-env-settings -b feat/storage-a7-pebble-env-settings origin/main
cd ../aorg-storage-a7-pebble-env-settings
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.

## 3. Read before editing

- `internal/database/pebble_store.go:389-410`. `newPebbleStore`: where
  `opts` is built and opened. After A4 it also holds the storage-format check
  and sidecar. Do not disturb those.
- `internal/database/pebble_store.go:5250-5280`. The `Optimize` doc comment
  and body.
- `internal/database/ai_scan_store.go:100-180`. `NewAIScanStore` (owned)
  and `Optimize`.
- `internal/openlibrary/store.go:31-51`. `NewOLStore` and `Optimize`.
- `internal/plugins/maintenance/db.go:41-100`. `runDBOptimizeSteps` and its
  first progress line.
- Pebble v2.1.7 (`P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7`):
  - `$P/cache.go:9-23`: `NewCache` and the reference-count contract;
  - `$P/options.go:385-425`: `LevelOptions`, `FilterPolicy`;
  - `:495-505`: `Cache`, `CacheSize`;
  - `:892-956`: `MemTableSize`, `CompactionConcurrencyRange`;
  - `:2380-2390`: the memtable size limit;
  - `$P/bloom/bloom.go:214-222`: `FilterPolicy`;
  - `$P/db.go:1825`: `Compact`.
- `deploy/audiobook-organizer.service:60-90`. The Environment lines,
  `GOMEMLIMIT=9GiB`, `MemoryMax=12G`.
- `docs/configuration.md:50-80`. The environment variable table.

## 4. Re-verify anchors

1. `P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7; grep -n '^	Cache \*cache.Cache\|^	CacheSize int64\|^	MemTableSize uint64\|^	CompactionConcurrencyRange func() (lower, upper int)\|^	Levels \[manifest.NumLevels\]LevelOptions\|^	FilterPolicy FilterPolicy' $P/options.go`
   → `420 FilterPolicy FilterPolicy` (in `LevelOptions`),
   `502 Cache *cache.Cache`, `504 CacheSize int64`,
   `867 Levels [manifest.NumLevels]LevelOptions` (values, not pointers),
   `900 MemTableSize uint64`,
   `956 CompactionConcurrencyRange func() (lower, upper int)`.
2. `grep -n '^func NewCache' $P/cache.go` → `21: func NewCache(size int64) *cache.Cache`.
   It is created with a reference count of 1; each DB opened with it adds
   one, and `DB.Close` releases it.
3. `grep -n '^type FilterPolicy int' $P/bloom/bloom.go` → `218:`. Use
   `bloom.FilterPolicy(n)` from package `github.com/cockroachdb/pebble/v2/bloom`.
4. `grep -n '^func (d \*DB) Compact(' $P/db.go`
   → `1825: func (d *DB) Compact(ctx context.Context, start, end []byte, parallelize bool) error`.
5. `grep -n 'maxMemTableSize\b' $P/open.go | head -2` →
   `62: maxMemTableSize = constants.MaxUint32OrInt`. `MemTableSize` must be
   below 4 GiB.
6. `grep -n 'return p.db.Compact(ctx, nil, \[\]byte{0xff}, false)' internal/database/pebble_store.go` → `5276:`
7. `grep -n 'return s.db.Compact(ctx, nil, \[\]byte{0xff}, false)' internal/database/ai_scan_store.go internal/openlibrary/store.go`
   → `ai_scan_store.go:175`, `openlibrary/store.go:50`.
8. `grep -n 'opts := &pebble.Options{FormatMajorVersion: pebble.FormatNewest}' internal/database/pebble_store.go` → `393:`
9. `grep -n 'db, err := pebble.Open(path, &pebble.Options{' internal/openlibrary/store.go internal/database/ai_scan_store.go`
   → `openlibrary/store.go:33`, `ai_scan_store.go:108`.
10. `grep -n 'Compacting main database (Pebble, full keyspace)' internal/plugins/maintenance/db.go` → `69:`
11. `grep -n 'MemoryMax=12G\|GOMEMLIMIT=9GiB' deploy/audiobook-organizer.service` → `73`, `88`.
12. `grep -n 'DATABASE_PATH' docs/configuration.md` → `59:`

Line numbers in `pebble_store.go` and `ai_scan_store.go` will have shifted
after A3 and A4 merged. Re-grep and use the current lines.

## 5. Steps

1. **`internal/database/pebble_settings.go` (new).**
   - The environment variables. Each defaults to "unset", which means "leave
     the Pebble default", which is today's behaviour:

     | Variable | Type and range | Effect when set |
     |---|---|---|
     | `AORG_PEBBLE_CACHE_BYTES` | int64, > 0, plain bytes (for example `4294967296`) | One shared `pebble.NewCache(n)` for the main and OpenLibrary stores (`Options.Cache`). Unset: each store gets Pebble's own 8 MB cache, as today. |
     | `AORG_PEBBLE_BLOOM_BITS_PER_KEY` | int, 1..32 | `opts.Levels[i].FilterPolicy = bloom.FilterPolicy(n)` for i = 0..6, main store only. Applies to sstables written from then on. Old tables stay readable whether or not this is set. |
     | `AORG_PEBBLE_MEMTABLE_BYTES` | uint64, from 1 MiB to below 4 GiB | `opts.MemTableSize`, main store only. |
     | `AORG_PEBBLE_COMPACTION_CONCURRENCY` | `"N"` or `"L-U"`, 1 ≤ L ≤ U ≤ 64 | `opts.CompactionConcurrencyRange = func() (int, int) { return L, U }`, main store only. |
     | `AORG_PEBBLE_PARALLEL_MANUAL_COMPACTION` | `strconv.ParseBool` | The `parallelize` argument of `Compact` in every `Optimize` (main, owned AI-scan, OpenLibrary). |

   - `type PebbleSettings struct { CacheBytes int64; BloomBitsPerKey int; MemTableBytes uint64; CompactionLower, CompactionUpper int; ParallelManualCompaction bool }`.
     Zero values mean unset.
   - `func pebbleSettingsFrom(getenv func(string) string) (PebbleSettings, error)`
     and `func PebbleSettingsFromEnv() (PebbleSettings, error) { return pebbleSettingsFrom(os.Getenv) }`.
     **An invalid or out-of-range value is an error that names the variable
     and the bad value. It never silently falls back to the default:** an
     operator who typed `4G` must learn that it was ignored.
   - `func (s PebbleSettings) ApplyMain(opts *pebble.Options)`. Applies all
     the options above.
   - `func (s PebbleSettings) ApplyShared(opts *pebble.Options)`. Applies the
     shared cache only, for the OpenLibrary store.
   - `func sharedPebbleCache(size int64) *pebble.Cache`. Created once per
     process (`sync.Once`), and the creator's reference is kept for the
     process lifetime, so the cache outlives an OpenLibrary store being
     closed and reopened (`metafetch/openlibrary.go` EnsureStore and the
     delete handler). If the store's size differs from the first call, log a
     Warn and keep the first.
   - `func (s PebbleSettings) LogEffective(store string, opts *pebble.Options)`.
     One `slog.Info("pebble store options", ...)` with:
     - `store`;
     - `cache_bytes`: the shared size, or `8388608` with `cache_shared=false`;
     - `bloom_bits_per_key` (0 = none);
     - `memtable_bytes`: the effective value, 4194304 when unset;
     - `compaction_concurrency`: `"1-1"` when unset;
     - `parallel_manual_compaction`.

     Values are read back from `opts` after `EnsureDefaults` when possible.
     Otherwise log the documented defaults (eval R2).
   - `func ParallelManualCompaction() bool`. Parses the environment and
     returns false on an error. That error was already reported at open,
     because the main store's open fails on it.
2. **Main store (`pebble_store.go`, `newPebbleStore`).** Right after `opts`
   is built (`:393`), call `settings, err := PebbleSettingsFromEnv()`. On
   error, return `fmt.Errorf("pebble settings: %w", err)` before opening.
   Then call `settings.ApplyMain(opts)`, keep the test `FS` override, open,
   and call `settings.LogEffective("main", opts)`.
3. **OpenLibrary (`openlibrary/store.go`, `NewOLStore`).** Parse the
   settings the same way; on error, return it. The caller already logs OL
   open failures (`metafetch/openlibrary.go:57`). Call `ApplyShared` on the
   options, open, then `LogEffective("openlibrary", ...)`.
4. **AI-scan store (`ai_scan_store.go`, `NewAIScanStore`).** Do not change
   its open options. On prod it is not opened at all: it shares the main DB
   (`NewAIScanStoreFromDB`). Only `Optimize` changes (step 5).
5. **Parallel manual compaction.** In all three `Optimize` methods
   (`pebble_store.go` `:5276`, `ai_scan_store.go` `:175`,
   `openlibrary/store.go` `:50`), replace the literal `false` with
   `ParallelManualCompaction()`. In `pebble_store.go`, extend the `Optimize`
   doc comment: `parallelize=true` lets Pebble split the manual compaction
   across the concurrency range, while `false` runs one whole-range
   compaction per level (eval F2).
6. **`internal/plugins/maintenance/db.go:69`.** Append the setting to the
   first log line, for example `... engine counters are logged every %s; parallel manual compaction=%t`,
   using `database.ParallelManualCompaction()`. Nothing else in this file.
7. **Docs.**
   - `deploy/audiobook-organizer.service`. Below the `GOGC` line, add a
     commented block listing the five variables, each with a commented-out
     example line (for example `# Environment="AORG_PEBBLE_CACHE_BYTES=4294967296"`).
     Add this warning, verbatim in substance: the block cache is allocated
     outside the Go heap when cgo is enabled, and inside it when cgo is
     disabled. Either way it counts toward `MemoryMax=12G`. With app RSS at
     about 8.5 GB (design 8), a 4 GiB cache needs `MemoryMax` raised first,
     and needs `GOMEMLIMIT` reviewed. Bump the header.
   - `docs/configuration.md`. Add a section "Pebble store settings
     (environment only)". It contains:
     - the table above;
     - why these are not in the saved settings (they live in the store they
       configure);
     - the one-setting-per-deploy procedure, with the metric each should
       move (section 1);
     - the `MemoryMax` warning;
     - that bloom filters apply only to newly written sstables until a full
       compaction rewrites the rest;
     - that once the cache is shared, A1's `pebble_block_cache_*` series for
       `store="main"` and `store="openlibrary"` report the same cache and
       must not be summed.

     Bump the header.

## 6. Do not touch

- The storage-format check and sidecar code from A4 in `newPebbleStore`.
- `CountPrefix`, `ScanPrefix` and the census from A2 and A3.
- Compression settings: unchanged by design (ZFS already applies zstd).
- `LBaseMaxBytes` and the target file sizes. Not in the approved list; do
  not add them.
- The signal store: it does not exist yet (release C).
- `database.Store`, `iface_*.go`, `mocks/`.
- The A1 metric names. No metric change. The shared-cache caveat goes into
  the docs only.

## 7. Tests

`internal/database/pebble_settings_test.go` (new):

- `TestPebbleSettings_UnsetIsTodaysDefaults`. With an empty getenv,
  `ApplyMain` on a fresh `pebble.Options{}` leaves `Cache == nil`,
  `MemTableSize == 0`, `CompactionConcurrencyRange == nil`, and every
  `Levels[i].FilterPolicy == nil`. Then `opts.EnsureDefaults()` yields
  `MemTableSize == 4<<20` and `CompactionConcurrencyRange()` returns `1, 1`.
- `TestPebbleSettings_ParsesEachVariable`, table-driven: each variable's
  valid value maps to the expected option. For example `"1-4"` gives
  `CompactionConcurrencyRange()` = `1, 4`, and `"2"` gives `2, 2`.
- `TestPebbleSettings_RejectsBadValues`, table-driven: `"4G"`, `"-1"`,
  `"0"` for cache; `"0"` and `"33"` for bloom; `"5368709120"` (5 GiB) for
  memtable; `"4-1"`, `"0-2"` and `"a"` for concurrency; `"maybe"` for
  parallel. Each error message contains the variable name.
- `TestPebbleSettings_SharedCacheIsOneInstance`. Two `ApplyShared` calls
  produce the same `*pebble.Cache` pointer.
- `TestNewPebbleStore_InvalidEnvFailsOpen`. `t.Setenv("AORG_PEBBLE_BLOOM_BITS_PER_KEY", "x")`,
  then `NewPebbleStoreInMemory(...)` returns an error that names the
  variable.
- `TestNewPebbleStore_WithSettingsOpensAndWrites`. `t.Setenv` all five
  variables to valid values; open in memory, write and read one key, close.
- `TestParallelManualCompaction_Env`. Unset gives false, `"true"` gives
  true, `"x"` gives false.

Use `t.Setenv`, never `os.Setenv`. The tests in this package must not leave
variables set for other tests.

## 8. Verify

```bash
go build ./...
go vet ./internal/database/... ./internal/openlibrary/... ./internal/plugins/maintenance/...
go test -race -count=1 -run 'PebbleSettings|NewPebbleStore_|ParallelManualCompaction' ./internal/database/
go test -race -count=1 ./internal/database/ ./internal/openlibrary/... ./internal/plugins/maintenance/...
make lint-errcheck-ratchet
make lint-width
make sdkguard
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required.

Manual check: run `make run-api` with no `AORG_PEBBLE_*` set, and confirm
the log line `pebble store options store=main cache_bytes=8388608 cache_shared=false ... compaction_concurrency=1-1`.
Then run with `AORG_PEBBLE_COMPACTION_CONCURRENCY=1-4` and confirm `1-4`.

## 9. Deliverables

- Version headers: the new Go files get fresh headers
  (`uuidgen | tr A-Z a-z`). Bump `pebble_store.go`, `ai_scan_store.go`,
  `openlibrary/store.go`, `plugins/maintenance/db.go`,
  `deploy/audiobook-organizer.service` and `docs/configuration.md`.
- Fragment `changelog.d/<YYYYMMDD>_storage_a7_pebble_env_settings.md`, no
  header, `### Added`, one `####` entry listing the five variables and
  stating that the defaults are unchanged.
- Check that `git diff origin/main | grep -nE 'abk_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}'` prints
  nothing.
- Commit, for example
  `feat(database): Pebble open settings from AORG_PEBBLE_* env, shared block cache, parallel manual compaction`,
  ending with:

  ```
  Co-Authored-By: <model name> <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/feat/storage-a7-pebble-env-settings"`
- `gh pr create --base main --head feat/storage-a7-pebble-env-settings`. Do
  NOT merge. The PR body repeats the `MemoryMax` warning and the
  one-setting-per-deploy order.

## 10. Exit criteria and report

- [ ] With no variable set, the effective options equal today's (unit test,
      plus the log line from a local run).
- [ ] Every variable parses, and every bad value fails the open with the
      variable's name.
- [ ] The main and OpenLibrary stores share one cache when
      `AORG_PEBBLE_CACHE_BYTES` is set.
- [ ] All three `Optimize` methods honour
      `AORG_PEBBLE_PARALLEL_MANUAL_COMPACTION`.
- [ ] The deploy unit and `docs/configuration.md` document all five variables
      and the `MemoryMax` warning.
- [ ] All tests pass with `-race`; `make lint-errcheck-ratchet`,
      `make lint-width` and `make sdkguard` pass.
- [ ] PR open, not merged.

Report:

```
TASK-A7 report
head sha: <sha>
PR: <url>
files changed: <list>
env vars: <5 names with parsed types>
default-run log line: <paste>
tests: <name> PASS (<time>) ...
not done / deviations: <list or "none">
```
