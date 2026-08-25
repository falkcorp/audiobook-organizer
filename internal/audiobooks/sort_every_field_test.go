// file: internal/audiobooks/sort_every_field_test.go
// version: 1.0.0
// guid: 3f81c07a-6b24-4de9-9c5b-8a2f14d7e603
// last-edited: 2026-08-25

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// Every sort key must actually order books, end to end through GetAudiobooks.
//
// This exists because 13 of the 23 keys in bookSortComparators ordered NOTHING
// and every test in the package passed. The two that looked like sort coverage
// (TestProp_SortStability, TestProp_SortIsPermutation) call applySorting
// DIRECTLY on hand-built database.Book values with Author/Series populated, so
// they never traverse bookSummariesToBooks -- and both properties they assert,
// stability and permutation, hold perfectly when every comparator returns "".
// A test can only see this defect if it goes through the real entry point and
// asserts a specific ORDER.
//
// See docs/audits/2026-08-25-author-series-sort-degenerate.md.

// sortmx* names are task-unique per repo convention for package-shared helpers.

// sortmxRank is the value rank a seeded book carries: 0 must sort FIRST
// ascending, 2 last.
const sortmxBooks = 3

// sortmxSetters assigns the field under test so that rank 0 < rank 1 < rank 2.
// Every key returned by database.SortableBookFields must appear here; see
// TestEverySortKeyIsCovered.
var sortmxSetters = map[string]func(b *database.Book, rank int){
	"title":            func(b *database.Book, r int) { b.Title = fmt.Sprintf("%d-title", r) },
	"narrator":         func(b *database.Book, r int) { b.Narrator = sortmxStr("%d-narr", r) },
	"year":             func(b *database.Book, r int) { b.AudiobookReleaseYear = sortmxInt(2000 + r) },
	"duration":         func(b *database.Book, r int) { b.Duration = sortmxInt(100 * (r + 1)) },
	"duration_seconds": func(b *database.Book, r int) { b.Duration = sortmxInt(100 * (r + 1)) },
	"bitrate":          func(b *database.Book, r int) { b.Bitrate = sortmxInt(64 * (r + 1)) },
	"bitrate_kbps":     func(b *database.Book, r int) { b.Bitrate = sortmxInt(64 * (r + 1)) },
	"file_size":        func(b *database.Book, r int) { v := int64(1000 * (r + 1)); b.FileSize = &v },
	"file_size_bytes":  func(b *database.Book, r int) { v := int64(1000 * (r + 1)); b.FileSize = &v },
	"genre":            func(b *database.Book, r int) { b.Genre = sortmxStr("%d-genre", r) },
	"language":         func(b *database.Book, r int) { b.Language = sortmxStr("%d-lang", r) },
	"publisher":        func(b *database.Book, r int) { b.Publisher = sortmxStr("%d-pub", r) },
	"codec":            func(b *database.Book, r int) { b.Codec = sortmxStr("%d-codec", r) },
	"quality":          func(b *database.Book, r int) { b.Quality = sortmxStr("%d-qual", r) },
	"edition":          func(b *database.Book, r int) { b.Edition = sortmxStr("%d-ed", r) },
	"library_state":    func(b *database.Book, r int) { b.LibraryState = sortmxStr("%d-state", r) },
	"format":           func(b *database.Book, r int) { b.Format = fmt.Sprintf("%d-fmt", r) },
	"sample_rate":      func(b *database.Book, r int) { b.SampleRate = sortmxInt(8000 * (r + 1)) },
	"sample_rate_hz":   func(b *database.Book, r int) { b.SampleRate = sortmxInt(8000 * (r + 1)) },
	// author and series are seeded as real rows, not as a pre-filled pointer:
	// stripBookForMemdb nils Book.Author/Book.Series, so the order comes from
	// resolving AuthorID/SeriesID against the authors/series tables. Seeding
	// the pointer instead would test a shape production never stores.
	"author": nil,
	"series": nil,
	// created_at/updated_at are stamped by the store on write, so the fixture
	// cannot choose their values. They are covered by construction below:
	// books are inserted in rank order 1,2,0, so ascending created_at is the
	// INSERTION order, which is exactly the degenerate signature -- an
	// assertion there could not tell a working sort from a broken one. They
	// are asserted only for agreement between ascending and descending.
	"created_at": nil,
	"updated_at": nil,
}

func sortmxStr(f string, r int) *string { s := fmt.Sprintf(f, r); return &s }
func sortmxInt(v int) *int              { return &v }

// TestEverySortKeyIsCovered is the ratchet: a comparator added to
// bookSortComparators without a fixture here fails immediately, rather than
// shipping a key that silently orders nothing.
func TestEverySortKeyIsCovered(t *testing.T) {
	for _, field := range database.SortableBookFields() {
		if _, ok := sortmxSetters[field]; !ok {
			t.Errorf("sort key %q has no fixture in sortmxSetters: add one so the "+
				"key is proven to order books, or it can ship ordering nothing", field)
		}
	}
}

