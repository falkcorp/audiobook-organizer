// file: internal/searchcache/changelog.go
// version: 1.2.0
// guid: dafe39f3-657a-4ae6-9e2a-0f9a18a774b5
// last-edited: 2026-09-26

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
	"slices"
	"sort"
	"sync"
	"sync/atomic"
)

// DefaultRingSize is how many (generation, book ID) records the change ring
// keeps (owner decision 2026-09-25: 65,536, raised from 8,192). A bulk write
// of more books than this between two reads of one entry forces that entry
// onto the rebuild path. A deeper ring lets an entry survive a larger burst of
// unrelated edits; the cache's PatchLimit, not the ring, still caps how many
// insertions one patch makes. Each record is a generation plus a string header
// (24 bytes) and the book ID it points at (a 26-byte ULID in a 32-byte
// allocation), so a full ring holds about 3.5 MiB.
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
// in the order they were first recorded, together with the generation the
// answer is current to. ok is false when the ring no longer covers since
// (overflow, or a RecordAll after it): the caller must rebuild rather than
// patch.
//
// limit > 0 bounds the answer: the walk stops as soon as it has limit distinct
// IDs, so a result of exactly limit IDs means "limit or more changed" and the
// rest are not reported. A caller that can use at most N IDs passes N+1 and
// treats len(ids) > N as too many. limit <= 0 returns every changed ID.
//
// Only the window of records newer than since is copied under the mutex; the
// dedupe runs after it is released. Record takes the same mutex on every book
// write, so the time a writer can wait here is one binary search plus one copy
// of that window, not a map insert per record.
func (c *ChangeLog) ChangedSince(since uint64, limit int) (ids []string, current uint64, ok bool) {
	if c == nil {
		return nil, 0, true
	}
	bufp := windowPool.Get().(*[]string)
	defer func() {
		// Drop the string references before pooling the buffer, so a pooled
		// buffer never keeps evicted book IDs alive. Only [0, len) was
		// written; the rest was cleared when an earlier call returned it.
		clear(*bufp)
		*bufp = (*bufp)[:0]
		windowPool.Put(bufp)
	}()
	var window []string
	window, current, ok = c.window(since, (*bufp)[:0])
	*bufp = window
	if !ok || len(window) == 0 {
		return nil, current, ok
	}
	return dedupeIDs(window, limit), current, true
}

// windowPool holds the buffers ChangedSince copies a window of the ring into.
// A whole-ring window at DefaultRingSize is 1 MiB of string headers; pooling
// it keeps a stale lookup from turning that into garbage on every call.
var windowPool = sync.Pool{New: func() any { return new([]string) }}

// window appends to buf the IDs of every record newer than since, oldest
// first, and reports the current generation and whether the ring covers since.
// It is the only part of ChangedSince that holds the mutex.
func (c *ChangeLog) window(since uint64, buf []string) (_ []string, current uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current = c.gen.Load()
	if since >= current {
		return buf, current, true
	}
	if since < c.floor {
		return buf, current, false
	}
	// Records sit in the ring in non-decreasing generation order from head:
	// Record appends under a generation newer than every live record,
	// eviction removes from head, and RecordAll empties the ring. So the
	// first record newer than since is found by binary search.
	n := len(c.ring)
	start := sort.Search(c.size, func(i int) bool { return c.ring[(c.head+i)%n].gen > since })
	if start == c.size {
		return buf, current, true
	}
	buf = slices.Grow(buf, c.size-start)
	// The logical window [start, size) is at most two physical runs.
	first := (c.head + start) % n
	last := first + (c.size - start) // exclusive, may run past n
	if last <= n {
		buf = appendIDs(buf, c.ring[first:last])
	} else {
		buf = appendIDs(buf, c.ring[first:])
		buf = appendIDs(buf, c.ring[:last-n])
	}
	return buf, current, true
}

func appendIDs(buf []string, recs []changeRecord) []string {
	for _, r := range recs {
		buf = append(buf, r.id)
	}
	return buf
}

// dedupeIDs returns the distinct IDs of window in first-seen order, stopping
// once it has limit of them (limit <= 0: no bound).
func dedupeIDs(window []string, limit int) []string {
	hint := len(window)
	if limit > 0 && limit < hint {
		hint = limit
	}
	seen := make(map[string]struct{}, hint)
	var ids []string
	for _, id := range window {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	return ids
}
