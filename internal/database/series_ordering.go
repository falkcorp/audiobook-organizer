// file: internal/database/series_ordering.go
// version: 1.0.0
// guid: 3e8b1d47-9c25-4a60-b7f8-2d04e916c5a3
// last-edited: 2026-08-14

package database

import (
	"sort"
	"strings"
)

// sortBooksInSeriesOrder orders a series' books the way a reader expects them:
// by SeriesSequence ascending, with unnumbered books last, and ties broken by
// title (case-insensitive).
//
// This lives in one place because GetBooksBySeriesIDCore has two backing
// implementations and they did not agree: the memdb walk sorted, the Pebble
// scan did not sort at all, so the same series came back in ULID order whenever
// it was served during the ~132 s memdb warmup window. Sequence order is the
// entire point of a series listing, so "same books, arbitrary order" is a real
// defect and not a cosmetic one. Sharing the comparator is what keeps the two
// paths from drifting again — see series_getter_conformance_test.go.
//
// The title tiebreaker compares titles directly rather than through a parallel
// precomputed slice. The previous memdb implementation built a `keys` slice of
// lowercased titles up front and then indexed it from inside the comparator,
// but sort permutes only the slice handed to it — `keys` kept the ORIGINAL
// order, so after the first swap the comparator was reading a title belonging
// to some other book. It was not observed producing a wrong order on a small
// fixture, and it is fixed here rather than preserved, because a comparator
// that reads stale data is not something to carry forward on the argument that
// it happened to work.
func sortBooksInSeriesOrder(books []Book) {
	if len(books) < 2 {
		return
	}
	sort.SliceStable(books, func(i, j int) bool {
		si, sj := books[i].SeriesSequence, books[j].SeriesSequence
		switch {
		case si == nil && sj == nil:
			return lessTitle(books[i].Title, books[j].Title)
		case si == nil:
			// Unnumbered books sort after numbered ones.
			return false
		case sj == nil:
			return true
		case *si != *sj:
			return *si < *sj
		default:
			return lessTitle(books[i].Title, books[j].Title)
		}
	})
}

// lessTitle is the case-insensitive title comparison used as the series-order
// tiebreaker.
func lessTitle(a, b string) bool {
	return strings.ToLower(a) < strings.ToLower(b)
}
