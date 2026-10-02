// file: internal/database/pebble_store_atpath_markers.go
// version: 1.0.0
// guid: 68a95fef-a156-403b-a6fa-3ae0ba84f8fd
// last-edited: 2026-10-02

package database

import (
	"errors"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// In-memory record of which book_atpath_undecodable:<id> markers may exist.
//
// WHY: until 2026-10-02 UpdateBook and DeleteBook staged
// batch.Delete(bookAtPathUndecodableKey(id)) on EVERY book write, and every
// LiveBookIDsAtPath / LiveBookPathsUnderDir lookup range-scanned the marker
// family. Markers almost never exist (only the book-atpath backfill writes
// them, for a row whose JSON does not decode), so each of those deletes left a
// point tombstone for a key that was never there. After tens of thousands of
// book writes (an ASIN/ISBN backfill, for one) the "empty" marker scan walked
// every tombstone, once per folder lookup: GET .../metadata/cache/review?all=true
// spent ~51s of its 2m52s inside that scan, with GC at 47% of CPU.
//
// DESIGN: PebbleStore owns this set, loaded once from the marker range at open
// (or lazily on first use for a store built without newPebbleStore). Writers
// stage the marker Delete only when the id is in the set; readers point-read
// only the ids in the set and never range-scan the family, so they cannot walk
// tombstones at all (old ones included) and an empty set costs nothing.
//
// INVARIANT: the set is a SUPERSET of the markers on disk (one harmless gap,
// below).
// A stale "may exist" costs one no-op delete; a missing id would skip a real
// delete (a marker that blocks every lookup forever) or hide a fail-closed
// marker from a reader, so every update errs toward "may exist":
//
//   - Add BEFORE the marker can commit, and bump again AFTER the commit
//     (backfill workers, markUndecodablePending / markUndecodableCommitted).
//   - Remove only AFTER the delete commits, and only if nothing re-added the
//     id since the remover looked (the per-id generation below). Without the
//     generation, an UpdateBook whose commit lands before a backfill worker's
//     marker commit would remove an id whose marker is then on disk.
//   - A failed commit leaves the set alone.
//   - The one gap: a worker notes id (gen g) and stages its marker; an
//     UpdateBook reads g and commits its Delete first; the worker commits the
//     marker; the UpdateBook's forget(g) runs before the worker's post-commit
//     re-note. For that instant the marker is on disk and not in the set. It
//     is harmless: UpdateBook/DeleteBook read the row through GetBookByID,
//     which fails on an undecodable row, so the writer can only have touched
//     a row that already decodes and whose book_atpath key is in the same
//     batch, so a reader that skips the marker loses no book; and the
//     re-note puts the id back right after.
//   - A crash between a commit and the set update needs no handling: the set
//     lives only in memory and is rebuilt from disk at the next open.
//
// Readers copy the set under its read lock while taking their Pebble snapshot
// (undecodableMarkerSnapshot): a marker in the snapshot committed before it, so
// its id was added before then and can only have been removed after a later
// delete commit, so it is in the copy.
//
// Why not a point Get under the book's write stripe before each Delete (the
// fallback)? It fixes the writers but not the readers: they would still
// range-scan a family whose tombstones survive until compaction, and a Get per
// book write is a read on the hottest write path. The set removes both costs,
// and the only writer of markers is the backfill, so keeping it exact is a
// small, local discipline.
type undecodableMarkerSet struct {
	mu     sync.RWMutex
	loaded bool
	seq    uint64            // monotonic; bumped by every add
	ids    map[string]uint64 // id -> seq of its latest add
}

var atpathMarkerLog = logger.New("database.atpath-markers")

// loadUndecodableMarkersLocked fills the set from the marker range. Caller
// holds mu for writing. This is the one place that still walks the range
// (tombstones included), once per process.
func (p *PebbleStore) loadUndecodableMarkersLocked() error {
	m := &p.atpathMarkers
	ids := make(map[string]uint64)
	lower := []byte(bookAtPathUndecodablePrefix)
	err := forEachKeyInRange(p.db, lower, bareRowUpperBound(bookAtPathUndecodablePrefix), func(key, _ []byte) error {
		m.seq++
		ids[string(key[len(lower):])] = m.seq
		return nil
	})
	if err != nil {
		return fmt.Errorf("load undecodable markers: %w", err)
	}
	m.ids = ids
	m.loaded = true
	return nil
}

// ensureUndecodableMarkersLoaded loads the set once. loaded never goes back to
// false, so callers may take mu again after this returns nil.
func (p *PebbleStore) ensureUndecodableMarkersLoaded() error {
	m := &p.atpathMarkers
	m.mu.RLock()
	ok := m.loaded
	m.mu.RUnlock()
	if ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		return nil
	}
	return p.loadUndecodableMarkersLocked()
}

