<!-- file: docs/archive/audits/2026-05-01-structure-audit.md -->
<!-- version: 1.0.1 -->
<!-- guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890 -->
<!-- last-edited: 2026-08-11 -->

# Structure & Refactor Audit — 2026-05-01

Evidence-based analysis of file size, code duplication, package cohesion, interface
segregation, and frontend structure. Findings are ranked by impact.

---

## Summary

| Area | Finding | Severity |
|------|---------|---------|
| File sizes | 6 files over 1600 lines; two exceed 6000 | 🔴 High |
| HTTP helpers | 287 `c.JSON(http.` calls; 36 identical "db not initialized" responses | 🔴 High |
| Pagination | 376 limit/offset mentions with no shared helper | 🟠 Medium |
| Retry logic | Duplicated in 3+ AI files | 🟠 Medium |
| server/ package | 105 Go files, 12+ domains in one package | 🟠 Medium |
| Frontend pages | 4 components over 2700 lines; 148 duplicated loading patterns | 🟠 Medium |
| Interface use | Full `Store` passed where narrow interface suffices | 🟡 Low |
| Path normalization | 611 scattered `ToLower/TrimSpace/Clean` calls | 🟡 Low |

---

## 1. Giant Files

### `internal/database/sqlite_store.go` — 6976 lines

Domains crammed into one file:

| Domain | Examples |
|--------|---------|
| Schema bootstrap | `createTables`, `ensureExtendedBookColumns` |
| Auth / sessions / API keys | `CreateUser`, `CreateSession`, `CreateAPIKey`, `CreateInvite` |
| User state / preferences | `SetUserPosition`, `GetUserBookState`, `SetUserPreference` |
| Books / segments / files | `CreateBookSegment`, `MergeBookSegments`, `GetBookVersion` |
| Playlists | `ListUserPlaylists`, `GetPlaylist` |
| Activity / settings / metadata / cache / ops | scattered throughout |
| Scan helpers / null converters | repeated inline scan patterns |
| Stub methods | dozens returning `nil, nil` |

**Proposed split:**
```
sqlite_store_core.go          — db open/close/schema/migrations
sqlite_store_users.go         — auth, sessions, roles, API keys, invites
sqlite_store_books.go         — book CRUD, segments, files, versions
sqlite_store_playlists.go     — playlists, positions, user state
sqlite_store_metadata_ops.go  — metadata fetch cache, AI scan store
sqlite_store_activity.go      — activity, settings, cache stats
sqlite_store_util.go          — scan helpers, null converters
```

---

### `internal/server/maintenance_fixups.go` — 6400 lines

Eight clear maintenance domains:

| Domain | Lines (approx) |
|--------|---------------|
| Read-by / narrator fixups | 78–600 |
| Series cleanup / merge / normalize | 600–1100 |
| Filesystem repair (backfill, empty folders) | 1100–1700 |
| Author/narrator swap, version-group fixes | 1700–2100 |
| Dedup / book merge | 2100–2700 |
| iTunes path / ITL / backup | 2700–3900 |
| Wipe endpoints | 3900–4400 |
| Hash backfill / chapter groups / duplicate files | 4400–6400 |

**Proposed split:**
```
maintenance_readby.go    maintenance_series.go
maintenance_files.go     maintenance_dedup.go
maintenance_itunes.go    maintenance_scans.go
maintenance_wipe.go      maintenance_hashes.go
```

---

### `internal/metafetch/service.go` — 3932 lines

| Domain | Key functions |
|--------|--------------|
| Wiring / config setters | `NewService`, `Set*` (lines 58–115) |
| Enrichment queue / source-chain | lines 118–260 |
| Fetch / search / apply pipeline | `FetchMetadataForBook`, `ApplyMetadataCandidate`, `persistFetchedMetadata` |
| Scoring / rerank | `computeF1Base`, `ScoreOneResult`, `RerankTopK` |
| Normalization / parsing | `NormalizeMetaSeries`, `ParseSeriesFromTitle` |
| Writeback / tag embedding | `writeBackMetadata`, `BuildTagMap`, `ApplyMetadataFileIO` |
| Path helpers / ASIN / cleanup | scattered at end |

