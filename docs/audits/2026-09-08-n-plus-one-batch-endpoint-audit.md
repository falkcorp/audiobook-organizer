<!-- file: docs/audits/2026-09-08-n-plus-one-batch-endpoint-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b41e9c7-5d20-4a83-91fe-0c7d3846ab52 -->
<!-- last-edited: 2026-09-08 -->

# N+1 / batch-endpoint audit — Go backend (2026-09-08)

Triggered by a production search taking 13.8–28.0 s. Root cause of *that* is fixed
in PR #3128 (finding 7 below); this document is the sweep for the same shape
elsewhere.

**Read `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` alongside
this.** That audit covers loops that are *serial when they should be parallel*.
This one covers loops that issue *one query per item when one query would do*.
They overlap but are not the same defect, and a few sites here are correctly
parallel and still N+1.

## Two premise corrections, recorded because both were believed on the way in

**`GetBookFiles` has no memdb branch BY DESIGN.** `memdb_strip.go:118` strips
`AcoustIDFingerprint`, the fingerprint diagnostic fields and `IntroTranscription`
from every memdb row, and `pebble_store_bookfiles.go:670-676` states the
contract: a caller needing any of those MUST use `GetBookFiles` (full Pebble).
It is the full-fidelity escape hatch, not an oversight. **Do not add a memdb
branch to it** — that would silently nil those fields for the dedup engine. The
correct fix for Core-only callers is to move them to `GetBookFilesForIDsCore`.
It is also prefix-bounded (`book_file:<bookID>:`), so it is O(that book's files),
not a scan. The dramatic "15 s → <1 ms" numbers in its doc comment describe its
own *Pebble fallback* path, which is a genuine full scan of all ~726K rows.

**`registry.RunItems` concurrency was NOT the problem here.** CLAUDE.md warns
that `RunItemsOptions.Concurrency` defaults to sequential. Checked on every
background op below: `relink_unlinked.go:151`, `regroup_shattered_ai.go:223`,
`duration_backfill.go:135`, `chapters_backfill.go:439`, `title_repair.go:318`,
`backfill_sync_ids.go:134` and `backfill_itunes_positions.go:253` all pass it;
`auto_match_transcribed.go:209` sets `Concurrency: 1` deliberately. The
genuinely single-threaded ones are plain `for` loops that never went through
`RunItems` at all: `generate_itl_tests`, `fix_read_by_narrator`,
`recompute_book_aggregates`, `backfill_book_files`.

## Dispatch check

`h.library` is not a bare `*PebbleStore`, so this was verified before anything
else: `indexedStore` (`internal/server/indexed_store.go:44`) embeds
`database.Store` and overrides **only** `CreateBook`/`UpdateBook`/`DeleteBook`
(`:57`, `:80`, `:104`). Every read dispatches to `*PebbleStore`. The claims below
describe code that actually runs.

## Part A — what batch methods exist

| Method | file:line | Actually batched? | memdb fast path? |
|---|---|---|---|
| `GetBookFilesForIDsCore(bookIDs)` | `pebble_store_bookfiles.go:678` | **Yes** | **Yes** → `memdb_reads.go:1008`. Fallback `:685` is a full scan |
| `GetAllBookFilesCore()` | `pebble_store_bookfiles.go:~712` | Yes | Yes |
| `GetAllBooksCore(limit, offset)` | `pebble_store.go:599` | Yes | **Yes** (`:600`) |
| `GetBooksByIDs(ids)` | `pebble_store.go:1341` | **No — internal loop**, plus `hydrateBookSig` = a *second* get per book | No |
| `GetAuthorsByIDs(ids)` | `pebble_store_authors.go:77` | **No — dedupe loop** over point-gets | No |
| `GetSeriesByIDs(ids)` | `pebble_store_series.go:75` | **No — dedupe loop** | No |
| `GetAuthorsByBookIDs(ctx, ids)` | `pebble_store_authors.go:1033` | **No** — per-book + per-author gets, no cross-book dedupe, `ctx` ignored | No |
| `GetNarratorsByBookIDs(ctx, ids)` | `pebble_store_authors.go:1059` | **No** — identical shape | No |
| `GetBookFiles(bookID)` | `pebble_store_bookfiles.go:630` | single-book | **No, deliberately** (see above) |
| `GetChaptersForBook(bookID)` | `pebble_store_chapters.go:40` | single | No. **No `…ByBookIDs` sibling exists** |
| `MintOrGetSyncID` / `MintOrGetSyncFileID` | `syncid.go:146` / `syncfile.go:88` | single | n/a — **fixed in #3128** |
| `BatchCreateBookFiles`, `BatchUpsertBookFiles`, `DeleteBookFilesByIDs`, `MoveBookFilesToBookBulk`, `BulkCreateExternalIDMappings` | bookfiles `:377`,`:1399`,`:1245`,`:1617`; externalids `:236` | Yes — genuinely batched | n/a |

**The headline of that table:** `memdb_schema.go:214/231/292/313` declares warm
`id` indexes on `authors`, `series`, `narrators` and a `book_id` index on
`book_authors`. **Zero getters read them.** The data is in RAM with exactly the
right indexes, and every author/series/narrator lookup in the codebase goes to
disk.

## Part B — ranked findings

Ordered by (request path > background) then cardinality then whether a drop-in
batch method already exists.

### 1. HIGH — abs mapper fetches book files one book per page item
`internal/server/handlers/abs/mapper.go:121` (errgroup at `NumCPU`), call at
`:163` → `GetBookFiles(book.ID)`. Fix: **`GetBookFilesForIDsCore`
(`pebble_store_bookfiles.go:678`)**, which slots alongside the three existing
pre-loop batch calls at `:102-116`. Every field abs reads off `fileView.File`
(`mapper.go:258,259,435,605,613-652,747-780,868-876`, `play.go:233`) is present
on `BookFileCore` — checked field by field. The memdb path does not sort, but the
mapper already sorts at `:170`. Bounded by page size (`defaultPageLimit = 50`,
`maxPageLimit = 250`, `browse.go:44-50`); 7 call sites. **HTTP request path.**

### 2. HIGH — `ListCachedCandidates`: its twin in the same file was already fixed
`internal/server/handlers/metadata_cache.go:128` → `:129` `GetBookByID` per row.
The sibling `GetCacheReviewResults` at `:205` already uses `GetBooksByIDs`, and
its comment (`:191-197`) prices the pattern: *"2N sequential point reads.
Production served it in 21.7 s and 35.2 s, which is what was timing the UI out."*
Someone fixed one handler and left its neighbour. Source
(`ListCachedSummaries(ctx)`, `:118`) takes **no limit** — the whole metadata
cache. **HTTP:** `GET /api/v1/audiobooks/metadata/cached`.

### 3. HIGH — `GetAuthorsByBookIDs` is a fake batch, and the mapper comment says otherwise
Call sites `mapper.go:102` / `:106` look batched; the loops are **inside the
store** at `pebble_store_authors.go:1039` (per book) and `:1044` (per author),
mirrored at `:1065`/`:1070`. `GetAuthorsByIDs` is itself a dedupe loop, so there
is no drop-in — this needs a new memdb-backed method reading the indexes named
above. A 250-book page issues ~500 relation gets plus ~500-1000 point-gets.

> **`mapper.go:82-84` asserts the opposite of what the code does:** *"The
> relation lookups in front of it are BATCHED (one call for all authors, one for
> all narrators, one for all series) so a page never issues a query per book for
> them."* It issues a query per book for all three. This is the CLAUDE.md worked
> example verbatim — a comment explaining why something is fine, outliving its
> reason. Two separate read-only sweeps believed that comment; only opening the
> store implementation caught it.

### 4. HIGH — dedup handlers scan up to 100K candidates with a point-get per book
`dedup/handler.go:769-771` (← `:756`), `:894-895` (← `:885`), `:626`/`:674`
(← `:593`, **nested** at `:596`), `:281-284` (← `:256`) — `GetBookByID` and
`GetAuthorByID`. `Limit: 100000` is hardcoded at `:739-743`, `:868-872`, `:553`;
`bothUnmatchedScanLimit = 1_000_000` (`:62`) flips `ListDedupCandidates` to
whole-table when `?both_unmatched=true` (`:221-229`). Memoized, but still one
read per *distinct* book — up to ~200K IDs × 2 gets each. **HTTP, synchronous.**

### 5. HIGH — `OptimizeDatabase`: memdb hands you the library, then you pay 100K disk gets
`internal/server/handlers/operations/handler.go:294` → `:297` `GetAuthorByID`.
The source list **is** memdb-fast (`GetAllBooksCore` branches at
`pebble_store.go:600`); the per-author read is not. That contrast is the finding.
`GetAllBooksCore(0, 0)` at `:285` = no limit. ~45-100K books, ~17,477 distinct
authors, so batching collapses it ~3-6×. **HTTP, synchronous** —
`POST /api/v1/operations/optimize-database`.

### 6. HIGH — `/api/me` recomputes every book's duration one book at a time
`internal/server/handlers/abs/userdata.go:218` (errgroup) → `:450`
`GetBookFiles`, `:461` `GetBookByID`, via `progressRow:306` → `durationFor`. Fix:
hoist `GetBookFilesForIDsCore` above the errgroup — `bookIDs` is already
materialized at `:207-213`. Cardinality is the user's distinct books with a
stored position. The comment at `:474-476` calls this "bounded," meaning it does
not scan the library — true, and a different claim from "small." **HTTP:**
`GET /api/me`, hit on every login and refresh.

### 7. FIXED in #3128 — the sync-id mints serialized the mapper's worker pool
`mapper.go:158` (per book) and `:209` (per **file**) took package-global mutexes
(`syncIDMintMu`, `syncFileMintMu`) **held across `s.db.Get` and across a
`pebble.Sync` commit**, making `errgroup.SetLimit(NumCPU)` at `:120` theatre. A
250-book page ≈ 250 + 10,000 mutex-serialized round trips.

Still open as a follow-up: the index key
`sync_file:book:<bookID>:<syncFileID>` stores the **fileID as its value**, so one
prefix scan per book yields the whole fileID→syncFileID map with zero record
gets — a `GetSyncFileIDsForBook` that `ListSyncFilesForBook` (`syncfile.go:164`)
stops just short of. `RepointSyncFile` does keep that index current (verified),
so the idea is sound; #3128 left it out because with the lock gone the remaining
point-gets are concurrent and cheap, and it should be done on measured evidence.

### 8. MEDIUM — per-book chapter read, no batch exists
`mapper.go:200` → `loadChapters` → `GetChaptersForBook(bookID)` at `:244`.
**No `…ByBookIDs` sibling anywhere in `internal/database/`** — needs a new
method. Same page bound and request path as finding 1. Also hit at
whole-library scale by `chapters_backfill.go:349`.

### 9. MEDIUM — iTunes and playlist read loops
`itunes.go:637` `ListBooksByITunesPID(0,0)` → `:660` `GetAuthorByID`
(unbounded, `POST /api/v1/itunes/write-back/preview`); `itunes.go:611/612`
`GetBookByID` over **uncapped client-supplied** `req.BookIDs`; `itunes.go:752/755`
(page-bounded). `playlists.go:481/482` `GetBookByID` per entry over a fully
materialized smart playlist (`GET /api/v1/playlists/:id/export.m3u`).

### 10. MEDIUM — background jobs, whole-library, batch API exists
Ranked last despite the largest cardinality because none is on a request path.
Cardinality source is `ListBookIDs()` or `GetAllBooksCore(0,0)` in every row.

| Loop | Per-item call | Batch fix |
|---|---|---|
| `plugins/maintenance/relink_unlinked.go:118` | `:119` `GetBookByID` + `:126` `GetBookFiles` | both |
| `maintenance/jobs/generate_itl_tests.go:51` | `:55` `GetBookFiles`, **serial, no pool** | **`GetAllBookFilesCore()` deletes the loop** |
| `maintenance/jobs/fix_read_by_narrator.go:45` | `:56` `GetAuthorByID`, **serial** | `GetAuthorsByIDs`; distinct authors ≪ books |
| `plugins/maintenance/regroup_shattered_ai.go:159` | `:160` + `:164` | both |
| `maintenance/jobs/recompute_book_aggregates.go:170` | `:180` + `:187`, **serial** | both |
| `duration_backfill.go:107`, `title_repair.go:215`, `backfill_sync_ids.go:76`, `backfill_book_files.go:56` | `GetBookFiles` | `GetBookFilesForIDsCore` |
| `chapters_backfill.go:343` | `:349` chapters, `:354` files | files only; no batch chapter getter |
| `auto_match_transcribed.go:125`, `backfill_itunes_positions.go:225`, `cleanup_orphan_embeddings.go:184` | `GetBookByID` | `GetBooksByIDs` |

### 11. LOW — loops that refetch the same book's file list once per file
`fix_book_file_paths.go:41→:56`, `enrich_book_files.go:46→:69`,
`recompute_itunes_paths.go:41→:56`, `acoustid/lsh_backfill.go:109→:122`,
`tag_backfill.go:112→:147`, `dedupe_book_file_rows.go:249→:253`. All iterate
`GetAllBookFilesCore()` (~726K rows) and call `GetBookFiles(c.BookID)` per
*file*. **The fix is group-by-`BookID`, not a batch API swap.** All are gated
behind `!dryRun` plus a per-file predicate, so the absolute count is bounded by
rows needing repair. `recompute_itunes_paths` is widest — a mapping-rule change
makes it fire on most of the library.

## Checked and dismissed — not findings

- `abs/userdata.go:483-524` `ListenedSeconds` — errgroup over per-book
  `GetUserBookState`, but **no library access at all**, no batch method exists,
  and the fail-soft path is documented as cosmetic at `:478-482`.
- `abs/progress.go:368`, `abs/stream.go:116` — one book per request.
- `dedup/handler.go:410` `fetchBook` — called exactly twice.
- `diagnostics.go:550/627`, `entities/handler.go:494/1128`,
  `metadata/handler.go:927` — read-then-`UpdateBook` loops, `applycap`-bounded.
  Write loops; batching the read would not help.
- ~11 plugin "hydrate-before-write" sites (`fix_version_groups.go:206`,
  `normalize_primary_flags.go:106`, `cleanup_series.go:233`, …) — the read is
  gated on `!dryRun` **and** on the item actually changing, so call count equals
  mutation count. This is a **deliberate, documented** pattern: writing a slim
  Core struct through `UpdateBook`/`UpdateBookFile` wipes fingerprints. See
  `docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md`.
- ~16 sites bounded by breakage count, ~18 bounded by a candidate/plan/group list.
- `acoustid/reset_all.go:111` looks like a 726K-row N+1, but `:95-102` documents
  a bulk-clear fast path in prod; the per-row branch is mock/sqlite-test only.

**Unrelated, spotted in passing:** `merge_same_path_dupes.go:366-372` does a
linear `for i := range groups` scan *inside* the per-group callback to recover
the index — O(n²) in group count, no DB involved.

## Suggested order

1 (drop-in, request path, memdb-backed, verified field-by-field) → 2 (precedent
already in the file) → 5 and 4 (largest request-path cardinality) → 3 + the
finding-7 follow-up (need new memdb-backed methods; fixing 3 also deletes a false
comment) → 8 → 10.
