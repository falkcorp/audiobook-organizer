<!-- file: docs/perf-audit-2026-05-29-getall-callers.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d9b9f4a-3c1f-46df-91f3-8f3e2db84a4f -->
<!-- last-edited: 2026-05-29 -->

# Perf Audit: GetAll\* Callers in Request Paths (MAYDEPLOY-C3)

**Date:** 2026-05-29
**Trigger:** PR #1149 (aggregateFileMetadata blew 46GB heap by materializing 308K BookFiles to enrich 20-book page) and PR #1153 (GetBookFilesForIDs did full Pebble scan despite ID filter). The bug class is "fetch the entire corpus to filter/project a tiny page-sized slice in request paths." This audit enumerates every other place we do that.

**Scope:** every `GetAll(Books|Authors|Series|BookFiles|BookAuthors|BookNarrators|Tags|Embeddings|Works|ImportPaths|AuthorAliases|BlockedHashes|UserBookStates|Operations|Candidates)` caller under `internal/server/`, `internal/audiobooks/`, `internal/dedup/`, `internal/scheduler/`, `internal/itunes/`, `internal/maintenance/`, `internal/scanner/`, `internal/diagnostics/`, `internal/reconcile/`, `internal/sweep/`, `internal/work/`, `internal/fileops/`, `internal/sysinfo/`, `internal/quarantine/`, `internal/organizer/`, `internal/metafetch/`, `internal/deluge/`, `internal/aiscan/`, `internal/writeback/`, `internal/importer/`, `internal/batch/`, `internal/plugins/`. Implementations under `internal/database/` deliberately excluded.

Total caller hits (non-test): **~140**. Test code: **~200**. Database implementations: **508**.

---

## Summary

| Severity | Count | Definition |
|---|---|---|
| **HOT-BAD** | **8** | Synchronous HTTP handler / request-path that fetches whole corpus to filter a small subset. Memory or latency bomb. |
| **HOT-OK** | **4** | Synchronous handler that legitimately returns the full corpus (export, list-all w/ cache). Acceptable. |
| **WARM-BAD** | **2** | Frequent background path that fetches whole corpus to filter a tiny slice. Should pushdown. |
| **WARM-OK** | **~22** | Long-running background ops (backfills, scheduled scans, manual maintenance, AI review). Full scan is the work. |
| **COLD** | **~104** | Startup-only paths (one-shot backfills with skip-flag), nightly scheduler jobs, maintenance jobs, scanner pipeline. Acceptable. |