// undecodableMarkerMayExist reports whether id's marker may be on disk, and
// the generation to hand back to forgetUndecodableMarker after the delete
// commits. Called under the book's write stripe. If the set cannot be loaded
// it answers "may exist" (gen 0, which never matches a live entry), so the
// writer falls back to the old unconditional delete rather than skipping one.
func (p *PebbleStore) undecodableMarkerMayExist(id string) (uint64, bool) {
	if err := p.ensureUndecodableMarkersLoaded(); err != nil {
		atpathMarkerLog.Warn("cannot load undecodable-marker set, deleting marker for %s unconditionally: %v",
			logger.SanitizeLogValue(id), err)
		return 0, true
	}
	m := &p.atpathMarkers
	m.mu.RLock()
	defer m.mu.RUnlock()
	gen, ok := m.ids[id]
	return gen, ok
}

// forgetUndecodableMarker drops id after a batch that deleted its marker has
// COMMITTED, unless the id was re-added since undecodableMarkerMayExist
// returned gen (a backfill worker staged or committed a new marker meanwhile).
func (p *PebbleStore) forgetUndecodableMarker(id string, gen uint64) {
	m := &p.atpathMarkers
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.ids[id]; ok && cur == gen {
		delete(m.ids, id)
	}
}

// noteUndecodableMarker records that id's marker may exist. The backfill calls
// it before staging the marker Set and again after the batch commits (see the
// invariant above).
func (p *PebbleStore) noteUndecodableMarker(id string) error {
	if err := p.ensureUndecodableMarkersLoaded(); err != nil {
		return err
	}
	m := &p.atpathMarkers
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.ids[id] = m.seq
	return nil
}

// undecodableMarkerGeneration returns the current generation, captured before
// a DeleteRange of the whole family so clearUndecodableMarkersUpTo can drop
// exactly the ids the range delete covered.
func (p *PebbleStore) undecodableMarkerGeneration() (uint64, error) {
	if err := p.ensureUndecodableMarkersLoaded(); err != nil {
		return 0, err
	}
	m := &p.atpathMarkers
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.seq, nil
}

// clearUndecodableMarkersUpTo drops every id last added at or before gen.
// Called only after the DeleteRange committed; an id added after gen may have
// a marker committed after the range delete, so it stays.
func (p *PebbleStore) clearUndecodableMarkersUpTo(gen uint64) {
	m := &p.atpathMarkers
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, g := range m.ids {
		if g <= gen {
			delete(m.ids, id)
		}
	}
}

// undecodableMarkerSnapshot opens a Pebble snapshot and copies the set under
// its read lock, so the copy is a superset of the markers in the snapshot.
// The caller closes the snapshot.
func (p *PebbleStore) undecodableMarkerSnapshot() (*pebble.Snapshot, []string, error) {
	if err := p.ensureUndecodableMarkersLoaded(); err != nil {
		return nil, nil, err
	}
	m := &p.atpathMarkers
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := p.db.NewSnapshot()
	if len(m.ids) == 0 {
		return snap, nil, nil
	}
	ids := make([]string, 0, len(m.ids))
	for id := range m.ids {
		ids = append(ids, id)
	}
	return snap, ids, nil
}

// undecodableMarkerCount is the set's size, for tests.
func (p *PebbleStore) undecodableMarkerCount() int {
	m := &p.atpathMarkers
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.ids)
}

// markersOnDiskIn returns which of ids have a marker key in snap. Point reads
// only: the bloom filter answers most absent keys, and no tombstone run is
// ever walked.
func markersOnDiskIn(snap *pebble.Snapshot, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		_, closer, err := snap.Get(bookAtPathUndecodableKey(id))
		if errors.Is(err, pebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read undecodable marker %s: %w", id, err)
		}
		closer.Close()
		out = append(out, id)
	}
	return out, nil
}

// stageDeleteIfPresent stages a Delete of key only if key exists now. For keys
// that are usually absent on a hot write path and whose family is
// range-scanned by a reader: an unconditional Delete of an absent key leaves
// a point tombstone that every later scan of the family walks until
// compaction. A probe error falls back to the unconditional Delete (a
// tombstone is cheaper than a leaked row).
//
// Race note: a writer of key that does not hold the caller's lock can land
// between the probe and the commit and survive. The same writer landing just
// after the commit already survives the unconditional Delete, so this only
// moves the edge of an existing window, it does not open a new class.
func (p *PebbleStore) stageDeleteIfPresent(batch *pebble.Batch, key []byte) error {
	_, closer, err := p.db.Get(key)
	switch {
	case errors.Is(err, pebble.ErrNotFound):
		return nil
	case err != nil:
		atpathMarkerLog.Warn("probe of %s before delete failed, deleting unconditionally: %v",
			logger.SanitizeLogValue(string(key)), err)
	default:
		closer.Close()
	}
	return batch.Delete(key, nil)
}

// bookWriteBatchPreCommitHook, when non-nil, sees UpdateBook's and
// DeleteBook's batch just before it commits. Tests only.
var bookWriteBatchPreCommitHook func(op, id string, b *pebble.Batch)
