// file: internal/database/retired_hash_scope.go
// version: 1.0.0
// guid: 2798c1b6-7d41-4410-b491-a44b3dfd2093
// last-edited: 2026-10-29

package database

import (
	"sync"
	"sync/atomic"
)

// retiredHashCache is the scan-lifetime cache behind RetiredBookFilesWithHash.
//
// Without it every hash lookup made while memdb is not serving enumerates every
// retired version and reads every retired row of each, once per scanned file.
// Inside a scope (BeginRetiredHashScope) the first lookup reads the retired
// rows once into a map keyed by hash, and later lookups read the map.
//
// Only the RETIRED side is cached. The live side of the answer (the two
// single-owner hash keys) is read on every call: a scan creates live rows that
// claim those keys while it runs, so a cached "no live row holds this" would be
// wrong within the very scan that made the row.
//
// Staleness is a generation counter. Every write that can change which retired
// rows hold which hash bumps gen AFTER it commits: RetireBookFiles,
// UnretireBookFiles and both rehomes (afterRetireWrite), and a rewrite of a
// retired row's hashes (updateBookFileOver, the batch upsert). A reader loads
// gen BEFORE it reads the rows and tags what it built with that value, and a
// lookup uses the map only while its tag still equals gen. Both orderings
// matter: bumping before the commit, or tagging with a generation read after
// the build, would let a build that missed the write carry the post-write tag.
// A write that lands during a build leaves the build tagged with the old
// generation, so the next lookup rebuilds.
//
// The zero value is ready to use, outside any scope it caches nothing.
type retiredHashCache struct {
	// gen is bumped by every write that can change the retired rows' hashes.
	gen atomic.Uint64

	mu sync.Mutex // guards the fields below, and serialises builds
	// open counts the live scopes. The map is dropped when it reaches zero.
	open     int
	built    bool
	builtGen uint64
	byHash   map[string][]BookFile

	// builds counts index builds, for tests.
	builds atomic.Int64
}

// noteRetiredRowsChanged invalidates the cache. Call it after the write that
// changed the retired rows has committed.
func (c *retiredHashCache) noteRetiredRowsChanged() { c.gen.Add(1) }

// BeginRetiredHashScope opens a scan-lifetime scope in which
// RetiredBookFilesWithHash serves the retired side of its answer from one read
// of the retired rows, and returns the func that closes it. Scopes are counted:
// overlapping scans (a library scan and a folder auto-scan) share the cache and
// the last one to close drops it. The returned func is safe to call more than
// once.
func (p *PebbleStore) BeginRetiredHashScope() (end func()) {
	c := &p.retiredHash
	c.mu.Lock()
	c.open++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.open--
			if c.open == 0 {
				c.built, c.byHash = false, nil
			}
		})
	}
}

// retiredHashScoper is the PebbleStore method BeginRetiredHashScope (the
// package func) resolves through AsCapability.
type retiredHashScoper interface {
	BeginRetiredHashScope() (end func())
}

// BeginRetiredHashScope opens the scan-lifetime cache of retired-row hash
// lookups on the store s resolves to, for callers that hold the store as any,
// and returns the func that closes it. A store with no such cache (a decorator
// that does not forward it, a test double) gets a no-op closer: the lookups are
// then simply uncached, never wrong. Callers defer the result:
//
//	defer database.BeginRetiredHashScope(store)()
func BeginRetiredHashScope(s any) (end func()) {
	if sc, ok := AsCapability[retiredHashScoper](s); ok {
		return sc.BeginRetiredHashScope()
	}
	return func() {}
}

// retiredRowsWithHashCached answers the retired side of a hash lookup from the
// scope's index, building it first when it is missing or stale. ok is false
// when no scope is open and the caller must read the rows itself. The rows are
// the memdb projection (stripBookFileForMemdb: no raw fingerprint, no
// transcript), cloned per call so a caller cannot change the cache; the only
// reader, the scanner, needs a row's book, version and hashes. An error is
// returned and never cached.
func (p *PebbleStore) retiredRowsWithHashCached(hash string) (rows []BookFile, ok bool, err error) {
	c := &p.retiredHash
	c.mu.Lock()
	if c.open == 0 {
		c.mu.Unlock()
		return nil, false, nil
	}
	if gen := c.gen.Load(); !c.built || c.builtGen != gen {
		// gen is read BEFORE the rows are. The build holds c.mu, so concurrent
		// lookups wait for it instead of each enumerating the retired rows.
		idx := map[string][]BookFile{}
		if err := p.forEachRetiredRow(func(r BookFile) error {
			stripped := stripBookFileForMemdb(&r)
			idx[r.FileHash] = append(idx[r.FileHash], *stripped)
			if r.OriginalFileHash != r.FileHash {
				idx[r.OriginalFileHash] = append(idx[r.OriginalFileHash], *stripped)
			}
			return nil
		}); err != nil {
			c.mu.Unlock()
			return nil, true, err
		}
		delete(idx, "") // a row with no hash matches no lookup
		c.byHash, c.built, c.builtGen = idx, true, gen
		c.builds.Add(1)
	}
	held := c.byHash[hash]
	c.mu.Unlock()
	for i := range held {
		rows = append(rows, cloneBookFile(&held[i]))
	}
	return rows, true, nil
}
