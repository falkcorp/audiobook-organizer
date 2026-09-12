// file: internal/database/book_row_iter_test.go
// version: 1.0.0
// guid: 015f80b3-e8e2-4b00-a6a5-629db0835655
// last-edited: 2026-09-12

package database

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin book_row_iter.go's rule on every Pebble scan routed through
// it: the full ["book:", "book;") range, and only bare book:<id> rows.
//
// Until 2026-09-12 about thirty scans used ["book:0", "book:;"), which admits
// only '0'-'9' and ':' as the first ID byte. Each fixture ID below except the
// digit one sorts ABOVE ';' and was invisible to those scans. The seek-edge IDs
// ("asin", "path0", "hash;x") sit immediately beside an index subtree the
// iterator jumps over with SeekGE, so an off-by-one in the skip loses them.

// bookRowRangeLiveIDs are the live (non-trashed) fixture books.
var bookRowRangeLiveIDs = []string{
	"01DIGIT", // digit: the only shape the old bound admitted
	"Upper",   // uppercase letter
	"lower",   // lowercase letter
	"_under",  // '_' (0x5F), as in the seed data's seed_<ULID>
	"~tilde",  // '~' (0x7E): above the book:~ bound #3327 used
	"asin",    // sorts immediately BEFORE the book:asin: index subtree
	"path0",   // '0' < ':' so this sorts before book:path:
	"hash;x",  // sorts immediately AFTER the book:hash; seek target
}

// bookRowRangeTrashedIDs are soft-deleted fixture books, one per side of ';'.
var bookRowRangeTrashedIDs = []string{"0trash", "zTrash"}

type bookRowRangeFixture struct {
	live     []string // byte-sorted
	trashed  []string // byte-sorted
	seriesID int
}

func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// buildBookRowRangeFixture writes the fixture books with every index family a
// book can carry (path, hash, asin, versiongroup), book files, a series, raw
// junk keys that are not book rows, and then asserts the index keys exist so
// the "no index key leaks" assertions below are not vacuous.
func buildBookRowRangeFixture(t *testing.T, p *PebbleStore, upsertMem bool) bookRowRangeFixture {
	t.Helper()
	ctx := context.Background()

	series, err := p.CreateSeries("Book Row Range Series", nil)
	require.NoError(t, err)

	str := func(s string) *string { return &s }
	yes := true
	create := func(id string, trashed bool) {
		b := &Book{
			ID:                 id,
			Title:              "RangeProbe " + id,
			FilePath:           "/lib/range/" + id + ".m4b",
			FileHash:           str("fh-" + id),
			ASIN:               str("B0RANGE" + id),
			VersionGroupID:     str("vg-range"),
			MetadataSourceHash: str("msh-range"),
			SeriesID:           &series.ID,
		}
		if trashed {
			b.MarkedForDeletion = &yes
		}
		created, err := p.CreateBook(b)
		require.NoError(t, err)
		require.Equal(t, id, created.ID, "CreateBook must keep a caller-supplied ID")
		if upsertMem {
			p.UpsertBookToMemDB(ctx, created)
		}
	}
	for _, id := range bookRowRangeLiveIDs {
		create(id, false)
	}
	for _, id := range bookRowRangeTrashedIDs {
		create(id, true)
	}

	// "lower" owns three files, so GetAllSeriesFileCounts can tell a book whose
	// files were found (3) from one whose files were missed (counts as 1).
	for _, fid := range []string{"f1", "f2", "f3"} {
		require.NoError(t, p.CreateBookFile(&BookFile{
			ID: fid, BookID: "lower", FilePath: "/lib/range/lower-" + fid + ".mp3",
		}))
	}

	// Keys inside the book range that are not book rows, with values that do
	// not decode as a Book: a per-book sub-key and the bare prefix itself.
	// Several scans fail the whole scan on a decode error, so a leak is loud.
	require.NoError(t, p.db.Set([]byte("book:lower:sidecar"), []byte("not json"), nil))
	require.NoError(t, p.db.Set([]byte("book:"), []byte("not json"), nil))

	// Non-vacuity: the index families the iterator must skip really exist.
	raw := map[string]bool{}
	it, err := p.db.NewIter(nil)
	require.NoError(t, err)
	for it.SeekGE([]byte("book:")); it.Valid() && string(it.Key()) < "book;"; it.Next() {
		raw[string(it.Key())] = true
	}
	require.NoError(t, it.Close())
	for _, family := range []string{"book:asin:", "book:path:", "book:hash:", "book:versiongroup:"} {
		found := false
		for k := range raw {
			if strings.HasPrefix(k, family) {
				found = true
				break
			}
		}
		require.True(t, found, "fixture must create %s index keys", family)
	}
	require.True(t, raw["book:lower:sidecar"])

	return bookRowRangeFixture{
		live:     sortedCopy(bookRowRangeLiveIDs),
		trashed:  sortedCopy(bookRowRangeTrashedIDs),
		seriesID: series.ID,
	}
}

