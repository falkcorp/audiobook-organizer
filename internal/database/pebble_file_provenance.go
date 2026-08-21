// file: internal/database/pebble_file_provenance.go
// version: 1.0.0
// guid: 2e8b6d05-91af-4c37-8b14-6d3a7f9e2c50
// last-edited: 2026-08-21

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Key layout
//
//	file_prov:<book_file_id>:<ts>   -> FileEvent   (the chain, one per file)
//	file_prov_hash:<hash>:<ts>      -> FileEvent   (index: any hash -> events)
//	file_prov_orphan:<sha_full>:<ts>-> FileEvent   (seen before it had a row)
//
// <ts> is a zero-padded UnixNano, matching path_history above it, so a prefix
// scan returns events in chronological order for free.
//
// The hash index is written for BOTH the full and chunked SHA. They are
// different functions over the same bytes and rows in the wild carry one or
// the other, so indexing only one would silently fail to resolve half the
// library.
const (
	fileProvPrefix       = "file_prov:"
	fileProvHashPrefix   = "file_prov_hash:"
	fileProvOrphanPrefix = "file_prov_orphan:"
)

// uniqueTSKey returns key(ts) for the first ts at or after the given one that
// is not already present. Two events recorded in the same nanosecond would
// otherwise silently overwrite each other, and this ledger's whole value is
// that it never loses an entry.
func (p *PebbleStore) uniqueTSKey(build func(ts int64) []byte, ts int64) []byte {
	for range 1000 {
		key := build(ts)
		if _, closer, err := p.db.Get(key); err != nil {
			// Not found (or unreadable) — the slot is ours.
			return key
		} else {
			closer.Close()
		}
		ts++
	}
	return build(ts)
}

// AppendFileEvent durably records one event in the provenance chain.
func (p *PebbleStore) AppendFileEvent(e FileEvent) error {
	if e.Kind == "" {
		return fmt.Errorf("AppendFileEvent: kind is required")
	}
	if e.BookFileID == "" && e.Digest.SHA256Full == "" {
		// An orphan with no full hash can never be adopted, so it would be
		// write-only data. Refuse it rather than silently accumulating rows
		// nothing can ever read back.
		return fmt.Errorf("AppendFileEvent: an event with no book_file_id requires digest.sha256_full")
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("AppendFileEvent: marshal: %w", err)
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	ts := e.At.UnixNano()

	if e.BookFileID != "" {
		key := p.uniqueTSKey(func(t int64) []byte {
			return fmt.Appendf(nil, "%s%s:%019d", fileProvPrefix, e.BookFileID, t)
		}, ts)
		if err := batch.Set(key, data, nil); err != nil {
			return fmt.Errorf("AppendFileEvent: set chain: %w", err)
		}
	} else {
		key := p.uniqueTSKey(func(t int64) []byte {
			return fmt.Appendf(nil, "%s%s:%019d", fileProvOrphanPrefix, e.Digest.SHA256Full, t)
		}, ts)
		if err := batch.Set(key, data, nil); err != nil {
			return fmt.Errorf("AppendFileEvent: set orphan: %w", err)
		}
	}

	// Index every hash this event carries, so a lookup by a hash the file used
	// to have still resolves after the bytes have moved on.
	for _, h := range []string{e.Digest.SHA256Full, e.Digest.SHA256Chunk} {
		if h == "" {
			continue
		}
		key := p.uniqueTSKey(func(t int64) []byte {
			return fmt.Appendf(nil, "%s%s:%019d", fileProvHashPrefix, h, t)
		}, ts)
		if err := batch.Set(key, data, nil); err != nil {
			return fmt.Errorf("AppendFileEvent: set hash index: %w", err)
		}
	}

	// Sync: a provenance record written before a mutation is worthless if it
	// is still sitting in a memtable when the process dies during that
	// mutation. This is the one place the durability cost is the point.
	return batch.Commit(pebble.Sync)
}

// scanEvents collects every FileEvent under a key prefix, oldest first.
func (p *PebbleStore) scanEvents(prefix []byte) ([]FileEvent, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []FileEvent
	for iter.First(); iter.Valid(); iter.Next() {
		var e FileEvent
		if err := json.Unmarshal(iter.Value(), &e); err != nil {
			// One corrupt row must not hide the rest of a file's history.
			continue
		}
		out = append(out, e)
	}
	return out, iter.Error()
}

// GetFileHistory returns every event for a book_file, oldest first.
func (p *PebbleStore) GetFileHistory(bookFileID string) ([]FileEvent, error) {
	if bookFileID == "" {
		return nil, fmt.Errorf("GetFileHistory: bookFileID is required")
	}
	return p.scanEvents(fmt.Appendf(nil, "%s%s:", fileProvPrefix, bookFileID))
}

// FindFileEventsByHash resolves any historical SHA to the events carrying it.
func (p *PebbleStore) FindFileEventsByHash(hash string) ([]FileEvent, error) {
	if hash == "" {
		return nil, fmt.Errorf("FindFileEventsByHash: hash is required")
	}
	return p.scanEvents(fmt.Appendf(nil, "%s%s:", fileProvHashPrefix, hash))
}

// AdoptOrphanEvents attaches observations made before the file had a row.
//
// The orphan rows are rewritten under the book_file's chain rather than
// copied, so a file observed on disk, then imported, reads back as one
// continuous history instead of two disconnected halves.
func (p *PebbleStore) AdoptOrphanEvents(bookFileID, sha256Full string) (int, error) {
	if bookFileID == "" || sha256Full == "" {
		return 0, fmt.Errorf("AdoptOrphanEvents: bookFileID and sha256Full are required")
	}

	prefix := fmt.Appendf(nil, "%s%s:", fileProvOrphanPrefix, sha256Full)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return 0, err
	}

	type pending struct {
		key   []byte
		event FileEvent
	}
	var found []pending
	for iter.First(); iter.Valid(); iter.Next() {
		var e FileEvent
		if err := json.Unmarshal(iter.Value(), &e); err != nil {
			continue
		}
		found = append(found, pending{key: append([]byte(nil), iter.Key()...), event: e})
	}
	iterErr := iter.Error()
	iter.Close()
	if iterErr != nil {
		return 0, iterErr
	}
	if len(found) == 0 {
		return 0, nil
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	for _, f := range found {
		f.event.BookFileID = bookFileID
		data, err := json.Marshal(f.event)
		if err != nil {
			return 0, fmt.Errorf("AdoptOrphanEvents: marshal: %w", err)
		}
		key := p.uniqueTSKey(func(t int64) []byte {
			return fmt.Appendf(nil, "%s%s:%019d", fileProvPrefix, bookFileID, t)
		}, f.event.At.UnixNano())
		if err := batch.Set(key, data, nil); err != nil {
			return 0, fmt.Errorf("AdoptOrphanEvents: set chain: %w", err)
		}
		if err := batch.Delete(f.key, nil); err != nil {
			return 0, fmt.Errorf("AdoptOrphanEvents: delete orphan: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("AdoptOrphanEvents: commit: %w", err)
	}
	return len(found), nil
}

// SortFileEvents orders events chronologically. Callers that merge chains from
// more than one prefix scan need this; a single scan is already ordered.
func SortFileEvents(events []FileEvent) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
}
