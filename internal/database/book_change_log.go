// file: internal/database/book_change_log.go
// version: 1.0.0
// guid: 1bbf59f8-a14b-4a96-8905-d33cf3329cf4
// last-edited: 2026-10-01

package database

import (
	"sort"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
)

// bookChangeLogCap bounds the log. It is sized for a library scan running
// beside a bulk repairs apply (applying during a scan is supported), which
// can write thousands of books between two apply rows; past it a reader falls
// back to a full rebuild, which is correct, only slower. 64k entries of a
// generation and a ULID are a few MB.
const bookChangeLogCap = 1 << 16

// bookChange is one library-generation bump and the book it was for.
type bookChange struct {
	gen uint64
	id  string
}

// bookChangeLog records the book behind every library-generation bump, so a
// generation-keyed index (the repairs fixers' title index) can catch up from
// generation g by re-reading only the books written since, rather than every
// book. The bump and its record happen under one mutex, so a reader holding
// the mutex sees a record for every generation up to the current value; a
// generation bumped without one (a bare Generation.Bump) shows up as a gap and
// the reader is told to rebuild.
type bookChangeLog struct {
	mu      sync.Mutex
	entries []bookChange // ascending, contiguous generations
	// floor: generations at or below it may have been dropped.
	floor uint64
}

// bump advances g and records id against the new generation.
func (l *bookChangeLog) bump(g *cache.Generation, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := g.Bump()
	if len(l.entries) >= bookChangeLogCap {
		drop := bookChangeLogCap / 4
		l.floor = l.entries[drop-1].gen
		l.entries = append(make([]bookChange, 0, bookChangeLogCap), l.entries[drop:]...)
	}
	l.entries = append(l.entries, bookChange{gen: n, id: id})
}

// since returns the ids of the books written after generation from (each
// once, in write order) and the generation that brings a reader up to date.
// ok is false when the log cannot prove the list complete: from predates what
// it still holds, or some generation in (from, current] has no record.
func (l *bookChangeLog) since(g *cache.Generation, from uint64) (ids []string, upTo uint64, ok bool) {
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
