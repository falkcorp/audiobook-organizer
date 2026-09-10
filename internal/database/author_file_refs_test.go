// file: internal/database/author_file_refs_test.go
// version: 1.0.0
// guid: 4d889ab5-fb0f-4d3a-af64-09f66cdaa453
// last-edited: 2026-09-10

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests exist because a DISPLAY counter was used as a SAFETY signal.
// GetAllAuthorFileCounts scans the primary-version index only, skips
// soft-deleted books, and maps books to authors through the legacy
// Book.AuthorID field alone — so it reports an unconditional zero for three
// populations whose files are real, and purge-empty-authors' require_zero_files
// gate read exactly that number.
//
// Every fixture below is built so the two counters DISAGREE. A fixture where
// they agree would pass with or without the fix.

// seedAuthorFileRefFixture builds one store holding all three missed
// populations plus a healthy control, and returns the author IDs.
//
// The control is not decoration: it proves the divergence asserted below is
// about book STATE and attachment ROUTE, not about arithmetic.
func seedAuthorFileRefFixture(t *testing.T) (*PebbleStore, map[string]int) {
	t.Helper()
	store := seedAuthorRefStore(t, t.TempDir())

	ids := map[string]int{
		"healthy":    9200, // legacy author of a live primary book, 2 files
		"junction":   9201, // co-author credited ONLY through the junction
		"trashed":    9202, // legacy author whose only book is soft-deleted
		"nonprimary": 9203, // legacy author whose only book is a secondary version
	}

	live := mkAuthorRefBook(t, store, "filerefs-live", ids["healthy"], true, false)
	require.NoError(t, store.SetBookAuthors(live.ID, []BookAuthor{
		{BookID: live.ID, AuthorID: ids["healthy"], Role: "author", Position: 0},
		{BookID: live.ID, AuthorID: ids["junction"], Role: "author", Position: 1},
	}))
	addAuthorFileRefFiles(t, store, live.ID, 2)

	trashed := mkAuthorRefBook(t, store, "filerefs-trashed", ids["trashed"], true, true)
	addAuthorFileRefFiles(t, store, trashed.ID, 3)

	secondary := mkAuthorRefBook(t, store, "filerefs-secondary", ids["nonprimary"], false, false)
	addAuthorFileRefFiles(t, store, secondary.ID, 4)

	return store, ids
}

func addAuthorFileRefFiles(t *testing.T, s *PebbleStore, bookID string, n int) {
	t.Helper()
	for i := range n {
		require.NoError(t, s.CreateBookFile(&BookFile{
			BookID:   bookID,
			FilePath: "/filerefs/" + bookID + "/" + string(rune('a'+i)) + ".m4b",
			Duration: 60,
			FileSize: 1_000,
		}))
	}
}

// TestGetAllAuthorFileRefCounts_SeesAllThreeMissedPopulations is THE defect:
// each of these authors has files on disk and the display counter says zero.
func TestGetAllAuthorFileRefCounts_SeesAllThreeMissedPopulations(t *testing.T) {
	store, ids := seedAuthorFileRefFixture(t)

	// PRECONDITION, not decoration: the old instrument's actual answers. This is
	// the pre-fix behaviour the gate was reading.
	display, err := store.GetAllAuthorFileCounts()
	require.NoError(t, err)
	require.Zero(t, display[ids["junction"]],
		"precondition: the display counter maps books via the legacy AuthorID only, so a junction-only co-author reads 0")
	require.Zero(t, display[ids["trashed"]],
		"precondition: the display counter skips soft-deleted books")
	require.Zero(t, display[ids["nonprimary"]],
		"precondition: the display counter scans the primary-version index only")
	require.Equal(t, 2, display[ids["healthy"]],
		"precondition: the counters agree on the healthy author, so the divergence above is about state and route, not arithmetic")

	counts, err := store.GetAllAuthorFileRefCounts()
	require.NoError(t, err)
	require.Equal(t, 2, counts[ids["junction"]],
		"a junction-only co-author's book has 2 files; reporting 0 clears the file-safety gate for an author whose files are on disk")
	require.Equal(t, 3, counts[ids["trashed"]],
		"an author whose only book is in the trash still has that book's 3 files")
	require.Equal(t, 4, counts[ids["nonprimary"]],
		"a non-primary version still holds 4 files")
	require.Equal(t, 2, counts[ids["healthy"]])
}

// TestGetAllAuthorFileRefCounts_NoMinOneFudge pins the semantic difference from
// the display counter, which scores a book with no files as 1. This counter
// counts FILES, so an author whose only book is empty is absent from the map.
// require_zero_files and ZeroBooksWithFiles are file-shaped names and must
// report file-shaped numbers.
func TestGetAllAuthorFileRefCounts_NoMinOneFudge(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	const fileless = 9210
	mkAuthorRefBook(t, store, "filerefs-fileless", fileless, true, false)

	counts, err := store.GetAllAuthorFileRefCounts()
	require.NoError(t, err)
	require.Zero(t, counts[fileless], "a book with no files contributes no files")

	display, err := store.GetAllAuthorFileCounts()
	require.NoError(t, err)
	require.Equal(t, 1, display[fileless],
		"precondition: the display counter's min-1 fudge is what makes it unusable as a FILE count")
}

// TestGetAllAuthorFileRefCounts_FailsClosedOnShortMemdb is the DELIBERATE
// DIVERGENCE from GetAllAuthorBookRefCounts, which falls through to Pebble on
// the same taint. Falling through here would be fail-OPEN, because
// GetBookFilesForIDsCore itself delegates to the memdb whenever the memdb is
// warm: the books would come from Pebble and the files from the very projection
// just refused, undercounting to "no files".
func TestGetAllAuthorFileRefCounts_FailsClosedOnShortMemdb(t *testing.T) {
	for _, table := range []string{memTableBookFiles, memTableBookAuthors, memTableBooks} {
		t.Run(table, func(t *testing.T) {
			store, ids := seedAuthorFileRefFixture(t)

			// Warm and correct first, so the refusal below is attributable to the
			// taint and not to an empty store.
			before, err := store.GetAllAuthorFileRefCounts()
			require.NoError(t, err)
			require.Equal(t, 3, before[ids["trashed"]])

			store.mem().recordLostRows(table, 1)

			counts, err := store.GetAllAuthorFileRefCounts()
			require.ErrorIs(t, err, ErrMemdbIncomplete,
				"a short %s must refuse rather than answer, and must NOT fall through to a Pebble scan that reads its files back out of the same short memdb", table)
			require.Nil(t, counts, "a refusal must not also hand back a partial count")
		})
	}
}

// TestAuthorFileRefCounts_FailsClosedWithoutTheCapability covers the resolution
// path: a store that cannot answer the unfiltered question must produce an
// error, never a silent fallback to the filtered display counter.
func TestAuthorFileRefCounts_FailsClosedWithoutTheCapability(t *testing.T) {
	counts, err := AuthorFileRefCounts(struct{ NotAStore bool }{})
	require.Error(t, err)
	require.Nil(t, counts)
	require.Nil(t, AsAuthorFileRefStore(nil))
}
