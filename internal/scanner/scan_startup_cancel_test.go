// file: internal/scanner/scan_startup_cancel_test.go
// version: 1.1.0
// guid: 2b8e6c14-5d97-4f30-9a1e-c7f04d82b6a3
// last-edited: 2026-09-13

package scanner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// slowWorksStore is a scanner store whose works load is slow, the shape of the
// production library (156,952 works, ~57s on 2026-09-13). GetAllWorks ignores
// cancellation, as the store method always has; ForEachWork honours it the
// way PebbleStore.ForEachWork does. Every other method a startup-only scan of
// an empty folder reaches is stubbed; anything else hits the nil embed.
type slowWorksStore struct {
	scannerStore
	importPath string
	loadDelay  time.Duration
	loads      atomic.Int32
	gen        atomic.Uint64
}

func (s *slowWorksStore) GetAllImportPaths() ([]database.ImportPath, error) {
	return []database.ImportPath{{ID: 1, Path: s.importPath, Enabled: true}}, nil
}
func (s *slowWorksStore) GetScanCacheMap() (map[string]database.ScanCacheEntry, error) {
	return map[string]database.ScanCacheEntry{}, nil
}
func (s *slowWorksStore) GetScanCacheMapContext(context.Context) (map[string]database.ScanCacheEntry, error) {
	return map[string]database.ScanCacheEntry{}, nil
}
func (s *slowWorksStore) GetDirtyBookFolders() ([]string, error) { return nil, nil }
func (s *slowWorksStore) GetDirtyBookFoldersContext(context.Context) ([]string, error) {
	return nil, nil
}
func (s *slowWorksStore) CountBooksByPathPrefix(string) (int, error) { return 0, nil }
func (s *slowWorksStore) UpdateImportPath(int, *database.ImportPath) error {
	return nil
}
func (s *slowWorksStore) GetAllWorks() ([]database.Work, error) {
	s.loads.Add(1)
	time.Sleep(s.loadDelay)
	return []database.Work{{ID: "w1", Title: "Some Work"}}, nil
}
func (s *slowWorksStore) ForEachWork(ctx context.Context, visit func(database.Work) error) error {
	s.loads.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.loadDelay):
	}
	return visit(database.Work{ID: "w1", Title: "Some Work"})
}
func (s *slowWorksStore) WorksGeneration() uint64 { return s.gen.Load() }

func installSlowWorksStore(t *testing.T, delay time.Duration) *slowWorksStore {
	t.Helper()
	resetScanCacheRefsForTest(t)
	s := &slowWorksStore{importPath: t.TempDir(), loadDelay: delay}
	s.gen.Store(1000)
	prev := getStore()
	SetStore(s)
	t.Cleanup(func() {
		SetStore(prev)
		ClearWorksLookupCache()
	})
	return s
}

// TestScanStartup_CancelDuringWorksLoadParksPromptly: a stand-down cancels the
// scan while it is loading the works table. The scan must return within a
// small bound, not after the load (57s in production, 3s here).
func TestScanStartup_CancelDuringWorksLoadParksPromptly(t *testing.T) {
	s := installSlowWorksStore(t, 3*time.Second)
	ss := NewScanService(s)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	begin := time.Now()
	err := ss.PerformScan(ctx, &ScanRequest{}, nil)
	elapsed := time.Since(begin)
	t.Logf("scan returned after %s (cancel at 200ms, works load 3s): %v", elapsed, err)
	if err == nil {
		t.Fatal("PerformScan returned nil after cancellation; want a scan-canceled error")
	}
	if elapsed > time.Second {
		t.Fatalf("scan took %s to return after a cancel during the works load; want < 1s", elapsed)
	}
}

