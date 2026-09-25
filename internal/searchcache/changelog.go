// file: internal/searchcache/changelog.go
// version: 1.1.0
// guid: dafe39f3-657a-4ae6-9e2a-0f9a18a774b5
// last-edited: 2026-09-25

// Package searchcache holds the shared search result cache: the full ranked
// list of matching book IDs per (query, filter set), served as page slices,
// kept current by a store-wide change generation plus a bounded ring of the
// book IDs changed since each generation.
//
// Invalidation model (owner decision 2026-09-25): ONE counter for the whole
// store, not per-book stamps. A cache entry remembers the generation it was
// built at. On a hit at an older generation the ring answers "which books
// changed since then"; the entry is patched by re-evaluating only those books.
// When the ring no longer reaches back that far (a bulk write evicted the
// oldest records), the entry is rebuilt instead.
package searchcache

import (
	"sync"
	"sync/atomic"
)

// DefaultRingSize is how many (generation, book ID) records the change ring
// keeps (owner decision 2026-09-25: 65,536, raised from 8,192). A bulk write
// of more books than this between two reads of one entry forces that entry
// onto the rebuild path. A deeper ring lets an entry survive a larger burst of
// unrelated edits: re-evaluating the changed IDs is cheap, and the cache's
// PatchLimit, not the ring, still caps how many insertions one patch makes.
// Each record is a generation plus a string header, about 24 bytes, so the
// ring costs about 1.5 MiB.
const DefaultRingSize = 65536

type changeRecord struct {
	gen uint64
	id  string
}

// ChangeLog is the store-wide change generation plus the ring of changed book
// IDs. The zero value is not usable; call NewChangeLog.
//
// Every book write that can change a search result calls Record with the IDs
// it touched. Writes whose affected books are unknown call RecordAll, which
// makes every older generation unpatchable.
type ChangeLog struct {
	gen atomic.Uint64

	mu   sync.Mutex
	ring []changeRecord
	head int // index of the oldest record
	size int // number of live records
	// floor is the newest generation whose records may be incomplete: a
	// record at or below it has been evicted (or a RecordAll made the whole
	// past unknown). ChangedSince(g) is answerable only for g >= floor.
	floor uint64
}

// NewChangeLog returns a change log whose ring holds ringSize records.
// ringSize <= 0 means DefaultRingSize.
func NewChangeLog(ringSize int) *ChangeLog {
	if ringSize <= 0 {
		ringSize = DefaultRingSize
	}
	return &ChangeLog{ring: make([]changeRecord, ringSize)}
}

// Generation is the current store-wide generation. It only ever grows.
func (c *ChangeLog) Generation() uint64 {
	if c == nil {
		return 0
	}
	return c.gen.Load()
}

// Record advances the generation by one and records ids under the new
// generation. Empty IDs are ignored; a call with no usable IDs is a no-op
// (nothing changed, so nothing is invalidated).
func (c *ChangeLog) Record(ids ...string) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	usable := 0
	for _, id := range ids {
		if id != "" {
			usable++
		}
	}
	if usable == 0 {
		return c.gen.Load()
	}
	// The generation is advanced BEFORE the records are appended and under the
	// same lock ChangedSince reads them with, so a reader can never observe the
	// new generation without also seeing the records stamped with it.
	g := c.gen.Add(1)
	for _, id := range ids {
		if id == "" {
			continue
		}
		if c.size == len(c.ring) {
			// Evict the oldest record. Its generation's change set is now
			// incomplete, so nothing at or before it can be patched.
			old := c.ring[c.head]
			if old.gen > c.floor {
				c.floor = old.gen
			}
			c.head = (c.head + 1) % len(c.ring)
			c.size--
		}
		c.ring[(c.head+c.size)%len(c.ring)] = changeRecord{gen: g, id: id}
		c.size++
	}
	return g
}

// RecordAll advances the generation and declares every earlier generation
// unpatchable: the change touched books this log was not told about (a search
// index rebuild completing, for example).
func (c *ChangeLog) RecordAll() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.gen.Add(1)
	c.floor = g
	c.head, c.size = 0, 0
	return g
}

// ChangedSince returns the distinct book IDs changed after generation since,
// together with the generation the answer is current to. ok is false when the
// ring no longer covers since (overflow, or a RecordAll after it): the caller
// must rebuild rather than patch.
func (c *ChangeLog) ChangedSince(since uint64) (ids []string, current uint64, ok bool) {
	if c == nil {
		return nil, 0, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current = c.gen.Load()
	if since >= current {
		return nil, current, true
	}
	if since < c.floor {
		return nil, current, false
	}
	seen := make(map[string]struct{})
	for i := 0; i < c.size; i++ {
		r := c.ring[(c.head+i)%len(c.ring)]
		if r.gen <= since {
			continue
		}
		if _, dup := seen[r.id]; dup {
			continue
		}
		seen[r.id] = struct{}{}
		ids = append(ids, r.id)
	}
	return ids, current, true
}