func bookRowIDs(books []Book) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	return ids
}

// requireSameIDSet asserts got holds exactly want, each once, in any order.
func requireSameIDSet(t *testing.T, want, got []string, what string) {
	t.Helper()
	require.Equal(t, want, sortedCopy(got), "%s must return every book exactly once and no index key", what)
}

func TestBookRowScans_FullKeyRange_PebblePath(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	p.UseMemDB = false // every getter below takes its Pebble branch
	fx := buildBookRowRangeFixture(t, p, false)

	cases := []struct {
		name string
		want []string
		run  func(t *testing.T) []string
	}{
		{"getAllBooksCoreFromPebble", fx.live, func(t *testing.T) []string {
			books, err := p.getAllBooksCoreFromPebble(0, 0)
			require.NoError(t, err)
			return bookCoreIDsOf(books)
		}},
		{"GetAllBooksCore", fx.live, func(t *testing.T) []string {
			books, err := p.GetAllBooksCore(0, 0)
			require.NoError(t, err)
			return bookCoreIDsOf(books)
		}},
		{"GetAllBooksCoreComplete", fx.live, func(t *testing.T) []string {
			books, err := p.GetAllBooksCoreComplete(0, 0)
			require.NoError(t, err)
			return bookCoreIDsOf(books)
		}},
		{"ListBookIDs", fx.live, func(t *testing.T) []string {
			ids, err := p.ListBookIDs()
			require.NoError(t, err)
			require.Equal(t, fx.live, ids, "ListBookIDs must be in plain byte order")
			return ids
		}},
		{"GetAllBooksFullFrom/unpaged", fx.live, func(t *testing.T) []string {
			books, err := p.GetAllBooksFullFrom("", 0)
			require.NoError(t, err)
			return bookRowIDs(books)
		}},
		{"GetAllBooksFullFrom/paged", fx.live, func(t *testing.T) []string {
			var all []string
			cursor := ""
			for {
				page, err := p.GetAllBooksFullFrom(cursor, 3)
				require.NoError(t, err)
				all = append(all, bookRowIDs(page)...)
				if len(page) < 3 {
					return all
				}
				cursor = page[len(page)-1].ID
			}
		}},
		{"GetAllBooksFullFrom/absent-cursor", []string{"Upper", "_under", "asin", "hash;x", "lower", "path0", "~tilde"}, func(t *testing.T) []string {
			// "1" is not a book; it sorts after "01DIGIT" and before "Upper".
			books, err := p.GetAllBooksFullFrom("1", 0)
			require.NoError(t, err)
			return bookRowIDs(books)
		}},
		{"walkFilteredBooksPebble", fx.live, func(t *testing.T) []string {
			var ids []string
			require.NoError(t, p.walkFilteredBooksPebble(BookSummaryFilter{}, func(b *Book) bool {
				ids = append(ids, b.ID)
				return true
			}))
			return ids
		}},
		{"GetBooksByMetadataSourceHash", fx.live, func(t *testing.T) []string {
			books, err := p.GetBooksByMetadataSourceHash("msh-range")
			require.NoError(t, err)
			return bookRowIDs(books)
		}},
		{"ListSoftDeletedBooks", fx.trashed, func(t *testing.T) []string {
			books, err := p.ListSoftDeletedBooks(0, 0, nil)
			require.NoError(t, err)
			return bookRowIDs(books)
		}},
		{"getAllBooksPebbleScan", fx.live, func(t *testing.T) []string {
			books, err := p.getAllBooksPebbleScan()
			require.NoError(t, err)
			return bookRowIDs(books)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireSameIDSet(t, tc.want, tc.run(t), tc.name)
		})
	}

	t.Run("SearchBooks", func(t *testing.T) {
		books, err := p.SearchBooks("rangeprobe", 0, 0)
		require.NoError(t, err)
		got := map[string]int{}
		for _, b := range books {
			got[b.ID]++
		}
		for _, id := range fx.live {
			require.Equal(t, 1, got[id], "SearchBooks must return %q exactly once", id)
		}
		for id := range got {
			require.Contains(t, append(append([]string(nil), fx.live...), fx.trashed...), id, "SearchBooks returned a non-book key")
		}
	})

	t.Run("counts", func(t *testing.T) {
		n, err := p.CountAllBooks()
		require.NoError(t, err)
		require.Equal(t, len(fx.live), n, "CountAllBooks")

		n, err = p.CountPrimaryBooks()
		require.NoError(t, err)
		require.Equal(t, len(fx.live), n, "CountPrimaryBooks")

		n, err = p.CountSoftDeletedBooks(nil)
		require.NoError(t, err)
		require.Equal(t, len(fx.trashed), n, "CountSoftDeletedBooks")

		books, err := p.GetAllSeriesBookCounts_Pebble()
		require.NoError(t, err)
		require.Equal(t, len(fx.live), books[fx.seriesID], "GetAllSeriesBookCounts_Pebble")

		// 7 fileless live books count 1 each, plus "lower"'s 3 files. The
		// book_file scan used ["book_file:0", "book_file:;") too, so a missed
		// file set reads as 1 and this would be 8.
		files, err := p.GetAllSeriesFileCounts()
		require.NoError(t, err)
		require.Equal(t, len(fx.live)-1+3, files[fx.seriesID], "GetAllSeriesFileCounts")
	})
}

