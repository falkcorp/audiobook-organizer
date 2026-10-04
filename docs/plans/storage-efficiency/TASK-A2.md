<!-- file: docs/plans/storage-efficiency/TASK-A2.md -->
<!-- version: 1.2.0 -->
<!-- guid: f3a0c6be-5f0f-4f00-b712-007dd0053997 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A2: Key-family registry and `GET /diagnostics/db-census`

Wave W1, in parallel with A1, A4, A5 and A6. Model: opus. Reviewer:
schema-auditor.

## 1. Goal and why

**Goal.** Add one registry of every key family in the main Pebble store
(prefix, description, owner). Add an endpoint that reports, per family, an
estimated key count and byte size, read from sstable metadata. The endpoint
must not iterate the store. Add the counts the families cannot give: retired
books and their files, fingerprints and transcripts, and (opt-in)
history-per-book distribution.

**Why.** The store holds 174,165,047 keys and 50.6 GB (eval R1). No per-prefix
census exists (R7), so every size figure in the design is an estimate built
from a 55-book sample (R5). Examples: `book_ver` at 25-37 GB, op logs at
30-80M keys, `fpidx` at 61-78M keys. Releases B-G must state before and after
numbers from a real census (design P6). The only full count today,
db-health's `KeyCount`, iterates all 174M keys and takes about 5 minutes (R1,
F6a).

## 2. ⛔ START HERE (run this first)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git worktree add ../aorg-storage-a2-db-census -b feat/storage-a2-db-census origin/main
cd ../aorg-storage-a2-db-census
npm ci --prefix web
```

- Do NOT run `go work init`. It breaks the build in this repo.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch
  (step 9 gives the push form).
- Next, run the "already done if" check in `## Idempotency / Rollback` below.
  If it says the work exists, stop and report instead of redoing it.

## Idempotency / Rollback

Already done if both of these hold (run from the worktree root):

```bash
test -f internal/database/census.go && echo "census file present"
grep -c GetDBCensus internal/server/handlers/diagnostics.go   # prints 1 or more when done
```

Both present: stop and report "already done". Only one present: a previous
attempt stopped half way; finish the missing step instead of starting over.

Rollback: before the first push, remove the worktree and branch, or
`git reset --hard origin/main` inside it. After the PR merges, `git revert`
is enough: the task adds new files and one route line, changes no stored data
and no existing response.

## 3. Read before editing

Line numbers in this section come from `origin/main` at `373ba19d2`. Section 4
re-greps each one; where a number differs, the grep result wins.

- Pebble v2.1.7 (`P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7`):
  - `$P/db.go:2228-2420`: `SSTables`, its options, `SSTableInfo`.
  - `$P/db.go:2465-2480`: `EstimateDiskUsage`. It **panics** with
    `ErrClosed` on a closed DB (`:2475`).
  - `$P/sstable/properties.go:80-105`: `CommonProperties`.
- `internal/database/pebble_store_ops_v2.go:1103-1114`:
  `recoverPebbleClosed`. Use it on every Pebble call in the census.
- `internal/database/embedding_store.go:2342-2352`: `prefixUpperBound`. This
  is the correct exclusive upper bound for a prefix: it handles trailing
  `0xff` bytes and returns nil when there is no bound. Use it, not
  `prefixEnd`.
- `docs/database-pebble-schema.md`. The documented families. Your seed for
  the registry, together with the list in step 1.
- `internal/database/memdb_reads.go:855-873` (`ListBookIDs`) and `:924-950`
  (`ListSoftDeletedBooks`). These show how to iterate memdb tables and the
  `requireTablesComplete` guard (`memdb_integrity.go:191`).
- `internal/database/memdb_schema.go:270-280`. The `book_files` table has a
  `book_id` index (`memIdxBookID`).
- `internal/database/book_visibility.go:27` (`bookIsSoftDeleted`) and
  `store.go:346` (`MergedIntoBookID`).
- `internal/server/handlers/diagnostics.go:186-217` (handler struct and
  constructor), `:678-790` (`GetDBHealth`, `resolveKeyCounter`). A3 later
  rewrites `GetDBHealth` to use your census. You only add a new method.
- `internal/server/wire_media_routes.go:60-75`. The diagnostics routes and
  their permission.

## 4. Re-verify anchors

