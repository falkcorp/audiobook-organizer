// file: internal/audiobooks/audiobook_service_prop_test.go
// version: 1.2.0
// guid: 864889b2-5529-4d23-9220-2f17e11fab35
// last-edited: 2026-07-07

// Property-based tests for the library-list sort, filter, and pagination
// code paths in internal/server/audiobook_service.go. These properties
// express invariants that must hold for ANY input, exercised with random
// shapes drawn from the shared rapidgen generators:
//
//   - Sort stability: calling applySorting twice on the same slice yields
//     identical order. A stable sort with a deterministic tiebreaker must
//     be idempotent.
//   - Sort is a permutation: the sorted slice contains exactly the same
//     multiset of IDs as the input — nothing added, nothing dropped.
//   - Filter partitioning: for any field filter P,
//     filter(books, P) ∪ filter(books, ¬P) == books, and the two halves
//     are disjoint. This is the classic partition law for a total predicate.
//   - Pagination consistency: GetAllBooks(limit=N, offset=0) ++
//     GetAllBooks(limit=N, offset=N) has no duplicates and covers the same
//     set of book IDs as GetAllBooks(limit=2N, offset=0). Verifies the
//     Pebble iterator's offset/limit arithmetic matches a single-scan read.
//
// Each property draws a fresh slice (or a fresh PebbleStore) per
// rapid.Check iteration so state never leaks between shrinks.

package audiobooks

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/rapidgen"
	"pgregory.net/rapid"
)

// bookIDs extracts the IDs from a slice of books as a sorted slice of
// strings, for set-equality comparisons that are independent of order.
func bookIDs(books []database.Book) []string {
	ids := make([]string, len(books))
	for i, b := range books {
		ids[i] = b.ID
	}
	sort.Strings(ids)
	return ids
}

// bookIDsInOrder returns the IDs in iteration order — used for the
// sort-stability test where order (not just set membership) matters.
func bookIDsInOrder(books []database.Book) []string {
	ids := make([]string, len(books))
	for i, b := range books {
		ids[i] = b.ID
	}
	return ids
}

// bookCoreIDs is bookIDs for []database.BookCore — used by the pagination
// property test, whose PebbleStore reads come back Core-typed.
func bookCoreIDs(books []database.BookCore) []string {
	ids := make([]string, len(books))
	for i, b := range books {
		ids[i] = b.ID
	}
	sort.Strings(ids)
	return ids
}

// sortableFields is the set of sort keys exercised by the stability and
// permutation properties. Covers string-valued, int-valued, and
// time-valued columns to stress every comparator in sortFieldMap.
var sortableFields = []string{
	"title", "author", "narrator", "series", "genre", "year",
	"language", "publisher", "format", "duration", "bitrate",
	"file_size", "codec", "created_at", "updated_at",
	"library_state", "quality", "edition",
}

// filterableFields enumerates the FieldFilter.Field values the partition
// property will test against. Each corresponds to a branch of
// fieldMatchesValue in audiobook_service.go.
var filterableFields = []string{
	"title", "author", "narrator", "series", "genre",
	"language", "publisher", "edition", "format", "codec",
	"quality", "library_state", "description",
}

// genBookSlice draws a slice of Books and assigns each a unique ID so
// the tiebreaker in applySorting (sort by ID) is always well-defined and
// tests can rely on ID-based set equality.
func genBookSlice(t *rapid.T, label string, minLen, maxLen int) []database.Book {
	n := rapid.IntRange(minLen, maxLen).Draw(t, label+"_len")
	books := make([]database.Book, n)
	for i := 0; i < n; i++ {
		b := rapidgen.Book(t)
		// Assign a unique ULID-shaped ID so the sort tiebreaker has
		// something deterministic to work with.
		b.ID = rapid.StringMatching(`[0-9A-HJKMNP-TV-Z]{26}`).Draw(t, label+"_id")
		// Attach a random author/series so the "author" / "series" sort
		// comparators aren't always comparing two empty strings.
		if rapid.Float64Range(0, 1).Draw(t, label+"_author_present") < 0.6 {
			a := rapidgen.Author(t)
			a.ID = i + 1
			b.Author = &a
			b.AuthorID = &a.ID
		}
		if rapid.Float64Range(0, 1).Draw(t, label+"_series_present") < 0.5 {
			s := rapidgen.Series(t)
			s.ID = i + 1
			b.Series = &s
			b.SeriesID = &s.ID
		}
		books[i] = *b
	}
	return books
}