// TestGetAllWorks_FullKeyRange pins the same fix on the work: family, whose
// CreateWork also keeps a caller-supplied ID and which has a work:title: index.
func TestGetAllWorks_FullKeyRange(t *testing.T) {
	p := setupTestPebbleStore(t)
	ids := []string{"0w", "Wk", "_w", "wk", "~w"}
	for _, id := range ids {
		_, err := p.CreateWork(&Work{ID: id, Title: "Work " + id})
		require.NoError(t, err)
	}
	works, err := p.GetAllWorks()
	require.NoError(t, err)
	got := make([]string, 0, len(works))
	for _, w := range works {
		got = append(got, w.ID)
	}
	require.Equal(t, sortedCopy(ids), sortedCopy(got), "GetAllWorks must return every work once and no work:title: key")
}

// TestOrphanMembership_LetterLeadingBooksSurviveDegradedMemdb is the orphan
// book_file sweep's real seam. findOrphanBookFiles hard-deletes every file row
// whose BookID is absent from GetAllBooksCoreComplete ∪ ListSoftDeletedBooks.
// When memdb has recorded a lost row both getters fall through to their Pebble
// scans, and those scans used ["book:0", "book:;"): every letter-leading book
// dropped out of the set and its file rows became "orphans".
func TestOrphanMembership_LetterLeadingBooksSurviveDegradedMemdb(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be published or the UseMemDB=true arm silently runs the Pebble path")
	fx := buildBookRowRangeFixture(t, p, true)

	// Drop a letter-leading live book from memdb and record the loss, so memdb
	// is both short and flagged; only the Pebble fall-through can answer.
	degradeOrphanMemdb(t, p, "lower")

	live, err := p.GetAllBooksCoreComplete(0, 0)
	require.NoError(t, err)
	requireSameIDSet(t, fx.live, bookCoreIDsOf(live), "GetAllBooksCoreComplete (degraded memdb)")

	trashed, err := p.ListSoftDeletedBooks(0, 0, nil)
	require.NoError(t, err)
	requireSameIDSet(t, fx.trashed, bookRowIDs(trashed), "ListSoftDeletedBooks (degraded memdb)")
}
