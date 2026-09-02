// file: internal/server/server_middleware_test.go
// version: 1.0.1
// guid: 6f3f3e2a-8f1a-4b3e-9c2d-7a1e5f8b9c10
// last-edited: 2026-09-02

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

	var calls atomic.Int32
	mock := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			calls.Add(1)
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
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected GetAllImportPaths to be called once within TTL, got %d", got)
	}

	time.Sleep(30 * time.Millisecond)

	srv.isProtectedPath("/library/imports/book3.m4b")
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected GetAllImportPaths to be re-fetched after TTL expiry, got %d", got)
	}
}
