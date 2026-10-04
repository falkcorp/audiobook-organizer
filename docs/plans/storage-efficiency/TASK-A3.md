<!-- file: docs/plans/storage-efficiency/TASK-A3.md -->
<!-- version: 1.2.0 -->
<!-- guid: 2dde370d-4e23-41f2-8820-7ef7d8c7e189 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A3: db-health and `/cache/stats` read the census; safe prefix bounds

Wave W2, in parallel with A8. Start only after TASK-A2
(`feat/storage-a2-db-census`) and PR #3704 (it edits `pebble_store.go`; plan
P-1) have merged to `main`. TASK-A7 runs after this task, because both edit
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
  `pebble_store.go:5662`) and takes about 5 minutes (eval R1, F6a). It also
  decodes every `metadata_fetch_cache:` value, 141,043 rows, to count the
  expired ones (`diagnostics.go:744`).
- `/cache/stats` took 2.963 s measured, because `CountPrefix` reads every
  cache value block (eval R4, F6b).
- On prod the AI-scan data lives inside the main DB under `aiscan:`
  (`ai_scan_store.go:129`), yet its `size_bytes` reports the whole main
  store (`:851`).
- `ScanPrefix` and `CountPrefix` build their upper bound by incrementing the
  last byte (`:4903`, `:4995`). A prefix ending in `0xff` wraps to `0x00`,
  which gives an empty or wrong range.

## 2. ⛔ START HERE (run this first)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git log --oneline origin/main | grep -m1 'db-census'   # must print A2's commit; if not, stop
gh pr view 3704 --json state -q .state   # must print MERGED; if not, stop
git worktree add ../aorg-storage-a3-db-health-census -b perf/storage-a3-db-health-census origin/main
cd ../aorg-storage-a3-db-health-census
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.
- Next, run the "already done if" check in `## Idempotency / Rollback` below.
  If it says the work exists, stop and report instead of redoing it.

## Idempotency / Rollback

This task removes and replaces code, so "done" needs the old thing absent AND
the new thing present. Already done if all of these hold (worktree root):

```bash
grep -c 'func (p \*PebbleStore) KeyCount' internal/database/pebble_store.go   # must print 0
grep -c 'prefixUpperBound(prefixBytes)' internal/database/pebble_store.go      # must print 2 or more (3 once ScanPrefix, CountPrefix and ScanPrefixPage all use it)
```

On `origin/main` at `373ba19d2` the first prints 1 and the second prints 1
(only `ScanPrefixPage`), so the task is not done. Both conditions true: stop
and report "already done". Mixed: a previous attempt stopped half way; finish
the missing steps.

Rollback: before the first push, remove the worktree and branch, or
`git reset --hard origin/main` inside it. After the PR merges, `git revert`
it, and say in the revert PR that the JSON field meanings of db-health and
`/cache/stats` change back (step 8 lists them). No stored data changes.

## 3. Read before editing

Line numbers in this section come from `origin/main` at `373ba19d2`. Section 4
re-greps each one; where a number differs, use the grep result.

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
- `internal/database/pebble_store.go:4899` (`ScanPrefix`), `:4991`
  (`CountPrefix`), `:4928` (`ScanPrefixPage`, already correct) and
  `:5660-5673` (`KeyCount`, from its first doc-comment line to the closing
  brace).
- `internal/database/embedding_store.go:2342-2352`, `prefixUpperBound`. It
  returns nil when every byte is `0xff`, which means "no upper bound".
- `internal/database/ai_scan_store.go:100-135` (owned vs shared) and
  `:831-858` (`AIScanHealthStats`, `HealthStats`).
- `web/src/services/api.ts:5787-5811` (`DBHealthStats`) and
  `web/src/pages/Diagnostics.tsx:745-935` (how the UI renders `key_count`,
  `ai_scans.size_bytes` and `expired_entries`).

## 4. Re-verify anchors

Line numbers were re-run on `origin/main` at `373ba19d2`, before A2 merged.
A2 edits `diagnostics.go` and `wire_media_routes.go`, so the `diagnostics.go`
lines will have shifted down by the size of A2's `GetDBCensus` by the time you
run these. Re-grep and use the current numbers; the pattern matching, not the
number, is what each anchor checks.

1. `grep -n 'func (p \*PebbleStore) ScanPrefix(\|func (p \*PebbleStore) CountPrefix(\|func (p \*PebbleStore) KeyCount(' internal/database/pebble_store.go`
   → `4899`, `4991`, `5662`.
2. `grep -n 'upperBound\[len(upperBound)-1\]++' internal/database/pebble_store.go`
   → `4903`, `4995`.
3. `grep -n 'store.ScanPrefix("metadata_fetch_cache:")\|st.KeyCount()\|database.CountCachedMetadataFetches(store)' internal/server/handlers/diagnostics.go`
   → `699 st.KeyCount()`, `735 CountCachedMetadataFetches`,
   `744 store.ScanPrefix("metadata_fetch_cache:")`.
4. `grep -n 'CountPrefix("metadata_fetch_cache:")' internal/server/handlers/cache.go`
   → `68` (comment), `99` (call).
5. `grep -n 'sizeBytes := s.db.Metrics().DiskSpaceUsage()\|prefix: "aiscan:", owned: false' internal/database/ai_scan_store.go`
   → `129`, `851`.
6. `grep -n 'expired_entries\|key_count' web/src/services/api.ts web/src/pages/Diagnostics.tsx`
   → `api.ts:5793`, `api.ts:5808`, `Diagnostics.tsx:754`,
   `Diagnostics.tsx:929`.
