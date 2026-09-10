// file: internal/database/batch_upsert_dedup_test.go
// version: 1.1.0
// guid: 0944459c-a178-4e09-a2a0-ba64568d7d43
// last-edited: 2026-09-10

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These two tests pin the within-batch duplicate gap described in TODO.md under
// "🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration".
//
// WHY THEY DO NOT USE sumStoredFileAggregates. That helper derives its expected
// totals from the STORED rows, so a duplicated row is summed on both sides of the
// comparison and the assertion still balances — it is named as a known blind spot
// in its own doc comment. Every expectation below is therefore an independent
// constant chosen by hand: one row, 600 seconds, 20,000,000 bytes. 600 is also
// safely below the millisecond threshold normalizeBookFileDuration (CONS-18)
// rewrites at, so the value that goes in is the value that comes out.

const (
	dedupTestDuration = 600
	dedupTestFileSize = int64(20_000_000)
)

// TestBatchUpsertBookFilesDedupesRowsSharingAFilePath covers TODO.md L4241.
//
// BEFORE: BatchUpsertBookFiles matched an existing row through GetBookFileByPath,
// a COMMITTED read. Row 2 of a batch cannot see row 1 sitting in the still
// uncommitted pebble.Batch, so two rows sharing a FilePath both missed the match,
// both took a fresh ULID and both landed under distinct book_file:<bookID>:<id>
// keys. The aggregate recompute then summed the duplicate into Book.Duration and
// Book.FileSize — 1200 seconds for a 600 second file.
//
// AFTER: the second row folds into the first. One stored row, un-doubled totals.
func TestBatchUpsertBookFilesDedupesRowsSharingAFilePath(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Dedup By Path", FilePath: "/lib/dedup/path"})
	require.NoError(t, err)

	const sharedPath = "/lib/dedup/path/track1.m4b"

	// Identical FilePath, deliberately different Title so the surviving row also
	// proves WHICH of the two won. The batch path is an upsert: it must behave as
	// if the rows had been handed to UpsertBookFile one at a time, and that is
	// last-wins on content.
	files := []*BookFile{
		{BookID: book.ID, FilePath: sharedPath, Title: "first observation", TrackNumber: 1,
			Duration: dedupTestDuration, FileSize: dedupTestFileSize},
		{BookID: book.ID, FilePath: sharedPath, Title: "second observation", TrackNumber: 1,
			Duration: dedupTestDuration, FileSize: dedupTestFileSize},
	}
	require.NoError(t, store.BatchUpsertBookFiles(files))

	stored, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1, "two rows sharing one FilePath must collapse to a single stored row")
	require.Equal(t, sharedPath, stored[0].FilePath)
	require.Equal(t, "second observation", stored[0].Title,
		"the later row wins on content, matching two sequential UpsertBookFile calls")

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration, "the batch recomputes aggregates")
	require.Equal(t, dedupTestDuration, *got.Duration,
		"Book.Duration must be the single file's duration, not the duplicated sum")
	require.NotNil(t, got.FileSize)
	require.Equal(t, dedupTestFileSize, *got.FileSize,
		"Book.FileSize must be the single file's size, not the duplicated sum")

	// Both caller-owned pointers describe the one surviving row, so a caller that
	// reads an ID back off either element of its own slice gets the stored row.
	require.Equal(t, files[0].ID, files[1].ID,
		"both input rows must end up naming the same stored row")
}

