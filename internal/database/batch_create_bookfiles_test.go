// file: internal/database/batch_create_bookfiles_test.go
// version: 1.1.0
// guid: 5c81f0a3-6b47-4d29-9e15-7a3f82c6b04d
// last-edited: 2026-08-24

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBatchCreateBookFilesRecomputesOncePerBook is the reason this method exists.
//
// The relink op created a book's files in a loop, one CreateBookFile per row, and
// each of those recomputed the book's aggregates — which re-reads the book's
// ENTIRE file set. An N-file book therefore cost 1+2+...+N reads. Production log
// attribution measured that single loop at 92.1% of all attributed recomputes.
//
// Counted as invocations rather than writes deliberately: only the first recompute
// of a run finds anything to change, so a per-row regression would still show
// exactly one "updated" line. See countAggregateInvocations.
func TestBatchCreateBookFilesRecomputesOncePerBook(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	bookA, err := store.CreateBook(&Book{Title: "Batch Create A", FilePath: "/lib/bc/a"})
	require.NoError(t, err)
	bookB, err := store.CreateBook(&Book{Title: "Batch Create B", FilePath: "/lib/bc/b"})
	require.NoError(t, err)
	require.Nil(t, bookA.Duration, "a freshly created book has no aggregate yet")

	readLogs := captureAggregateLogs(t)

	files := []*BookFile{}
	for i := range 5 {
		files = append(files, &BookFile{
			BookID:      bookA.ID,
			FilePath:    "/lib/bc/a/t" + string(rune('1'+i)) + ".m4b",
			Duration:    600 + i,
			FileSize:    int64(1_000_000 + i),
			TrackNumber: i + 1,
		})
	}
	for i := range 3 {
		files = append(files, &BookFile{
			BookID:      bookB.ID,
			FilePath:    "/lib/bc/b/t" + string(rune('1'+i)) + ".m4b",
			Duration:    900 + i,
			FileSize:    int64(2_000_000 + i),
			TrackNumber: i + 1,
		})
	}

	require.NoError(t, store.BatchCreateBookFiles(files))

	logs := readLogs()
	require.Equal(t, 1, countAggregateInvocations(logs, bookA.ID),
		"5 rows for one book must cost ONE recompute, not five — this is the O(N^2) guard")
	require.Equal(t, 1, countAggregateInvocations(logs, bookB.ID),
		"3 rows for one book must cost ONE recompute, not three")

	// Every row was stored, and the parent's totals equal the sum of its children.
	for _, tc := range []struct {
		id        string
		wantFiles int
	}{{bookA.ID, 5}, {bookB.ID, 3}} {
		stored, err := store.GetBookFiles(tc.id)
		require.NoError(t, err)
		require.Len(t, stored, tc.wantFiles, "every submitted row must be created")

		wantDur, wantSize := sumStoredFileAggregates(t, store, tc.id)
		require.Positive(t, wantDur, "fixture must produce a non-zero duration or the assertion is vacuous")

		b, err := store.GetBookByID(tc.id)
		require.NoError(t, err)
		require.NotNil(t, b.Duration)
		require.Equal(t, wantDur, *b.Duration)
		require.NotNil(t, b.FileSize)
		require.Equal(t, wantSize, *b.FileSize)
	}
}

// TestBatchCreateBookFilesAlwaysCreates pins the semantic difference from
// BatchUpsertBookFiles, which is the one way this method can be misused.
//
// BatchUpsert matches an existing row by path or iTunes PID and updates it.
// BatchCreate does not look: every row is new. A caller that may re-run over rows
// that already exist must check first — relinkOne re-reads GetBookFiles under the
// write path and returns early if the book owns anything. Get that wrong and this
// method produces duplicate rows rather than updates, so the behaviour is written
// down here rather than left to the doc comment.
func TestBatchCreateBookFilesAlwaysCreates(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Always Creates", FilePath: "/lib/ac"})
	require.NoError(t, err)

	const samePath = "/lib/ac/t1.m4b"
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:   book.ID,
		FilePath: samePath,
		Duration: 100,
		FileSize: 1_000,
	}))

	require.NoError(t, store.BatchCreateBookFiles([]*BookFile{{
		BookID:   book.ID,
		FilePath: samePath,
		Duration: 250,
		FileSize: 2_000,
	}}))

	stored, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, stored, 2,
		"BatchCreateBookFiles must CREATE, not match-and-update — if this reads 1 it has "+
			"acquired upsert semantics and every caller's duplicate-avoidance assumption is wrong")
}

