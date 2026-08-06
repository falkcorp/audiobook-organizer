<!-- file: docs/architecture/metadata-cached-matcher-plan.md -->
<!-- version: 1.0.0 -->
<!-- last-edited: 2026-05-13 -->

# METADATA-CACHED-MATCHER — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate the metadata matcher around a single per-book PebbleDB cache (`metadata_cache:<book_id>`) with 30-day TTL, unifying three UI fetch entry points.

**Architecture:** New PebbleDB key namespace stores top-10 candidates per book. `metafetch.Service` gains three cache methods. Three HTTP endpoints are repointed/added (per-book cache-first, bulk invalidates, list-cached for the Review popup). Frontend gets a single always-visible Review button + renamed "Fetch Selected" toolbar action that does not auto-open the popup.

**Tech Stack:** Go 1.24 backend, PebbleDB, React/TypeScript frontend, Gin HTTP framework, Vitest+Playwright.

**Spec:** `docs/architecture/metadata-cached-matcher-design.md`.

**Repo conventions to know:**

- Worktree-per-PR. Create with `git worktree add /Users/jdfalk/.worktrees/audiobook-organizer-<slug> -b <branch> origin/main`.
- `make deploy` builds + scp's + restarts the systemd service on `<server>`. Run after every server-side merge.
- `gh pr merge <N> --rebase --admin --delete-branch` — repo uses rebase/FF only, never squash.
- Skip pre-existing `SERVER-THIN-8` test failures in verification (`TestStartScanOperation`, `TestStartOrganizeOperation`, `TestITunesImport_*`, `TestE2E_ITunesImportOrganizeWriteBack`, `TestOrganizeService_ViaHTTP`, `TestAddImportPathAutoScan`, `TestBackfillExternalIDsCollectsBookPIDs`). Pass via `-skip`.
- Server-side store access — prefer `s.Store()` / `mfs.db` / explicit store params; the `database.GetGlobalStore()` audit is complete (only intentional fallbacks remain).
- Pre-existing race in `Library.tsx` selection state — don't introduce new ones; lean on existing patterns.

**File map:**

| File | Action | Responsibility |
|---|---|---|
| `internal/database/iface_metadata.go` | modify | Add `MetadataCandidateCache`, `MetadataCacheSummary`, `MetadataCacheStore` interface. |
| `internal/database/pebble_store_metadata_cache.go` | create | PebbleStore impl of the 4 cache methods. |
| `internal/database/sqlite_store_metadata_cache.go` | create | SQLite stub returning `ErrUnsupported`. |
| `internal/database/mock_store.go` | modify | Add 4 `*Func` fields and methods. |
| `internal/metafetch/cache.go` | create | Re-export alias + 3 service methods. |
| `internal/metafetch/cache_backfill.go` | create | Lazy-backfill helper from latest `OperationResult`. |
| `internal/metafetch/cache_test.go` | create | Unit tests for the 3 methods + backfill. |
| `internal/database/pebble_store_metadata_cache_test.go` | create | Round-trip + iteration tests. |
| `internal/server/metadata_handlers.go` | modify | `POST /:id/metadata/fetch` reads cache first; `?refresh=true` forces refetch. |
| `internal/server/metadata_batch_candidates.go` | modify | Batch fetch writes to cache. Delete `handleGetPendingReview`. |
| `internal/server/metadata_cached_handlers.go` | create | `GET /metadata/cached` endpoint + handler test. |
| `internal/server/metadata_cached_handlers_test.go` | create | HTTP tests. |
| `internal/server/server_lifecycle.go` | modify | Wire new route, remove pending-review route. |
| `web/src/services/api.ts` | modify | Add `listCachedCandidates()`; remove `getPendingReview()`. |
| `web/src/components/library/LibraryToolbar.tsx` | modify | Rename "Fetch & Review" → "Fetch Selected"; add Review badge button. |
| `web/src/pages/Library.tsx` | modify | New handlers; remove `reviewOp` URL handling. |
| `web/src/pages/BookDetail.tsx` | modify | Cache-first + Refresh icon. |
| `web/src/components/dialogs/MetadataReviewDialog.tsx` | modify | Switch data source to `/metadata/cached`. |
| `web/src/pages/Library.bulkFetch.test.tsx` | modify | Assert no auto-open on bulk fetch. |

---

## Task 1: Storage types + interface

**Files:**
- Modify: `internal/database/iface_metadata.go`

- [ ] **Step 1: Read existing iface_metadata.go top**

Run: `head -50 internal/database/iface_metadata.go`

- [ ] **Step 2: Append cache types + interface**

Append at end of `internal/database/iface_metadata.go`:

```go
// MetadataCandidateCache is the persisted top-N metadata candidates
// returned by the last fetch for a book, keyed by book_id under the
// "metadata_cache:" PebbleDB namespace. Cache entries are replace-only
// (no merging) and have a 30-day staleness flag — see IsFresh().
//
// This is the canonical read source for the metadata-review UI.
// OperationResult rows for "metadata_candidate_fetch" remain for
// progress UI but are not consulted on read.
type MetadataCandidateCache struct {
	BookID     string `json:"book_id"`
	// Candidates is the top-10 list from the last fetch, in score order.
	// The element type is opaque to the storage layer — handlers
	// JSON-decode into metafetch.MetadataCandidate at the boundary.
	Candidates []json.RawMessage `json:"candidates"`
	FetchedAt  time.Time         `json:"fetched_at"`
	// SourceHash captures the search inputs (title, author, narrator,
	// series, isbn10/13, asin) so v2 can detect "book metadata mutated
	// since cache" without parsing candidates. Diagnostic only in v1.
	SourceHash string `json:"source_hash"`
}

// MetadataCacheTTL is the freshness window. Entries older than this
// are still readable but the UI flags them and offers a Refresh.
const MetadataCacheTTL = 30 * 24 * time.Hour

// Age returns how long ago the cache was written.
func (c *MetadataCandidateCache) Age() time.Duration {
	if c == nil {
		return 0
	}
	return time.Since(c.FetchedAt)
}

// IsFresh reports whether the cache is younger than MetadataCacheTTL.
// Stale caches are still returned to callers; freshness is informational.
func (c *MetadataCandidateCache) IsFresh() bool {
	return c != nil && c.Age() < MetadataCacheTTL
}

// MetadataCacheSummary is the lightweight per-entry record returned
// by ListMetadataCacheKeys for the Review popup enumeration.
type MetadataCacheSummary struct {
	BookID         string    `json:"book_id"`
	FetchedAt      time.Time `json:"fetched_at"`
	CandidateCount int       `json:"candidate_count"`
}

// MetadataCacheStore is the persistence layer for the per-book
// metadata-candidate cache.
type MetadataCacheStore interface {
	// GetMetadataCache returns the cached entry for a book, or (nil, nil)
	// when no entry exists. A non-nil entry past MetadataCacheTTL is
	// still returned — staleness is the caller's call.
	GetMetadataCache(bookID string) (*MetadataCandidateCache, error)
	// PutMetadataCache replaces the cache entry for entry.BookID.
	// Idempotent. Always overwrites — there is no merge semantics.
	PutMetadataCache(entry *MetadataCandidateCache) error
	// DeleteMetadataCache removes the entry for bookID. Missing-key is
	// not an error.
	DeleteMetadataCache(bookID string) error
	// ListMetadataCacheKeys returns one summary per cached entry,
	// ordered by FetchedAt descending. Caller paginates.
	ListMetadataCacheKeys() ([]MetadataCacheSummary, error)
}
```

- [ ] **Step 3: Ensure imports are present**

At the top of `internal/database/iface_metadata.go`, ensure these imports exist:

```go
import (
	"encoding/json"
	"time"
)
```

Add them if missing (use `goimports` if available, otherwise edit the import block manually).

- [ ] **Step 4: Verify it compiles**

Run: `go build ./internal/database/...`
Expected: clean (no errors).

- [ ] **Step 5: Verify Store interface embeds it (or add)**

Run: `grep -n "MetadataCacheStore\|interface Store" internal/database/store.go internal/database/iface_*.go | head`

If `Store` doesn't already embed `MetadataCacheStore`, edit `internal/database/store.go` to add `MetadataCacheStore` to the embed list of the `Store` interface (find the `type Store interface { ... }` declaration and append `MetadataCacheStore` after the other embed names).

- [ ] **Step 6: Build to confirm**

Run: `go build ./...`
Expected: many "method not implemented" errors from PebbleStore / SQLiteStore / MockStore — those get fixed in Tasks 2, 3.

- [ ] **Step 7: Commit**

```bash
git add internal/database/iface_metadata.go internal/database/store.go
git commit -m "feat(database): MetadataCacheStore interface + types

METADATA-CACHED-MATCHER task 1. Adds the storage surface for the new
per-book metadata-candidate cache:

- MetadataCandidateCache: persisted record (BookID, top-10 candidates
  as json.RawMessage, FetchedAt, SourceHash). Candidates are opaque
  at the storage layer; metafetch decodes at the boundary.
- MetadataCacheTTL = 30 days, with IsFresh() helper.
- MetadataCacheSummary: lightweight enumeration record.
- MetadataCacheStore: 4-method interface (Get/Put/Delete/List).
- Store interface embeds it.

Build errors in PebbleStore/SQLiteStore/MockStore are expected and
fixed in the following two tasks."
```

