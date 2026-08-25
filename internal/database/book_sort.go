// file: internal/database/book_sort.go
// version: 1.1.0
// guid: 3c1f7a52-9d84-4e6b-b0a7-2f5c8e1d4a93
// last-edited: 2026-08-25
//
// The single ordering authority for a []Book.
//
// WHY THIS EXISTS
//
// Three code paths sorted books, and they disagreed:
//
//  1. the memdb sorted indexes (memdb_sort_indexers.go), walked in order;
//  2. audiobooks.applySorting's sortFieldMap, materialise-and-sort;
//  3. the ABS filtered views, which did not sort at all.
//
// (1) and (2) were kept in step by a comment asking future editors to change
// both. They were not in step. The indexes encode a missing value as 0x01
// against 0x00 for present, so an unknown sorts AFTER every known value
// ascending; the comparators reached for derefInt/derefStr, which turn a nil
// year into the year 0 and a nil narrator into "", both of which sort FIRST.
// Same field, opposite ends, and which one a request got depended on whether
// its sort could be pushed down -- i.e. on config, not on the query.
//
// So the ordering rule lives here once, expressed the way the indexes express
// it, and every path calls it. A comment cannot keep two implementations
// honest; having one implementation can.

package database

import (
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// normalizeSortString matches encodeSortableString: trim, lowercase, and
// treat the empty result as "no value".
func normalizeSortString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// compareSortStrings ranks two already-extracted string values, placing a
// missing (empty) value after every present one -- the ascending order the
// 0x00/0x01 presence marker gives the indexes.
func compareSortStrings(a, b string) int {
	a, b = normalizeSortString(a), normalizeSortString(b)
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	return strings.Compare(a, b)
}

// compareSortInts ranks two numeric values by (present, value), so a genuine
// zero sorts with the numbers and an absent value sorts last ascending. This
// is the distinction bookIntSortIndex's (int64, bool) accessors exist to
// preserve and that derefInt() destroys.
func compareSortInts(av int64, ap bool, bv int64, bp bool) int {
	switch {
	case !ap && !bp:
		return 0
	case !ap:
		return 1
	case !bp:
		return -1
	case av < bv:
		return -1
	case av > bv:
		return 1
	}
	return 0
}

// bookTitleSortValue mirrors titleSortIndex: normalised title, falling back to
// the original filename when the title is empty.
//
// It returns "" rather than titleSortIndex's "~" sentinel for "no title". The
// sentinel is that index's way of saying "sort to end", and "" is how every
// other field here says it, so the two agree on intent. They can still differ
// on one input: "~" is 0x7E, so a title whose normalised form starts with a
// byte above it (any non-ASCII first letter, e.g. "Ärger" -> 0xC3) sorts after
// the sentinel in the index but before a missing title here. That is a bug in
// the sentinel, not in this function; fixing it means re-keying the title
// index, which is a migration, not a comparator change.
func bookTitleSortValue(b *Book) string {
	key := util.NormalizeTitle(b.Title)
	if key == "" && b.OriginalFilename != nil {
		key = util.NormalizeTitle(*b.OriginalFilename)
	}
	return key
}

func derefSortStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func bookSampleRateSortValue(b *Book) (int64, bool) {
	if b.SampleRate == nil {
		return 0, false
	}
	return int64(*b.SampleRate), true
}

// stringSortComparator lifts a string accessor into a comparator.
func stringSortComparator(get func(*Book) string) func(a, b *Book) int {
	return func(a, b *Book) int { return compareSortStrings(get(a), get(b)) }
}

// intSortComparator lifts a (value, present) accessor into a comparator.
func intSortComparator(get func(*Book) (int64, bool)) func(a, b *Book) int {
	return func(a, b *Book) int {
		av, ap := get(a)
		bv, bp := get(b)
		return compareSortInts(av, ap, bv, bp)
	}
}

// bookSortComparators is the ordering rule for every sortable field.
//
// Entries that also have a memdb index reuse that index's accessor verbatim
// (bookYearSortValue and friends), so an indexed walk and a materialised sort
// cannot drift: there is one accessor, not a matched pair. Entries with no
// index exist only on the materialised path.
//
// Alias keys (duration_seconds, bitrate_kbps, file_size_bytes, sample_rate_hz)
// mirror sortIndexForField's aliases -- one rule, two spellings.
var bookSortComparators = map[string]func(a, b *Book) int{
	// Index-backed.
	"title":            stringSortComparator(bookTitleSortValue),
	"author":           stringSortComparator(bookAuthorSortValue),
	"narrator":         stringSortComparator(bookNarratorSortValue),
	"series":           stringSortComparator(bookSeriesSortValue),
	"year":             intSortComparator(bookYearSortValue),
	"created_at":       intSortComparator(bookCreatedAtSortValue),
	"updated_at":       intSortComparator(bookUpdatedAtSortValue),
	"duration":         intSortComparator(bookDurationSortValue),
	"duration_seconds": intSortComparator(bookDurationSortValue),
	"bitrate":          intSortComparator(bookBitrateSortValue),
	"bitrate_kbps":     intSortComparator(bookBitrateSortValue),
	"file_size":        intSortComparator(bookFileSizeSortValue),
	"file_size_bytes":  intSortComparator(bookFileSizeSortValue),

	// Materialised path only -- no index exists for these.
	"genre":          stringSortComparator(func(b *Book) string { return derefSortStr(b.Genre) }),
	"language":       stringSortComparator(func(b *Book) string { return derefSortStr(b.Language) }),
	"publisher":      stringSortComparator(func(b *Book) string { return derefSortStr(b.Publisher) }),
	"codec":          stringSortComparator(func(b *Book) string { return derefSortStr(b.Codec) }),
	"quality":        stringSortComparator(func(b *Book) string { return derefSortStr(b.Quality) }),
	"edition":        stringSortComparator(func(b *Book) string { return derefSortStr(b.Edition) }),
	"library_state":  stringSortComparator(func(b *Book) string { return derefSortStr(b.LibraryState) }),
	"format":         stringSortComparator(func(b *Book) string { return b.Format }),
	"sample_rate":    intSortComparator(bookSampleRateSortValue),
	"sample_rate_hz": intSortComparator(bookSampleRateSortValue),
}

// CanSortBooksBy reports whether SortBooks understands field. It is broader
// than CanPushDownSort, which answers the narrower question of whether the
// store can stream that order from an index.
func CanSortBooksBy(field string) bool {
	_, ok := bookSortComparators[field]
	return ok
}

// SortableBookFields returns every key SortBooks understands, sorted for a
// stable iteration order.
//
// Exported for tests that must cover the WHOLE set rather than a list someone
// maintains by hand. A hand-listed set is how 13 of these keys shipped
// ordering nothing: the fields were sortable in principle, nothing asserted
// each one actually ordered anything end to end, and the two tests that looked
// like they covered sorting checked stability and permutation -- properties an
// all-equal comparator satisfies perfectly. Enumerating from the map means a
// comparator added without a working path fails on arrival.
func SortableBookFields() []string {
	out := make([]string, 0, len(bookSortComparators))
	for field := range bookSortComparators {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// SortBooks orders books in place by field.
//
// An unknown field leaves the slice untouched, matching the previous
// behaviour of applySorting: callers validate the field name upstream, and
// silently returning an arbitrary order is worse than returning the order the
// caller already had.
//
// Descending negates the whole comparison, tiebreaker included, because the
// store serves descending with txn.GetReverse -- a full reversal of the key
// order, which also flips where missing values land and reverses the trailing
// primary-key tiebreak. Sorting missing-last in both directions would look
// tidier and would not match what the index does.
func SortBooks(books []Book, field string, ascending bool) {
	cmp, ok := bookSortComparators[field]
	if !ok {
		return
	}
	sort.SliceStable(books, func(i, j int) bool {
		r := cmp(&books[i], &books[j])
		if r == 0 {
			// memdb appends the primary key to a non-unique index entry, so
			// ties break by book ID there; match that.
			r = strings.Compare(books[i].ID, books[j].ID)
		}
		if !ascending {
			return r > 0
		}
		return r < 0
	})
}