**Proposed split:**
```
service_wiring.go    service_fetch.go     service_search.go
service_apply.go     service_scoring.go   service_normalize.go
service_writeback.go service_files.go
```

---

### `internal/server/server.go` — 3401 lines

| Domain | Lines |
|--------|-------|
| Metadata state / provenance helpers | ~139–673 |
| Library sizing / response enrichment | ~680–768 |
| Server construction / routing | `NewServer` 886, `Start` 1702, `setupRoutes` 2295 |
| Indexing / search hooks | `IndexBookByID`, `DeleteIndexedBook` |
| Operation resume / recovery | `resumeInterruptedOperations` |
| Middleware / CORS / path protection | scattered |

**Proposed split:**
```
server_bootstrap.go       server_metadata_state.go
server_indexing.go        server_routes.go
server_startup.go         server_path.go
```

---

### `internal/server/audiobook_service.go` — 1891 lines

**Proposed split:**
```
audiobook_list.go     audiobook_read.go
audiobook_write.go    audiobook_tags.go
audiobook_search.go   audiobook_filters.go
```

---

### `internal/server/scheduler.go` — 1686 lines

**Proposed split:**
```
scheduler_registry.go    scheduler_trigger.go
scheduler_runtime.go     scheduler_maintenance.go
scheduler_state.go
```

---

## 2. Common Utility Duplication

### 2a. HTTP JSON response helpers (🔴 HIGH impact)

Evidence:
```
$ grep -c 'c\.JSON(http\.' internal/server/**/*.go
287 total hits
36 × c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
14 × c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
```

Every handler re-types the same gin response patterns. A small helper file would eliminate ~200 lines:

```go
// internal/server/response.go (proposed)
func jsonOK(c *gin.Context, v any)            { c.JSON(http.StatusOK, v) }
func jsonErr(c *gin.Context, code int, err error) { c.JSON(code, gin.H{"error": err.Error()}) }
func jsonBadRequest(c *gin.Context, err error)    { jsonErr(c, http.StatusBadRequest, err) }
func jsonInternalErr(c *gin.Context, err error)   { jsonErr(c, http.StatusInternalServerError, err) }
func jsonNotFound(c *gin.Context, msg string)     { c.JSON(http.StatusNotFound, gin.H{"error": msg}) }
func jsonAccepted(c *gin.Context, v any)          { c.JSON(http.StatusAccepted, v) }
func jsonDBNotInit(c *gin.Context)                { jsonErr(c, http.StatusInternalServerError, ErrDBNotInitialized) }
```

---

### 2b. Pagination helper (🟠 MEDIUM impact)

Evidence: 376 mentions of `limit`, `offset`, `page` across handlers — each parses and clamps
query params independently.

```go
// internal/server/pagination.go (proposed)
type Pagination struct { Limit, Offset int }

func PaginationFromQuery(c *gin.Context) Pagination {
    limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
    offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
    if limit <= 0 || limit > 1000 { limit = 50 }
    if offset < 0 { offset = 0 }
    return Pagination{Limit: limit, Offset: offset}
}
```

---

### 2c. Retry / backoff helper (🟠 MEDIUM impact)

Duplicated in at least 3 AI files:
- `internal/ai/openai_parser.go`
- `internal/ai/metadata_llm_review.go`
- `internal/ai/embedding_client.go`

```go
// internal/ai/retry.go (proposed)
func withRetry(ctx context.Context, maxAttempts int, fn func() error) error
```

---

### 2d. Path / string normalization (🟡 LOW impact)

611 scattered `strings.ToLower`, `strings.TrimSpace`, `filepath.Clean` calls — many are
fine inline, but a dozen callsites normalize the same "author name" or "file path" with
slightly different sequences, creating subtle inconsistencies.

Candidates for `internal/util/normalize.go`:
- `NormalizeAuthorName(s string) string`
- `NormalizeFilePath(s string) string`
- `NormalizeSeriesName(s string) string`

---

## 3. Package Cohesion

### `internal/server/` — 105 Go files (🟠 MEDIUM)

This package is both an HTTP transport layer **and** a business orchestration layer.
It currently covers:
- HTTP handlers (audiobooks, metadata, iTunes, auth, dedup, entities, playlists)
- Scheduler
- Maintenance fixups
- Reconciliation
- AI scan pipeline
- File operations
- Diagnostics