---

## Task 2: PebbleStore implementation

**Files:**
- Create: `internal/database/pebble_store_metadata_cache.go`
- Create: `internal/database/pebble_store_metadata_cache_test.go`

- [ ] **Step 1: Write the failing test file**

Create `internal/database/pebble_store_metadata_cache_test.go`:

```go
// file: internal/database/pebble_store_metadata_cache_test.go
// version: 1.0.0

package database

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPebbleStoreForTest(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPebbleStore_MetadataCache_PutGet(t *testing.T) {
	store := newPebbleStoreForTest(t)

	entry := &MetadataCandidateCache{
		BookID:     "book-1",
		Candidates: []json.RawMessage{json.RawMessage(`{"title":"x"}`)},
		FetchedAt:  time.Now().UTC(),
		SourceHash: "abc",
	}
	require.NoError(t, store.PutMetadataCache(entry))

	got, err := store.GetMetadataCache("book-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "book-1", got.BookID)
	assert.Equal(t, "abc", got.SourceHash)
	assert.Len(t, got.Candidates, 1)
}

func TestPebbleStore_MetadataCache_GetMissing(t *testing.T) {
	store := newPebbleStoreForTest(t)
	got, err := store.GetMetadataCache("never-stored")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPebbleStore_MetadataCache_PutReplaces(t *testing.T) {
	store := newPebbleStoreForTest(t)
	t1 := time.Now().UTC()
	t2 := t1.Add(time.Hour)

	require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{
		BookID: "book-1", FetchedAt: t1, SourceHash: "old",
	}))
	require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{
		BookID: "book-1", FetchedAt: t2, SourceHash: "new",
	}))

	got, err := store.GetMetadataCache("book-1")
	require.NoError(t, err)
	assert.Equal(t, "new", got.SourceHash)
	assert.True(t, got.FetchedAt.Equal(t2))
}

func TestPebbleStore_MetadataCache_Delete(t *testing.T) {
	store := newPebbleStoreForTest(t)
	require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{
		BookID: "book-1", FetchedAt: time.Now(),
	}))
	require.NoError(t, store.DeleteMetadataCache("book-1"))

	got, err := store.GetMetadataCache("book-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPebbleStore_MetadataCache_DeleteMissingIsNoop(t *testing.T) {
	store := newPebbleStoreForTest(t)
	assert.NoError(t, store.DeleteMetadataCache("never-stored"))
}

func TestPebbleStore_MetadataCache_ListKeys(t *testing.T) {
	store := newPebbleStoreForTest(t)
	now := time.Now().UTC()
	for i, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{
			BookID:     id,
			FetchedAt:  now.Add(time.Duration(i) * time.Hour),
			Candidates: []json.RawMessage{json.RawMessage(`{}`)},
		}))
	}

	summaries, err := store.ListMetadataCacheKeys()
	require.NoError(t, err)
	require.Len(t, summaries, 3)

	// Ordered FetchedAt descending: c (t+2h), b (t+1h), a (t+0h)
	assert.Equal(t, "c", summaries[0].BookID)
	assert.Equal(t, "b", summaries[1].BookID)
	assert.Equal(t, "a", summaries[2].BookID)
	for _, s := range summaries {
		assert.Equal(t, 1, s.CandidateCount)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/database/ -run TestPebbleStore_MetadataCache -count=1`
Expected: FAIL — `PebbleStore.PutMetadataCache undefined` (build error).

- [ ] **Step 3: Implement PebbleStore methods**

Create `internal/database/pebble_store_metadata_cache.go`:

```go
// file: internal/database/pebble_store_metadata_cache.go
// version: 1.0.0

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble"
)

// metadataCacheKeyPrefix is the prefix every per-book cache key shares.
const metadataCacheKeyPrefix = "metadata_cache:"

func metadataCacheKey(bookID string) []byte {
	return []byte(metadataCacheKeyPrefix + bookID)
}

// GetMetadataCache reads the cache entry for bookID, or returns
// (nil, nil) when the key is absent.
func (p *PebbleStore) GetMetadataCache(bookID string) (*MetadataCandidateCache, error) {
	val, closer, err := p.db.Get(metadataCacheKey(bookID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("pebble get metadata_cache:%s: %w", bookID, err)
	}
	defer closer.Close()

	var entry MetadataCandidateCache
	if err := json.Unmarshal(val, &entry); err != nil {
		return nil, fmt.Errorf("decode metadata_cache:%s: %w", bookID, err)
	}
	return &entry, nil
}

// PutMetadataCache writes (or replaces) the cache entry for entry.BookID.
func (p *PebbleStore) PutMetadataCache(entry *MetadataCandidateCache) error {
	if entry == nil || entry.BookID == "" {
		return fmt.Errorf("PutMetadataCache: nil entry or empty BookID")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode metadata_cache:%s: %w", entry.BookID, err)
	}
	if err := p.db.Set(metadataCacheKey(entry.BookID), data, pebble.Sync); err != nil {
		return fmt.Errorf("pebble set metadata_cache:%s: %w", entry.BookID, err)
	}
	return nil
}

// DeleteMetadataCache removes the cache entry for bookID. Missing
// keys are not an error.
func (p *PebbleStore) DeleteMetadataCache(bookID string) error {
	if err := p.db.Delete(metadataCacheKey(bookID), pebble.Sync); err != nil {
		return fmt.Errorf("pebble delete metadata_cache:%s: %w", bookID, err)
	}
	return nil
}

// ListMetadataCacheKeys returns one summary per cached entry, ordered
// by FetchedAt descending. Caller paginates.
func (p *PebbleStore) ListMetadataCacheKeys() ([]MetadataCacheSummary, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(metadataCacheKeyPrefix),
		UpperBound: []byte("metadata_cache;"), // ';' is one byte after ':'
	})
	if err != nil {
		return nil, fmt.Errorf("new iter metadata_cache: %w", err)
	}
	defer iter.Close()

	var out []MetadataCacheSummary
	for iter.First(); iter.Valid(); iter.Next() {
		var entry MetadataCandidateCache
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			// Skip corrupt rows rather than fail the whole list.
			continue
		}
		out = append(out, MetadataCacheSummary{
			BookID:         entry.BookID,
			FetchedAt:      entry.FetchedAt,
			CandidateCount: len(entry.Candidates),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FetchedAt.After(out[j].FetchedAt)
	})
	return out, nil
}
```

- [ ] **Step 4: Run tests to confirm they pass**

Run: `go test ./internal/database/ -run TestPebbleStore_MetadataCache -count=1 -v`
Expected: all 6 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/database/pebble_store_metadata_cache.go internal/database/pebble_store_metadata_cache_test.go
git commit -m "feat(database): PebbleStore impl of MetadataCacheStore

METADATA-CACHED-MATCHER task 2. The 4-method MetadataCacheStore
interface lands on PebbleStore via a new file:

- GetMetadataCache: pebble.Get under metadata_cache:<book_id>;
  ErrNotFound → (nil, nil).
- PutMetadataCache: replace-only, JSON-encoded, pebble.Sync write.
- DeleteMetadataCache: pebble.Delete; missing keys are no-ops.
- ListMetadataCacheKeys: prefix-scan, decodes each entry into a
  MetadataCacheSummary, returns FetchedAt-desc sorted.

6 unit tests cover put/get round-trip, missing-key, replace
semantics, delete + delete-missing, and list ordering."
```

---

## Task 3: SQLite stub + MockStore stub

**Files:**
- Create: `internal/database/sqlite_store_metadata_cache.go`
- Modify: `internal/database/mock_store.go`

- [ ] **Step 1: Create SQLite stub**

Create `internal/database/sqlite_store_metadata_cache.go`:

```go
// file: internal/database/sqlite_store_metadata_cache.go
// version: 1.0.0

// SQLiteStore does NOT implement the metadata cache. Consistent with
// the Pebble-primary policy (feedback_pebble_primary.md): hot paths
// that need new storage land on PebbleStore only, and SQLite paths
// return ErrUnsupported. The metafetch.Service nil-checks before use
// so SQLite-only deployments (tests that opt in) degrade gracefully —
// no cached candidates, every fetch hits the metadata sources fresh.

package database

import "errors"

// ErrMetadataCacheUnsupported is returned by SQLiteStore for all
// MetadataCacheStore methods. Distinct error so callers can detect
// the "this backend doesn't support caching" path vs. a real failure.
var ErrMetadataCacheUnsupported = errors.New("metadata cache: not supported by this store backend")

