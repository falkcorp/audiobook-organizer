<!-- file: docs/archive/2026-08-consolidation-wave2/architecture/metadata-cached-matcher-design.md -->
<!-- version: 1.0.1 -->
<!-- last-edited: 2026-08-12 -->

# METADATA-CACHED-MATCHER — Design

> Consolidate the metadata-candidate matching system around a single
> per-book cache. Replaces the current "walk recent ops to find the
> latest result" read path with a direct `metadata_cache:<book_id>`
> lookup, hard-codes a 30-day TTL, and unifies the three UI fetch
> entry points around the same cache.

## Goals

- One global cache of top metadata candidates per book, keyed by
  `book_id`. Reads are O(1).
- Manual fetches (per-book "Refresh", bulk "Fetch Selected", global
  "Fetch Unmatched") always invalidate the cache for the targeted
  books and refetch.
- A single global "Review" button opens a popup over **all** books
  with cached candidates. No more "review the results of operation X"
  flow — the popup is no longer tied to a specific operation id.
- 30-day TTL hard-coded. Stale entries are still readable so the UI
  can show "X days old, Refresh recommended".

## Non-goals

- Changing the matching algorithm itself (`metafetch.Service.SearchMetadataForBookWithOptions`
  stays as-is).
- Adding new metadata sources or scoring tiers.
- Removing `OperationResult` storage entirely. The bulk fetch op still
  creates an Operation row for the bell-badge progress UI, but per-book
  result storage moves to the cache.

## Storage

### New PebbleDB key namespace

```
metadata_cache:<book_id>  →  JSON(MetadataCandidateCache)
```

```go
// internal/metafetch/cache.go
type MetadataCandidateCache struct {
    BookID     string              `json:"book_id"`
    Candidates []MetadataCandidate `json:"candidates"`  // top 10 from last fetch
    FetchedAt  time.Time           `json:"fetched_at"`
    SourceHash string              `json:"source_hash"` // hash of (title, author, narrator, series, isbn10, isbn13, asin) — diagnostic only
}

func (c *MetadataCandidateCache) Age() time.Duration { return time.Since(c.FetchedAt) }
func (c *MetadataCandidateCache) IsFresh() bool      { return c.Age() < 30*24*time.Hour }
```

- `Candidates` capped at top 10 (matches current default response size).
- Writes are always replace-not-merge: every fetch overwrites.
- No background TTL sweep. Stale entries are kept until next fetch overwrites them.

### Store interface

```go
// internal/database/iface_metadata.go
type MetadataCacheStore interface {
    GetMetadataCache(bookID string) (*MetadataCandidateCache, error) // nil, nil = not cached
    PutMetadataCache(entry *MetadataCandidateCache) error
    DeleteMetadataCache(bookID string) error
    // ListMetadataCacheKeys returns all cache entries' summary metadata
    // (no candidate payloads — caller pulls full entries by book_id).
    ListMetadataCacheKeys() ([]MetadataCacheSummary, error)
}

type MetadataCacheSummary struct {
    BookID         string
    FetchedAt      time.Time
    CandidateCount int
}
```

- `PebbleStore` implements all four. Iteration uses the existing prefix-scan
  pattern (lower=`metadata_cache:`, upper=`metadata_cache;`).
- `SQLiteStore` returns `ErrUnsupported` for writes (consistent with
  Pebble-primary policy from `feedback_pebble_primary.md`).
- `MockStore` gets the standard `*Func` field pattern for tests.

The persistence type lives in `internal/database` next to the other
`*Store` types (`database.MetadataCandidateCache`). The
`internal/metafetch` package re-exports it via a type alias
(`type MetadataCandidateCache = database.MetadataCandidateCache`) so
existing metafetch callers keep their import path. This avoids the
forbidden cycle of `internal/database` importing `internal/metafetch`
while letting the cache entry sit naturally alongside the other store
types.

## Service surface

Extend `*metafetch.Service` (not a new service — the cache is just a new
layer over the existing search):

```go
// GetCachedCandidates returns the cached entry for a book, plus a
// fresh flag. (nil, false, nil) if no entry exists.
func (mfs *Service) GetCachedCandidates(bookID string) (*MetadataCandidateCache, bool, error)

// FetchAndCache runs SearchMetadataForBookWithOptions, writes top N to
// the cache, and returns the resulting cache entry. Always replaces
// the existing entry (this is the "invalidate" path).
func (mfs *Service) FetchAndCache(ctx context.Context, bookID string, query string, opts SearchOptions) (*MetadataCandidateCache, error)

// ListCachedSummaries returns all cache entries' summary metadata.
// Used by the Review popup to enumerate the queue without loading
// full candidate payloads.
func (mfs *Service) ListCachedSummaries() ([]MetadataCacheSummary, error)
```

`metafetch.Service` already has `mfs.db` (added in audit phase 4) which
satisfies `MetadataCacheStore` once the interface is added.

## HTTP API

Three endpoints — two are repointed versions of existing handlers,
one is new.

| Endpoint | Behavior |
|---|---|
| `POST /api/v1/audiobooks/:id/metadata/fetch` | Per-book. **Cache-first.** Body / query `?refresh=true` forces a fresh fetch + cache replace. Without `refresh`, returns the cached entry as-is (regardless of staleness — the UI shows the staleness flag). |
| `POST /api/v1/audiobooks/metadata/batch-fetch` | Bulk. Body: `{book_ids: [...]}` OR `{filter: {only_unmatched: true}}`. **Always invalidates + refetches** the targeted set. Returns `operation_id` for progress polling. Per-book results land in the cache as each fetch completes; the operation row stays for progress UI but is not consulted on read. |
| `GET /api/v1/audiobooks/metadata/cached` | Lists all cached entries with summary metadata and book info needed for the Review popup. Supports `?status=pending\|matched\|no_match\|all` (filters on `Book.MetadataReviewStatus`) and `?stale=true` (only stale entries). Pagination: standard `limit`/`offset`. |