// TestBatchUpsertBookFilesDedupesRowsSharingAnITunesPID covers TODO.md L4242.
//
// The PID branch of BatchUpsertBookFiles matches through GetBookFileByPID, which
// has the identical committed-read gap as the FilePath branch immediately below
// it. TODO.md attributes this to enforceBookFilePIDUniqueness; that function no
// longer exists (stagePIDTransfer replaced it and BatchUpsertBookFiles does not
// call it), but the match lookup carries the same gap it named.
//
// THE TWO ROWS CARRY DIFFERENT FilePaths ON PURPOSE. Give them the same path and
// the FilePath dedup alone collapses them, and this test would pass with the PID
// branch entirely unwritten. Same PID + distinct paths is the only fixture that
// isolates the PID gap.
func TestBatchUpsertBookFilesDedupesRowsSharingAnITunesPID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Dedup By PID", FilePath: "/lib/dedup/pid"})
	require.NoError(t, err)

	const sharedPID = "A1B2C3D4E5F60718"

	files := []*BookFile{
		{BookID: book.ID, FilePath: "/lib/dedup/pid/old-name.m4b", ITunesPersistentID: sharedPID,
			Title: "first observation", TrackNumber: 1,
			Duration: dedupTestDuration, FileSize: dedupTestFileSize},
		{BookID: book.ID, FilePath: "/lib/dedup/pid/new-name.m4b", ITunesPersistentID: sharedPID,
			Title: "second observation", TrackNumber: 1,
			Duration: dedupTestDuration, FileSize: dedupTestFileSize},
	}
	require.NoError(t, store.BatchUpsertBookFiles(files))

	stored, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1, "a PID must identify exactly one row, within a batch as well as across commits")
	require.Equal(t, sharedPID, stored[0].ITunesPersistentID)
	require.Equal(t, "/lib/dedup/pid/new-name.m4b", stored[0].FilePath,
		"the later row wins on content, including the path the PID now points at")
	require.Equal(t, "second observation", stored[0].Title)

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.Equal(t, dedupTestDuration, *got.Duration,
		"Book.Duration must be the single file's duration, not the duplicated sum")
	require.NotNil(t, got.FileSize)
	require.Equal(t, dedupTestFileSize, *got.FileSize)

	// The PID index must resolve to the one surviving row.
	byPID, err := store.GetBookFileByPID(sharedPID)
	require.NoError(t, err)
	require.NotNil(t, byPID, "the PID index must still resolve after the merge")
	require.Equal(t, stored[0].ID, byPID.ID)
}

// TestBatchUpsertBookFilesChecksBothStagedMapsBeforeEitherCommittedLookup pins the
// ORDER of the four lookups, which is the load-bearing half of the fix and the
// part a later "harmonise these two branches" pass would silently undo.
//
// The fixture is the one arrangement that separates the two orders:
//
//	committed:  row R in another book, holding PID x
//	batch:      row1 { path: p }            — no PID
//	            row2 { path: p, PID: x }
//
// Correct order (both staged maps, THEN either committed read): row2 misses
// stagedByPID (row1 carries no PID), hits stagedByPath[p], and merges into row1.
// One row on path p.
//
// Interleaved order (stagedByPID, committed-by-PID, stagedByPath, committed-by-path):
// row2 misses stagedByPID, the committed PID lookup finds R, row2 adopts R's ID
// and BookID — and never consults stagedByPath at all. Row1 stays where it is and
// path p carries two rows again, in two different books.
func TestBatchUpsertBookFilesChecksBothStagedMapsBeforeEitherCommittedLookup(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	otherBook, err := store.CreateBook(&Book{Title: "PID Holder", FilePath: "/lib/dedup/order/other"})
	require.NoError(t, err)
	book, err := store.CreateBook(&Book{Title: "Dedup Order", FilePath: "/lib/dedup/order"})
	require.NoError(t, err)

	const sharedPID = "0F1E2D3C4B5A6978"
	const sharedPath = "/lib/dedup/order/track1.m4b"

	// COMMITTED holder of the PID, in a different book and on a different path.
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID: otherBook.ID, FilePath: "/lib/dedup/order/other/held.m4b",
		ITunesPersistentID: sharedPID, TrackNumber: 1,
		Duration: dedupTestDuration, FileSize: dedupTestFileSize,
	}))

	files := []*BookFile{
		{BookID: book.ID, FilePath: sharedPath, Title: "first observation", TrackNumber: 1,
			Duration: dedupTestDuration, FileSize: dedupTestFileSize},
		{BookID: book.ID, FilePath: sharedPath, ITunesPersistentID: sharedPID,
			Title: "second observation", TrackNumber: 1,
			Duration: dedupTestDuration, FileSize: dedupTestFileSize},
	}
	require.NoError(t, store.BatchUpsertBookFiles(files))

	stored, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1,
		"the staged path must be consulted before the committed PID lookup, or path %s ends up on two rows", sharedPath)
	require.Equal(t, sharedPath, stored[0].FilePath)

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.Equal(t, dedupTestDuration, *got.Duration)

	// The committed PID holder must be untouched. On the pre-fix code row2's
	// committed PID lookup retargeted it onto R, so R's stored row was rewritten
	// with row2's path and title and R's own file was lost from the record — which
	// is why this doubles as a regression test and not only an ordering guard.
	otherFiles, err := store.GetBookFiles(otherBook.ID)
	require.NoError(t, err)
	require.Len(t, otherFiles, 1)
	require.Equal(t, "/lib/dedup/order/other/held.m4b", otherFiles[0].FilePath,
		"the committed PID holder must not be rewritten by a row this batch merged elsewhere")
}
