// file: internal/scanner/scanner_reliability_test.go
// version: 1.0.0
// guid: 9f8e7d6c-5b4a-3921-8c7d-6e5f4a3b2c1d
// last-edited: 2026-07-17

// Tests for the 2026-07-17 multi-discipline-review scanner findings:
// R-4 (refcounted scan/works caches surviving concurrent runs) and
// H5 (duplicate-detection store errors must skip import, not re-import).

package scanner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// resetScanCacheRefsForTest forces the refcounted scan-cache globals back to a
// clean state regardless of test outcome. Name is task-unique (relscn) to
// avoid parallel-worker helper collisions.
func resetScanCacheRefsForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		globalScanCacheMu.Lock()
		scanCacheRefs = 0
		scanCacheFullRuns = 0
		globalScanCache = nil
		globalScanCacheMu.Unlock()
		worksLookupMu.Lock()
		worksLookupRefs = 0
		worksLookupCache = nil
		worksLookupReady = false
		worksLookupMu.Unlock()
	})
}

// peekGlobalScanCache reads the shared cache under its lock.
func peekGlobalScanCache() map[string]database.ScanCacheEntry {
	globalScanCacheMu.RLock()
	defer globalScanCacheMu.RUnlock()
	return globalScanCache
}

// TestRelScn_ScanCacheSurvivesFirstRelease reproduces R-4: a concurrent
// library.scan and library.import each acquire the cache; the first finisher's
// release must NOT clear it while the second run is still active.
func TestRelScn_ScanCacheSurvivesFirstRelease(t *testing.T) {
	resetScanCacheRefsForTest(t)

	cacheA := map[string]database.ScanCacheEntry{"/lib/a.m4b": {Mtime: 1, Size: 10}}
	cacheB := map[string]database.ScanCacheEntry{"/import/b.m4b": {Mtime: 2, Size: 20}}

	releaseA := AcquireScanCache(cacheA)
	releaseB := AcquireScanCache(cacheB)

	// First run finishes.
	releaseA()

	if got := peekGlobalScanCache(); got == nil {
		t.Fatal("R-4 regression: scan cache cleared while a concurrent run was still active")
	}

	// Second (still-active) run must still be able to skip via the cache.
	if !shouldSkipFile("/lib/a.m4b", 1, 10, peekGlobalScanCache()) {
		t.Fatal("expected incremental skip to still work for the surviving run")
	}

	releaseB()
	if got := peekGlobalScanCache(); got != nil {
		t.Fatal("scan cache should be cleared after the last run releases")
	}

	// Release funcs must be idempotent.
	releaseA()
	releaseB()
	globalScanCacheMu.RLock()
	refs := scanCacheRefs
	globalScanCacheMu.RUnlock()
	if refs != 0 {
		t.Fatalf("scanCacheRefs = %d after all releases, want 0", refs)
	}
}

// TestRelScn_FullRunDisablesIncrementalSkip verifies the documented rule: any
// active full (force) run disables the shared cache for every concurrent run —
// skipping unchanged files under a force-rescan would be a correctness bug.
func TestRelScn_FullRunDisablesIncrementalSkip(t *testing.T) {
	resetScanCacheRefsForTest(t)

	releaseIncr := AcquireScanCache(map[string]database.ScanCacheEntry{"/lib/a.m4b": {Mtime: 1, Size: 10}})
	releaseFull := AcquireScanCache(nil) // force/full run

	if got := peekGlobalScanCache(); got != nil {
		t.Fatal("an active full run must disable the shared incremental-skip cache")
	}

	releaseFull()
	releaseIncr()
	if got := peekGlobalScanCache(); got != nil {
		t.Fatal("cache should be nil after all runs release")
	}
}

// TestRelScn_ConcurrentAcquireReleaseRace hammers acquire/release from many
// goroutines under -race; each incremental run asserts mid-run that the cache
// it depends on is still installed.
func TestRelScn_ConcurrentAcquireReleaseRace(t *testing.T) {
	resetScanCacheRefsForTest(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cache := map[string]database.ScanCacheEntry{
				fmt.Sprintf("/lib/f%d.m4b", i): {Mtime: int64(i), Size: int64(i)},
			}
			release := AcquireScanCache(cache)
			defer release()
			for j := 0; j < 200; j++ {
				// Worker read path (mirrors ProcessBooksParallel).
				globalScanCacheMu.RLock()
				c := globalScanCache
				globalScanCacheMu.RUnlock()
				if c == nil {
					t.Errorf("run %d: cache nil mid-run (iteration %d) with no full runs active", i, j)
					return
				}
				_ = shouldSkipFile("/nope", 0, 0, c)
			}
		}(i)
	}
	wg.Wait()

	if got := peekGlobalScanCache(); got != nil {
		t.Fatal("cache should be cleared after every concurrent run released")
	}
}