### Endpoints that go away

- `GET /api/v1/audiobooks/metadata/pending-review` (the current
  `handleGetPendingReview`) — superseded by the new
  `/metadata/cached?status=pending` endpoint.
- `GET /api/v1/operations/:id/results` is kept (used elsewhere) but
  callers in metadata flows stop using it.

## Frontend UX

### Library page toolbar

```
┌────────────────────────────────────────────────────────────────────┐
│ Audiobook Organizer       [search…]    [⚙] [bell]      [Review N]  │
├────────────────────────────────────────────────────────────────────┤
│ [Scan]  [Fetch Unmatched]    │  [Fetch Selected]  ← when N>0       │
└────────────────────────────────────────────────────────────────────┘
```

- **Fetch Selected** — visible only when N books are selected.
  Calls `POST /metadata/batch-fetch` with the selection. Toast on
  completion: *"Fetched candidates for N books. Click Review to see."*
  No auto-open of the Review popup.
- **Fetch Unmatched** — global. Calls `POST /metadata/batch-fetch`
  with `{filter: {only_unmatched: true}}`. Same toast.
- **Review (N)** — always visible. Badge `N` is the count of
  cached entries with `Book.MetadataReviewStatus == null`. Opens
  the Review popup.

### Per-book (BookDetail)

- **Fetch Metadata** button → cache-first. If a cache entry exists,
  shows it immediately. Otherwise hits `POST /:id/metadata/fetch`
  without `?refresh`, which on first call effectively fetches because
  there's no cache to read.
- Candidate list shows "Last fetched X days ago" if the entry is past
  7 days. Past 30 days: "Stale (X days old) — Refresh recommended".
- **Refresh** icon next to the candidate list → calls the same
  endpoint with `?refresh=true` to force a fresh fetch.

### Review popup

Same `MetadataReviewDialog` component, data source switches to the new
cached endpoint.

- Top filter tabs: `Pending` (default, `status == null`) / `All` /
  `Matched` / `No match` / `Stale`.
- Row: book title, top candidate score, cache age, action buttons
  (Accept top / Choose / Skip / Refresh this one).
- "Refresh this one" calls the per-book endpoint with `?refresh=true`
  and reloads the row.
- Closing the popup persists nothing extra. Accept/Skip already write
  `Book.MetadataReviewStatus`; the cache stays.

### What goes away in the frontend

- The `?reviewOp=<opid>` URL param in `Library.tsx` (lines ~224, 227,
  830-839) and the auto-open-on-op-complete logic — the Review popup
  is no longer tied to a specific operation id.
- `handleResumeReview` (line ~1648) and the `getPendingReview` API
  call — replaced by the new `/metadata/cached?status=pending`
  endpoint.

## Data migration

No schema migration required — the new key namespace is additive.
Existing `OperationResult` rows for `metadata_candidate_fetch`
remain untouched.

One-shot lazy backfill: on first read of a book that has no
`metadata_cache:` entry but has a recent `OperationResult` for that
book, the backend can lazily populate the cache from the latest result
row. This avoids a "everyone has to refetch everything" moment after
deployment. The backfill code is small enough to ship in the same PR
as the cache itself but stays behind a `MetadataCacheLazyBackfill`
config flag (default true) so it's reversible.

After 30 days post-deploy, the backfill is dead code and gets
deleted in a follow-up.

## Test strategy

- **Cache store tests** (`internal/database/pebble_store_test.go`):
  unit tests for Put/Get/Delete/ListSummaries — exercising the prefix
  iterator, JSON round-trip, and "no entry → nil, nil" path.
- **Service tests** (`internal/metafetch/cache_test.go`):
  - `FetchAndCache` writes to store.
  - `GetCachedCandidates` reads with freshness flag.
  - `FetchAndCache` always replaces (idempotency check).
  - Lazy-backfill path: cached miss + matching OperationResult → cache populated, second call hits cache.
- **HTTP handler tests** (`internal/server/metadata_batch_candidates_test.go`):
  - `POST /:id/metadata/fetch` without `refresh` → cached read.
  - `POST /:id/metadata/fetch?refresh=true` → forced refetch.
  - `POST /metadata/batch-fetch` → all cache entries replaced.
  - `GET /metadata/cached?status=pending` → filters correctly.
- **Frontend smoke** (Playwright): existing `Library.bulkFetch.test.tsx`
  pattern — mock `api.batchFetchCandidates`, click "Fetch Selected",
  assert toast appears without auto-opening the dialog.

Pre-existing SERVER-THIN-8 failures (`TestStartScanOperation` etc.)
remain skipped during verification.

## Rollout

- Single PR for backend (storage + service + handlers + lazy backfill).
- Single PR for frontend (toolbar buttons + Review popup data source
  switch + cleanup of `reviewOp` URL handling).
- Deploy backend first; frontend can ship in the same window since the
  new endpoints accept the existing request shapes (`book_ids`,
  `filter.only_unmatched`).

## Open items

- Lazy backfill TTL: the design assumes "delete the backfill code after
  30 days." Easy enough to gate on a config flag and remove later;
  noted here as a follow-up task to delete it post-deploy.
- Cache invalidation when book metadata mutates (title/author changes):
  not invalidating today. The `SourceHash` field is recorded but unused
  in v1; a follow-up can compare and invalidate stale-by-content if
  needed.