1. `P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7; grep -n '^func (d \*DB) SSTables\|^func WithProperties\|^func WithKeyRangeFilter\|^func WithApproximateSpanBytes\|^func (d \*DB) EstimateDiskUsage(' $P/db.go`
   → `2234 WithProperties`, `2242 WithKeyRangeFilter`,
   `2252 WithApproximateSpanBytes`,
   `2322 func (d *DB) SSTables(opts ...SSTablesOption) ([][]SSTableInfo, error)`,
   `2465 func (d *DB) EstimateDiskUsage(start, end []byte) (uint64, error)`.
2. `grep -n 'ApproximateSpanBytes uint64\|Properties \*sstable.Properties' $P/db.go`
   → `2311`, `2315`.
3. `grep -n 'NumEntries uint64\|RawKeySize uint64\|RawValueSize uint64\|NumDeletions uint64' $P/sstable/properties.go`
   → `83`, `85`, `88`, `101`. All four sit on the embedded
   `CommonProperties`, so `info.Properties.NumEntries` works.
4. `grep -n 'Smallest InternalKey\|Largest InternalKey\|Size uint64' $P/internal/manifest/table_metadata.go | sed -n 1,4p`
   → `107 Size` (a different struct; ignore it), `1112 Size`, `1114 Smallest`,
   `1116 Largest`. The last three are fields of
   `TableInfo`, embedded in `SSTableInfo`. User keys are `.Smallest.UserKey`
   and `.Largest.UserKey`.
5. `grep -n 'if err := d.closed.Load(); err != nil' $P/db.go | grep 2475`
   → `2475:` (EstimateDiskUsage panics on a closed DB).
6. `grep -n 'func prefixUpperBound' internal/database/embedding_store.go` → `2342:`
7. `grep -n 'func recoverPebbleClosed' internal/database/pebble_store_ops_v2.go` → `1103:`
8. `grep -n 'diagnostics/db-health' internal/server/wire_media_routes.go`
   → `68: protected.GET("/diagnostics/db-health", s.perm(auth.PermSettingsManage), diagH.GetDBHealth)`
9. `grep -n 'func bookIsSoftDeleted' internal/database/book_visibility.go` → `27:`
10. `grep -n 'MergedIntoBookID \*string' internal/database/store.go` → `346:`
11. `grep -n 'memTableBooks *=\|memTableBookFiles *=\|memIdxBookID *=\|memIdxMarkedForDeletion *=' internal/database/memdb_schema.go`
    → `12`, `15`, `30`, `40`.
12. `grep -n 'func (p \*PebbleStore) mem() \*MemStore' internal/database/pebble_store.go` → `232:`
13. `grep -n 'AcoustIDFingerprintDurationSec float64\|IntroTranscribedAt \*time.Time' internal/database/store.go`
    → `395` (Book.IntroTranscribedAt), `943` (BookFile fingerprint duration),
    `1036` (BookFile.IntroTranscribedAt).
14. `grep -rhoE '"book:[a-z_]+:' --include='*.go' internal/database | sort -u | tr '\n' ' '`
    → `"book:asin: "book:author: "book:c: "book:hash: "book:id: "book:lower: "book:my: "book:organizedhash: "book:originalhash: "book:path: "book:series: "book:sig: "book:versiongroup: "book:work:`.
    The `book:` family holds the book rows (`book:<ULID>`, which start with an
    uppercase digit or letter) AND lowercase index sub-families.
15. `grep -n 'func NewPebbleStoreInMemory\|func (p \*PebbleStore) WaitForWarmup' internal/database/pebble_store.go`
    → `409`, `257`.
16. `grep -n 'func (m \*MemStore) ListBookIDs\|func (m \*MemStore) ListSoftDeletedBooks' internal/database/memdb_reads.go`
    → `855`, `924` (the iteration pattern step 9 copies).
17. `grep -n 'func (m \*MemStore) requireTablesComplete' internal/database/memdb_integrity.go`
    → `191:` (the guard step 9 starts with).
18. `grep -n 'type DiagnosticsHandler struct\|func NewDiagnosticsHandler\|func (h \*DiagnosticsHandler) GetDBHealth' internal/server/handlers/diagnostics.go`
    → `186`, `203`, `678` (step 12 adds `GetDBCensus` as a new method on
    `DiagnosticsHandler`; it never edits `GetDBHealth`).