// sortmxSeed inserts sortmxBooks books for one sort key.
//
// Insertion order is 1,2,0 deliberately. It makes the three outcomes distinct
// strings, so a failure says which one happened:
//
//	correct ascending   -> rank0 rank1 rank2
//	correct descending  -> rank2 rank1 rank0
//	no ordering applied -> rank1 rank2 rank0  (insertion order)
//
// Seeding 2,1,0 instead -- the obvious choice -- makes "no ordering applied"
// and "correct descending" the SAME string, and the ascending assertion then
// passes against a fixture that cannot distinguish them.
func sortmxSeed(t *testing.T, ps *database.PebbleStore, key string) map[string]string {
	t.Helper()
	marks := make(map[string]string, sortmxBooks)
	for _, rank := range []int{1, 2, 0} {
		primary := true
		b := &database.Book{
			Title:            fmt.Sprintf("ins%d", rank),
			FilePath:         fmt.Sprintf("/tmp/sortmx_%s_%d.m4b", key, rank),
			IsPrimaryVersion: &primary,
		}
		switch key {
		case "author":
			a, err := ps.CreateAuthor(fmt.Sprintf("%d-author", rank))
			require.NoError(t, err)
			b.AuthorID = &a.ID
		case "series":
			s, err := ps.CreateSeries(fmt.Sprintf("%d-series", rank), nil)
			require.NoError(t, err)
			b.SeriesID = &s.ID
		default:
			if set := sortmxSetters[key]; set != nil {
				set(b, rank)
			}
			// Keep the title neutral so it cannot stand in for the field
			// under test.
			b.Title = fmt.Sprintf("ins%d", rank)
		}
		created, err := ps.CreateBook(b)
		require.NoErrorf(t, err, "seed %s rank %d", key, rank)
		marks[created.ID] = fmt.Sprintf("rank%d", rank)
	}
	ps.WaitForWarmup()
	return marks
}

func sortmxOrder(marks map[string]string, books []database.Book) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, marks[b.ID])
	}
	return out
}

func sortmxStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	return ps
}

// TestEverySortKeyOrdersBooks is the regression proper: for each key, a full
// page must come back in value order and not in insertion order.
func TestEverySortKeyOrdersBooks(t *testing.T) {
	for _, field := range database.SortableBookFields() {
		if field == "created_at" || field == "updated_at" {
			continue // store-stamped; see sortmxSetters
		}
		t.Run(field, func(t *testing.T) {
			ps := sortmxStore(t)
			marks := sortmxSeed(t, ps, field)
			svc := NewAudiobookService(ps)

			asc, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
				ListFilters{SortBy: field, SortOrder: "asc"})
			require.NoError(t, err)
			require.Len(t, asc, sortmxBooks)
			require.Equal(t, []string{"rank0", "rank1", "rank2"}, sortmxOrder(marks, asc),
				"ascending sort_by=%s; [rank1 rank2 rank0] means no ordering was applied at all", field)

			desc, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
				ListFilters{SortBy: field, SortOrder: "desc"})
			require.NoError(t, err)
			require.Equal(t, []string{"rank2", "rank1", "rank0"}, sortmxOrder(marks, desc),
				"descending sort_by=%s", field)
		})
	}
}

// TestEverySortKeyPaginatesTheOrderedSet covers the half a full page cannot
// see: the page must be a window into the SORTED set, not a window into the
// walk that is sorted afterwards. Those differ only when offset > 0, which is
// why every assertion here uses one.
func TestEverySortKeyPaginatesTheOrderedSet(t *testing.T) {
	for _, field := range database.SortableBookFields() {
		if field == "created_at" || field == "updated_at" {
			continue
		}
		t.Run(field, func(t *testing.T) {
			ps := sortmxStore(t)
			marks := sortmxSeed(t, ps, field)
			svc := NewAudiobookService(ps)

			// Each single-row page must be the correct row of the ordered set,
			// and the three pages together must partition it in order.
			var seen []string
			for offset := 0; offset < sortmxBooks; offset++ {
				page, err := svc.GetAudiobooks(context.Background(), 1, offset, "", nil, nil,
					ListFilters{SortBy: field, SortOrder: "asc"})
				require.NoErrorf(t, err, "offset %d", offset)
				require.Lenf(t, page, 1, "sort_by=%s offset=%d must return exactly one row", field, offset)
				got := sortmxOrder(marks, page)[0]
				require.Equalf(t, fmt.Sprintf("rank%d", offset), got,
					"sort_by=%s limit=1 offset=%d must be row %d of the ORDERED set", field, offset, offset)
				seen = append(seen, got)
			}
			require.Equal(t, []string{"rank0", "rank1", "rank2"}, seen)

			// Offset past the end is empty, not a wrapped or clamped page.
			empty, err := svc.GetAudiobooks(context.Background(), 10, sortmxBooks+5, "", nil, nil,
				ListFilters{SortBy: field, SortOrder: "asc"})
			require.NoError(t, err)
			require.Emptyf(t, empty, "sort_by=%s past-the-end offset must be empty", field)
		})
	}
}
