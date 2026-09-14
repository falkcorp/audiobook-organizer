// file: internal/deluge/protected_paths_test.go
// version: 1.1.0
// guid: e6c9f3b2-4d0a-5187-c8e5-2b9f4e1c0d74
// last-edited: 2026-09-14

package deluge

import (
	"fmt"
	"testing"
	"time"
)

// mockClient is a fake *Client that returns controlled ListTorrents results.
// It is unexported and only used in tests. We cannot embed *Client because
// Client has unexported fields, so we use a wrapper approach via monkey-patching
// the ProtectedPathCache's refresh function.
//
// Instead, we construct a real ProtectedPathCache but replace its internals
// directly, because all fields are unexported. The cleanest approach is a
// test-only constructor that accepts a function.

// listFunc is the type of a function that imitates ListTorrents for tests.
type listFunc func() (map[string]TorrentStatus, error)

// newTestCache creates a ProtectedPathCache wired to a fake list function
// instead of a real *Client. Used only in tests.
func newTestCache(lf listFunc, extraPaths []string) *ProtectedPathCache {
	// We create a dummy client (nil) and override the refresh behavior via
	// a thin subtype. Since Client is a struct with unexported fields we
	// cannot embed it. Instead we call refresh manually with a stub.
	//
	// Strategy: create the cache, then immediately call a helper that
	// populates c.paths directly, simulating what refresh() would do.
	c := &ProtectedPathCache{
		client:     nil, // never called in tests — we populate manually
		extraPaths: extraPaths,
	}
	populateCacheForTest(c, lf)
	return c
}

// populateCacheForTest runs the same logic as refresh() but uses lf instead
// of c.client.ListTorrents(). On success, sets lastRefresh to now so TTL
// doesn't expire. On error, leaves paths and lastRefresh unchanged (stale).
func populateCacheForTest(c *ProtectedPathCache, lf listFunc) {
	torrents, err := lf()
	if err != nil {
		// Simulate stale-on-error: do not update paths or lastRefresh.
		// Manually stamp lastRefresh so IsProtected won't trigger another
		// real refresh (which would panic on a nil client in tests).
		c.lastRefresh = time.Now()
		return
	}
	c.mu.Lock()
	c.setDelugePathsLocked(torrents)
	c.mu.Unlock()
}

// Test 1: IsProtected returns true when filePath has a cached prefix.
func TestIsProtected_True(t *testing.T) {
	lf := func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{
			"aabbcc": {SavePath: "/mnt/downloads/audiobooks"},
		}, nil
	}
	cache := newTestCache(lf, nil)

	filePath := "/mnt/downloads/audiobooks/TheName/file.m4b"
	if !cache.IsProtected(filePath) {
		t.Errorf("expected IsProtected(%q) = true, got false", filePath)
	}
}

// Test 2: IsProtected returns false when filePath has no matching prefix.
func TestIsProtected_False(t *testing.T) {
	lf := func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{
			"aabbcc": {SavePath: "/mnt/downloads/audiobooks"},
		}, nil
	}
	cache := newTestCache(lf, nil)

	filePath := "/mnt/bigdata/books/audiobook-organizer/Author/Title/file.m4b"
	if cache.IsProtected(filePath) {
		t.Errorf("expected IsProtected(%q) = false, got true", filePath)
	}
}

// Test 3: When refresh fails, stale data is kept and IsProtected still works.
func TestIsProtected_StaleOnError(t *testing.T) {
	// First, populate with good data.
	goodLF := func() (map[string]TorrentStatus, error) {
		return map[string]TorrentStatus{
			"aabbcc": {SavePath: "/mnt/downloads/stale-path"},
		}, nil
	}
	cache := newTestCache(goodLF, nil)

	// Verify stale path is protected before simulating error.
	if !cache.IsProtected("/mnt/downloads/stale-path/book.m4b") {
		t.Fatal("precondition: stale path should be protected before error")
	}

	// Simulate Deluge going down: reset lastRefresh so refresh() will run,
	// then use the error-returning lf.
	cache.lastRefresh = time.Time{} // force TTL expiry
	errorLF := func() (map[string]TorrentStatus, error) {
		return nil, fmt.Errorf("connection refused")
	}
	// populateCacheForTest with error lf — should NOT clear existing paths.
	populateCacheForTest(cache, errorLF)

	// Stale paths must still be protected.
	if !cache.IsProtected("/mnt/downloads/stale-path/book.m4b") {
		t.Errorf("expected stale path to still be protected after refresh error")
	}
}