// TestProp_SortStability verifies that applying the same sort twice
// produces identical ordering. applySorting uses sort.SliceStable plus
// an ID tiebreaker, so the second call must be a no-op at the order
// level.
func TestProp_SortStability(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		books := genBookSlice(t, "books", 0, 40)
		field := rapid.SampledFrom(sortableFields).Draw(t, "sort_field")
		order := rapid.SampledFrom([]string{"asc", "desc"}).Draw(t, "sort_order")
		f := ListFilters{SortBy: field, SortOrder: order}

		// Deep-copy once, sort both copies, then sort one of them a
		// second time. Sorting must be idempotent: copy2 == copy1.
		copy1 := append([]database.Book(nil), books...)
		copy2 := append([]database.Book(nil), books...)
		applySorting(copy1, f)
		applySorting(copy2, f)
		applySorting(copy2, f) // second pass

		ids1 := bookIDsInOrder(copy1)
		ids2 := bookIDsInOrder(copy2)
		if len(ids1) != len(ids2) {
			t.Fatalf("length mismatch: %d vs %d", len(ids1), len(ids2))
		}
		for i := range ids1 {
			if ids1[i] != ids2[i] {
				t.Fatalf("sort unstable at index %d: %q vs %q (field=%s order=%s)",
					i, ids1[i], ids2[i], field, order)
			}
		}
	})
}

// TestProp_SortIsPermutation verifies that sorting neither adds nor
// removes elements: the multiset of book IDs before and after sorting
// must be identical.
func TestProp_SortIsPermutation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		books := genBookSlice(t, "books", 0, 40)
		field := rapid.SampledFrom(sortableFields).Draw(t, "sort_field")
		order := rapid.SampledFrom([]string{"asc", "desc"}).Draw(t, "sort_order")
		f := ListFilters{SortBy: field, SortOrder: order}

		before := bookIDs(books)
		sorted := append([]database.Book(nil), books...)
		applySorting(sorted, f)
		after := bookIDs(sorted)

		if len(before) != len(after) {
			t.Fatalf("length changed: %d → %d", len(before), len(after))
		}
		for i := range before {
			if before[i] != after[i] {
				t.Fatalf("multiset differs at %d: %q vs %q", i, before[i], after[i])
			}
		}
	})
}

// TestProp_FilterPartitioning verifies that for any field filter P,
// the books matching P and the books NOT matching P together form
// exactly the original input, with no overlap.
func TestProp_FilterPartitioning(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	rapid.Check(t, func(t *rapid.T) {
		books := genBookSlice(t, "books", 0, 40)
		field := rapid.SampledFrom(filterableFields).Draw(t, "filter_field")
		// Draw a short value — real queries are short substrings, and
		// we want the "matches" and "doesn't match" partitions both to
		// have non-trivial probability of being non-empty.
		value := rapid.StringMatching(`[a-z]{1,4}`).Draw(t, "filter_value")

		positive := FieldFilter{Field: field, Value: value, Negated: false}
		negative := FieldFilter{Field: field, Value: value, Negated: true}

		matched := make([]database.Book, 0, len(books))
		unmatched := make([]database.Book, 0, len(books))
		for _, b := range books {
			if matchesFieldFilters(b, []FieldFilter{positive}) {
				matched = append(matched, b)
			}
			if matchesFieldFilters(b, []FieldFilter{negative}) {
				unmatched = append(unmatched, b)
			}
		}

		// Partition law #1: sizes add up.
		if len(matched)+len(unmatched) != len(books) {
			t.Fatalf("partition sizes don't sum: %d + %d != %d (field=%s value=%q)",
				len(matched), len(unmatched), len(books), field, value)
		}

		// Partition law #2: union equals the input (as a multiset).
		union := append(append([]database.Book(nil), matched...), unmatched...)
		got := bookIDs(union)
		want := bookIDs(books)
		if len(got) != len(want) {
			t.Fatalf("union has %d ids, input has %d", len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("union differs from input at %d: %q vs %q", i, got[i], want[i])
			}
		}

		// Partition law #3: the two halves are disjoint.
		inMatched := make(map[string]struct{}, len(matched))
		for _, b := range matched {
			inMatched[b.ID] = struct{}{}
		}
		for _, b := range unmatched {
			if _, dup := inMatched[b.ID]; dup {
				t.Fatalf("book %q appears in both partitions (field=%s value=%q)",
					b.ID, field, value)
			}
		}
	})
}

