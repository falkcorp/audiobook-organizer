// file: internal/database/pebble_store_search_dirty.go
// version: 1.0.0
// guid: 8508267e-71d5-4292-877d-1c3a803cedae
// last-edited: 2026-08-09
//
// Durable dirty-set for search-index reconciliation.
//
// The Bleve index is fed by a bounded channel that DROPS events when full
// (internal/server/indexed_store.go). Dropping under pressure is the right
// call — blocking the indexer would backpressure every write path — but
// until this file existed, nothing recorded WHICH updates were dropped, so
// the index diverged from the DB permanently. Measured on prod 2026-08-10:
// 56,537 dropped operations in seven days.
//
// Three comments in the server package used to claim a startup reindex
// healed these gaps. It does not: buildSearchIndexIfEmpty returns early
// unless the index has zero documents, so on a populated library it has
// never run. Those comments are corrected in the same change that added
// this file.
//
// Shape deliberately mirrors the existing pending-push set for playlists
// ("idx:upl:dirty:{id}", pebble_store_playlists.go) — same prefix
// convention, same iteration bounds.
//
// NOTE ON WHAT IS *NOT* STORED: the entry records only that a book needs
// re-indexing, never whether the lost event was an upsert or a delete. The
// reconciler re-derives that from the DB at drain time. Storing the intent
// would re-introduce the exact failure this set exists to fix — a recorded
// intent that can be stale or lost — so truth is re-read instead.

package database

import (
	"github.com/cockroachdb/pebble/v2"
)

// searchDirtyPrefix is the keyspace for the reconciliation set.
// Value is always "1"; the key carries the only information needed.
const searchDirtyPrefix = "idx:sidx:dirty:"

// SearchIndexDirtyStore is a narrow capability interface, deliberately kept
// OUT of database.Store. Adding methods to database.Store forces every
// implementation and every generated mock to grow with it; capabilities that
// only PebbleStore can satisfy live here instead and are reached through
// AsSearchIndexDirtyStore, which looks through the indexedStore decorator.
type SearchIndexDirtyStore interface {
	// MarkSearchIndexDirty records that a book's index entry may be stale.
	// Idempotent: marking an already-marked book is a no-op overwrite.
	MarkSearchIndexDirty(bookID string) error
	// ListSearchIndexDirty returns up to limit book IDs needing re-index.
	// limit <= 0 returns all of them.
	ListSearchIndexDirty(limit int) ([]string, error)
	// ClearSearchIndexDirty removes a book from the set after a successful
	// re-index. Clearing an absent key is not an error.
	ClearSearchIndexDirty(bookID string) error
	// CountSearchIndexDirty reports the backlog size, so the reconciler can
	// size its batch and so the metric reflects real outstanding work.
	CountSearchIndexDirty() (int, error)
}

// AsSearchIndexDirtyStore returns s as a SearchIndexDirtyStore if the
// underlying store supports it (true for *PebbleStore), or nil otherwise.
// Callers MUST nil-check the result, exactly like AsBookmarkStore's callers.
func AsSearchIndexDirtyStore(s any) SearchIndexDirtyStore {
	if s == nil {
		return nil
	}
	// Looks through the indexedStore decorator; see store_capability.go.
	if ds, ok := AsCapability[SearchIndexDirtyStore](s); ok {
		return ds
	}
	return nil
}

// Compile-time assertion that *PebbleStore satisfies the interface, so
// signature drift on either side fails the build rather than surfacing at
// runtime as a nil from AsSearchIndexDirtyStore.
var _ SearchIndexDirtyStore = (*PebbleStore)(nil)

// MarkSearchIndexDirty records a book as needing re-index.
//
// NoSync, deliberately, and this was measured rather than assumed. The first
// version used pebble.Sync on the reasoning that a durability record should
// be durable; a test marking 2,500 IDs then took 13.9s — ~180 marks/sec,
// because every mark fsyncs. Drops are not rare-and-isolated: they arrive in
// bursts during bulk operations (56,537 in seven days, all on two days), and
// this runs on the write path while enqueueIndex holds indexQueueMu.RLock.
// Sync would have added ~5ms to every write during exactly the overload the
// drop exists to relieve, and would have stalled shutdown's write-lock
// acquisition too.
//
// NoSync still writes the WAL into the OS page cache, so the set survives a
// process crash, panic or SIGKILL. Only machine power-loss can lose it — and
// a host that lost power mid-bulk-operation warrants a full reindex anyway,
// which is a decision a human makes, not a fsync.
//
// Precedent: the Pebble write-stall that froze this app for 9 hours was
// fixed the same way.
func (p *PebbleStore) MarkSearchIndexDirty(bookID string) error {
	if bookID == "" {
		return nil
	}
	return p.db.Set([]byte(searchDirtyPrefix+bookID), []byte("1"), pebble.NoSync)
}

// ClearSearchIndexDirty removes a book from the set.
//
// NoSync for the same reason as the mark, and with an even weaker
// consequence: a lost clear costs one redundant re-index on the next tick,
// which is idempotent. Syncing here would be worse than pointless — the
// reconciler clears once per repaired book, so a 5,000-item batch would
// spend ~28s in fsync.
func (p *PebbleStore) ClearSearchIndexDirty(bookID string) error {
	if bookID == "" {
		return nil
	}
	return p.db.Delete([]byte(searchDirtyPrefix+bookID), pebble.NoSync)
}

// ListSearchIndexDirty returns up to limit dirty book IDs in key order.
// limit <= 0 means "all". Key order is stable, so repeated partial drains
// make forward progress instead of re-reading the same head of the set —
// each successful re-index clears its key, so the next call starts past it.
func (p *PebbleStore) ListSearchIndexDirty(limit int) ([]string, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(searchDirtyPrefix),
		UpperBound: []byte(searchDirtyPrefix + "~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []string
	plen := len(searchDirtyPrefix)
	for iter.First(); iter.Valid(); iter.Next() {
		if limit > 0 && len(out) >= limit {
			break
		}
		// iter.Key() is only valid until the next Next(); string() copies.
		out = append(out, string(iter.Key()[plen:]))
	}
	return out, iter.Error()
}

// CountSearchIndexDirty returns the number of outstanding dirty books.
//
// This is a full prefix scan of keys (no values read). The set is expected to
// be empty in steady state and to spike only during bulk operations, so an
// O(backlog) count on a ticker is acceptable; it is NOT a per-request path.
func (p *PebbleStore) CountSearchIndexDirty() (int, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(searchDirtyPrefix),
		UpperBound: []byte(searchDirtyPrefix + "~"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n, iter.Error()
}