// TestRelScn_WorksLookupCacheSurvivesFirstRelease covers the works-lookup half
// of R-4: the first finisher's release must not drop the map (degrading the
// still-running scan to O(N) GetAllWorks per book).
func TestRelScn_WorksLookupCacheSurvivesFirstRelease(t *testing.T) {
	resetScanCacheRefsForTest(t)
	prevStore := getStore()
	SetStore(nil) // init path with no store: empty but ready cache
	t.Cleanup(func() { SetStore(prevStore) })

	AcquireWorksLookupCache()
	AcquireWorksLookupCache()

	ReleaseWorksLookupCache() // first run finishes

	worksLookupMu.RLock()
	ready := worksLookupReady
	cache := worksLookupCache
	worksLookupMu.RUnlock()
	if !ready || cache == nil {
		t.Fatal("R-4 regression: works lookup cache dropped while a concurrent run was still active")
	}

	ReleaseWorksLookupCache() // last run finishes

	worksLookupMu.RLock()
	ready = worksLookupReady
	cache = worksLookupCache
	worksLookupMu.RUnlock()
	if ready || cache != nil {
		t.Fatal("works lookup cache should be dropped after the last run releases")
	}
}

// TestRelScn_HashLookupStoreErrorSkipsImport covers H5: when the store fails
// during duplicate-detection hash lookups and no duplicate can be proven, the
// file must be SKIPPED (error returned, no CreateBook), never re-imported.
func TestRelScn_HashLookupStoreErrorSkipsImport(t *testing.T) {
	resetScanCacheRefsForTest(t)

	storeErr := errors.New("pebble: injected I/O failure")
	created := false
	mock := &database.MockStore{
		GetBookByFilePathFunc: func(path string) (*database.Book, error) { return nil, nil },
		GetBookByFileHashFunc: func(hash string) (*database.Book, error) { return nil, storeErr },
		GetBookByOriginalHashFunc: func(hash string) (*database.Book, error) {
			return nil, storeErr
		},
		GetBookByOrganizedHashFunc: func(hash string) (*database.Book, error) {
			return nil, storeErr
		},
		CreateBookFunc: func(book *database.Book) (*database.Book, error) {
			created = true
			return book, nil
		},
	}

	prevStore := getStore()
	SetStore(mock)
	t.Cleanup(func() { SetStore(prevStore) })

	skipsBefore := dupLookupSkipCount.Load()
	errsBefore := dupLookupErrCount.Load()

	book := &Book{
		FilePath: "/import/new-file.m4b",
		FileHash: "deadbeef", // pre-computed: skips on-disk hashing
	}
	err := saveBookToDatabase(context.Background(), book)
	if err == nil {
		t.Fatal("H5 regression: store failure during dup detection did not skip the import")
	}
	if !strings.Contains(err.Error(), "duplicate status undeterminable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Fatal("H5 regression: CreateBook was called despite undeterminable duplicate status")
	}
	if got := dupLookupSkipCount.Load() - skipsBefore; got != 1 {
		t.Fatalf("dupLookupSkipCount delta = %d, want 1", got)
	}
	if got := dupLookupErrCount.Load() - errsBefore; got != 3 {
		t.Fatalf("dupLookupErrCount delta = %d, want 3 (one per failing lookup)", got)
	}
}

// TestRelScn_HashLookupPartialErrorStillFindsDuplicate verifies that a store
// error on one index does not mask a duplicate found via another index: the
// found duplicate wins and the file is handled by the normal dedup path.
func TestRelScn_HashLookupPartialErrorStillFindsDuplicate(t *testing.T) {
	resetScanCacheRefsForTest(t)

	groupID := "vg-existing"
	existing := &database.Book{
		ID:             "01EXISTING",
		Title:          "Already Imported",
		FilePath:       "/library/already-imported.m4b",
		VersionGroupID: &groupID, // already version-linked → dedup path returns nil
	}
	created := false
	mock := &database.MockStore{
		GetBookByFilePathFunc: func(path string) (*database.Book, error) { return nil, nil },
		GetBookByFileHashFunc: func(hash string) (*database.Book, error) {
			return nil, errors.New("pebble: injected I/O failure")
		},
		GetBookByOriginalHashFunc: func(hash string) (*database.Book, error) {
			return existing, nil
		},
		CreateBookFunc: func(book *database.Book) (*database.Book, error) {
			created = true
			return book, nil
		},
	}

	prevStore := getStore()
	SetStore(mock)
	t.Cleanup(func() { SetStore(prevStore) })

	book := &Book{
		FilePath: "/import/duplicate-copy.m4b",
		FileHash: "deadbeef",
	}
	if err := saveBookToDatabase(context.Background(), book); err != nil {
		t.Fatalf("duplicate found via a healthy index should not error, got: %v", err)
	}
	if created {
		t.Fatal("already-version-linked duplicate must not be re-imported")
	}
}
