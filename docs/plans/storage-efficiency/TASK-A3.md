<!-- file: docs/plans/storage-efficiency/TASK-A3.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2dde370d-4e23-41f2-8820-7ef7d8c7e189 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A3: db-health and `/cache/stats` read the census; safe prefix bounds

Wave W2. Start only after TASK-A2 (`feat/storage-a2-db-census`) has merged
to `main`. TASK-A7 runs after this task, because both edit
`pebble_store.go` and `ai_scan_store.go`. Model: sonnet. Reviewer:
code-reviewer.

## 1. Goal and why

**Goal.** Stop full scans on two request paths:
- db-health must take its key count and cache-entry count from A2's census,
  and stop decoding every fetch-cache value;
- `/cache/stats` must take its fetch-cache size from the census.

Also report the AI-scan size for its own key prefix instead of the whole
shared store, and fix the upper bound that `ScanPrefix` and `CountPrefix`
build.

**Why.**
- db-health iterates all 174,165,047 keys (`KeyCount`,
  `pebble_store.go:5582`) and takes about 5 minutes (eval R1, F6a). It also
  decodes every `metadata_fetch_cache:` value, 141,043 rows, to count the
  expired ones (`diagnostics.go:744`).
- `/cache/stats` took 2.963 s measured, because `CountPrefix` reads every
  cache value block (eval R4, F6b).
- On prod the AI-scan data lives inside the main DB under `aiscan:`
  (`ai_scan_store.go:129`), yet its `size_bytes` reports the whole main
  store (`:851`).
- `ScanPrefix` and `CountPrefix` build their upper bound by incrementing the
  last byte (`:4831`, `:4923`). A prefix ending in `0xff` wraps to `0x00`,
  which gives an empty or wrong range.

## 2. Setup

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git log --oneline origin/main | grep -m1 'db-census'   # must print A2's commit; if not, stop
git worktree add ../aorg-storage-a3-db-health-census -b perf/storage-a3-db-health-census origin/main
cd ../aorg-storage-a3-db-health-census
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.

## 3. Read before editing

- `internal/database/census.go` and `keyfamilies.go`, both from A2. Note:
  - `DBCensusProvider`, `CensusOptions`, `DBCensus.TotalKeys`, `DBCensus.Families`;
  - `FamilyCensus.Keys` and `.Estimated`;
  - the 5-minute cache.
- `internal/server/handlers/diagnostics.go`:
  - `:100-140`: the response structs;
  - `:678-790`: `GetDBHealth`, `keyCounter`, `resolveKeyCounter`;
  - A2's `GetDBCensus`.
- `internal/server/handlers/key_counter_capability_test.go`. This is the
  decorator test for `resolveKeyCounter`. You replace it.
- `internal/server/handlers/cache.go:26-115`. `CacheStat`,
  `CacheMetadataStore`, `HandleCacheStats`.
- `internal/database/pebble_store.go:4827-4848` (`ScanPrefix`),
  `:4919-4937` (`CountPrefix`), `:5578-5594` (`KeyCount`).
- `internal/database/embedding_store.go:2342-2352`, `prefixUpperBound`. It
  returns nil when every byte is `0xff`, which means "no upper bound".
- `internal/database/ai_scan_store.go:100-135` (owned vs shared) and
  `:830-857` (`AIScanHealthStats`, `HealthStats`).
- `web/src/services/api.ts:5783-5812` (`DBHealthStats`) and
  `web/src/pages/Diagnostics.tsx:745-935` (how the UI renders `key_count`,
  `ai_scans.size_bytes` and `expired_entries`).

## 4. Re-verify anchors

Line numbers are from `d1f069fac`. A2 does not edit these files except
`diagnostics.go`, so the `diagnostics.go` lines may have shifted down by the
size of A2's `GetDBCensus`. Re-grep and use the current numbers.

1. `grep -n 'func (p \*PebbleStore) ScanPrefix(\|func (p \*PebbleStore) CountPrefix(\|func (p \*PebbleStore) KeyCount(' internal/database/pebble_store.go`
   → `4827`, `4919`, `5582`.
2. `grep -n 'upperBound\[len(upperBound)-1\]++' internal/database/pebble_store.go`
   → `4831`, `4923`.