// TestProp_PaginationConsistency verifies that two half-page reads
// stitch together into the same set as one full-page read, with no
// duplicates across the two halves. This exercises PebbleStore.GetAllBooks
// offset/limit arithmetic through random book counts and page sizes.
func TestProp_PaginationConsistency(outer *testing.T) {
	if testing.Short() {
		outer.Skip("slow property test; run without -short")
	}
	// Root temp dir for the whole test; each rapid iteration gets a
	// fresh subdirectory so PebbleDB file locks never collide.
	root := outer.TempDir()
	rapid.Check(outer, func(t *rapid.T) {
		// Total books in the store. Keep it small so the test stays
		// fast — iterating the Pebble keyspace is O(n) per call, and
		// we do four calls per iteration (create + 3 GetAllBooks).
		total := rapid.IntRange(0, 25).Draw(t, "total")
		// Page size N. The comparison is between [0..N) ∪ [N..2N) and
		// [0..2N). Allow N=1 to stress the single-item edge case.
		pageSize := rapid.IntRange(1, 15).Draw(t, "page_size")

		// Fresh store per iteration — each gets its own subdirectory
		// so the Pebble on-disk lock is released between shrinks.
		subdir := rapid.StringMatching(`[a-f0-9]{16}`).Draw(t, "db_subdir")
		store, err := database.NewPebbleStore(filepath.Join(root, subdir))
		if err != nil {
			t.Fatalf("NewPebbleStore: %v", err)
		}
		defer store.Close()

		// Seed with `total` random books. CreateBook assigns the ID.
		for i := 0; i < total; i++ {
			b := rapidgen.Book(t)
			if _, err := store.CreateBook(b); err != nil {
				t.Fatalf("CreateBook: %v", err)
			}
		}

		page1, err := store.GetAllBooksCore(pageSize, 0)
		if err != nil {
			t.Fatalf("GetAllBooksCore page1: %v", err)
		}
		page2, err := store.GetAllBooksCore(pageSize, pageSize)
		if err != nil {
			t.Fatalf("GetAllBooksCore page2: %v", err)
		}
		fullPage, err := store.GetAllBooksCore(2*pageSize, 0)
		if err != nil {
			t.Fatalf("GetAllBooksCore fullPage: %v", err)
		}

		// Invariant 1: no duplicates across page1 and page2.
		seen := make(map[string]struct{}, len(page1)+len(page2))
		for _, b := range page1 {
			seen[b.ID] = struct{}{}
		}
		for _, b := range page2 {
			if _, dup := seen[b.ID]; dup {
				t.Fatalf("duplicate ID %q across page1/page2 (total=%d pageSize=%d)",
					b.ID, total, pageSize)
			}
			seen[b.ID] = struct{}{}
		}

		// Invariant 2: page1 ++ page2 covers the same ID set as
		// fullPage. The fullPage request asks for up to 2*pageSize, so
		// any IDs a caller could see in the stitched read are exactly
		// those they'd see in the single read.
		stitched := append(append([]database.BookCore(nil), page1...), page2...)
		gotIDs := bookCoreIDs(stitched)
		wantIDs := bookCoreIDs(fullPage)
		if len(gotIDs) != len(wantIDs) {
			t.Fatalf("stitched has %d ids, fullPage has %d (total=%d pageSize=%d)",
				len(gotIDs), len(wantIDs), total, pageSize)
		}
		for i := range gotIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Fatalf("id mismatch at %d: stitched=%q fullPage=%q (total=%d pageSize=%d)",
					i, gotIDs[i], wantIDs[i], total, pageSize)
			}
		}
	})
}
