// file: internal/server/server_middleware_test.go
// version: 1.0.0
// guid: 6f3f3e2a-8f1a-4b3e-9c2d-7a1e5f8b9c10
// last-edited: 2026-07-01

package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIsProtectedPathCachesImportPaths verifies that repeated calls to
// isProtectedPath within the TTL window reuse the cached import-path list
// instead of re-fetching from the store on every call (MAYDEPLOY-H7).
func TestIsProtectedPathCachesImportPaths(t *testing.T) {
	// Reset shared cache state so this test is isolated from others.
	importPathCacheMu.Lock()
	importPathCache = nil
	importPathCacheAt = time.Time{}
	importPathCacheMu.Unlock()

	origTTL := importPathCacheTTL
	importPathCacheTTL = 20 * time.Millisecond
	t.Cleanup(func() { importPathCacheTTL = origTTL })

	var calls int32
	mock := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			atomic.AddInt32(&calls, 1)
			return []database.ImportPath{{Path: "/library/imports"}}, nil
		},
	}

	srv := NewServer(mock)

	if !srv.isProtectedPath("/library/imports/book.m4b") {
		t.Fatalf("expected /library/imports/book.m4b to be protected")
	}
	if !srv.isProtectedPath("/library/imports/book2.m4b") {
		t.Fatalf("expected second call to also report protected")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected GetAllImportPaths to be called once within TTL, got %d", got)
	}

	time.Sleep(30 * time.Millisecond)

	srv.isProtectedPath("/library/imports/book3.m4b")
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected GetAllImportPaths to be re-fetched after TTL expiry, got %d", got)
	}
}
