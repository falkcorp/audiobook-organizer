// file: internal/deluge/protected_paths.go
// version: 1.2.0
// guid: d5b8e2a1-3c9f-4076-b7d4-0e8a2c5f1b93
// last-edited: 2026-09-14

// Package deluge provides integration with the Deluge BitTorrent client.
package deluge

import (
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

const protectedPathTTL = 5 * time.Minute

var protectedLog = logger.New("deluge-protected")

// Protection sources reported by ProtectedBy.
const (
	// ProtectedByStatic: under a configured static prefix (config.ProtectedPaths,
	// e.g. the iTunes library). Never imported, moved or rewritten.
	ProtectedByStatic = "static"
	// ProtectedByDeluge: under the save_path of a Deluge torrent. May be
	// copied into the library; the seeding file itself is never touched.
	ProtectedByDeluge = "deluge"
)

// ProtectedPathCache maintains a set of filesystem path prefixes that must
// not be moved, renamed, or deleted. Paths come from two sources:
//
//  1. The SavePath of every active Deluge torrent (refreshed every 5 min).
//  2. Extra paths supplied at construction time (e.g. config.ProtectedPaths).
//
// The static paths are always consulted, whatever state Deluge is in. Until
// 2026-09-14 they were merged into the Deluge list only after a successful
// ListTorrents, so while Deluge was unreachable from startup IsProtected
// returned false for everything, the iTunes library included.
//
// Loaded reports whether the Deluge list has ever been fetched. A caller that
// rewrites or moves files must not proceed on a false Loaded: an empty Deluge
// list then means "unknown", not "nothing is seeding".
//
// Thread-safe. If Deluge is unreachable on refresh, the last-known set is kept.
type ProtectedPathCache struct {
	mu          sync.RWMutex
	delugePaths []string
	extraPaths  []string
	client      *Client
	lastRefresh time.Time
	// loaded is true once the Deluge list has been fetched successfully, or
	// immediately when there is no client (no list to load).
	loaded bool
}

// NewProtectedPathCache creates a cache backed by the given Deluge client.
// extraPaths is a static list of additional protected prefixes (e.g. from config).
// The Deluge list is empty until the first call to IsProtected triggers a refresh.
func NewProtectedPathCache(client *Client, extraPaths []string) *ProtectedPathCache {
	extra := make([]string, 0, len(extraPaths))
	for _, p := range extraPaths {
		if p != "" {
			extra = append(extra, p)
		}
	}
	return &ProtectedPathCache{
		client:     client,
		extraPaths: extra,
	}
}

// IsProtected returns true if filePath lies under a static prefix or a known
// Deluge save_path. It lazily refreshes the Deluge list when the TTL has
// expired.
func (c *ProtectedPathCache) IsProtected(filePath string) bool {
	return c.ProtectedBy(filePath) != ""
}

// ProtectedBy says why filePath is protected: ProtectedByStatic,
// ProtectedByDeluge, or "" when it is not (as far as the loaded list knows;
// see Loaded). A path under both is reported static, the stricter of the two.
func (c *ProtectedPathCache) ProtectedBy(filePath string) string {
	if filePath == "" {
		return ""
	}
	c.maybeRefresh()
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, prefix := range c.extraPaths {
		if pathutil.IsWithin(filePath, prefix) {
			return ProtectedByStatic
		}
	}
	for _, prefix := range c.delugePaths {
		if prefix != "" && pathutil.IsWithin(filePath, prefix) {
			return ProtectedByDeluge
		}
	}
	return ""
}

// Loaded reports whether the Deluge save_path list has been fetched at least
// once (always true with no Deluge client). It tries a refresh first, so a
// Deluge that has come back is picked up.
func (c *ProtectedPathCache) Loaded() bool {
	c.maybeRefresh()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

// Invalidate forces the next IsProtected call to refresh from Deluge immediately.
func (c *ProtectedPathCache) Invalidate() {
	c.mu.Lock()
	c.lastRefresh = time.Time{} // zero value — older than any TTL
	c.mu.Unlock()
}

// maybeRefresh checks whether the TTL has expired and, if so, calls refresh.
// Uses a double-checked pattern: read lock to check, write lock to update.
func (c *ProtectedPathCache) maybeRefresh() {
	c.mu.RLock()
	expired := time.Since(c.lastRefresh) > protectedPathTTL
	c.mu.RUnlock()
	if !expired {
		return
	}
	c.refresh()
}

// refresh fetches current torrent save paths from Deluge and rebuilds the
// Deluge list. If Deluge is unreachable, the existing list is kept unchanged
// (and, if it never loaded, Loaded stays false).
func (c *ProtectedPathCache) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check under write lock in case another goroutine refreshed first.
	if time.Since(c.lastRefresh) <= protectedPathTTL {
		return
	}

	// No Deluge client (deluge not configured): there is no list to load, and
	// the static paths are consulted directly by ProtectedBy. Without this
	// guard, c.client.ListTorrents() → Login() dereferences the nil receiver
	// and every tag write's IsProtected pre-flight panics.
	if c.client == nil {
		c.delugePaths = nil
		c.loaded = true
		c.lastRefresh = time.Now()
		return
	}

	torrents, err := c.client.ListTorrents()
	if err != nil {
		// Deluge unreachable — keep stale data, do not update lastRefresh
		// so the next call will try again.
		protectedLog.Warn("ProtectedPathCache failed to refresh from Deluge (loaded=%v, using last-known list of %d save paths): %v",
			c.loaded, len(c.delugePaths), err)
		return
	}
	c.setDelugePathsLocked(torrents)
}

// setDelugePathsLocked replaces the Deluge list with the unique save paths of
// torrents and marks the cache loaded. Caller holds c.mu.
func (c *ProtectedPathCache) setDelugePathsLocked(torrents map[string]TorrentStatus) {
	seen := make(map[string]struct{})
	var fresh []string
	for _, t := range torrents {
		if t.SavePath == "" {
			continue
		}
		if _, dup := seen[t.SavePath]; !dup {
			seen[t.SavePath] = struct{}{}
			fresh = append(fresh, t.SavePath)
		}
	}
	c.delugePaths = fresh
	c.loaded = true
	c.lastRefresh = time.Now()
	protectedLog.Debug("ProtectedPathCache refreshed: %d Deluge save paths, %d static paths", len(c.delugePaths), len(c.extraPaths))
}
