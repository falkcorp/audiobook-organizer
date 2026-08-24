// file: internal/database/batch_create_bookfiles_test.go
// version: 1.0.0
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
// enforceBookFilePIDUniqueness resolves the prior owner through a COMMITTED read,
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

	// Atomic: the refusal must leave nothing behind.
	stored, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Empty(t, stored, "a refused batch must write no rows at all")
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
