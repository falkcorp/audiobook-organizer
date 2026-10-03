// file: internal/database/book_change_log.go
// version: 2.0.0
// guid: 1bbf59f8-a14b-4a96-8905-d33cf3329cf4
// last-edited: 2026-10-03

package database

import (
	"sort"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
)

// idChangeLogCap bounds a log. It is sized for a library scan running beside
// a bulk repairs apply (applying during a scan is supported), which can write
// thousands of books between two apply rows; past it a reader falls back to a
// full rebuild, which is correct, only slower. 64k entries of a generation
// and a ULID are a few MB.
const idChangeLogCap = 1 << 16

// idChange is one generation bump and the id it was for.
type idChange struct {
	gen uint64
	id  string
}

// idChangeLog records the id behind every bump of a cache.Generation, so a
// generation-keyed index (the repairs fixers' title index, the review
// listing's snapshot) can catch up from generation g by re-reading only the
// ids written since, rather than every row. The bump and its record happen
// under one mutex, so a reader holding the mutex sees a record for every
// generation up to the current value; a generation bumped without one (a bare
// Generation.Bump, or bumpAll) shows up as a gap and the reader is told to
// rebuild.
//
// Two instances live on PebbleStore: bookChanges over the library generation
// (one record per book write) and cacheChanges over the metadata-cache
// generation (one record per "metadata_cache:" key write).
type idChangeLog struct {
	mu      sync.Mutex
	entries []idChange // ascending, contiguous generations
	// floor: generations at or below it may have been dropped.
	floor uint64
}

// bump advances g and records id against the new generation.
func (l *idChangeLog) bump(g *cache.Generation, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := g.Bump()
	if len(l.entries) >= idChangeLogCap {
		drop := idChangeLogCap / 4
		l.floor = l.entries[drop-1].gen
		l.entries = append(make([]idChange, 0, idChangeLogCap), l.entries[drop:]...)
	}
	l.entries = append(l.entries, idChange{gen: n, id: id})
}

// bumpAll advances g for a write that touched every id (Reset's wipe). No
// record can name them, so the log forgets what it held and sets its floor to
// the new generation: since(from) is ok only for from == the new generation,
// and every reader behind it rebuilds.
func (l *idChangeLog) bumpAll(g *cache.Generation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := g.Bump()
	l.entries = l.entries[:0]
	l.floor = n
}

// since returns the ids written after generation from (each once, in write
// order) and the generation that brings a reader up to date. ok is false when
// the log cannot prove the list complete: from predates what it still holds,
// or some generation in (from, current] has no record.
func (l *idChangeLog) since(g *cache.Generation, from uint64) (ids []string, upTo uint64, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := g.Value()
	if from == cur {
		return nil, cur, true
	}
	if from > cur || from < l.floor {
		return nil, cur, false
	}
	i := sort.Search(len(l.entries), func(i int) bool { return l.entries[i].gen > from })
	seen := map[string]bool{}
	want := from + 1
	for _, e := range l.entries[i:] {
		if e.gen != want {
			return nil, cur, false
		}
		want++
		if !seen[e.id] {
			seen[e.id] = true
			ids = append(ids, e.id)
		}
	}
	if want-1 != cur {
		return nil, cur, false
	}
	return ids, cur, true
}

// BookChangeLogProvider is implemented by a store whose library generation
// (LibraryGenerationProvider) records the book behind each bump.
type BookChangeLogProvider interface {
	// BooksChangedSince returns the ids of the books created, updated or
	// deleted after library generation gen, and the generation that brings
	// a reader up to date. ok is false when the store cannot list them
	// completely; the caller must then rebuild from every book.
	BooksChangedSince(gen uint64) (ids []string, upTo uint64, ok bool)
}

// BooksChangedSince implements BookChangeLogProvider.
func (p *PebbleStore) BooksChangedSince(gen uint64) ([]string, uint64, bool) {
	return p.bookChanges.since(&p.libGen, gen)
}

// BooksChangedSinceOf resolves BookChangeLogProvider from s through any
// opted-in decorator chain; ok is false when s has none.
func BooksChangedSinceOf(s any, gen uint64) (ids []string, upTo uint64, ok bool) {
	p, found := AsCapability[BookChangeLogProvider](s)
	if !found {
		return nil, 0, false
	}
	return p.BooksChangedSince(gen)
}

// MetadataCacheChangeLogProvider is implemented by a store whose
// metadata-cache generation (MetadataCacheGeneration) records the book behind
// each bump.
type MetadataCacheChangeLogProvider interface {
	// MetadataCacheChangedSince returns the ids of the books whose
	// "metadata_cache:" row was written or deleted after metadata-cache
	// generation gen, and the generation that brings a reader up to date. ok
	// is false when the store cannot list them completely (Reset wiped the
	// keyspace, or gen predates what the log holds); the caller must then
	// rebuild from every row.
	MetadataCacheChangedSince(gen uint64) (ids []string, upTo uint64, ok bool)
}

// MetadataCacheChangedSince implements MetadataCacheChangeLogProvider.
func (p *PebbleStore) MetadataCacheChangedSince(gen uint64) ([]string, uint64, bool) {
	return p.cacheChanges.since(&p.cacheGen, gen)
}

// MetadataCacheChangedSinceOf resolves MetadataCacheChangeLogProvider from s
// through any opted-in decorator chain; ok is false when s has none.
func MetadataCacheChangedSinceOf(s any, gen uint64) (ids []string, upTo uint64, ok bool) {
	p, found := AsCapability[MetadataCacheChangeLogProvider](s)
	if !found {
		return nil, 0, false
	}
	return p.MetadataCacheChangedSince(gen)
}

// bumpMetadataCacheGeneration records a committed write of bookID's
// "metadata_cache:" row (Put or Delete alike) against the metadata-cache
// generation.
func (p *PebbleStore) bumpMetadataCacheGeneration(bookID string) {
	p.cacheChanges.bump(&p.cacheGen, bookID)
}

// noteRawMetadataCacheWrite moves MetadataCacheGeneration when a raw key
// write landed in the "metadata_cache:" keyspace, logged against the book the
// key names.
func (p *PebbleStore) noteRawMetadataCacheWrite(key string) {
	if id, ok := strings.CutPrefix(key, metadataCacheKeyPrefix); ok {
		p.bumpMetadataCacheGeneration(id)
	}
}