7. `grep -rn '\.KeyCount()\|KeyCount() (' --include='*.go' internal cmd | grep -v _test`
   → only `pebble_store.go:5662` and `diagnostics.go:699` / `:773`. That is
   one caller, so `KeyCount` can be deleted.
8. `grep -n 'func CountCachedMetadataFetches' internal/database/metadata_fetch_cache.go` → `298:`
9. `grep -n 'func prefixUpperBound' internal/database/embedding_store.go` → `2342:`
10. `grep -n 'type DBCensusProvider\|func (p \*PebbleStore) DBCensus' internal/database/census.go`
    → both present once A2 has merged. (On `origin/main` at `373ba19d2` this
    prints a "No such file" error, which is expected: A2 has not merged yet,
    so this is the one anchor that cannot pass before A2 lands.) If they are
    missing when you run it, stop: A2 has not merged.
11. `grep -n 'func (p \*PebbleStore) ScanPrefixPage' internal/database/pebble_store.go`
    → `4928:`. This is the correct-bound implementation step 1 copies
    (`UpperBound: prefixUpperBound(prefixBytes)` at `4935`).
12. `grep -n 'json:"cached_at"' internal/database/metadata_fetch_cache.go`
    → `55:` (the tag step 4's local struct must match).
13. `grep -n 'type CacheMetadataStore interface' internal/server/handlers/cache.go`
    → `69:` (its doc comment at `:66-68` is the one step 6 rewrites).
14. `grep -n 'export interface DBHealthStats' web/src/services/api.ts`
    → `5787:` (step 7 adds optional fields to it).
15. `grep -n '^// KeyCount returns the total number of keys' internal/database/pebble_store.go`
    → `5660:` (the first line of the block step 2 deletes; the closing
    brace of `KeyCount` is `5673`, and nothing else is deleted).
16. `grep -n 'type dbHealthPebble struct\|type dbHealthMetadataCache struct\|type dbHealthAiScans struct' internal/server/handlers/diagnostics.go`
    → `121`, `137`, `131` (the response structs steps 3-5 add fields to).

## 5. Steps

1. **Prefix bounds (`pebble_store.go`). Test first.** Write
   `TestScanPrefix_TrailingFFPrefix` and `TestScanPrefix_NormalPrefixStillMatches`
   (section 7) before touching the code. Run
   `go test -race -count=1 -run 'TestScanPrefix_TrailingFFPrefix' ./internal/database/`
   and confirm it FAILS on the unfixed code (the bound wraps to `"b\x00"`
   and the scan returns nothing). Record the FAIL line for the report. Then
   fix: in `ScanPrefix` and `CountPrefix`, replace the copy-and-increment
   block with `UpperBound: prefixUpperBound(prefixBytes)`. Copy
   `ScanPrefixPage` (`:4928`), which already does this. Also guard the empty prefix: with
   `prefix == ""` the old code panicked on `upperBound[-1]`. The new code
   must return every key, which is what `LowerBound: nil` and
   `UpperBound: nil` give.
2. **Delete `KeyCount`**: from its first doc-comment line (`// KeyCount
   returns the total number of keys`, anchor 15) down to the closing brace of
   the function, `pebble_store.go:5660-5673`. Leave the `// --- AIJobsStore`
   comment above it and `SweepBookFileSegDropResult` below it alone. Also delete `keyCounter` and `resolveKeyCounter` from
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
   - `pebble.key_count`: was an exact count of live keys from a full
     iteration. It is now the census `TotalKeys`: entries in sstables,
     excluding tombstones, but including overwritten versions not yet
     compacted, and excluding the memtable. `estimated: true`. It can read
     higher than the old live count on a store with many uncompacted
     rewrites.
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
  - `TestScanPrefix_NormalPrefixStillMatches`. Write `"ab1"`, `"ab2"`, `"ac1"`.
    Assert `ScanPrefix("ab")` returns exactly the two `ab` keys and
    `CountPrefix("ab") == 2`.
  - Anti-over-suppression: `TestScanPrefix_NormalPrefixStillMatches` is the
    named test. Keys that carry no `0xff` byte must still match an ordinary
    prefix through the new bound, so the fix cannot have narrowed the common
    case.
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
under 1 s. Record the total, and time each section separately with
temporary `time.Since` logging, removed before commit: census call,
embeddings `HealthStats`, AI-scan `HealthStats` (it still runs `ListScans`,
a full decode), and the metadata-cache section. If any section dominates,
name it in the report, so a miss on prod can be attributed.

## 9. Deliverables

- Bump version headers on every edited file: `pebble_store.go`,
  `ai_scan_store.go`, `diagnostics.go`, `cache.go`, `api.ts`,
  `Diagnostics.tsx` if it has one. New files get fresh headers
  (`uuidgen | tr A-Z a-z`).
- Fragment `changelog.d/<YYYYMMDD>_storage_a3_db_health_census.md`, no
  header. Use `### Changed` for the field-meaning list and `### Fixed` for the
  prefix-bound bug. `####` entries only.
- Check that `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"` prints
  nothing.
- Commit with exactly
  `perf(diagnostics): db-health and cache stats read the census; fix prefix upper bound`
  (`<type>(<scope>): ...` form), ending with:

  ```
  Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
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
db-health local latency: <ms> total; census <ms>, embeddings <ms>, ai-scans <ms>, metadata-cache <ms>
not done / deviations: <list or "none">
```