// TestBatchCreateBookFilesRejectsDuplicatePIDInOneBatch guards an invariant the
// per-row path cannot guard on its own.
//
// stagePIDTransfer resolves the prior owner through a COMMITTED read,
// so it cannot see a row staged earlier in the same uncommitted batch. Two rows
// sharing a PID would both find no prior owner, both pass, and both be written —
// silently producing exactly the duplicate PIDs that function exists to prevent.
// Refusing the batch is the loud alternative.
func TestBatchCreateBookFilesRejectsDuplicatePIDInOneBatch(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Dup PID", FilePath: "/lib/dp"})
	require.NoError(t, err)

	err = store.BatchCreateBookFiles([]*BookFile{
		{BookID: book.ID, FilePath: "/lib/dp/t1.m4b", ITunesPersistentID: "PID-SAME", Duration: 100, FileSize: 1_000},
		{BookID: book.ID, FilePath: "/lib/dp/t2.m4b", ITunesPersistentID: "PID-SAME", Duration: 200, FileSize: 2_000},
	})
	require.Error(t, err, "two rows sharing an iTunes PID in one batch must be refused")
	require.Contains(t, err.Error(), "PID-SAME")

	// The refusal must leave no ROWS behind. Note what this does NOT show: this
	// fixture has no committed prior owner for PID-SAME, so stagePIDTransfer
	// early-returns at `prior == nil` and no transfer is ever staged. The batch is
	// trivially clean here, and this assertion passed even when a batch that DID
	// transfer a PID left the prior owner stripped. That case is
	// TestBatchCreateBookFilesRefusalDoesNotStripAPriorPIDOwner below; do not read
	// this test as covering it.
	stored, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Empty(t, stored, "a refused batch must write no rows at all")
}

// TestBatchCreateBookFilesRefusalDoesNotStripAPriorPIDOwner is the atomicity test
// that the fixture above cannot be.
//
// The PID transfer used to run through ClearITunesPID -> UpdateBookFile, which
// COMMITS A BATCH OF ITS OWN. So a batch that transferred a PID on row 1 and then
// failed on row 3 discarded its own writes while row 1's transfer stayed
// committed: the prior owner had lost the PID, and the row that was supposed to
// take it was never stored. The PID belonged to nobody, and no error said so.
//
// The shape below is the cheapest way to reach "succeed at a transfer, then fail":
// row 1 legitimately takes PID-MOVE from a committed owner, and rows 2 and 3 trip
// the in-batch duplicate check. Everything must roll back, transfer included.
//
// MUTATION CHECK: point stagePIDTransfer's prior-owner rewrite back at
// s.ClearITunesPID and this fails on the ownerAfter assertion while every other
// test in this file stays green.
func TestBatchCreateBookFilesRefusalDoesNotStripAPriorPIDOwner(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	priorBook, err := store.CreateBook(&Book{Title: "Prior Owner", FilePath: "/lib/prior"})
	require.NoError(t, err)
	priorRow := &BookFile{
		BookID: priorBook.ID, FilePath: "/lib/prior/a.m4b",
		ITunesPersistentID: "PID-MOVE", ITunesPath: "/itunes/a.m4b",
		Duration: 300, FileSize: 3_000,
	}
	require.NoError(t, store.CreateBookFile(priorRow))

	owner, err := store.GetBookFileByPID("PID-MOVE")
	require.NoError(t, err)
	require.NotNil(t, owner, "precondition: PID-MOVE must start out owned")
	require.Equal(t, priorRow.ID, owner.ID, "precondition: owned by the row we just made")

	newBook, err := store.CreateBook(&Book{Title: "Taker", FilePath: "/lib/taker"})
	require.NoError(t, err)

	err = store.BatchCreateBookFiles([]*BookFile{
		// Row 1 transfers PID-MOVE off priorRow — this is the work that used to
		// commit on its own.
		{BookID: newBook.ID, FilePath: "/lib/taker/t1.m4b", ITunesPersistentID: "PID-MOVE", Duration: 100, FileSize: 1_000},
		// Rows 2 and 3 collide, aborting the batch AFTER that transfer.
		{BookID: newBook.ID, FilePath: "/lib/taker/t2.m4b", ITunesPersistentID: "PID-DUP", Duration: 100, FileSize: 1_000},
		{BookID: newBook.ID, FilePath: "/lib/taker/t3.m4b", ITunesPersistentID: "PID-DUP", Duration: 100, FileSize: 1_000},
	})
	require.Error(t, err, "the duplicate PID-DUP rows must be refused")
	require.Contains(t, err.Error(), "PID-DUP")

	// THE ASSERTION. A refused batch must not have moved the PID.
	ownerAfter, err := store.GetBookFileByPID("PID-MOVE")
	require.NoError(t, err)
	require.NotNil(t, ownerAfter,
		"PID-MOVE is owned by nobody: the refused batch stripped it from the prior "+
			"owner and never stored the row meant to take it")
	require.Equal(t, priorRow.ID, ownerAfter.ID,
		"PID-MOVE must still belong to the prior owner after a refused batch")

	// And the row itself must be intact, not merely reachable by index.
	priorAfter, err := store.GetBookFileByID(priorBook.ID, priorRow.ID)
	require.NoError(t, err)
	require.NotNil(t, priorAfter)
	require.Equal(t, "PID-MOVE", priorAfter.ITunesPersistentID,
		"the prior owner's stored row lost its PID even though the batch was refused")
	require.Equal(t, "/itunes/a.m4b", priorAfter.ITunesPath,
		"ITunesPath is cleared alongside the PID, so it must roll back too")

	// Nothing from the refused batch was written.
	stored, err := store.GetBookFiles(newBook.ID)
	require.NoError(t, err)
	require.Empty(t, stored, "a refused batch must write no rows at all")
}