19. `grep -n 'AcoustIDFingerprintDurationSec > 0' internal/database/signal_coverage.go`
    → `28` (comment), `189` and `208` (the proxy step 6 says "the same proxy
    `signal_coverage.go` uses").
20. `grep -n 'memIdxBookID,' internal/database/memdb_schema.go | head -3`
    → first hit `278:` (the `book_files` table's `book_id` index; section 3
    cites `:270-280`; the later hits at `349` and `373` are other tables).

## 5. Steps

### 5.1 Registry: `internal/database/keyfamilies.go` (new)

1. `type KeyFamily struct { Prefix, Description, Owner string }`. `Owner` is
   the file that writes the family, for example
   `"internal/database/pebble_store_ops_v2.go"`.
2. `var keyFamilies = []KeyFamily{...}` and
   `func KeyFamilies() []KeyFamily` (returns a copy). This list is the single
   source; later work generates the schema doc from it.
3. **Nesting rule.** A family whose prefix starts with another family's
   prefix is that family's child. The parent's figures exclude every
   registered child.
4. `func keyFamilyRanges(fams []KeyFamily) []keyRange`, with
   `type keyRange struct { Family string; Lo, Hi []byte }` (`Hi == nil` means
   no upper bound). It returns a sorted, gap-free, non-overlapping partition
   of the WHOLE key space:
   - each family's own range is `[prefix, prefixUpperBound(prefix))` minus
     its direct children's ranges, so a parent can yield several pieces;
   - every gap between top-level families becomes a range with
     `Family: "(unregistered)"`, including `[nil, firstPrefix)` and
     `[endOfLast, nil)`.
5. Seed list. Verify EACH prefix with
   `grep -rn '"<prefix>' --include='*.go' internal cmd | grep -v _test`.
   **Keep/drop rule (mechanical):** keep a prefix only if at least one non-test
   hit builds a key, meaning the string literal sits in `[]byte(`,
   `fmt.Sprintf(` or a `+` concatenation that is then passed to a Pebble call
   or returned as a key. Drop it if every non-test hit is in a `slog` call, a
   `fmt.Errorf` or a comment. A prefix with no non-test hit at all is dropped.
   List every dropped and every added prefix in the report. Add any real
   prefix you find that is missing (a literal key prefix passed to
   `NewIter`, `Get`, `Set` or `Delete` that no registered family covers).
   - top level: `act:`, `aijob:`, `aijob_batch:`, `aijob_payload:`,
     `aiscan:`, `abs_sess:`, `apikey:`, `author:`, `author_alias:`,
     `author_tag:`, `author_tag_idx:`, `author_tombstone:`, `book:`,
     `book_atpath:`, `book_atpath_undecodable:`, `book_authors:`,
     `book_file:`, `book_file_acoustid:`, `book_file_error:`,
     `book_file_errors_by_book:`, `book_file_gone:`, `book_file_hash:`,
     `book_file_id:`, `book_file_orig_hash:`, `book_file_path:`,
     `book_file_pid:`, `book_narrators:`, `book_sig:`, `book_tag:`,
     `book_ver:`, `bookmark:`, `cat:`, `cat_author:`, `cat_author_state:`,
     `cat_eg:`, `cat_egkey:`, `cat_hv:`, `cat_id:`, `cat_pid:`, `cat_raw:`,
     `cat_series:`, `cat_stale:`, `counter:`, `dedup:`, `emb:`, `ext_id:`,
     `file_prov:`, `file_prov_hash:`, `file_prov_orphan:`, `file_prov_seq:`,
     `fpidx:`, `fpidx_meta:`, `fpwin:`, `fpwin_fail:`, `import_path:`,
     `invite:`, `library:`, `met:`, `metadata_change:`,
     `metadata_fetch_cache:`, `metadata_rejection:`, `metadata_state:`,
     `narrator:`, `op:`, `opchange:`, `operation:`, `operationlog:`, `opv2:`,
     `path_history:`, `playlist:`, `playlistitem:`, `pref:`, `preference:`,
     `provider_throttle:`, `provider_throttle_v2:`, `review_item:`,
     `series:`, `series_tag:`, `series_tag_idx:`, `sess:`,
     `sync_alias_use:`, `sync_file:`, `sync_item:`, `syslog:`, `system:`,
     `tag:`, `tag_idx:`, `u:`, `user_tag:`, `work:`.
   - children of `book:`: `book:asin:`, `book:author:`, `book:hash:`,
     `book:lower:`, `book:organizedhash:`, `book:originalhash:`,
     `book:path:`, `book:series:`, `book:sig:`, `book:versiongroup:`,
     `book:work:`. Apply the same keep/drop rule to `book:c:`, `book:id:` and `book:my:`.
   - children of `opv2:`: `opv2:def:`, `opv2:op:`, `opv2:q:`, `opv2:act:`,
     `opv2:state:`, `opv2:log:`, `opv2:err:`, `opv2:strike:`, and,
     **pre-registered for TASK-A5**, `opv2:open:` ("timeline index: op not
     completed", owner `pebble_store_ops_v2.go`) and `opv2:done:` ("timeline
     index by completed_at nanos", same owner). These count 0 until A5
     merges. Registering them now means A5 never edits this file.
   - children of `act:`: `act:op:`, `act:bk:`, `act:change:`, `act:digest:`,
     `act:typ:`, `act:src:`, `act:lvl:`, `act:info:`, `act:debug:`.
   - children of `dedup:`: `dedup:r:`, `dedup:p:`, `dedup:e:`, `dedup:s:`,
     `dedup:label:`, `dedup:automerge:`, `dedup:pair:`, `dedup:lbe:`.
   - children of `emb:`: `emb:v:`, `emb:c:`.
   - children of `op:`: `op:deprev:`, `op:completion:`, `op:batch:`.
   - children of `system:`: `system:backfill:`.

### 5.2 Census: `internal/database/census.go` (new)

6. Types (JSON tags in snake_case):
   - `CensusOptions{ Deep bool; Fresh bool }`.
   - `FamilyCensus{ Prefix, Description, Owner string; Keys, Deletions, RawKeyBytes, RawValueBytes int64; DiskBytes uint64; Tables int; Estimated bool }`.
   - `RetiredCensus{ SoftDeletedBooks, MergedBooks, RetiredBooks, RetiredBookFiles int }`.
     A book is retired when it is soft-deleted, or when `MergedIntoBookID` is
     non-nil and non-empty. `RetiredBooks` counts each book once.
   - `SignalCensus{ FilesWithFingerprint, FilesWithTranscript, BooksWithTranscript int }`.
     A fingerprint counts when `AcoustIDFingerprintDurationSec > 0`, the same
     proxy `signal_coverage.go` uses. A transcript counts when
     `IntroTranscribedAt != nil`.
   - `HistoryCensus{ BooksWithHistory, Entries int64; Mean float64; P50, P90, P99, Max int64; Buckets map[string]int64; Top []BookHistoryCount; OrphanBooks, OrphanEntries int64; OrphansKnown bool }`.
     Bucket keys: `"1-9"`, `"10-49"`, `"50-99"`, `"100-499"`, `"500-999"`,
     `"1000+"`. `Top` holds the 20 books with the most entries,
     `{book_id, entries}`. An orphan is a book id with history but no row in
     memdb's books table. `OrphansKnown` is false when memdb is not warm.
   - `DBCensus{ GeneratedAt time.Time; DurationMS int64; Cached bool; TotalKeys, TotalDeletions int64; TotalTables int; TotalTableBytes uint64; DiskSpaceUsage uint64; Families []FamilyCensus; Retired *RetiredCensus; Signals *SignalCensus; History *HistoryCensus; FamilyFiguresBasis string; Notes []string }`.
     `FamilyFiguresBasis` is always
     `"on-disk entries incl. tombstones and shadowed versions; deletions reported separately; figures move only after compaction"`
     (design 8: per-family figures are labelled as on-disk entries). Every
     consumer that shows per-family figures (A3's db-health and
     `/cache/stats`, the census page) shows this label next to them.
7. `func (p *PebbleStore) DBCensus(ctx context.Context, opts CensusOptions) (*DBCensus, error)`,
   plus `type DBCensusProvider interface { DBCensus(ctx context.Context, opts CensusOptions) (*DBCensus, error) }`
   and `var _ DBCensusProvider = (*PebbleStore)(nil)`. Wrap the method body
   with `defer recoverPebbleClosed("DBCensus", &err)`.
8. Family figures. Run this once per call.

   **Definition of a key count, decided.** Pebble's `NumEntries` counts every
   internal entry in a table: puts, point tombstones, range deletions
   (`NumDeletions` includes range deletions, `$P/sstable/properties.go:213`),
   and every older version of a key that compaction has not dropped yet. Per
   table, the census therefore uses `entries = NumEntries − NumDeletions`
   (clamped at 0) as its key count, and keeps `NumDeletions` separately as
   `Deletions`. Overwritten versions that are not yet compacted still count
   once each. `opv2:op:` rows (rewritten on every progress tick) and `book:`
   rows (rewritten on every save) are inflated by them until compaction. Add
   the note
   `"keys = sstable entries minus deletions; overwritten versions not yet compacted count once each"`.

   - `levels, err := p.db.SSTables(pebble.WithProperties())`. Index the
     tables by `FileNum`. Sum, over all tables, `TotalKeys` (Σ entries as
     defined above), `TotalDeletions` (Σ `NumDeletions`), `TotalTables` and
     `TotalTableBytes`.
   - For each `keyRange` r from `keyFamilyRanges(keyFamilies)`, call
     `p.db.SSTables(pebble.WithKeyRangeFilter(lo, hi), pebble.WithApproximateSpanBytes())`.
     `WithApproximateSpanBytes` needs both bounds non-nil: use `[]byte{0x00}`
     for a nil `Lo` and `[]byte{0xff, 0xff, 0xff, 0xff}` for a nil `Hi`.
     For each returned table, record `span[fileNum][r] = info.ApproximateSpanBytes`.
     Pebble computes that value with the same `fileCache.estimateSize` that
     `EstimateDiskUsage` uses, restricted to one table. That is the
     "apportion by EstimateDiskUsage share" the design asks for.
   - For each table, take the ranges it overlaps:
     - exactly one range: the table's entries (as defined above),
       `NumDeletions`, `RawKeySize` and `RawValueSize` go to that range's
       family unchanged, as `Keys`, `Deletions`, `RawKeyBytes` and
       `RawValueBytes`;
     - several ranges: share for r = `span[f][r] / Σ span[f][*]`. If the sum
       is 0 (tiny tables), split equally across the overlapping ranges. Add
       `share × property` to each family and set `Estimated = true` on every
       family that received a share below 1.
     - `Tables` counts each table once per family it contributes to.
   - Per family, `DiskBytes` = Σ over its ranges of
     `p.db.EstimateDiskUsage(lo, hi)`, using the same bound substitutes.
   - Round to int64 only at the end. Invariant (tested): Σ family `Keys`,
     including `(unregistered)`, equals `TotalKeys` within ±(number of
     families) after rounding.
   - Add the note `"sstable properties only: unflushed memtable contents (up to 2 x MemTableSize) are not counted"`.
   - `DiskSpaceUsage = p.db.Metrics().DiskSpaceUsage()`.
9. Retired and signal counts. Add a new file
   `internal/database/memdb_census.go` with
   `func (m *MemStore) retiredAndSignalCensus() (RetiredCensus, SignalCensus, error)`:
   - start with
     `m.requireTablesComplete("db census retired/signal counts", memTableBooks, memTableBookFiles)`;
   - make one pass over `memTableBooks` by `memIdxID`. For each retired book,
     count its files with `txn.Get(memTableBookFiles, memIdxBookID, b.ID)`;
   - make one pass over `memTableBookFiles` for the signal counts;
   - pointer reads only, no JSON. This is the same cost class as
     `ListBookIDs` (`memdb_reads.go:855`).

   In `DBCensus`: if `p.mem() == nil`, or the call returns an error, leave
   `Retired` and `Signals` nil and append a note. The note is
   `"memdb not warm: retired and signal counts unavailable"`, or the error
   text. Never report zeros instead.
10. History (only when `opts.Deep`). Iterate
    `[]byte("book_ver:")` to `prefixUpperBound` and never call
    `iter.Value()`. The book id is the text between `book_ver:` and the LAST
    `:`; keys look like `book_ver:<id>:<nanos>`. Count per book in a
    `map[string]int64`. Check `ctx.Err()` every 10,000 keys and return it.
    Then compute the distribution. Orphans use memdb when warm: look up each
    id in `memTableBooks`/`memIdxID`. This is one iterator doing no per-item
    I/O, so it stays sequential (the CLAUDE.md concurrency rule covers loops
    that do per-item DB, network or CPU-heavy work).
11. Cache. Use a package-level `censusCache` keyed by `*pebble.DB`, holding
    two slots (shallow and deep). Each slot stores `{value *DBCensus, at time.Time}`
    with a TTL of 5 minutes, and the cache is guarded by a `sync.Mutex`. Use
    `golang.org/x/sync/singleflight` (already in `go.mod`) keyed by
    `fmt.Sprintf("%p/%t", p.db, deep)`, so concurrent callers share one
    computation. `opts.Fresh` bypasses the cache for the read and refreshes
    it. A cache hit returns a shallow copy with `Cached = true`.
    Why package-level: a struct field would mean editing `pebble_store.go`,
    which A4 owns in this wave. Leave a `// TODO(storage-a3+): move into
    PebbleStore once pebble_store.go is free` comment.

### 5.3 Endpoint

12. In `internal/server/handlers/diagnostics.go`, add
    `func (h *DiagnosticsHandler) GetDBCensus(c *gin.Context)`:
    - `deep := c.Query("deep") == "true"`, `fresh := c.Query("fresh") == "true"`;
    - resolve `database.AsCapability[database.DBCensusProvider](h.store)`. On
      failure, call `httputil.RespondWithInternalError(c, "db census requires the Pebble store")`;
    - call it with `c.Request.Context()`. Respond with
      `httputil.RespondWithOK(c, census)`, or with the internal error and a
      `slog.Warn` on failure;
    - doc comment: `// GET /api/v1/diagnostics/db-census[?deep=true][&fresh=true]`.
      Explain that `deep` adds the `book_ver:` keys-only pass, which
      iterates millions of keys and is never the default.
13. In `internal/server/wire_media_routes.go`, add, right after `:68`:
    `protected.GET("/diagnostics/db-census", s.perm(auth.PermSettingsManage), diagH.GetDBCensus)`.
    Use the same permission as db-health.

## 6. Do not touch

- `GetDBHealth`, `resolveKeyCounter`, `KeyCount`, `ScanPrefix`, `CountPrefix`,
  `handlers/cache.go`, `ai_scan_store.go`. All are TASK-A3.
- `internal/database/pebble_store.go` (A4), `pebble_store_ops_v2.go` (A5),
  `docs/database-pebble-schema.md` (A5), `internal/server/server.go` (A1),
  `internal/server/server_lifecycle.go` (A5).
- `database.Store`, `iface_*.go`, `mocks/`. No interface widening, no mock
  regeneration.
- `db.ScanStatistics` (`$P/db.go:2969`) iterates everything. Never call it.

## 7. Tests

- `internal/database/keyfamilies_test.go`:
  - `TestKeyFamilies_NoDuplicatePrefixes`.
  - `TestKeyFamilyRanges_PartitionWholeKeyspace`. The ranges are sorted;
    `ranges[i].Hi == ranges[i+1].Lo`; the first `Lo` is nil and the last `Hi`
    is nil.
  - `TestKeyFamilyRanges_ParentExcludesChildren`. `book:asin:x` maps to
    `book:asin:`, `book:01ABC` maps to `book:`, `opv2:log:x` maps to
    `opv2:log:`, and `zzz:` maps to `(unregistered)`. Add a helper
    `familyForKey([]byte) string` that does a binary search over the ranges,
    and test it.
  - `TestKeyFamilies_A5IndexesPreRegistered`. `opv2:open:` and `opv2:done:`
    are present.
- `internal/database/census_test.go`, each test on a small real store from
  `NewPebbleStoreInMemory(t.TempDir())`:
  - `TestDBCensus_ExactFamiliesInSeparateTables`. Write 500 `book_ver:` keys,
    then `p.db.Flush()`. Write 300 `opv2:log:` keys, then flush. Assert those
    two families report exactly 500 and 300 keys with `Estimated == false`.
  - `TestDBCensus_StraddlingTableIsEstimatedAndConserved`. Write 200
    `book_file:` and 200 `fpidx:` keys in one batch, then flush. Assert both
    families have `Estimated == true`, and that Σ `Keys` over all families
    equals `TotalKeys` within ±len(families). Do NOT assert exact
    apportioned values: tiny tables can return `ApproximateSpanBytes == 0`.
  - `TestDBCensus_RetiredFromMemdb`. Create 3 books: 1 live, 1 soft-deleted
    with 2 files, 1 merged with 1 file. Call `p.WaitForWarmup()`. Assert
    `RetiredBooks == 2` and `RetiredBookFiles == 3`.
  - `TestDBCensus_HistoryOnlyWhenDeep`. Write `book_ver:` keys: book A × 3,
    book B × 12. Assert `History == nil` without Deep. With Deep, assert
    `Entries == 15`, `Max == 12`, `Buckets["1-9"] == 1`,
    `Buckets["10-49"] == 1`, and `OrphanBooks == 2` (neither book has a row).
  - `TestDBCensus_CachedWithinTTL`. Two calls; the second has
    `Cached == true`. A call with `Fresh: true` has `Cached == false`.
  - `TestDBCensus_ClosedStoreReturnsError`. Close the store, then call.
    Assert an error and no panic.
- `internal/server/handlers/diagnostics_census_test.go`:
  - `TestGetDBCensus_ReturnsFamilies`. Build the handler around an in-memory
    PebbleStore; GET without params. Assert status 200, a `families` array
    containing `(unregistered)`, and that `history` is absent or null.
  - `TestGetDBCensus_DeepAddsHistory`.
- Anti-over-suppression: N/A. The census is read-only and rejects or hides
  nothing; its only "absent" outputs are `Retired`, `Signals` and `History`,
  set to nil with a note when memdb is not warm or `deep` is off. The normal
  path is pinned by `TestDBCensus_ExactFamiliesInSeparateTables` (a healthy
  store reports exact counts for its families) and
  `TestDBCensus_RetiredFromMemdb` (a warm store reports non-nil `Retired` and
  `Signals`), both of which fail if the census suppressed its figures.

## 8. Verify

```bash
go build ./...
go vet ./internal/database/... ./internal/server/...
go test -race -count=1 -run 'KeyFamil|DBCensus' ./internal/database/
go test -race -count=1 -run 'DBCensus' ./internal/server/handlers/
go test -race -count=1 ./internal/server/handlers/
make lint-errcheck-ratchet
make lint-width
make sdkguard
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required.

Timing check: on a local store, `curl -sk https://localhost:8484/api/v1/diagnostics/db-census`
(with auth) must answer in under 2 s without `deep`. Record the time. Also
time it on the largest store you can reach: the dedup sandbox on `:8485` if
one is running, otherwise the largest local DB. Record the cold time (first
call after start, with `fresh=true`), the warm time (second call with
`fresh=true`) and `total_tables`. The local store is tiny, so these numbers
are what predict prod.

## 9. Deliverables

- Go version headers on every new file; bump the version and `last-edited`
  on `diagnostics.go` and `wire_media_routes.go`. Generate guids with
  `uuidgen | tr A-Z a-z`.
- Fragment `changelog.d/<YYYYMMDD>_storage_a2_db_census.md`, no header,
  category `### Added`, one `####` entry. Never use `#` or `##`.
- Check that `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"` prints
  nothing.
- Commit with exactly
  `feat(diagnostics): key-family registry and db-census endpoint from sstable properties`
  (`<type>(<scope>): ...` form), ending with:

  ```
  Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/feat/storage-a2-db-census"`
- `gh pr create --base main --head feat/storage-a2-db-census`. Do NOT merge.

## 10. Exit criteria and report

- [ ] `GET /api/v1/diagnostics/db-census` returns families, totals, retired
      and signal counts, and notes.
- [ ] `?deep=true` adds `history`.
- [ ] The census never calls `NewIter(nil)` or decodes a value.
      `grep -n 'NewIter(nil)\|json.Unmarshal' internal/database/census.go`
      prints nothing.
- [ ] All tests in section 7 pass with `-race`.
- [ ] `make lint-errcheck-ratchet`, `make lint-width` and `make sdkguard`
      pass.
- [ ] PR open, not merged.

Report:

```
TASK-A2 report
head sha: <sha>
PR: <url>
files changed: <list>
registry: <N> families (<M> top level); prefixes dropped from the seed list: <list>; added: <list>
tests: <name> PASS (<time>) ...
local census timing: shallow <ms>, deep <ms>
largest-store census: store <which>, total_tables <n>, cold <ms>, warm <ms>
not done / deviations: <list or "none">
```
