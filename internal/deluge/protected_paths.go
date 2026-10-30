// file: internal/deluge/protected_paths.go
// version: 1.3.0
// guid: d5b8e2a1-3c9f-4076-b7d4-0e8a2c5f1b93
// last-edited: 2026-10-30

// Package deluge provides integration with the Deluge BitTorrent client.
package deluge

import (
	"fmt"
	"path/filepath"
	"strings"
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
// Symlinks. A path is tested as spelled AND with its symlinks resolved, against
// each prefix as spelled AND resolved (the pairs are a union; a hit on any is a
// hit). A file reached through a link into a seeding or static folder is the
// same file, and so is any file under a folder that is itself configured
// through a link. The resolved forms of the prefixes are computed once per
// refresh (every protectedPathTTL, and on Invalidate), not per call; the
// candidate path is resolved per call, only when the spelled check missed, and
// is not cached: a cached "not protected" for a path whose link was re-pointed
// is the one wrong answer that loses a seeding file. Resolution is
// pathutil.ResolveExistingPrefix: a path or prefix that does not exist is
// resolved through its nearest existing parent, and a dangling link is followed
// to where it points, so a dangling link inside a protected folder stays
// protected (the spelled check also catches it). A prefix whose links cannot be
// resolved (a cycle, a permission error, a stale mount) keeps the resolved form
// it had at the last refresh that could resolve it, or its spelled form when it
// never had one, and is logged once per refresh interval (not once per call,
// including while Deluge is down). A candidate that cannot be resolved is
// protected (fail closed), reported as ProtectedByStatic.
//
// Only a spelled ProtectedByStatic hit is final. A spelled Deluge hit is still
// resolved, because the same file can be a static one by another route (a
// seeding folder holding a link into the iTunes tree or a static prefix): the
// answer is then upgraded to ProtectedByStatic.
//
// A candidate with a ".." segment is protected (ProtectedByStatic) without being
// looked at. filepath.Abs cleans ".." lexically, but the kernel applies it to
// the directory a symlink has led to, so "protected/../free/f" can name a
// different file than its cleaned spelling does. Callers pass cleaned absolute
// paths; one that does not is not trusted.
//
// The hands-off iTunes tree (books/itunes/**, pathutil.UnderFrozenITunesTree)
// is protected whatever the configuration says, as ProtectedByStatic.
//
// Thread-safe. If Deluge is unreachable on refresh, the last-known set is kept.
type ProtectedPathCache struct {
	mu          sync.RWMutex
	delugePaths []string
	extraPaths  []string
	// delugeResolved and extraResolved are the symlink-resolved absolute forms
	// of delugePaths and extraPaths, rebuilt with them (see resolveRoots).
	delugeResolved []string
	extraResolved  []string
	// delugeKnown and extraKnown map each prefix to the resolved form it had at
	// the last refresh that could resolve it, so a prefix that fails to resolve
	// keeps that form (last-known), instead of falling back to its spelling.
	delugeKnown map[string]string
	extraKnown  map[string]string
	// lastResolve is when the static prefixes were last resolved, whether or not
	// that worked, so a failure is retried and reported once per interval even
	// while a failing Deluge keeps lastRefresh from being stamped.
	lastResolve time.Time
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
	// The frozen iTunes tree needs no list and no filesystem access.
	if pathutil.UnderFrozenITunesTree(filePath) {
		return ProtectedByStatic
	}
	if hasParentSegment(filePath) {
		protectedLog.Debug("treating path as protected: it has a '..' segment: path=%s", logger.SanitizeLogValue(filePath))
		return ProtectedByStatic
	}
	c.maybeRefresh()
	// The lists are replaced wholesale on refresh, never edited in place, so a
	// snapshot of the slice headers is safe to use after the lock is released:
	// the stat walk below must not hold up a refresh.
	c.mu.RLock()
	snap := prefixSnapshot{c.extraPaths, c.extraResolved, c.delugePaths, c.delugeResolved}
	c.mu.RUnlock()

	// As spelled, against the prefixes as spelled: no filesystem access. A
	// static hit is final, so what was protected before symlinks were considered
	// still is, whatever resolution does. A Deluge hit is not: see below.
	spelled := snap.match([]string{filePath}, false)
	if spelled == ProtectedByStatic {
		return ProtectedByStatic
	}

	// Resolved. The spelled path is made absolute first (the prefixes' resolved
	// forms are absolute), then resolved through its nearest existing parent.
	abs, resolved, err := resolveCandidate(filePath)
	if err != nil {
		protectedLog.Warn("treating path as protected: cannot resolve its symlinks: path=%s error=%s",
			logger.SanitizeLogValue(filePath), logger.SanitizeLogValue(err.Error()))
		return ProtectedByStatic
	}
	if pathutil.UnderFrozenITunesTree(resolved) {
		return ProtectedByStatic
	}
	// Every pairing of {abs, resolved} path with {spelled, resolved} prefixes
	// that the spelled check above did not already make.
	if by := snap.match([]string{abs, resolved}, true); by != "" {
		return by
	}
	return spelled
}

// resolveCandidate makes filePath absolute and resolves its symlinks through
// the nearest existing parent (pathutil.ResolveExistingPrefix).
func resolveCandidate(filePath string) (abs, resolved string, err error) {
	abs, err = filepath.Abs(filePath)
	if err != nil {
		return "", "", fmt.Errorf("make %q absolute: %w", filePath, err)
	}
	resolved, err = pathutil.ResolveExistingPrefix(abs)
	if err != nil {
		return "", "", err
	}
	return abs, resolved, nil
}

// hasParentSegment reports whether p has a ".." path segment, before any
// cleaning.
func hasParentSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// underResolvedPrefix reports whether path lies under any of roots, tested the
// way ProtectedBy tests the static list: as spelled, then resolved, against the
// roots as spelled and resolved, failing closed on a ".." segment or a path
// that cannot be resolved. It resolves the roots on each call and keeps nothing,
// for callers (staticProtection) that hold no cache; a root that cannot be
// resolved keeps its spelled protection.
func underResolvedPrefix(path string, roots []string) bool {
	if path == "" {
		return false
	}
	if underAny([]string{path}, roots) {
		return true
	}
	if hasParentSegment(path) {
		return true
	}
	abs, resolved, err := resolveCandidate(path)
	if err != nil {
		return true
	}
	resolvedRoots, _, _ := resolveRoots(roots, nil)
	return underAny([]string{abs, resolved}, roots) || underAny([]string{abs, resolved}, resolvedRoots)
}

// prefixSnapshot is the four prefix lists as ProtectedBy read them: the static
// and Deluge prefixes as spelled, and their symlink-resolved forms.
type prefixSnapshot struct {
	extra, extraResolved, deluge, delugeResolved []string
}

// match says which source, if any, has a prefix that some form of the path
// lies under: static before Deluge, since a path under both is reported
// static. The forms are always tested against the prefixes as spelled, and
// against their symlink-resolved forms too when withResolved is set.
func (s prefixSnapshot) match(forms []string, withResolved bool) string {
	if underAny(forms, s.extra) || (withResolved && underAny(forms, s.extraResolved)) {
		return ProtectedByStatic
	}
	if underAny(forms, s.deluge) || (withResolved && underAny(forms, s.delugeResolved)) {
		return ProtectedByDeluge
	}
	return ""
}

// underAny reports whether any of forms lies under any of the non-empty roots.
func underAny(forms, roots []string) bool {
	for _, r := range roots {
		if r == "" {
			continue
		}
		for _, f := range forms {
			if pathutil.IsWithin(f, r) {
				return true
			}
		}
	}
	return false
}

// rootFailure is a prefix that could not be resolved.
type rootFailure struct {
	root string
	err  error
	// lastKnown is true when the prefix keeps the resolved form it had at an
	// earlier refresh, false when only its spelled form protects it.
	lastKnown bool
}

// resolveRoots returns the absolute, symlink-resolved form of each non-empty
// root, for the prefixes of one list, and the map from root to its resolved
// form (the next call's last). A root that cannot be made absolute or resolved
// (a link cycle, a permission error, a stale mount) keeps its form from last,
// when last has one, and otherwise only its spelled form protects it; either
// way it is reported in failed, and does not close every path in the library
// the way one bad entry otherwise would.
func resolveRoots(roots []string, last map[string]string) (out []string, known map[string]string, failed []rootFailure) {
	out = make([]string, 0, len(roots))
	known = make(map[string]string, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err == nil {
			var res string
			if res, err = pathutil.ResolveExistingPrefix(abs); err == nil {
				out = append(out, res)
				known[r] = res
				continue
			}
		}
		prev, ok := last[r]
		if ok {
			out = append(out, prev)
			known[r] = prev
		}
		failed = append(failed, rootFailure{root: r, err: err, lastKnown: ok})
	}
	return out, known, failed
}

// warnRootFailures logs each unresolvable prefix of one list; its callers run
// once per refresh interval.
func warnRootFailures(kind string, failed []rootFailure) {
	for _, f := range failed {
		keep := "only its spelled form protects it"
		if f.lastKnown {
			keep = "keeping the form it resolved to at the last refresh"
		}
		protectedLog.Warn("protected %s path %s cannot be resolved through its symlinks; %s: %s",
			kind, logger.SanitizeLogValue(f.root), keep, logger.SanitizeLogValue(f.err.Error()))
	}
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
	c.lastResolve = time.Time{}
	c.mu.Unlock()
}

// Refresh fetches the Deluge save_path list now, whatever the TTL says, and
// re-resolves the static prefixes. It returns the error that kept the list from
// loading (Deluge unreachable); the last-known list is kept then, as for a lazy
// refresh. With no Deluge client there is nothing to fetch and it returns nil.
// This is what the scheduled sync calls: a call to IsProtected("") cannot do it,
// because an empty path is answered before any refresh.
func (c *ProtectedPathCache) Refresh() error {
	return c.refresh(true)
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
	_ = c.refresh(false) // a failure is logged there and retried on the next call
}

// refresh fetches current torrent save paths from Deluge and rebuilds the
// Deluge list; force skips the TTL checks. If Deluge is unreachable, the
// existing list is kept unchanged (and, if it never loaded, Loaded stays false)
// and the error is returned.
func (c *ProtectedPathCache) refresh(force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check under write lock in case another goroutine refreshed first.
	if !force && time.Since(c.lastRefresh) <= protectedPathTTL {
		return nil
	}

	// The static prefixes' resolved forms are rebuilt once per interval, whatever
	// Deluge does, so a link re-pointed in front of one is seen within
	// protectedPathTTL even while Deluge is down. The attempt is stamped
	// whether or not it worked: while Deluge is down lastRefresh is not, and an
	// unresolvable prefix would otherwise be retried and logged on every call.
	if force || time.Since(c.lastResolve) > protectedPathTTL {
		var failed []rootFailure
		c.extraResolved, c.extraKnown, failed = resolveRoots(c.extraPaths, c.extraKnown)
		c.lastResolve = time.Now()
		warnRootFailures("static", failed)
	}

	// No Deluge client (deluge not configured): there is no list to load, and
	// the static paths are consulted directly by ProtectedBy. Without this
	// guard, c.client.ListTorrents() → Login() dereferences the nil receiver
	// and every tag write's IsProtected pre-flight panics.
	if c.client == nil {
		c.delugePaths = nil
		c.delugeResolved = nil
		c.delugeKnown = nil
		c.loaded = true
		c.lastRefresh = time.Now()
		return nil
	}

	torrents, err := c.client.ListTorrents()
	if err != nil {
		// Deluge unreachable — keep stale data, do not update lastRefresh
		// so the next call will try again.
		protectedLog.Warn("ProtectedPathCache failed to refresh from Deluge (loaded=%v, using last-known list of %d save paths): %v",
			c.loaded, len(c.delugePaths), err)
		return fmt.Errorf("refresh protected paths from Deluge: %w", err)
	}
	c.setDelugePathsLocked(torrents)
	return nil
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
	var failed []rootFailure
	c.delugeResolved, c.delugeKnown, failed = resolveRoots(fresh, c.delugeKnown)
	warnRootFailures("deluge save", failed)
	c.loaded = true
	c.lastRefresh = time.Now()
	protectedLog.Debug("ProtectedPathCache refreshed: %d Deluge save paths, %d static paths", len(c.delugePaths), len(c.extraPaths))
}
