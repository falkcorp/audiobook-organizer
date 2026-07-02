// file: internal/audiobooks/helpers_test.go
// version: 1.0.0
// guid: 9d2b6f1c-4a3e-4d5f-8b2a-1c6e9f0a7d21
// last-edited: 2026-07-01

package audiobooks

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeImportPathLister is a minimal importPathLister used to count how many
// times GetAllImportPaths is actually invoked, independent of any cache.
type fakeImportPathLister struct {
	calls int32
	paths []database.ImportPath
}

func (f *fakeImportPathLister) GetAllImportPaths() ([]database.ImportPath, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.paths, nil
}

// TestIsProtectedPathCachesImportPaths verifies that repeated calls to the
// package-level isProtectedPath within the TTL window reuse the cached
// import-path list instead of re-fetching from the store every call
// (MAYDEPLOY-H7).
func TestIsProtectedPathCachesImportPaths(t *testing.T) {
	// Reset shared cache state so this test is isolated from others.
	importPathCacheForHelperMu.Lock()
	importPathCacheForHelper = nil
	importPathCacheForHelperAt = time.Time{}
	importPathCacheForHelperMu.Unlock()

	origTTL := importPathCacheTTLForHelper
	importPathCacheTTLForHelper = 20 * time.Millisecond
	t.Cleanup(func() { importPathCacheTTLForHelper = origTTL })

	store := &fakeImportPathLister{paths: []database.ImportPath{{Path: "/library/imports"}}}

	if !isProtectedPath(store, "/library/imports/book.m4b") {
		t.Fatalf("expected /library/imports/book.m4b to be protected")
	}
	if !isProtectedPath(store, "/library/imports/book2.m4b") {
		t.Fatalf("expected second call to also report protected")
	}
	if got := atomic.LoadInt32(&store.calls); got != 1 {
		t.Fatalf("expected GetAllImportPaths to be called once within TTL, got %d", got)
	}

	time.Sleep(30 * time.Millisecond)

	isProtectedPath(store, "/library/imports/book3.m4b")
	if got := atomic.LoadInt32(&store.calls); got != 2 {
		t.Fatalf("expected GetAllImportPaths to be re-fetched after TTL expiry, got %d", got)
	}
}
