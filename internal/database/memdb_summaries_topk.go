// file: internal/database/memdb_summaries_topk.go
// version: 1.0.0
// guid: c1a06a77-93c5-4446-8f31-20457a4ff9ac
// last-edited: 2026-10-30

package database

import (
	"container/heap"
	"math"
	"sort"
)

// sortHydratesRows reports whether ordering by sortBy needs hydrateSortNames
// to WRITE to the rows it sorts, which is what decides how
// MemStore.GetBookSummaries may hold them.
//
// The two paths, and why this is the dispatch between them:
//
//   - A sort key that reads a field the memdb row still carries (created_at,
//     updated_at, year, duration, bitrate, file_size, narrator, genre, ...)
//     can rank the memdb objects in place. memdb objects are shared and
//     read-only, so the walk collects *Book pointers into a bounded top-k
//     (bookTopK) and copies nothing but the final page's projection.
//   - author and series sort on a name stripBookForMemdb removed from the row,
//     so hydrateSortNames sets Book.Author / Book.Series before the sort. That
//     is a write, and writing through a memdb pointer would corrupt the shared
//     row for every concurrent reader. Those two keys therefore stay on the
//     copying path: every match is copied into a []Book, hydrated, sorted.
//
// This must name exactly the keys hydrateSortNames mutates for.
// TestSortHydratesRowsMatchesHydrateSortNames fails if the two drift, in
// either direction: a key listed here that hydrateSortNames leaves alone only
// costs the copy; a key hydrateSortNames mutates that is missing here would
// write to shared memdb rows, so that is the case the test exists for.
func sortHydratesRows(sortBy string) bool {
	switch sortBy {
	case "author", "series":
		return true
	}
	return false
}

// bookTopK keeps the first k books in sort order out of a stream of *Book,
// holding at most k pointers. It replaces "copy every match, sort all of them,
// cut the page" for sorts that need no hydration: a 17k-match addedAt-desc
// page of 40 held ~17k copied Books (976 B each, ~78 MB per call) and now
// holds 40 pointers.
//
// The heap is a max-heap on the output order: its root is the WORST row kept,
// so a candidate only has to beat the root to displace it. A candidate that
// does not beat it costs one comparison and no allocation.
//
// The rows are the memdb's own objects. They are read-only here -- bookTopK
// only ever reads through them -- and valid for the lifetime of the read
// transaction that produced them.
type bookTopK struct {
	less  func(a, b *Book) bool // output order: less(a, b) means a is listed before b
	k     int
	items []*Book // max-heap on less once len(items) == k, a plain list before
}

// newBookTopK returns a collector for the first k rows of the order
// bookSortLess(sortBy, ascending) defines. ok is false for a sort key with no
// comparator, which callers have already ruled out (CanSortBooksBy).
func newBookTopK(sortBy string, ascending bool, k int) (*bookTopK, bool) {
	less, ok := bookSortLess(sortBy, ascending)
	if !ok {
		return nil, false
	}
	if k < 1 {
		k = 1
	}
	// Initial capacity only; the slice grows to at most k. Spelled as a switch
	// for the CodeQL go/uncontrolled-allocation-size reason documented in
	// GetBookSummaries.
	var cap0 int
	switch {
	case k > 4096:
		cap0 = 4096
	default:
		cap0 = k
	}
	return &bookTopK{less: less, k: k, items: make([]*Book, 0, cap0)}, true
}

// heap.Interface: root is the worst kept row, so the "less" the heap package
// sees is the inverse of output order.
func (t *bookTopK) Len() int           { return len(t.items) }
func (t *bookTopK) Less(i, j int) bool { return t.less(t.items[j], t.items[i]) }
func (t *bookTopK) Swap(i, j int)      { t.items[i], t.items[j] = t.items[j], t.items[i] }
func (t *bookTopK) Push(x any)         { t.items = append(t.items, x.(*Book)) }
func (t *bookTopK) Pop() any {
	n := len(t.items) - 1
	x := t.items[n]
	t.items[n] = nil
	t.items = t.items[:n]
	return x
}

// add offers b to the collector. It never retains anything but the pointer.
func (t *bookTopK) add(b *Book) {
	if len(t.items) < t.k {
		heap.Push(t, b)
		return
	}
	if t.less(b, t.items[0]) {
		t.items[0] = b
		heap.Fix(t, 0)
	}
}

// page returns the kept rows in output order, skipping offset and capped at
// limit: the same slice sorting every offered row and cutting [offset,offset+limit)
// would give. The collector is spent afterwards.
func (t *bookTopK) page(offset, limit int) []*Book {
	sort.Slice(t.items, func(i, j int) bool { return t.less(t.items[i], t.items[j]) })
	if offset >= len(t.items) {
		return nil
	}
	end := len(t.items)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return t.items[offset:end]
}

// topKWindow is how many rows must be kept to serve offset+limit, saturating
// instead of overflowing for an absurd offset.
func topKWindow(offset, limit int) int {
	if limit > math.MaxInt-offset {
		return math.MaxInt
	}
	return offset + limit
}