**Reassuring finding:** the `GetAllBooks` / `GetAllAuthors` / `GetAllSeries` / `GetAllBookFiles` / `GetAllWorks` paths on PebbleStore **already have a memdb fastpath** (PR #1166 for BookFiles; the others were already memdb-routed). So a "full scan" today is mostly a pointer walk over the in-memory table — cheap on memory and CPU once memdb is published. The remaining BAD entries below all share **one or both** of these properties:

1. **Cold start window:** memdb is not yet published, so the call falls back to Pebble prefix-scan + JSON unmarshal across the entire keyspace. This is the #1149-equivalent landmine — a single such call during the warmup window can OOM the process.
2. **N+1 amplifier:** the GetAll is called *per scan item* or *per request item* (e.g., scanner per-book `GetAllWorks`), so even the memdb pointer walk becomes O(N²).

The HOT-BAD entries below are all rankable on these two axes.

---

## HOT-BAD (fix candidates — same class as #1149/#1153)

### 1. `internal/server/itunes_handlers.go:607` — `handleListITunesBooks`

- **Caller:** `GET /api/v1/itunes/books` (paginated list)
- **Pattern:** `store.GetAllBooks(0, 0)` → iterate to filter `ITunesPersistentID != ""`, then slice for pagination
- **Cost:** ~50K books × Book struct (~1KB) ≈ 50MB heap per request during memdb miss, plus full-scan deserialize cost. Even on hot memdb path it's a 50K pointer walk + linear filter to return 20 rows.
- **Fix:** add a `book_itunes_pid` secondary index in memdb (the index already exists in PebbleStore as `book_file_pid:` for BookFile but not on Book; check Book.ITunesPersistentID column-index). Then `ListBooksByITunesPID(limit, offset)`. Falls back to current path.
- **Note:** this handler is the iTunes Linked panel. Loaded on every iTunes-tab page view.

### 2. `internal/server/itunes_handlers.go:534` — write-back preview

- **Caller:** `POST /api/v1/itunes/writeback-preview` when no `book_ids` filter
- **Pattern:** Same `GetAllBooks(0, 0)` + filter by `ITunesPersistentID`
- **Cost:** identical to #1.
- **Fix:** same memdb index. The pinned `if len(req.BookIDs) > 0` fast path already exists; just need the empty case to use the index.

### 3. `internal/server/deluge_discovery.go:134` — `runDelugeDiscovery` handler

- **Caller:** `POST /api/v1/deluge/discover` HTTP handler
- **Pattern:** `store.GetAllBookFiles()` → filter where `DelugeHash != "" && ImportedFromDelugeAt == nil`
- **Cost:** 308K BookFiles. Memdb fastpath via PR #1166 reduces this to a pointer walk (~20ms) but it still pulls every row to filter ~hundreds.
- **Fix:** call existing `store.GetBookFilesNeedingDelugeImport()` (already implemented in pebble_store.go:8515) — currently *also* a `GetAllBookFiles` wrapper, but at least it concentrates the fix. Better: add a memdb index keyed on `deluge_hash` (non-empty) and route via `GetBookFilesNeedingDelugeImport_Mem`. Same 5-line memdb-fastpath pattern as #1153/#1166.
- **Synchronous handler — user-facing, returns latency.**

### 4. `internal/server/entities_handlers.go:118` — `listWork`

- **Caller:** `GET /api/v1/works`
- **Pattern:** `store.GetAllWorks()` then *for each work* `GetBooksByWorkID(work.ID)`
- **Cost:** N+1 amplifier. Works table is ~50K rows (one per audiobook in steady state). For each work we look up its books → 50K + 50K secondary lookups per request. Even on memdb that's 5-10s.
- **Fix:** add `GetWorkBookCounts() map[string]int` (mirrors `GetAllAuthorBookCounts` / `GetAllSeriesBookCounts`), call once, fold into the works iteration. Avoids the N+1.
- **Synchronous handler.**

### 5. `internal/server/entities_handlers.go:154` — `getWorkStats`

- **Caller:** `GET /api/v1/works/stats`
- **Pattern:** identical to #4 — GetAllWorks + per-work GetBooksByWorkID, just for count statistics.
- **Cost:** identical.
- **Fix:** same. Use `GetWorkBookCounts()`; never load Book structs at all here.

### 6. `internal/server/metadata_batch_candidates.go:846` — unfetched count

- **Caller:** `GET /api/v1/metadata/candidates` when `include_unfetched=true` or `status=unfetched`
- **Pattern:** `store.GetAllBooks(0, 0)` to delta against the metadata-results map to compute "unfetched" set
- **Cost:** 50K book structs per request, only to extract IDs. On memdb miss this is the #1149 pattern verbatim — full Pebble scan + JSON unmarshal.
- **Fix:** add `store.ListBookIDs()` that returns only `[]string` (no struct deserialize). Pebble can iterate keys without `iter.Value()` calls; memdb can project from the books table without copying. Saves ~50× memory.

### 7. `internal/server/metadata_handlers.go:1283` — metadata-fetch-ids op author lookup

- **Caller:** `runMetadataFetchByIDs` op (HTTP-initiated background op, but runs per-user-request)
- **Pattern:** `store.GetAllAuthors()` → build `authorByID` map for *every* metadata-fetch-by-ids invocation, even when `bookIDs` length is 20.
- **Cost:** 8,837 authors per op. Authors are 200B each → 1.8MB per op. Modest, but it's allocated on every batch invocation.
- **Fix:** when `len(bookIDs)` is small (< some threshold like 100), look up authors per-book via `GetAuthorByID`. Or: only resolve authors for the books that we actually process.
- **WARM-leaning — flag for cleanup but lower priority.**

### 8. `internal/server/system_handlers.go:46,51` — `/health` author + series count

- **Caller:** `GET /health` HTTP handler — pinged frequently by uptime monitors / k8s probes
- **Pattern:** `GetAllAuthors()` + `GetAllSeries()` to compute `len(authors)` / `len(series)`
- **Cost:** with memdb it's a pointer walk (cheap), but cold-start fallback materializes 8,837 + 21,668 structs *every health check* during warmup.
- **Fix:** call `store.CountAuthors()` / `store.CountSeries()` (`CountBooks` already used on line 41). Avoids materializing structs entirely.
- **EASY 5-line fix — applied in this PR.** See "Applied wins" below.

---

## WARM-BAD (background, but happens often enough to matter)

### W1. `internal/scanner/scanner.go:1533, 1551` — per-book Work lookup in scanner

- **Caller:** scanner pipeline, runs `GetAllWorks()` *for every book being scanned* to deduplicate against existing works
- **Cost:** scanner can process ~10K books in one run. With ~50K works each call walks 50K pointer entries. That's 5×10⁸ comparisons for a single scan = ~10 minutes pure CPU.
- **Fix:** scanner should build a `map[normalizedTitle+authorID]workID` once at the start of the scan, then look up O(1) per book. Invalidate on new-work creation.
- **Filed as MAYDEPLOY-H3.**

### W2. `internal/server/server_middleware.go:90` and `internal/audiobooks/helpers.go:248` — `isProtectedPath`

- **Caller:** called per-file in many pipelines (rename, organize, delete) — quite frequent
- **Pattern:** `store.GetAllImportPaths()` per call
- **Cost:** small (~10-20 rows) but repeated thousands of times in a single batch op
- **Fix:** cache `importPaths` with short TTL or invalidate on import-path mutation. Same pattern as `authorsCache`. Memdb already makes the read cheap, but it's still avoidable per-call work.
- **Filed as MAYDEPLOY-H4. Low-priority.**

---

## HOT-OK (legit full-corpus returns)

| Location | Endpoint | Why OK |
|---|---|---|
| `server/metadata_handlers.go:116` | `GET /api/v1/metadata/export` | Export is by definition the whole corpus. |
| `server/system_handlers.go:128` | `GET /api/v1/system/announcements` | Needs all authors for duplicate detection. Could be cached, but result already feeds `dedupCache`. |
| `audiobooks/author_series.go:62,77,127,142` | `GET /api/v1/authors`, `/series` (+ with-counts variants) | Returns full list — that *is* the endpoint contract. Backed by `authorsCache` / `seriesCache` (PR #1053). |
| `server/filesystem_handlers.go:108` | `GET /api/v1/import-paths` | ~10-20 rows total. |
| `server/system_handlers.go:556` | `GET /api/v1/blocked-hashes` | List endpoint, small corpus (<1K rows typically). |

---

## WARM-OK (background ops; full scan is the work)

These are all legitimate full-corpus iterations in maintenance, scheduling, or AI/dedup ops. They run via `opsregistry` / `scheduler` / manual-trigger endpoints — paginated where possible, batch-flushed throughout.

- `internal/server/metadata_handlers.go:913,928,1451,1687,1451` — metadata-fetch-all op, metadata refresh scan, isbn-enrich.
- `internal/server/ai_handlers.go:491,600` — AI author review (groups + full mode).
- `internal/server/duplicates_handlers.go:465,568,707` and `duplicates_ops.go:212` — series prune, author duplicate scan.
- `internal/server/operations_handlers.go:239` — `optimizeDatabase` (manual maintenance endpoint).
- `internal/server/maintenance_fixups.go:119,257` — one-shot startup fixups with skip-flag.
- `internal/server/acoustid_backfill.go:120` — backfill loop.
- `internal/server/embedding_backfill.go:70,110` — backfill loop.
- `internal/server/external_id_backfill.go:74,75` — backfill adapter pass-through.
- `internal/server/server_search.go:72` — search index full backfill (one-shot startup).
- `internal/server/server_lifecycle.go:458,515,555` — import-path watcher init.
- `internal/server/server_helpers.go:93` — import-path lookup (helper).
- `internal/server/library_size_refresh_op.go:48` — scheduler op.
- `internal/server/quarantine_known_bad.go:28` — one-shot startup.
- `internal/audiobooks/service.go:1283` — fingerprint-filter count fallback (hit only when caller passes `?fingerprint=...`, rare).
- `internal/dedup/engine.go:354,1646` — book/author dedup batch iteration.
- `internal/dedup/series_dedup.go:142,160,270` — series dedup ops.
- `internal/dedup/split_book_detector.go:114` — split-book detector batch.
- `internal/diagnostics/service.go:244,305,332,337,342` — diagnostics ZIP export.
- `internal/itunes/backfill.go:53,142` — startup-with-skip-flag backfills.
- `internal/itunes/rebuild.go:49,266,318` — manual iTunes rebuild op.
- `internal/itunes/service/importer.go:491,724,766,808,885,933` — iTunes import operation (manual + scheduled).
- `internal/itunes/service/path_reconcile.go:70` — path reconciliation op.
- `internal/itunes/service/position_sync.go:72` — iTunes position sync scheduled op.
- `internal/scheduler/extra_ops.go:233,655,801` — scheduled author-split / series-prune / book-scan ops.
- `internal/maintenance/jobs/*.go` (every file) — nightly maintenance jobs.
- `internal/plugins/deluge/centralization.go:66` — deluge centralization plugin op (background).

---

## COLD (startup or rare)

- Tests (`*_test.go`) — all 200+ test callers, irrelevant.
- One-shot adapters (`external_id_backfill.go:74`) — pure pass-through.
- Startup backfills with persisted skip-flag (`acoustid_backfill`, `quarantine_known_bad`, `embedding_backfill`, `search` backfill).

---

## Hidden full-corpus patterns (not named GetAll\*)

Searched for `Pebble*ScanAll`, `iterPrefix`, raw `NewIter` in non-database callers, `scanAll`, `fullScan`, and `GetBookFilesNeedingDelugeImport`. Findings:

- `database.PebbleStore.GetBookFilesNeedingDelugeImport` (pebble_store.go:8515) is just a `GetAllBookFiles` wrapper. Same class as #3 above — should get a memdb index pushdown.
- No raw `pebble.NewIter` calls in caller code outside `internal/database/`. Good.

---

## Applied wins (in this PR)

### Win 1 — `/health` author/series count via `CountAuthors` / `CountSeries`

`internal/server/system_handlers.go:46-55` — replaced `GetAllAuthors()`/`GetAllSeries()` with `CountAuthors()`/`CountSeries()`. Matches the existing `CountBooks()` call three lines above. Eliminates struct-materialization on every health probe.

*Acceptance:* curl `/health`, response unchanged, `metrics.authors` / `metrics.series` still populated.

### Other candidates evaluated, deferred

The remaining HOT-BAD entries (#1, #2, #3, #4, #5, #6) all need a new memdb secondary index or a new dedicated store method (`GetWorkBookCounts`, `ListBookIDs`, `ListBooksByITunesPID`). These are not 5-line fixes — each gets its own subtask under MAYDEPLOY-H below.

---

## MAYDEPLOY-H — Followups from this audit

See TODO.md → `MAYDEPLOY-H` section for the actionable subtasks (H1-H4).