3. `grep -n 'store.ScanPrefix("metadata_fetch_cache:")\|st.KeyCount()\|database.CountCachedMetadataFetches(store)' internal/server/handlers/diagnostics.go`
   → `699 st.KeyCount()`, `735 CountCachedMetadataFetches`,
   `744 store.ScanPrefix("metadata_fetch_cache:")`.
4. `grep -n 'CountPrefix("metadata_fetch_cache:")' internal/server/handlers/cache.go`
   → `68` (comment), `99` (call).
5. `grep -n 'sizeBytes := s.db.Metrics().DiskSpaceUsage()\|prefix: "aiscan:", owned: false' internal/database/ai_scan_store.go`
   → `129`, `851`.
6. `grep -n 'expired_entries\|key_count' web/src/services/api.ts web/src/pages/Diagnostics.tsx`
   → `api.ts:5789`, `api.ts:5804`, `Diagnostics.tsx:754`,
   `Diagnostics.tsx:929`.
7. `grep -rn '\.KeyCount()\|KeyCount() (' --include='*.go' internal cmd | grep -v _test`
   → only `pebble_store.go:5582` and `diagnostics.go:699` / `:773`. That is
   one caller, so `KeyCount` can be deleted.
8. `grep -n 'func CountCachedMetadataFetches' internal/database/metadata_fetch_cache.go` → `298:`
9. `grep -n 'func prefixUpperBound' internal/database/embedding_store.go` → `2342:`
10. `grep -n 'type DBCensusProvider\|func (p \*PebbleStore) DBCensus' internal/database/census.go`
    → both present once A2 has merged. (On `d1f069fac` this prints nothing,
    which is expected: the file does not exist yet.) If they are missing, stop:
    A2 has not merged.

## 5. Steps

1. **Prefix bounds (`pebble_store.go`).** In `ScanPrefix` and
   `CountPrefix`, replace the copy-and-increment block with
   `UpperBound: prefixUpperBound(prefixBytes)`. Copy `ScanPrefixPage`
   (`:4863`), which already does this. Also guard the empty prefix: with
   `prefix == ""` the old code panicked on `upperBound[-1]`. The new code
   must return every key, which is what `LowerBound: nil` and
   `UpperBound: nil` give.
2. **Delete `KeyCount`** (`pebble_store.go:5578-5594`, doc comment
   included). Also delete `keyCounter` and `resolveKeyCounter` from
   `diagnostics.go`. Delete `key_counter_capability_test.go` and replace it
   with the census decorator test in section 7.
3. **db-health main-store section.** In `GetDBHealth`:
   - resolve `database.AsCapability[database.DBCensusProvider](store)`;
   - call it with `c.Request.Context()` and `database.CensusOptions{}` (cached
     and shallow). The census never iterates keys, so this is the fast path;
   - set `resp.Pebble = &dbHealthPebble{KeyCount: census.TotalKeys, SizeBytes: census.DiskSpaceUsage, Estimated: true}`.
     Add `Estimated bool \`json:"estimated"\`` to `dbHealthPebble`;
   - on error, `slog.Warn` and leave `resp.Pebble` nil. That matches today's
     non-Pebble branch.
4. **Metadata cache section.**
   - `TotalEntries`: take the `Keys` of the `metadata_fetch_cache:` family
     from the same census (find it by `Prefix`). Add
     `Estimated bool \`json:"estimated"\`` to `dbHealthMetadataCache`. If no
     census is available (non-Pebble backend), keep the current
     `database.CountCachedMetadataFetches(store)` fallback and set
     `Estimated: false`.
   - `ExpiredEntries`: no decode on the default path. Add
     `ExpiredComputed bool \`json:"expired_entries_computed"\``.
     - Default: `ExpiredEntries = -1`, `ExpiredComputed = false`.
     - Only when `c.Query("deep") == "true"`: count expired entries with a
       streaming loop over `store.ScanPrefixPage("metadata_fetch_cache:", after, 1000)`
       (a method on `database.RawKVStore`, already embedded in
       `diagnosticsStore`). Decode only `CachedAt`, into a local struct
       `struct{ CachedAt time.Time \`json:"cached_at"\` }`; the tag is
       verified at `metadata_fetch_cache.go:55`. Then set `ExpiredComputed = true`.
       Never call `ScanPrefix` here: it loads all 141k values into memory at
       once.