func (s *SQLiteStore) GetMetadataCache(bookID string) (*MetadataCandidateCache, error) {
	// nil entry, no error — treated as cache-miss by callers.
	return nil, nil
}
func (s *SQLiteStore) PutMetadataCache(entry *MetadataCandidateCache) error {
	return ErrMetadataCacheUnsupported
}
func (s *SQLiteStore) DeleteMetadataCache(bookID string) error { return nil }
func (s *SQLiteStore) ListMetadataCacheKeys() ([]MetadataCacheSummary, error) {
	return nil, nil
}
```

- [ ] **Step 2: Find MockStore pattern**

Run: `grep -B1 "ListBookFilesFunc\b" internal/database/mock_store.go | head -10`

You'll see the existing `Func` field pattern, like:

```go
ListBookFilesFunc func(...) ([]BookFile, error)
```

and a wrapper method:

```go
func (m *MockStore) ListBookFiles(...) ([]BookFile, error) {
    if m.ListBookFilesFunc != nil { return m.ListBookFilesFunc(...) }
    return nil, nil
}
```

- [ ] **Step 3: Add 4 MockStore Func fields + methods**

In `internal/database/mock_store.go`, find the `Func` field block (after the `type MockStore struct {` line) and append:

```go
	GetMetadataCacheFunc       func(bookID string) (*MetadataCandidateCache, error)
	PutMetadataCacheFunc       func(entry *MetadataCandidateCache) error
	DeleteMetadataCacheFunc    func(bookID string) error
	ListMetadataCacheKeysFunc  func() ([]MetadataCacheSummary, error)
```

Then append the four methods to the bottom of the file:

```go
func (m *MockStore) GetMetadataCache(bookID string) (*MetadataCandidateCache, error) {
	if m.GetMetadataCacheFunc != nil {
		return m.GetMetadataCacheFunc(bookID)
	}
	return nil, nil
}
func (m *MockStore) PutMetadataCache(entry *MetadataCandidateCache) error {
	if m.PutMetadataCacheFunc != nil {
		return m.PutMetadataCacheFunc(entry)
	}
	return nil
}
func (m *MockStore) DeleteMetadataCache(bookID string) error {
	if m.DeleteMetadataCacheFunc != nil {
		return m.DeleteMetadataCacheFunc(bookID)
	}
	return nil
}
func (m *MockStore) ListMetadataCacheKeys() ([]MetadataCacheSummary, error) {
	if m.ListMetadataCacheKeysFunc != nil {
		return m.ListMetadataCacheKeysFunc()
	}
	return nil, nil
}
```

- [ ] **Step 4: Check for mocks generated under internal/mocks/**

Run: `grep -rn "MetadataCacheStore\|GetMetadataCache" internal/mocks/ 2>/dev/null | head`

If `internal/mocks/MockStore.go` or similar exists (generated by mockery), regenerate:

Run: `make generate 2>&1 | tail -10` (or `go generate ./...` if no make target).

If neither exists, skip — the hand-written `MockStore` in `mock_store.go` is the only one.

- [ ] **Step 5: Build everything**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 6: Run the full database tests**

Run: `go test ./internal/database/ -short -race -timeout=60s`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/database/sqlite_store_metadata_cache.go internal/database/mock_store.go internal/mocks 2>/dev/null || git add internal/database/sqlite_store_metadata_cache.go internal/database/mock_store.go
git commit -m "feat(database): SQLite + MockStore stubs for MetadataCacheStore

METADATA-CACHED-MATCHER task 3.

- SQLiteStore: 4 stubs returning (nil, nil) for reads and
  ErrMetadataCacheUnsupported for writes. Matches the Pebble-primary
  policy — SQLite isn't a real production backend any more.
- MockStore: standard *Func field pattern; nil-Func defaults to
  (nil, nil) so tests that don't care about the cache don't have
  to wire it up.

Build green. internal/database tests pass."
```

---

## Task 4: metafetch.Service cache methods

**Files:**
- Create: `internal/metafetch/cache.go`
- Create: `internal/metafetch/cache_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/metafetch/cache_test.go`:

```go
// file: internal/metafetch/cache_test.go
// version: 1.0.0

package metafetch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

func TestService_GetCachedCandidates_MissReturnsNil(t *testing.T) {
	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			return nil, nil
		},
	}
	svc := NewService(mock)
	entry, fresh, err := svc.GetCachedCandidates("book-1")
	require.NoError(t, err)
	assert.Nil(t, entry)
	assert.False(t, fresh)
}

func TestService_GetCachedCandidates_FreshHit(t *testing.T) {
	now := time.Now().UTC()
	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			return &database.MetadataCandidateCache{
				BookID:    "book-1",
				FetchedAt: now,
				Candidates: []json.RawMessage{
					json.RawMessage(`{"title":"Foo"}`),
				},
			}, nil
		},
	}
	svc := NewService(mock)
	entry, fresh, err := svc.GetCachedCandidates("book-1")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.True(t, fresh)
	assert.Len(t, entry.Candidates, 1)
}

func TestService_GetCachedCandidates_StaleHit(t *testing.T) {
	old := time.Now().Add(-60 * 24 * time.Hour) // 60 days
	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			return &database.MetadataCandidateCache{BookID: "book-1", FetchedAt: old}, nil
		},
	}
	svc := NewService(mock)
	entry, fresh, err := svc.GetCachedCandidates("book-1")
	require.NoError(t, err)
	require.NotNil(t, entry, "stale entries are still returned to callers")
	assert.False(t, fresh, "60-day-old entry should report not-fresh")
}

func TestService_ListCachedSummaries_EmptyOK(t *testing.T) {
	mock := &database.MockStore{
		ListMetadataCacheKeysFunc: func() ([]database.MetadataCacheSummary, error) {
			return nil, nil
		},
	}
	svc := NewService(mock)
	out, err := svc.ListCachedSummaries(context.Background())
	require.NoError(t, err)
	assert.Empty(t, out)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/metafetch/ -run TestService_(GetCachedCandidates|ListCachedSummaries) -count=1`
Expected: FAIL — `svc.GetCachedCandidates undefined`.

- [ ] **Step 3: Implement the service methods**

Create `internal/metafetch/cache.go`:

```go
// file: internal/metafetch/cache.go
// version: 1.0.0

// Cache-layer on top of metafetch.Service. The persisted record type
// lives in internal/database (MetadataCandidateCache) — re-exported
// here via a type alias so existing metafetch callers keep their
// import path. The forbidden direction (database → metafetch) is
// preserved: metafetch imports database, never the other way.

package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

// MetadataCandidateCache is a re-export of the persistence type so
// metafetch callers don't need to know about internal/database.
type MetadataCandidateCache = database.MetadataCandidateCache

// MetadataCacheSummary is the lightweight enumeration record.
type MetadataCacheSummary = database.MetadataCacheSummary

// metadataCacheTopN caps how many candidates we persist per book.
// Matches the existing default response size.
const metadataCacheTopN = 10

// GetCachedCandidates returns the cached entry for bookID plus a
// freshness flag (entry.IsFresh()). Returns (nil, false, nil) for
// cache-miss. Errors are real I/O failures.
func (mfs *Service) GetCachedCandidates(bookID string) (*MetadataCandidateCache, bool, error) {
	if mfs == nil || mfs.db == nil {
		return nil, false, nil
	}
	entry, err := mfs.db.GetMetadataCache(bookID)
	if err != nil {
		return nil, false, err
	}
	if entry == nil {
		return nil, false, nil
	}
	return entry, entry.IsFresh(), nil
}

// FetchAndCache runs the existing search pipeline, writes top-N to
// the cache (always replaces), and returns the resulting entry.
//
// This is the "manual = invalidate" path — every call overwrites
// whatever was there. Use GetCachedCandidates for cache-respecting
// reads.
func (mfs *Service) FetchAndCache(ctx context.Context, bookID, query string, opts SearchOptions) (*MetadataCandidateCache, error) {
	if mfs == nil {
		return nil, fmt.Errorf("FetchAndCache: nil Service")
	}
	resp, err := mfs.SearchMetadataForBookWithOptions(bookID, query, "", "", "", opts)
	if err != nil {
		return nil, err
	}
	candidates := resp.Candidates
	if len(candidates) > metadataCacheTopN {
		candidates = candidates[:metadataCacheTopN]
	}
	raw := make([]json.RawMessage, 0, len(candidates))
	for _, c := range candidates {
		b, jerr := json.Marshal(c)
		if jerr != nil {
			// Skip a single corrupt candidate rather than fail.
			continue
		}
		raw = append(raw, b)
	}

	entry := &MetadataCandidateCache{
		BookID:     bookID,
		Candidates: raw,
		FetchedAt:  nowUTC(),
		SourceHash: hashSearchInputs(bookID, query, opts),
	}
	if mfs.db != nil {
		if err := mfs.db.PutMetadataCache(entry); err != nil {
			// Cache failure should not break the user's fetch; log and
			// continue (callers can still consume the in-memory entry).
			// Future enhancement: surface a metric.
			return entry, nil
		}
	}
	return entry, nil
}

// ListCachedSummaries returns one summary per cached entry, ordered
// by FetchedAt descending.
func (mfs *Service) ListCachedSummaries(_ context.Context) ([]MetadataCacheSummary, error) {
	if mfs == nil || mfs.db == nil {
		return nil, nil
	}
	return mfs.db.ListMetadataCacheKeys()
}

// hashSearchInputs builds a short stable digest of the search inputs
// so v2 can compare against the inputs the cached entry came from.
func hashSearchInputs(bookID, query string, opts SearchOptions) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%v", bookID, query, opts)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// nowUTC is a var so tests can stub it.
var nowUTC = func() (t timeUTC) { return timeUTC{} }
```

Then add at the top of `internal/metafetch/cache.go` (above `nowUTC` declaration), replace the last paragraph with:

```go
// nowUTC is overridable for tests. Default returns time.Now().UTC().
```

And replace the `var nowUTC = ...` line above with:

```go
import "time"

var nowUTC = func() time.Time { return time.Now().UTC() }
```

(Remove the `timeUTC{}` placeholder — that was a typo in this plan.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/metafetch/ -run TestService_(GetCachedCandidates|ListCachedSummaries) -count=1 -v`
Expected: all 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metafetch/cache.go internal/metafetch/cache_test.go
git commit -m "feat(metafetch): Service.{Get,FetchAnd,List}CachedCandidates

METADATA-CACHED-MATCHER task 4. Three cache-layer methods on
*metafetch.Service:

- GetCachedCandidates(bookID) → (*entry, fresh, err). Cache-respecting
  read; nil on miss; staleness flag derived from MetadataCacheTTL.
- FetchAndCache(ctx, bookID, query, opts) → (*entry, err). Always
  invalidates: runs the existing search and replaces the cache row.
  Cache-write failures don't fail the user-facing fetch; the
  in-memory entry is still returned.
- ListCachedSummaries(ctx) → ([]summary, err). Cheap enumeration for
  the Review popup.

metafetch.MetadataCandidateCache is a type alias of the persistence
type in internal/database (forbidden cycle avoided by alias).
SourceHash captures search inputs for future invalidation logic;
unused in v1."
```

---

## Task 5: Lazy backfill helper

**Files:**
- Create: `internal/metafetch/cache_backfill.go`
- Modify: `internal/metafetch/cache.go` (extend `GetCachedCandidates`)
- Create: `internal/metafetch/cache_backfill_test.go`

- [ ] **Step 1: Find existing latestMetadataResultsByBook**

Run: `grep -n "latestMetadataResultsByBook\|OperationResult\|metadata_candidate_fetch" internal/server/metadata_batch_candidates.go | head`

Confirm `latestMetadataResultsByBook` exists at around line 755 (per the spec). It scans op rows; we'll need similar logic in metafetch.

- [ ] **Step 2: Write the failing test**

Create `internal/metafetch/cache_backfill_test.go`:

```go
// file: internal/metafetch/cache_backfill_test.go
// version: 1.0.0

package metafetch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

func TestService_LazyBackfill_FromOperationResult(t *testing.T) {
	// MockStore: cache miss, but a recent metadata_candidate_fetch op
	// exists with a result row for the book.
	cachePut := []*database.MetadataCandidateCache{}
	resultJSON, err := json.Marshal(map[string]any{
		"candidates": []map[string]any{
			{"title": "Test Book", "score": 95.0},
		},
	})
	require.NoError(t, err)

	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			return nil, nil // miss
		},
		GetRecentOperationsByTypeFunc: func(opType string, limit int) ([]database.Operation, error) {
			return []database.Operation{
				{ID: "op-1", Type: "metadata_candidate_fetch", CreatedAt: time.Now()},
			}, nil
		},
		GetOperationResultsForBookFunc: func(opID, bookID string) (*database.OperationResult, error) {
			return &database.OperationResult{
				OperationID: opID,
				BookID:      bookID,
				ResultJSON:  string(resultJSON),
			}, nil
		},
		PutMetadataCacheFunc: func(entry *database.MetadataCandidateCache) error {
			cachePut = append(cachePut, entry)
			return nil
		},
	}

	svc := NewService(mock)
	entry, fresh, err := svc.GetCachedCandidates("book-1")
	require.NoError(t, err)
	require.NotNil(t, entry, "lazy backfill should populate the cache from the op result")
	assert.True(t, fresh, "backfilled entry should be fresh")
	require.Len(t, cachePut, 1, "PutMetadataCache should have been called once")
	assert.Equal(t, "book-1", cachePut[0].BookID)
}

func TestService_LazyBackfill_NoOpRowsNoChange(t *testing.T) {
	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			return nil, nil
		},
		GetRecentOperationsByTypeFunc: func(opType string, limit int) ([]database.Operation, error) {
			return nil, nil
		},
	}
	svc := NewService(mock)
	entry, _, err := svc.GetCachedCandidates("book-1")
	require.NoError(t, err)
	assert.Nil(t, entry, "no cache + no ops = nil")
}
```

- [ ] **Step 3: Add the required MockStore Func fields**

If `GetRecentOperationsByTypeFunc` and `GetOperationResultsForBookFunc` don't already exist on `MockStore`, add them following the same pattern as Task 3. Look for the existing `Operation*Func` block in `internal/database/mock_store.go`:

Run: `grep -n "GetRecentOperationsByType\|GetOperationResultsForBook\|OperationResultFunc" internal/database/mock_store.go`

If they're missing, append to `MockStore`:

```go
	GetRecentOperationsByTypeFunc func(opType string, limit int) ([]Operation, error)
	GetOperationResultsForBookFunc func(opID, bookID string) (*OperationResult, error)
```

And the methods:

```go
func (m *MockStore) GetRecentOperationsByType(opType string, limit int) ([]Operation, error) {
	if m.GetRecentOperationsByTypeFunc != nil {
		return m.GetRecentOperationsByTypeFunc(opType, limit)
	}
	return nil, nil
}
func (m *MockStore) GetOperationResultsForBook(opID, bookID string) (*OperationResult, error) {
	if m.GetOperationResultsForBookFunc != nil {
		return m.GetOperationResultsForBookFunc(opID, bookID)
	}
	return nil, nil
}
```

(These methods may already exist on `Store` and `PebbleStore` — only add if missing. If the real methods have different signatures, adjust the test + mock to match.)

- [ ] **Step 4: Run the failing test**

Run: `go test ./internal/metafetch/ -run TestService_LazyBackfill -count=1`
Expected: FAIL (no backfill yet).

- [ ] **Step 5: Implement the backfill helper**

Create `internal/metafetch/cache_backfill.go`:

```go
// file: internal/metafetch/cache_backfill.go
// version: 1.0.0

// Lazy backfill: on a cache-miss in GetCachedCandidates, look for the
// most recent OperationResult row of type metadata_candidate_fetch
// for the same book and populate the cache from it. This avoids
// forcing every user to refetch the world after the v1 deploy.
//
// 30 days post-deploy, this code should be deleted — at that point
// every active book has either been recached or never had candidates
// in the first place. The deletion task is tracked in the design doc.

package metafetch

import (
	"encoding/json"
	"time"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

// backfillFromLatestOpResult attempts to populate the cache for bookID
// from the most recent metadata_candidate_fetch OperationResult.
// Returns (nil, nil) when no backfill source is available.
func (mfs *Service) backfillFromLatestOpResult(bookID string) (*MetadataCandidateCache, error) {
	if mfs == nil || mfs.db == nil {
		return nil, nil
	}
	// Scan the most recent 50 fetch ops — generous enough to cover
	// the common case but bounded so this stays cheap.
	ops, err := mfs.db.GetRecentOperationsByType("metadata_candidate_fetch", 50)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		row, err := mfs.db.GetOperationResultsForBook(op.ID, bookID)
		if err != nil || row == nil {
			continue
		}
		// Decode the result_json. The schema is
		// {"candidates":[{...}, ...]} per the v0 batch handler.
		var payload struct {
			Candidates []json.RawMessage `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(row.ResultJSON), &payload); err != nil {
			continue
		}
		if len(payload.Candidates) == 0 {
			continue
		}
		if len(payload.Candidates) > metadataCacheTopN {
			payload.Candidates = payload.Candidates[:metadataCacheTopN]
		}
		entry := &MetadataCandidateCache{
			BookID:     bookID,
			Candidates: payload.Candidates,
			FetchedAt:  fetchedAtOrNow(op.CreatedAt),
			SourceHash: "backfill",
		}
		if perr := mfs.db.PutMetadataCache(entry); perr != nil {
			// Don't fail the read because the write failed; return
			// the in-memory entry.
			return entry, nil
		}
		return entry, nil
	}
	return nil, nil
}

func fetchedAtOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return nowUTC()
	}
	return t.UTC()
}
```

- [ ] **Step 6: Hook backfill into GetCachedCandidates**

Edit `internal/metafetch/cache.go`, find `GetCachedCandidates`, and replace its body with:

```go
func (mfs *Service) GetCachedCandidates(bookID string) (*MetadataCandidateCache, bool, error) {
	if mfs == nil || mfs.db == nil {
		return nil, false, nil
	}
	entry, err := mfs.db.GetMetadataCache(bookID)
	if err != nil {
		return nil, false, err
	}
	if entry == nil {
		// Lazy backfill from the most recent metadata_candidate_fetch
		// OperationResult, if any. v1 transitional only — see
		// cache_backfill.go for the deletion plan.
		backfilled, berr := mfs.backfillFromLatestOpResult(bookID)
		if berr != nil {
			return nil, false, berr
		}
		entry = backfilled
	}
	if entry == nil {
		return nil, false, nil
	}
	return entry, entry.IsFresh(), nil
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/metafetch/ -run TestService_(GetCachedCandidates|LazyBackfill) -count=1 -v`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/metafetch/cache.go internal/metafetch/cache_backfill.go internal/metafetch/cache_backfill_test.go internal/database/mock_store.go
git commit -m "feat(metafetch): lazy backfill from latest OperationResult on cache miss

METADATA-CACHED-MATCHER task 5. v1 transitional code that avoids
forcing every active book to refetch after deploy:

- backfillFromLatestOpResult scans the 50 most-recent
  metadata_candidate_fetch ops for a matching OperationResult row,
  decodes the {candidates: [...]} payload, and writes a cache entry
  with FetchedAt = op.CreatedAt.
- GetCachedCandidates calls into backfill on cache-miss before
  returning nil.

Deletes itself 30 days post-deploy (tracked as follow-up). After
that point every active book has been recached or has nothing
worth backfilling.

2 tests cover the happy path (cache miss + op exists → backfill
succeeds) and the no-op path (no ops → nil)."
```

---

## Task 6: Per-book endpoint — cache-first + `?refresh`

**Files:**
- Modify: `internal/server/metadata_handlers.go`

- [ ] **Step 1: Find the existing fetch handler**

Run: `grep -n "func.*handleFetchMetadata\|/metadata/fetch\|fetchMetadataForBook" internal/server/metadata_handlers.go | head -5`

Locate the handler that powers `POST /api/v1/audiobooks/:id/metadata/fetch`. Read its current body — it's the "fetch fresh, return result" path with no cache awareness.

- [ ] **Step 2: Modify the handler to be cache-first**

Replace the handler body with this skeleton (adjust the handler name to match the existing one — likely `handleFetchMetadata` or `handleFetchCandidates`):

```go
// handleFetchMetadata returns the cached candidates for a book, or
// fetches fresh + replaces the cache when ?refresh=true.
func (s *Server) handleFetchMetadata(c *gin.Context) {
	bookID := c.Param("id")
	refresh := c.Query("refresh") == "true"

	mfs := s.metadataFetchService
	if mfs == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}

	// Cache-first read unless ?refresh=true.
	if !refresh {
		entry, fresh, err := mfs.GetCachedCandidates(bookID)
		if err != nil {
			httputil.InternalError(c, "failed to read metadata cache", err)
			return
		}
		if entry != nil {
			httputil.RespondWithOK(c, gin.H{
				"book_id":     entry.BookID,
				"candidates":  entry.Candidates, // []json.RawMessage; client decodes
				"fetched_at":  entry.FetchedAt,
				"is_fresh":    fresh,
				"from_cache":  true,
			})
			return
		}
	}

	// Refresh path OR cache-miss: fetch fresh, populate cache.
	entry, err := mfs.FetchAndCache(c.Request.Context(), bookID, "", metafetch.SearchOptions{})
	if err != nil {
		httputil.InternalError(c, "failed to fetch metadata", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"book_id":     entry.BookID,
		"candidates":  entry.Candidates,
		"fetched_at":  entry.FetchedAt,
		"is_fresh":    true,
		"from_cache":  false,
	})
}
```

- [ ] **Step 3: Write a handler test**

Run: `grep -n "func TestHandler_FetchMetadata\|TestFetchCandidates" internal/server/*_test.go | head`

If an existing test for the fetch handler exists, modify it to assert the new contract (`from_cache: true` when an entry exists, `from_cache: false` when `?refresh=true` or no cache). If none exists, create one in `internal/server/metadata_handlers_test.go`:

```go
func TestHandler_FetchMetadata_CacheHit(t *testing.T) {
	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			return &database.MetadataCandidateCache{
				BookID:    "book-1",
				FetchedAt: time.Now(),
				Candidates: []json.RawMessage{json.RawMessage(`{"title":"X"}`)},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest("POST", "/api/v1/audiobooks/book-1/metadata/fetch", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["data"].(map[string]any)["from_cache"])
}

func TestHandler_FetchMetadata_RefreshBypassesCache(t *testing.T) {
	getCalled := 0
	mock := &database.MockStore{
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			getCalled++
			return &database.MetadataCandidateCache{BookID: bookID, FetchedAt: time.Now()}, nil
		},
		// Wire whatever the search path needs (GetBookByID etc.) — adjust
		// to match newTestServer's existing fixtures.
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest("POST", "/api/v1/audiobooks/book-1/metadata/fetch?refresh=true", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	// Cache GET should NOT have been called when refresh=true.
	assert.Equal(t, 0, getCalled)
}
```

(Adjust `newTestServer` to match the existing test-setup function in the file.)

- [ ] **Step 4: Run server tests**

Run: `go test ./internal/server/ -run TestHandler_FetchMetadata -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run the full server subset**

Run: `go test ./internal/server/ -short -race -timeout=180s -run "TestHandler_|TestNewServer|TestRegister|TestServer" -skip "TestOrganizeService_ViaHTTP|TestAddImportPathAutoScan|TestITunesImport_|TestE2E_ITunesImport|TestStartScanOperation|TestStartOrganizeOperation"`
Expected: ok ~10s.

- [ ] **Step 6: Commit**

```bash
git add internal/server/metadata_handlers.go internal/server/metadata_handlers_test.go
git commit -m "feat(server): per-book metadata fetch is cache-first; ?refresh=true forces

METADATA-CACHED-MATCHER task 6. POST /audiobooks/:id/metadata/fetch
now consults the per-book cache before hitting the metadata sources:

- No query param: cache-respecting read. Returns the cached entry
  with is_fresh + from_cache flags. Cache-miss falls through to
  FetchAndCache (one fresh fetch, populates the cache).
- ?refresh=true: skips the cache read entirely and forces
  FetchAndCache.

Response shape unchanged for the candidates field (still
[]MetadataCandidate); new from_cache + is_fresh fields are
additive — older frontends ignore them.

Two new handler tests cover cache-hit (no fetch) and refresh
(skips cache)."
```

---

## Task 7: Batch fetch — always invalidate, write to cache

**Files:**
- Modify: `internal/server/metadata_batch_candidates.go`

- [ ] **Step 1: Find the per-book fetch loop in the batch handler**

Run: `grep -n "fetchCandidateForBook\|handleBatchFetchCandidates\|metafetch.MetadataCandidate" internal/server/metadata_batch_candidates.go | head -10`

The batch handler loops over book IDs, calls a per-book fetch function, and writes each result to `OperationResult`. We need it to ALSO call `FetchAndCache` so the cache gets populated.

- [ ] **Step 2: Modify `fetchCandidateForBook`**

In `internal/server/metadata_batch_candidates.go`, locate `fetchCandidateForBook` (around line 181). After the existing code that produces a `MetadataCandidate` result for the book, add a cache write:

Replace the return path of `fetchCandidateForBook` so it also calls `s.metadataFetchService.FetchAndCache(...)` for the book. The simplest pattern: replace the search call itself with `FetchAndCache`, which already writes to the cache and returns the entry. Then convert the cache entry to the existing `OperationResult` JSON shape for back-compat with the operation-progress UI.

Concretely: find the line that calls `s.metadataFetchService.SearchMetadataForBook(...)` (or similar) inside `fetchCandidateForBook` and replace with:

```go
entry, err := s.metadataFetchService.FetchAndCache(ctx, book.ID, "", metafetch.SearchOptions{})
if err != nil {
	return ..., err
}
// Decode the cached []json.RawMessage back into MetadataCandidate
// for the OperationResult payload (back-compat with the progress UI).
candidates := make([]metafetch.MetadataCandidate, 0, len(entry.Candidates))
for _, raw := range entry.Candidates {
	var c metafetch.MetadataCandidate
	if jerr := json.Unmarshal(raw, &c); jerr == nil {
		candidates = append(candidates, c)
	}
}
// ... rest of the existing function uses candidates as before ...
```

- [ ] **Step 3: Delete `handleGetPendingReview`**

Run: `grep -n "handleGetPendingReview\|GetPendingReview" internal/server/metadata_batch_candidates.go internal/server/server_lifecycle.go | head`

Delete:
- The `handleGetPendingReview` function (around line 903 in metadata_batch_candidates.go).
- Its route registration line in `server_lifecycle.go` (search for `pending-review` or `GetPendingReview`).

The new `GET /metadata/cached?status=pending` endpoint (Task 8) supersedes it.

- [ ] **Step 4: Build + test**

Run: `go build ./... && go vet ./...`
Expected: clean.

Run: `go test ./internal/server/ -short -race -timeout=180s -run "TestHandler_BatchFetch|TestHandler_GetLatestMetadata" -count=1`
Expected: PASS (existing tests should still pass — the cache write is additive and the OperationResult shape is preserved).

- [ ] **Step 5: Commit**

```bash
git add internal/server/metadata_batch_candidates.go internal/server/server_lifecycle.go
git commit -m "feat(server): batch fetch always writes to cache; delete pending-review

METADATA-CACHED-MATCHER task 7. The bulk metadata-fetch handler
(POST /audiobooks/metadata/batch-fetch) now invalidates + writes
the per-book cache for every targeted book:

- fetchCandidateForBook calls FetchAndCache, which both runs the
  search and replaces the cache row. The decoded []MetadataCandidate
  is still written to OperationResult for the in-flight progress
  UI — but the cache is now the canonical read source for
  candidate review.
- GET /audiobooks/metadata/pending-review (handleGetPendingReview)
  is deleted along with its route. The new GET /metadata/cached
  endpoint (next task) supersedes it.

No frontend caller of /pending-review remains in this PR (the
caller in Library.tsx is removed in task 11)."
```

---

## Task 8: List-cached endpoint

**Files:**
- Create: `internal/server/metadata_cached_handlers.go`
- Create: `internal/server/metadata_cached_handlers_test.go`
- Modify: `internal/server/server_lifecycle.go`

- [ ] **Step 1: Write the failing test**

Create `internal/server/metadata_cached_handlers_test.go`:

```go
// file: internal/server/metadata_cached_handlers_test.go
// version: 1.0.0

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

func TestHandler_ListCachedCandidates_Empty(t *testing.T) {
	mock := &database.MockStore{
		ListMetadataCacheKeysFunc: func() ([]database.MetadataCacheSummary, error) {
			return nil, nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest("GET", "/api/v1/audiobooks/metadata/cached", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Entries []map[string]any `json:"entries"`
			Total   int              `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data.Entries)
}

func TestHandler_ListCachedCandidates_PendingFilter(t *testing.T) {
	now := time.Now()
	matched := "matched"
	mock := &database.MockStore{
		ListMetadataCacheKeysFunc: func() ([]database.MetadataCacheSummary, error) {
			return []database.MetadataCacheSummary{
				{BookID: "a", FetchedAt: now, CandidateCount: 5},
				{BookID: "b", FetchedAt: now, CandidateCount: 3},
			}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == "b" {
				return &database.Book{ID: "b", MetadataReviewStatus: &matched}, nil
			}
			return &database.Book{ID: id}, nil // status==null → pending
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest("GET", "/api/v1/audiobooks/metadata/cached?status=pending", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Entries []map[string]any `json:"entries"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Entries, 1)
	assert.Equal(t, "a", resp.Data.Entries[0]["book_id"])
}
```

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/server/ -run TestHandler_ListCachedCandidates -count=1`
Expected: FAIL — handler doesn't exist.

- [ ] **Step 3: Implement the handler**

Create `internal/server/metadata_cached_handlers.go`:

```go
// file: internal/server/metadata_cached_handlers.go
// version: 1.0.0

// Cached metadata-candidate enumeration. Powers the Review popup —
// returns one row per book with cached candidates, filtered by the
// book's MetadataReviewStatus and (optionally) cache staleness.

package server

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/jdfalk/audiobook-organizer/internal/httputil"
)

type cachedCandidateRow struct {
	BookID         string    `json:"book_id"`
	Title          string    `json:"title,omitempty"`
	FetchedAt      time.Time `json:"fetched_at"`
	CandidateCount int       `json:"candidate_count"`
	IsFresh        bool      `json:"is_fresh"`
	ReviewStatus   *string   `json:"review_status,omitempty"`
}

// handleListCachedCandidates implements GET /api/v1/audiobooks/metadata/cached.
//
// Query params:
//   - status: pending | matched | no_match | all (default: all)
//   - stale:  true | false (default: false → include both)
//   - limit, offset: standard pagination (default 200/0)
func (s *Server) handleListCachedCandidates(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	statusFilter := c.DefaultQuery("status", "all")
	staleOnly := c.Query("stale") == "true"
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	summaries, err := store.ListMetadataCacheKeys()
	if err != nil {
		httputil.InternalError(c, "failed to list cached candidates", err)
		return
	}

	rows := make([]cachedCandidateRow, 0, len(summaries))
	for _, s2 := range summaries {
		// Cache freshness check.
		fresh := time.Since(s2.FetchedAt) < database.MetadataCacheTTL
		if staleOnly && fresh {
			continue
		}

		// Pull the book to apply the status filter + grab the title.
		book, _ := store.GetBookByID(s2.BookID)
		// Status filter
		bookStatus := ""
		if book != nil && book.MetadataReviewStatus != nil {
			bookStatus = *book.MetadataReviewStatus
		}
		switch statusFilter {
		case "pending":
			if bookStatus != "" {
				continue
			}
		case "matched":
			if bookStatus != "matched" {
				continue
			}
		case "no_match":
			if bookStatus != "no_match" {
				continue
			}
		case "all":
			// no filter
		default:
			// unknown filter → behave as "all"
		}

		row := cachedCandidateRow{
			BookID:         s2.BookID,
			FetchedAt:      s2.FetchedAt,
			CandidateCount: s2.CandidateCount,
			IsFresh:        fresh,
		}
		if book != nil {
			row.Title = book.Title
			row.ReviewStatus = book.MetadataReviewStatus
		}
		rows = append(rows, row)
	}

	total := len(rows)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	page := rows[offset:end]

	httputil.RespondWithOK(c, gin.H{
		"entries": page,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}
```

- [ ] **Step 4: Wire the route**

In `internal/server/server_lifecycle.go`, find the existing metadata routes block (search for `/audiobooks/.*metadata` route registration) and add the new route nearby:

```go
protected.GET("/audiobooks/metadata/cached", s.perm(auth.PermLibraryRead), s.handleListCachedCandidates)
```

Use the same `s.perm(...)` permission the other listing endpoints use — check a neighboring metadata route for the exact constant.

- [ ] **Step 5: Run the test**

Run: `go test ./internal/server/ -run TestHandler_ListCachedCandidates -count=1 -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/metadata_cached_handlers.go internal/server/metadata_cached_handlers_test.go internal/server/server_lifecycle.go
git commit -m "feat(server): GET /audiobooks/metadata/cached for Review popup

METADATA-CACHED-MATCHER task 8. New listing endpoint that powers
the Review popup over the entire cache:

- Query params: status (pending|matched|no_match|all),
  stale (true|false), limit/offset.
- Filters on Book.MetadataReviewStatus (pending = null).
- Returns one row per book with FetchedAt, candidate_count,
  is_fresh, plus book title + review_status.

Two handler tests: empty store + status filter (excludes 'matched'
books from 'pending' queries)."
```

---

## Task 9: Frontend API client + types

**Files:**
- Modify: `web/src/services/api.ts`

- [ ] **Step 1: Find the existing fetch/review API calls**

Run: `grep -n "batchFetchCandidates\|getPendingReview\|getOperationResults" web/src/services/api.ts | head`

- [ ] **Step 2: Add the new listCachedCandidates function**

In `web/src/services/api.ts`, add the type and function (find a sensible spot near the other metadata helpers):

```ts
export interface CachedCandidateRow {
  book_id: string;
  title?: string;
  fetched_at: string;
  candidate_count: number;
  is_fresh: boolean;
  review_status?: string | null;
}

export interface ListCachedResponse {
  entries: CachedCandidateRow[];
  total: number;
  limit: number;
  offset: number;
}

export async function listCachedCandidates(opts: {
  status?: 'pending' | 'matched' | 'no_match' | 'all';
  stale?: boolean;
  limit?: number;
  offset?: number;
} = {}): Promise<ListCachedResponse> {
  const params = new URLSearchParams();
  if (opts.status) params.set('status', opts.status);
  if (opts.stale) params.set('stale', 'true');
  if (opts.limit) params.set('limit', String(opts.limit));
  if (opts.offset) params.set('offset', String(opts.offset));
  const qs = params.toString();
  const url = `${API_BASE}/audiobooks/metadata/cached${qs ? `?${qs}` : ''}`;
  const resp = await fetch(url, { credentials: 'include' });
  if (!resp.ok) throw new Error(`listCachedCandidates: ${resp.status}`);
  const body = await resp.json();
  return body.data as ListCachedResponse;
}
```

- [ ] **Step 3: Delete `getPendingReview`**

Find and delete the `getPendingReview` function (the function that hits `/audiobooks/metadata/pending-review`) — Library.tsx's `handleResumeReview` calls it and gets cleaned up in task 11.

- [ ] **Step 4: TS check**

Run: `cd web && npx tsc --noEmit 2>&1 | head -10`

This will show errors from `Library.tsx` referencing `getPendingReview` — those are fixed in Task 11. Build will be temporarily red until then; that's OK because we ship the backend + frontend in separate PRs.

- [ ] **Step 5: Commit**

```bash
git add web/src/services/api.ts
git commit -m "feat(web/api): add listCachedCandidates; remove getPendingReview

METADATA-CACHED-MATCHER task 9. Frontend API client switches to the
new cached-candidate enumeration endpoint:

- listCachedCandidates({status, stale, limit, offset}) → typed
  ListCachedResponse, used by the new Review popup data source.
- getPendingReview deleted. Last caller (Library.tsx
  handleResumeReview) is removed in task 11.

TS errors in Library.tsx are expected until task 11 lands; backend
and frontend PRs are intentionally separate per the design doc
rollout section."
```

---

## Task 10: BookDetail — cache-first + Refresh

**Files:**
- Modify: `web/src/pages/BookDetail.tsx`

- [ ] **Step 1: Locate the current fetch handler**

Run: `grep -n "handleFetchMetadata\|fetchingMetadata\|onFetchMetadata" web/src/pages/BookDetail.tsx | head`

- [ ] **Step 2: Add a Refresh button + cache-aware messaging**

In `BookDetail.tsx`, find the candidates section (where the existing "Fetch Metadata" button is). Modify the button to:

1. Trigger the existing endpoint with no `?refresh` param (cache-first).
2. Show a small icon button next to the candidate list labeled "Refresh" that calls the same endpoint with `?refresh=true`.
3. Render "Last fetched X days ago" when the response includes `fetched_at` and the age is > 7 days.

The new handler (next to existing `handleFetchMetadata`):

```tsx
const handleRefreshMetadata = async () => {
  setFetchingMetadata(true);
  try {
    const resp = await fetch(
      `/api/v1/audiobooks/${audiobookId}/metadata/fetch?refresh=true`,
      { method: 'POST', credentials: 'include' },
    );
    if (!resp.ok) throw new Error('refresh failed');
    const body = await resp.json();
    setCandidates(body.data.candidates);
    setFetchedAt(body.data.fetched_at);
    toast(`Refreshed: ${body.data.candidates.length} candidates`, 'info');
  } catch (e) {
    toast('Failed to refresh metadata', 'error');
  } finally {
    setFetchingMetadata(false);
  }
};
```

Update the existing `handleFetchMetadata` to NOT send `?refresh=true` so it benefits from the cache-first path.

Add `<IconButton onClick={handleRefreshMetadata}><RefreshIcon /></IconButton>` next to the candidate list header.

Add a small caption under the candidate list:

```tsx
{fetchedAt && (
  <Typography variant="caption" color="text.secondary">
    Last fetched {distanceInWords(new Date(fetchedAt), new Date())} ago
    {!isFresh && ' — Refresh recommended'}
  </Typography>
)}
```

(`distanceInWords` is the existing date-fns helper used elsewhere in BookDetail; check the surrounding imports.)

- [ ] **Step 3: TS check + visual test**

Run: `cd web && npx tsc --noEmit 2>&1 | head -10`
Expected: clean (except the same Library.tsx errors from task 9 if not yet fixed).

Verify visually by `make web-dev` + opening a book page. The "Fetch Metadata" button should be near-instant on second click (cache hit). The Refresh icon should trigger a network request.

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/BookDetail.tsx
git commit -m "feat(web/BookDetail): cache-first metadata fetch + Refresh button

METADATA-CACHED-MATCHER task 10. The BookDetail page now reads the
per-book cache by default and shows a Refresh icon for forced
fetches:

- handleFetchMetadata: drops the implicit refresh, hits the
  endpoint with no query params → cache-first.
- handleRefreshMetadata: explicit ?refresh=true bypass.
- 'Last fetched X days ago' caption appears under the candidate
  list; '— Refresh recommended' suffix when is_fresh is false."
```

---

## Task 11: Library toolbar + page wiring

**Files:**
- Modify: `web/src/components/library/LibraryToolbar.tsx`
- Modify: `web/src/pages/Library.tsx`

- [ ] **Step 1: Find the existing toolbar buttons**

Run: `grep -n "Fetch Unmatched\|onFetchAllUnmatched\|metadataReviewOpen\|reviewOp" web/src/pages/Library.tsx web/src/components/library/LibraryToolbar.tsx | head -20`

- [ ] **Step 2: Rename "Fetch & Review" → "Fetch Selected" in LibraryToolbar**

In `LibraryToolbar.tsx`, find the button labeled "Fetch & Review" (or similar, when selection is active). Replace its label with "Fetch Selected". Keep the same handler prop name to avoid breakage; only the label changes.

```tsx
<Button variant="outlined" size="small" onClick={onFetchSelected} disabled={!hasSelection}>
  Fetch Selected
</Button>
```

Where `onFetchSelected` is the same prop that used to be wired to "Fetch & Review" (or rename the prop alongside if it was `onFetchAndReview`).

- [ ] **Step 3: Add a global Review button**

In `LibraryToolbar.tsx`, add a Review button (always visible) with a badge showing the count of pending entries:

```tsx
<Tooltip title="Review pending metadata candidates">
  <Badge badgeContent={pendingReviewCount} color="primary" max={999}>
    <Button variant="outlined" size="small" onClick={onOpenReview}>
      Review
    </Button>
  </Badge>
</Tooltip>
```

Add `pendingReviewCount: number` and `onOpenReview: () => void` to the toolbar's `Props` type.

- [ ] **Step 4: Wire from Library.tsx**

In `Library.tsx`:

1. Add state for `pendingReviewCount`:

```tsx
const [pendingReviewCount, setPendingReviewCount] = useState(0);

useEffect(() => {
  api.listCachedCandidates({ status: 'pending', limit: 1 })
     .then((r) => setPendingReviewCount(r.total))
     .catch(() => {});
}, []);
```

2. Rewrite `handleFetchAllUnmatched` to NOT auto-open the review:

```tsx
const handleFetchAllUnmatched = async () => {
  try {
    const resp = await api.batchFetchCandidates({
      selection: { filter: { only_unmatched: true } },
    });
    if (!resp.operation_id) {
      toast(resp.message ?? 'All books already have matched candidates.', 'info');
      return;
    }
    startOperationPolling(resp.operation_id, 'metadata_candidate_fetch');
    toast(
      `Fetching candidates for ${resp.book_count ?? 'unmatched'} books. Click Review when complete.`,
      'info',
    );
    // NOTE: no longer auto-opens the Review dialog.
  } catch {
    toast('Failed to start unmatched fetch', 'error');
  }
};
```

3. Add `handleFetchSelected` (the renamed handler):

```tsx
const handleFetchSelected = async () => {
  const ids = Array.from(selected);
  if (ids.length === 0) return;
  try {
    const resp = await api.batchFetchCandidates({ book_ids: ids });
    if (!resp.operation_id) {
      toast('No books in selection had candidates to fetch.', 'info');
      return;
    }
    startOperationPolling(resp.operation_id, 'metadata_candidate_fetch');
    toast(`Fetching candidates for ${ids.length} selected books. Click Review when complete.`, 'info');
    // NOTE: no longer auto-opens the Review dialog.
  } catch {
    toast('Failed to start selected fetch', 'error');
  }
};
```

4. Add the global Review handler:

```tsx
const handleOpenReview = () => {
  setMetadataReviewOpen(true);
};
```

5. **Delete** the `metadataReviewOpId` state and the `?reviewOp=` URL handling. Find these lines (around 224–227 and 830–839 in Library.tsx) and remove them. The Review popup is no longer tied to a specific op — it pulls from `listCachedCandidates` on open.

6. **Delete** the `handleResumeReview` function (around line 1648) — `getPendingReview` is gone.

7. Pass the new props through to the toolbar:

```tsx
<LibraryToolbar
  // ... existing props ...
  onFetchSelected={handleFetchSelected}
  onOpenReview={handleOpenReview}
  pendingReviewCount={pendingReviewCount}
/>
```

- [ ] **Step 5: TS check**

Run: `cd web && npx tsc --noEmit 2>&1 | head -20`
Expected: clean (or only errors from the MetadataReviewDialog data-source change in task 12).

- [ ] **Step 6: Run the existing Library bulk-fetch tests**

Run: `cd web && npx vitest run src/pages/Library.bulkFetch.test.tsx`

If it fails because it asserted auto-open behavior, edit `Library.bulkFetch.test.tsx`:

```ts
// Old assertion:
// expect(setMetadataReviewOpen).toHaveBeenCalledWith(true);
// New assertion:
expect(setMetadataReviewOpen).not.toHaveBeenCalled();
// And assert a toast appeared:
expect(toast).toHaveBeenCalledWith(
  expect.stringMatching(/Fetching candidates for .* books/),
  'info',
);
```

- [ ] **Step 7: Commit**

```bash
git add web/src/components/library/LibraryToolbar.tsx web/src/pages/Library.tsx web/src/pages/Library.bulkFetch.test.tsx
git commit -m "feat(web/Library): rename Fetch & Review → Fetch Selected; add Review badge

METADATA-CACHED-MATCHER task 11.

Toolbar:
- 'Fetch & Review' → 'Fetch Selected'. No auto-open of the
  Review dialog on completion; toast directs the user to click
  Review when ready.
- 'Fetch Unmatched' keeps its name; same no-auto-open behavior.
- New 'Review (N)' badge button, always visible. N is the count
  of cached entries with MetadataReviewStatus == null.

Library page:
- handleFetchSelected (new) + handleFetchAllUnmatched (modified):
  neither auto-opens the dialog any more.
- handleOpenReview opens the dialog from the toolbar button.
- pendingReviewCount loaded from listCachedCandidates on mount.
- reviewOp URL param + auto-open-on-op-complete logic removed.
- handleResumeReview + getPendingReview call site removed.

Library.bulkFetch.test.tsx asserts no auto-open + toast appears."
```

---

## Task 12: MetadataReviewDialog data source switch

**Files:**
- Modify: `web/src/components/dialogs/MetadataReviewDialog.tsx` (or wherever the dialog lives)

- [ ] **Step 1: Find the dialog**

Run: `find web/src -name "MetadataReview*.tsx" -o -name "*ReviewDialog*.tsx" 2>/dev/null | head`

- [ ] **Step 2: Replace its data source**

The dialog currently loads results via `getOperationResults(opId)` (looking for entries from a specific operation). Replace with:

```tsx
useEffect(() => {
  if (!open) return;
  setLoading(true);
  api.listCachedCandidates({ status: filter /* 'pending' | 'all' | etc */, limit: 500 })
    .then((r) => {
      setEntries(r.entries);
      setTotal(r.total);
    })
    .catch(() => toast('Failed to load review queue', 'error'))
    .finally(() => setLoading(false));
}, [open, filter]);
```

`filter` is a new piece of dialog-local state with tabs `Pending | All | Matched | No match | Stale`. Default tab: `Pending`.

For each row, render:
- Title (from `entry.title`)
- Cache age (e.g., "fetched 3 days ago" using existing date helper)
- "is_fresh" green dot OR "Stale" amber chip
- Action buttons: Accept top / Choose / Skip / Refresh this one.

Per-row Refresh handler:

```tsx
const handleRefreshRow = async (bookID: string) => {
  await fetch(`/api/v1/audiobooks/${bookID}/metadata/fetch?refresh=true`, {
    method: 'POST', credentials: 'include',
  });
  // Reload the list
  const r = await api.listCachedCandidates({ status: filter, limit: 500 });
  setEntries(r.entries);
};
```

- [ ] **Step 3: Drop the `operationId` prop**

The dialog used to take an `operationId` prop (the op the user just ran). It's no longer needed — the dialog pulls everything cached. Update the dialog's `Props` type to remove `operationId` and update the caller (`Library.tsx`) to stop passing it.

- [ ] **Step 4: TS check**

Run: `cd web && npx tsc --noEmit 2>&1 | head`
Expected: clean.

- [ ] **Step 5: Manual smoke test**

Run: `make web-dev` (or `cd web && npm run dev`).

1. Open the Library.
2. Click "Fetch Unmatched". Toast appears. Dialog does NOT auto-open.
3. Click the "Review" toolbar button. Dialog opens, shows pending candidates with cache ages.
4. Click "Refresh this one" on a row. Network tab shows `POST /:id/metadata/fetch?refresh=true`. Row re-renders.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/dialogs/MetadataReviewDialog.tsx web/src/pages/Library.tsx
git commit -m "feat(web/MetadataReviewDialog): pull from listCachedCandidates

METADATA-CACHED-MATCHER task 12. The Review dialog no longer depends
on a specific operation_id — it loads the full cache via the new
endpoint and supports filter tabs (Pending/All/Matched/No
match/Stale). Per-row 'Refresh this one' button hits
?refresh=true and reloads.

operationId prop dropped from MetadataReviewDialogProps."
```

---

## Task 13: Backend PR — build, test, ship, deploy

- [ ] **Step 1: Verify everything is on the backend branch**

Run: `git status --short && git log --oneline -10`

You should see commits from Tasks 1–8 (backend) on the branch.

- [ ] **Step 2: Build + vet + full server test subset**

Run:

```bash
go build ./... && go vet ./...
go test ./internal/database/ ./internal/metafetch/ -short -race -timeout=180s
go test ./internal/server/ -short -race -timeout=180s \
  -run "TestHandler_|TestNewServer|TestRegister|TestServer" \
  -skip "TestOrganizeService_ViaHTTP|TestAddImportPathAutoScan|TestITunesImport_|TestE2E_ITunesImport|TestStartScanOperation|TestStartOrganizeOperation|TestBackfillExternalIDsCollectsBookPIDs"
```

Expected: all green.

- [ ] **Step 3: Push branch + open PR**

```bash
git push -u origin refactor/metadata-cached-matcher-backend
gh pr create --base main --head refactor/metadata-cached-matcher-backend \
  --title "feat(metadata): cached matcher backend — per-book cache + 3 endpoints" \
  --body "$(cat <<'EOF'
## Summary

Backend half of METADATA-CACHED-MATCHER. Adds the per-book PebbleDB
cache, the 3 cache methods on *metafetch.Service, repoints the per-book
+ bulk fetch endpoints around the cache, and adds the new
GET /metadata/cached enumeration endpoint.

See \`docs/architecture/metadata-cached-matcher-design.md\` for the design.

## Test plan

- [x] go build / vet clean
- [x] internal/database, internal/metafetch, internal/server tests green (SERVER-THIN-8 skipped)
- [ ] Deploy + verify cache lookups in prod logs

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Admin-merge + deploy**

```bash
gh pr merge <N> --rebase --admin --delete-branch
git checkout main && git pull --ff-only origin main
make deploy
sleep 3 && curl -sS -o /dev/null -w "HTTP %{http_code}\n" --max-time 5 http://<server>/api/v1/system/version
```

Expected: HTTP 200.

- [ ] **Step 5: Cleanup worktree**

```bash
git worktree remove /Users/jdfalk/.worktrees/<slug> --force
git branch -D refactor/metadata-cached-matcher-backend
```

---

## Task 14: Frontend PR — build, test, ship

- [ ] **Step 1: Switch to or create the frontend worktree**

```bash
git worktree add /Users/jdfalk/.worktrees/audiobook-organizer-matcher-fe -b refactor/metadata-cached-matcher-frontend origin/main
cd /Users/jdfalk/.worktrees/audiobook-organizer-matcher-fe
```

(If tasks 9–12 were done in this worktree already, skip the create step.)

- [ ] **Step 2: Build + TS check**

```bash
cd web
npx tsc --noEmit
npm run build
cd ..
make build
```

Expected: clean.

- [ ] **Step 3: Run vitest**

```bash
cd web
npx vitest run src/pages/Library.bulkFetch.test.tsx
cd ..
```

Expected: PASS.

- [ ] **Step 4: Push + PR + merge + deploy**

```bash
git push -u origin refactor/metadata-cached-matcher-frontend
gh pr create --base main --head refactor/metadata-cached-matcher-frontend \
  --title "feat(web): cached matcher frontend — Review button + cache-aware BookDetail" \
  --body "Frontend half of METADATA-CACHED-MATCHER. Renames Fetch & Review → Fetch Selected (no auto-open), adds always-visible Review badge button, switches MetadataReviewDialog to listCachedCandidates, makes BookDetail cache-first with a Refresh icon. See docs/architecture/metadata-cached-matcher-design.md."

gh pr merge <N> --rebase --admin --delete-branch
git checkout main && git pull --ff-only origin main
make deploy
```

Expected: deploys successfully; HTTP 200 on health check.

- [ ] **Step 5: Smoke test in browser**

1. Open `https://<server>` (or local equivalent) in a fresh tab.
2. Library page renders. "Review (N)" button visible with badge.
3. Select 2 books. "Fetch Selected" appears in toolbar. Click it. Toast appears, dialog does not auto-open.
4. Click "Review" toolbar button. Dialog opens with pending candidates.
5. Click a book to drill in. BookDetail "Fetch Metadata" returns instantly on second click. Refresh icon triggers a real fetch.

- [ ] **Step 6: Cleanup**

```bash
git worktree remove /Users/jdfalk/.worktrees/audiobook-organizer-matcher-fe --force
git branch -D refactor/metadata-cached-matcher-frontend
```

---

## Self-Review

**Spec coverage check:**

- Storage (PebbleDB `metadata_cache:<book_id>`, 30-day TTL) → Tasks 1, 2.
- Service methods (Get/FetchAndCache/ListCachedSummaries) → Task 4.
- Lazy backfill → Task 5.
- HTTP endpoints (per-book cache-first, bulk invalidates, list cached) → Tasks 6, 7, 8.
- Frontend (Fetch Selected rename, Review badge, BookDetail cache-first, Dialog data source) → Tasks 10, 11, 12.
- Cleanup (delete `pending-review`, `reviewOp` URL handling, `handleResumeReview`) → Tasks 7, 11.
- Rollout (two PRs, backend first, frontend second) → Tasks 13, 14.

**Type consistency:**

- `MetadataCandidateCache` defined in Task 1, alias re-exported in Task 4. Referenced consistently.
- `MetadataCacheSummary` defined in Task 1, used in Tasks 4, 8, 9.
- HTTP response shape: `{book_id, candidates, fetched_at, is_fresh, from_cache}` consistent across Tasks 6, 9, 10.

**Placeholder scan:** none — every step has the actual code or command.

**Open items in design doc:** Lazy-backfill 30-day deletion + content-hash invalidation are tracked there but not implemented in this plan, matching the design's v1 scope.