// TestBatchCreateBookFilesTransfersAPIDOnSuccess is the positive twin of the test
// above: staging the transfer must not have broken the transfer itself.
//
// Without this, deleting the whole prior-owner branch from stagePIDTransfer would
// leave the rollback test green — nothing is stripped if nothing ever transfers.
func TestBatchCreateBookFilesTransfersAPIDOnSuccess(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	priorBook, err := store.CreateBook(&Book{Title: "Prior", FilePath: "/lib/p2"})
	require.NoError(t, err)
	priorRow := &BookFile{
		BookID: priorBook.ID, FilePath: "/lib/p2/a.m4b",
		ITunesPersistentID: "PID-OK", ITunesPath: "/itunes/a.m4b",
		Duration: 300, FileSize: 3_000,
	}
	require.NoError(t, store.CreateBookFile(priorRow))

	newBook, err := store.CreateBook(&Book{Title: "Taker2", FilePath: "/lib/t2"})
	require.NoError(t, err)
	taker := &BookFile{
		BookID: newBook.ID, FilePath: "/lib/t2/t1.m4b",
		ITunesPersistentID: "PID-OK", Duration: 100, FileSize: 1_000,
	}
	require.NoError(t, store.BatchCreateBookFiles([]*BookFile{taker}))

	owner, err := store.GetBookFileByPID("PID-OK")
	require.NoError(t, err)
	require.NotNil(t, owner, "PID-OK must be owned after a successful batch")
	require.Equal(t, taker.ID, owner.ID, "the new row must own the PID it claimed")

	priorAfter, err := store.GetBookFileByID(priorBook.ID, priorRow.ID)
	require.NoError(t, err)
	require.NotNil(t, priorAfter, "the prior owner's row must survive, just without the PID")
	require.Empty(t, priorAfter.ITunesPersistentID, "the prior owner must have released the PID")
	require.Empty(t, priorAfter.ITunesPath, "ITunesPath is released with the PID")
	require.Equal(t, int64(3_000), priorAfter.FileSize,
		"only the PID fields move; the prior row's other data must be untouched")
}

// TestBatchCreateBookFilesEmptyIsNoop pins that the empty and all-nil cases return
// before touching the store, so no caller needs to guard the call.
func TestBatchCreateBookFilesEmptyIsNoop(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Empty", FilePath: "/lib/empty"})
	require.NoError(t, err)

	readLogs := captureAggregateLogs(t)

	require.NoError(t, store.BatchCreateBookFiles(nil))
	require.NoError(t, store.BatchCreateBookFiles([]*BookFile{}))
	require.NoError(t, store.BatchCreateBookFiles([]*BookFile{nil, nil}))

	require.Zero(t, countAggregateInvocations(readLogs(), book.ID),
		"an empty batch must not recompute anything")
}