5. **AI-scan size (`ai_scan_store.go`).** In `HealthStats`, when `!s.owned`,
   set `SizeBytes` from `s.db.EstimateDiskUsage([]byte(s.prefix), prefixUpperBound([]byte(s.prefix)))`.
   When `prefixUpperBound` returns nil, pass `[]byte{0xff, 0xff, 0xff, 0xff}`.
   Add `SizeSource string \`json:"size_source"\`` to `AIScanHealthStats`.
   Its values are `"store_disk_usage"` (owned) and `"aiscan_prefix_estimate"`
   (shared). Then:
   - add `SizeSource` to `dbHealthAiScans` with the same JSON name, and copy
     it in `GetDBHealth`;
   - leave `ListScans` unchanged. It is out of scope; say so in the PR body.
6. **`/cache/stats` (`cache.go`).**
   - In `HandleCacheStats`, first try
     `database.AsCapability[database.DBCensusProvider](h.metadataStore)`. If
     it works, set the `metadata_fetch` entry's `Size` from the
     `metadata_fetch_cache:` family `Keys`, and set a new field
     `SizeEstimated bool \`json:"size_estimated,omitempty"\`` on `CacheStat`
     to true.
   - If it fails, keep the `CountPrefix` call and leave `SizeEstimated`
     false.
   - Update the `CacheMetadataStore` doc comment at `:66-69` to describe
     both paths. Do not widen `CacheMetadataStore`; the capability is
     resolved at runtime.
7. **Frontend.**
   - `web/src/services/api.ts` `DBHealthStats`: add `estimated?: boolean` to
     `pebble` and `metadata_cache`, `expired_entries_computed?: boolean` to
     `metadata_cache`, and `size_source?: string` to `ai_scans`.
   - `Diagnostics.tsx`:
     - `:754`: render the key count with a leading `~` when
       `pebble.estimated`;
     - `:929`: when `expired_entries_computed === false` or
       `expired_entries < 0`, render `not computed` instead of the number.
       Add a tooltip or caption: "pass ?deep=true to count; decodes every
       cache row";
     - the total-entries cell: add a leading `~` when `estimated`.
   - Bump the version headers if these files have them. Do not change any
     other UI.
8. **Field meaning changes.** List these in the PR body and in the
   changelog fragment:
   - `pebble.key_count`: was an exact iterator count; is now the sstable
     property sum (excludes the memtable). `estimated: true`.
   - `metadata_cache.total_entries`: was an exact count; is now the census
     family estimate, `estimated: true`.
   - `metadata_cache.expired_entries`: is now `-1` unless `?deep=true`.
   - `ai_scans.size_bytes`: on the shared store, was the whole DB; is now the
     `aiscan:` prefix estimate.
   - `/cache/stats` `metadata_fetch.size`: is now the estimate,
     `size_estimated: true`.

## 6. Do not touch

- `internal/database/census.go` and `keyfamilies.go`. They belong to A2. If
  you find a bug there, report it; do not fix it here.
- `newPebbleStore`, `Optimize`, the store options. That is A7, the next wave.
- `database.Store`, `iface_*.go`, `mocks/`. No interface widening. If
  deleting `KeyCount` breaks a mock or interface, report it; do not widen
  anything. Verified: `KeyCount` is on no interface except the handler's
  local `keyCounter`.
- `internal/database/pebble_store_ops_v2.go`, `internal/server/server_lifecycle.go`.

## 7. Tests

- `internal/database/pebble_store_prefix_bound_test.go` (new):
  - `TestScanPrefix_TrailingFFPrefix`. Write keys `"a\xff1"`, `"a\xff2"` and
    `"b"`. Assert `ScanPrefix("a\xff")` returns exactly the two `a\xff` keys,
    and `CountPrefix("a\xff") == 2`. On the old code this returns 0, because
    the bound wraps to `"b\x00"`. Run the test before the fix to see it fail.
  - `TestCountPrefix_EmptyPrefixCountsAll`.
- `internal/database/ai_scan_store_health_test.go` (new):
  - `TestAIScanHealthStats_SharedReportsPrefixEstimate`. Use
    `NewAIScanStoreFromDB` on a PebbleStore DB that also holds 10,000
    non-aiscan keys; flush. Assert `SizeSource == "aiscan_prefix_estimate"`
    and that `SizeBytes` is strictly less than `db.Metrics().DiskSpaceUsage()`.