The clearest extraction would be moving the pure-business-logic layer (currently
`audiobook_service.go`, `reconcile.go`, `ai_scan_pipeline.go`) into their own packages
that the HTTP handlers depend on, rather than having it all in one package.

### `internal/database/` — 29 non-test files (✅ Mostly fine)

`Store` is already split into 9+ sub-interfaces (`BookReader`, `BookWriter`,
`BookFileStore`, etc.). The main issue is that some callers still accept the full
`database.Store` when they only need one or two sub-interfaces.

### `internal/metafetch/` — 8 files (✅ Fine as package, `service.go` is the problem)

The package itself is well-scoped. Just needs the 3932-line `service.go` split into
logical files within the same package.

### `internal/itunes/service/` — 17 files (✅ Fine)

Cohesive around iTunes import/sync/repair. The DEP-1 work just completed (removing
deprecated `Book.ITunesPath`) is the right level of cleanup here.

---

## 4. Interface Segregation

**Already good:**
- `internal/database/store.go` exports 9 sub-interfaces, combined via `Store`
- `internal/server/audiobook_service.go` (lines 29–50) defines `audiobookStore` as
  a narrow composite of only the required sub-interfaces — this is the pattern to follow

**Still wide:**
- Several handler functions accept `*Server` or `database.Store` when they only need
  `database.BookReader` or `database.BookFileStore`
- `maintenance_fixups.go` closures capture `*Server` (14+ hits) rather than declaring
  their exact dependency surface

**Recommendation:** New handler files should declare a narrow local interface at the
top (like `audiobook_service.go` does). Existing handlers can be tightened
incrementally — no big-bang refactor needed.

---

## 5. Frontend Structure

### Oversized page components

| File | Lines |
|------|-------|
| `web/src/pages/BookDedup.tsx` | 3656 |
| `web/src/pages/Library.tsx` | 3333 |
| `web/src/pages/Settings.tsx` | 2902 |
| `web/src/pages/BookDetail.tsx` | 2773 |

These are 3–4× larger than healthy React components. Each should be split into
feature sections (e.g. `Library` → `LibraryTable`, `LibraryFilters`, `LibraryToolbar`,
`LibraryBulkActions`).

### Loading state duplication — 148 hits

```tsx
// Pattern repeated 148 times across components:
const [loading, setLoading] = useState(false);
// ...
setLoading(true);
try { ... } finally { setLoading(false); }
```

A shared `useAsyncAction` hook would eliminate most of these:

```ts
// web/src/hooks/useAsyncAction.ts (proposed)
function useAsyncAction<T>(fn: () => Promise<T>) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    setLoading(true); setError(null);
    try { return await fn(); }
    catch (e) { setError(e.message); }
    finally { setLoading(false); }
  };
  return { run, loading, error };
}
```

### API call duplication — 488 axios calls

`web/src/lib/api.ts` already exists — check whether it wraps all calls or if some
components call axios directly. Direct axios calls bypass any centralized error handling.

---

## Prioritized Action Plan

| Priority | Item | Effort | Impact |
|----------|------|--------|--------|
| 1 | `server/response.go` HTTP helpers | Small (1 file) | High — eliminates 200+ boilerplate lines |
| 2 | `server/pagination.go` pagination helper | Small (1 file) | Medium — normalizes 376 callsites |
| 3 | Split `maintenance_fixups.go` into 8 files | Medium | High — 6400 lines is unmaintainable |
| 4 | Split `metafetch/service.go` into 8 files | Medium | High — easier to review/test |
| 5 | `ai/retry.go` retry helper | Small (1 file) | Medium — DRY in AI layer |
| 6 | Split `sqlite_store.go` into 7 files | Large | Medium — navigation/comprehension |
| 7 | Split `server.go` into 6 files | Large | Medium — requires careful routing split |
| 8 | `useAsyncAction` frontend hook | Small (1 file) | Medium — eliminates 148 loading patterns |
| 9 | Split `BookDedup.tsx` / `Library.tsx` | Medium | Medium — reviewability |
| 10 | Narrow `*Server` to small interfaces in handlers | Large | Low (correctness already fine) |