// TestWorksLookupCache_ReusedAcrossScanRestarts: a scan that is stood down and
// re-queued three times must load the works table once, not three times.
func TestWorksLookupCache_ReusedAcrossScanRestarts(t *testing.T) {
	s := installSlowWorksStore(t, 300*time.Millisecond)
	ss := NewScanService(s)

	begin := time.Now()
	for i := 0; i < 3; i++ {
		if err := ss.PerformScan(context.Background(), &ScanRequest{}, nil); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
	}
	t.Logf("3 scans took %s with a 300ms works load", time.Since(begin))
	if got := s.loads.Load(); got != 1 {
		t.Fatalf("works table loaded %d times across 3 scan runs; want 1", got)
	}
}

// TestWorksLookupCache_InvalidatedByWorkWrite: the retained map must be
// reloaded after any work write it does not reflect, and must survive the
// scanner's own CreateWork (which it does reflect).
func TestWorksLookupCache_InvalidatedByWorkWrite(t *testing.T) {
	s := installSlowWorksStore(t, 0)
	ctx := context.Background()

	mustAcquire := func() {
		t.Helper()
		if err := AcquireWorksLookupCache(ctx); err != nil {
			t.Fatalf("AcquireWorksLookupCache: %v", err)
		}
	}

	mustAcquire()
	// The scanner creates a work: the store bumps its generation once and the
	// scanner records it in the map.
	s.gen.Add(1)
	rememberCreatedWork(&database.Work{ID: "w2", Title: "New Work"})
	ReleaseWorksLookupCache()

	mustAcquire()
	if got := s.loads.Load(); got != 1 {
		t.Fatalf("after the scanner's own CreateWork: loads = %d; want 1 (map already reflects it)", got)
	}
	if id := lookupWorkID("new work", nil); id != "w2" {
		t.Fatalf("reused map lost the scanner-created work: lookup = %q", id)
	}
	ReleaseWorksLookupCache()

	// Another writer (merge, works API edit, delete) changes a work.
	s.gen.Add(1)
	mustAcquire()
	if got := s.loads.Load(); got != 2 {
		t.Fatalf("after a foreign work write: loads = %d; want 2 (stale map must be reloaded)", got)
	}
	ReleaseWorksLookupCache()

	// A retained, idle map is never used for lookups outside a run.
	worksLookupMu.RLock()
	ready := worksLookupReady
	worksLookupMu.RUnlock()
	if ready {
		t.Fatal("retained works map is live for lookups with no scan run holding it")
	}
}

// TestWorksLookupCache_MixedOwnAndForeignWritesReload: one foreign work write
// plus one scanner CreateWork is two generation bumps against one counted
// own-write. The map reflects only the scanner's write, so the next run must
// reload it rather than reuse a map missing the foreign change.
func TestWorksLookupCache_MixedOwnAndForeignWritesReload(t *testing.T) {
	s := installSlowWorksStore(t, 0)
	ctx := context.Background()
	if err := AcquireWorksLookupCache(ctx); err != nil {
		t.Fatal(err)
	}
	s.gen.Add(1)                                                  // foreign write (merge, works API edit, delete)
	s.gen.Add(1)                                                  // the scanner's own CreateWork...
	rememberCreatedWork(&database.Work{ID: "w3", Title: "Mixed"}) // ...recorded
	ReleaseWorksLookupCache()

	if err := AcquireWorksLookupCache(ctx); err != nil {
		t.Fatal(err)
	}
	ReleaseWorksLookupCache()
	if got := s.loads.Load(); got != 2 {
		t.Fatalf("loads = %d after a foreign write mixed with an own write; want 2 (reload)", got)
	}
}

// TestWorksLookupCache_SetStoreDropsRetainedMap: a map loaded from one store
// must never serve another.
func TestWorksLookupCache_SetStoreDropsRetainedMap(t *testing.T) {
	s := installSlowWorksStore(t, 0)
	if err := AcquireWorksLookupCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	ReleaseWorksLookupCache()

	other := &slowWorksStore{importPath: s.importPath}
	other.gen.Store(s.gen.Load()) // same generation value on purpose
	SetStore(other)
	if err := AcquireWorksLookupCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	ReleaseWorksLookupCache()
	if got := other.loads.Load(); got != 1 {
		t.Fatalf("new store loads = %d; want 1 (retained map from the old store must not be reused)", got)
	}
}