- `internal/server/handlers/census_capability_test.go`, replacing
  `key_counter_capability_test.go`:
  - `TestResolveCensusProviderThroughDecorator`. Same decorator shape as the
    deleted test: a `StoreUnwrapper` wrapper around a fake that implements
    `DBCensusProvider`. Assert resolution works.
  - `TestResolveCensusProviderOnUncapableBackend`.
- In `diagnostics_test.go` (extend it) or a new `diagnostics_dbhealth_census_test.go`:
  - `TestGetDBHealth_UsesCensusAndSkipsDecode`. Use an in-memory PebbleStore
    holding 50 `metadata_fetch_cache:` rows; flush. Without `deep`, assert:
    `expired_entries == -1`, `expired_entries_computed == false`,
    `metadata_cache.estimated == true`, `pebble.estimated == true`.
  - `TestGetDBHealth_DeepCountsExpired`. Store 3 expired and 2 fresh rows
    (set `config.AppConfig.MetadataFetchCacheTTLDays` in the test and restore
    it). With `?deep=true`, assert `expired_entries == 3`.
- `internal/server/handlers/cache_census_test.go` (new):
  `TestHandleCacheStats_MetadataFetchSizeFromCensus`. Assert
  `size_estimated == true`.
- Frontend: if a Vitest test covers the Diagnostics DB-health card
  (`grep -rln 'expired_entries\|DBHealth' web/src --include='*.test.tsx'`),
  add a case for `not computed`. Run `npm --prefix web run test -- --run Diagnostics`.

## 8. Verify

```bash
go build ./...
go vet ./internal/database/... ./internal/server/...
go test -race -count=1 -run 'Prefix|AIScanHealth|DBCensus' ./internal/database/
go test -race -count=1 ./internal/server/handlers/
make lint-errcheck-ratchet
make lint-width
make mocks-check
npm --prefix web run build          # runs tsc, then vite build (web/package.json:8)
```

Run `bash scripts/check-interface-width.sh` because `CacheMetadataStore`'s
doc changed. Its width must not change.

Timing check (local): `GET /api/v1/diagnostics/db-health` must answer in
under 1 s. Record the time.

## 9. Deliverables

- Bump version headers on every edited file: `pebble_store.go`,
  `ai_scan_store.go`, `diagnostics.go`, `cache.go`, `api.ts`,
  `Diagnostics.tsx` if it has one. New files get fresh headers
  (`uuidgen | tr A-Z a-z`).
- Fragment `changelog.d/<YYYYMMDD>_storage_a3_db_health_census.md`, no
  header. Use `### Changed` for the field-meaning list and `### Fixed` for the
  prefix-bound bug. `####` entries only.
- Check that `git diff origin/main | grep -nE 'abk_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}'` prints
  nothing.
- Commit, for example
  `perf(diagnostics): db-health and cache stats read the census; fix prefix upper bound`,
  ending with:

  ```
  Co-Authored-By: <model name> <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/perf/storage-a3-db-health-census"`
- `gh pr create --base main --head perf/storage-a3-db-health-census`, with
  the field-meaning list in the body. Do NOT merge.

## 10. Exit criteria and report

- [ ] `grep -n 'NewIter(nil)' internal/database/pebble_store.go` no longer
      shows the `KeyCount` loop.
- [ ] `grep -n 'ScanPrefix("metadata_fetch_cache:")' internal/server/handlers/diagnostics.go`
      prints nothing.
- [ ] `TestScanPrefix_TrailingFFPrefix` failed before the fix and passes
      after it. Record both runs.
- [ ] db-health answers in under 1 s locally.
- [ ] All tests pass with `-race`. `make mocks-check`, `make lint-width` and
      `make lint-errcheck-ratchet` pass.
- [ ] PR open, not merged.

Report:

```
TASK-A3 report
head sha: <sha>
PR: <url>
files changed: <list>
field meaning changes: <list>
tests: <name> PASS (<time>) ...; prefix-bound test before fix: FAIL (<msg>)
db-health local latency: <ms>
not done / deviations: <list or "none">
```
