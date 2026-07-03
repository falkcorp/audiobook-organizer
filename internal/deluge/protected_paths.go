// file: internal/deluge/protected_paths.go
// version: 1.1.0
// guid: d5b8e2a1-3c9f-4076-b7d4-0e8a2c5f1b93
// last-edited: 2026-07-03

// Package deluge provides integration with the Deluge BitTorrent client.
package deluge

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

const protectedPathTTL = 5 * time.Minute

// ProtectedPathCache maintains a set of filesystem path prefixes that must
// not be moved, renamed, or deleted. Paths come from two sources:
//
//  1. The SavePath of every active Deluge torrent (refreshed every 5 min).
//  2. Extra paths supplied at construction time (e.g. config.ProtectedPaths).
//
// Thread-safe. If Deluge is unreachable on refresh, the last-known set is kept.
type ProtectedPathCache struct {
	mu          sync.RWMutex
	paths       []string
	extraPaths  []string
	client      *Client
	lastRefresh time.Time
}

// NewProtectedPathCache creates a cache backed by the given Deluge client.
// extraPaths is a static list of additional protected prefixes (e.g. from config).
// The cache is empty until the first call to IsProtected triggers a refresh.
func NewProtectedPathCache(client *Client, extraPaths []string) *ProtectedPathCache {
	extra := make([]string, len(extraPaths))
	copy(extra, extraPaths)
	return &ProtectedPathCache{
		client:     client,
		extraPaths: extra,
	}
}

// IsProtected returns true if filePath has any cached path as a prefix.
// It lazily refreshes the cache when the TTL has expired.
func (c *ProtectedPathCache) IsProtected(filePath string) bool {
	c.maybeRefresh()
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, prefix := range c.paths {
		if prefix != "" && strings.HasPrefix(filePath, prefix) {
			return true
		}
	}
	return false
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
// path list. If Deluge is unreachable, the existing list is kept unchanged.
func (c *ProtectedPathCache) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check under write lock in case another goroutine refreshed first.
	if time.Since(c.lastRefresh) <= protectedPathTTL {
		return
	}

	// No Deluge client (deluge not configured): serve the static extraPaths
	// only. NewServer explicitly builds this cache with a nil client in that
	// case ("the cache still works for the static paths") — without this
	// guard, c.client.ListTorrents() → Login() dereferences the nil receiver
	// and every tag write's IsProtected pre-flight panics (500 on the update
	// endpoints; latent in the test suite because a leaked deluge client
	// singleton from an earlier test masked it until the leak was fixed).
	if c.client == nil {
		fresh := make([]string, 0, len(c.extraPaths))
		seenStatic := make(map[string]struct{}, len(c.extraPaths))
		for _, p := range c.extraPaths {
			if p == "" {
				continue
			}
			if _, dup := seenStatic[p]; !dup {
				seenStatic[p] = struct{}{}
				fresh = append(fresh, p)
			}
		}
		c.paths = fresh
		c.lastRefresh = time.Now()
		return
	}

	torrents, err := c.client.ListTorrents()
	if err != nil {
		// Deluge unreachable — keep stale data, do not update lastRefresh
		// so the next call will try again.
		slog.Warn("ProtectedPathCache failed to refresh from Deluge (using stale data)", "err", err)
		return
	}

	// Collect unique save paths from all torrents.
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

	// Merge in extra (static) paths.
	for _, p := range c.extraPaths {
		if p == "" {
			continue
		}
		if _, dup := seen[p]; !dup {
			seen[p] = struct{}{}
			fresh = append(fresh, p)
		}
	}

	c.paths = fresh
	c.lastRefresh = time.Now()
	slog.Debug("ProtectedPathCache refreshed protected path prefixes", "count", len(c.paths))
}
