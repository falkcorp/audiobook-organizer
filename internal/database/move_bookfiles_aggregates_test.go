// file: internal/database/move_bookfiles_aggregates_test.go
// version: 1.0.0
// guid: 8b41d7e2-3f95-4c60-a1d8-6e0937b2c5f4
// last-edited: 2026-08-24

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMoveBookFilesToBookRecomputesBothBooks is the reason this fix exists.
//
// MoveBookFilesToBook is the only BookFile mutator that changes which book a row
// belongs to, and it recomputed NEITHER side. Duration and FileSize moved between
// two books while both books' cached totals kept their pre-move values. Nothing
// re-derives them afterwards, so the wrong numbers were permanent.
//
// The fixture moves SOME of the source's files, never all of them. That is
// deliberate: RecomputeBookAggregates' partial-data rule declines to overwrite a
// populated Duration when no remaining file carries one, so a fully drained source
// legitimately keeps its old total and could not distinguish a fixed
// implementation from a broken one.
func TestMoveBookFilesToBookRecomputesBothBooks(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	src, err := store.CreateBook(&Book{Title: "Move Source", FilePath: "/lib/mv/src"})
	require.NoError(t, err)
	dst, err := store.CreateBook(&Book{Title: "Move Target", FilePath: "/lib/mv/dst"})
	require.NoError(t, err)

	// Three files on the source, one already on the target so the target has a
	// non-nil starting aggregate to be wrong about.
	srcFiles := []*BookFile{
		{BookID: src.ID, FilePath: "/lib/mv/src/t1.m4b", Duration: 100, FileSize: 1_000, TrackNumber: 1},
		{BookID: src.ID, FilePath: "/lib/mv/src/t2.m4b", Duration: 200, FileSize: 2_000, TrackNumber: 2},
		{BookID: src.ID, FilePath: "/lib/mv/src/t3.m4b", Duration: 300, FileSize: 3_000, TrackNumber: 3},
	}
	for _, f := range srcFiles {
		require.NoError(t, store.CreateBookFile(f))
	}
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID: dst.ID, FilePath: "/lib/mv/dst/t1.m4b", Duration: 50, FileSize: 500, TrackNumber: 1,
	}))

	// Preconditions: both books already carry correct totals, so a no-op
	// implementation cannot pass by accident.
	before, err := store.GetBookByID(src.ID)
	require.NoError(t, err)
	require.NotNil(t, before.Duration)
	require.Equal(t, 600, *before.Duration, "precondition: source totals its three files")

	dstBefore, err := store.GetBookByID(dst.ID)
	require.NoError(t, err)
	require.NotNil(t, dstBefore.Duration)
	require.Equal(t, 50, *dstBefore.Duration, "precondition: target totals its one file")

	readLogs := captureAggregateLogs(t)

	// Move two of the three: the source keeps t3, so both books stay observable.
	moveIDs := []string{srcFiles[0].ID, srcFiles[1].ID}
	require.NoError(t, store.MoveBookFilesToBook(moveIDs, src.ID, dst.ID))

	// --- the rows actually moved ---
	srcStored, err := store.GetBookFiles(src.ID)
	require.NoError(t, err)
	require.Len(t, srcStored, 1, "source must retain exactly the un-moved file")
	require.Equal(t, srcFiles[2].ID, srcStored[0].ID)

	dstStored, err := store.GetBookFiles(dst.ID)
	require.NoError(t, err)
	require.Len(t, dstStored, 3, "target must own its original file plus the two moved rows")

	// --- BOTH books' aggregates were recomputed ---
	// This is the assertion the bug fails. Before the fix the source still read
	// 600 (runtime it no longer owns) and the target still read 50 (missing the
	// 300 it just gained).
	srcAfter, err := store.GetBookByID(src.ID)
	require.NoError(t, err)
	require.NotNil(t, srcAfter.Duration)
	require.Equal(t, 300, *srcAfter.Duration,
		"source Duration must drop to its remaining file; a stale 600 means the source was never recomputed")
	require.NotNil(t, srcAfter.FileSize)
	require.Equal(t, int64(3_000), *srcAfter.FileSize)

	dstAfter, err := store.GetBookByID(dst.ID)
	require.NoError(t, err)
	require.NotNil(t, dstAfter.Duration)
	require.Equal(t, 350, *dstAfter.Duration,
		"target Duration must gain the moved runtime; a stale 50 means the target was never recomputed")
	require.NotNil(t, dstAfter.FileSize)
	require.Equal(t, int64(3_500), *dstAfter.FileSize)

	// --- each book recomputed exactly ONCE ---
	// Counted as invocations rather than writes: a redundant recompute finds
	// nothing to change and logs only the Debug no-change line, so a per-row
	// regression would still show exactly one "updated". See
	// countAggregateInvocations.
	logs := readLogs()
	require.Equal(t, 1, countAggregateInvocations(logs, src.ID),
		"moving 2 rows must recompute the source ONCE, not once per row")
	require.Equal(t, 1, countAggregateInvocations(logs, dst.ID),
		"moving 2 rows must recompute the target ONCE, not once per row")
}

// TestMoveBookFilesToBookRefreshesMemDB pins the second half of the derived-state
// refresh.
//
// memdb is keyed by file ID and is what GetAllBookFilesCore and the UI read. The
// move rewrote Pebble but left the memdb copy carrying the OLD BookID, so the move
// was invisible to every memdb reader until the next warmup. Asserting through the
// store's own memdb-backed getter rather than through Pebble is the point: a test
// that reads GetBookFiles alone passes with memdb still stale.
//
// ⚠️ WHAT THIS TEST DOES AND DOES NOT PIN. It fails against the true pre-fix code
// (all four post-commit steps absent) — measured. It does NOT fail if only the
// UpsertBookFileToMemDB loop is removed, because the recompute that follows
// reaches GetBookFiles for both books and re-syncs memdb as a side effect. That
// masking is why the loop is kept on a correctness argument rather than a test:
// notifyBookFileChanges is best-effort and swallows its errors, so a failed
// recompute performs no such side effect and memdb would keep the old owner.
// Do not read a green run here as proof the explicit upsert is redundant.
func TestMoveBookFilesToBookRefreshesMemDB(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	src, err := store.CreateBook(&Book{Title: "MemDB Source", FilePath: "/lib/mm/src"})
	require.NoError(t, err)
	dst, err := store.CreateBook(&Book{Title: "MemDB Target", FilePath: "/lib/mm/dst"})
	require.NoError(t, err)

	keep := &BookFile{BookID: src.ID, FilePath: "/lib/mm/src/keep.m4b", Duration: 90, FileSize: 900, TrackNumber: 1}
	move := &BookFile{BookID: src.ID, FilePath: "/lib/mm/src/move.m4b", Duration: 60, FileSize: 600, TrackNumber: 2}
	require.NoError(t, store.CreateBookFile(keep))
	require.NoError(t, store.CreateBookFile(move))

	require.NoError(t, store.MoveBookFilesToBook([]string{move.ID}, src.ID, dst.ID))

	core, err := store.GetAllBookFilesCore()
	require.NoError(t, err)

	var seen bool
	for _, c := range core {
		if c.ID == move.ID {
			seen = true
			require.Equal(t, dst.ID, c.BookID,
				"memdb still reports the OLD BookID — the moved row was never re-upserted, "+
					"so every memdb reader sees the pre-move ownership")
		}
	}
	require.True(t, seen, "fixture is vacuous: the moved row is absent from the memdb projection entirely")
}
