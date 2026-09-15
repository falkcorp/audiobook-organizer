// file: internal/dedup/split_book_merge_integrity_test.go
// version: 1.0.0
// guid: 5f81c3d2-9e4a-4b67-a0d8-2c7e9b13f456
// last-edited: 2026-09-14

package dedup

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statefulBookMock keeps book rows in a map so a whole-row write of a stale
// read is observable: GetBookByID hands out copies, UpdateBook stores copies.
// MockStore.ModifyBook composes the two, so a ModifyBook caller reads the
// CURRENT row while an UpdateBook of an earlier read overwrites it.
func statefulBookMock(rows map[string]*database.Book) (*database.MockStore, *sync.Mutex) {
	var mu sync.Mutex
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		b, ok := rows[id]
		if !ok {
			return nil, nil
		}
		cp := *b
		return &cp, nil
	}
	m.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *book
		rows[id] = &cp
		return book, nil
	}
	return m, &mu
}

// A1#5 stale write: the keep row was read at the top of the merge and written
// back whole at the end, so any column changed in between -- here FileSize,
// which MoveBookFilesToBook's aggregate recompute (or any other writer) sets --
// was reverted.
func TestMergeSplitBookCluster_KeepWriteDoesNotRevertConcurrentColumns(t *testing.T) {
	const keepID, srcID = "K", "S"
	rows := map[string]*database.Book{
		keepID: {ID: keepID, Title: "Keep"},
		srcID:  {ID: srcID, Title: "Src"},
	}
	m, mu := statefulBookMock(rows)
	m.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		if bookID == srcID {
			return []database.BookFile{{ID: "f1", BookID: srcID, Duration: 100}}, nil
		}
		return []database.BookFile{{ID: "f1", BookID: keepID, Duration: 100}, {ID: "f0", BookID: keepID, Duration: 200}}, nil
	}
	m.MoveBookFilesToBookFunc = func(_ []string, _, target string) error {
		mu.Lock()
		defer mu.Unlock()
		size := int64(500)
		rows[target].FileSize = &size // the store's aggregate recompute
		return nil
	}

	res, err := MergeSplitBookCluster(m, keepID, []string{srcID}, "New Title")
	require.NoError(t, err)
	require.Empty(t, res.Errors)

	keep := rows[keepID]
	require.NotNil(t, keep.FileSize, "FileSize written between the merge's read and its write must survive")
	assert.Equal(t, int64(500), *keep.FileSize)
	require.NotNil(t, keep.Duration)
	assert.Equal(t, 300, *keep.Duration)
	assert.Equal(t, "New Title", keep.Title)
	assert.True(t, rows[srcID].IsSoftDeleted())
}

// A1#5 fail closed: a src whose external IDs cannot be reassigned stays live.
func TestMergeSplitBookCluster_ExternalIDReassignFailureLeavesSrcLive(t *testing.T) {
	const keepID, srcID = "K", "S"
	rows := map[string]*database.Book{
		keepID: {ID: keepID, Title: "Keep"},
		srcID:  {ID: srcID, Title: "Src"},
	}
	m, _ := statefulBookMock(rows)
	m.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }
	m.GetExternalIDsForBookFunc = func(bookID string) ([]database.ExternalIDMapping, error) {
		if bookID == srcID {
			return []database.ExternalIDMapping{{Source: "itunes", ExternalID: "PID1", BookID: srcID}}, nil
		}
		return nil, nil
	}
	m.ReassignExternalIDsFunc = func(string, string) error { return assert.AnError }

	res, err := MergeSplitBookCluster(m, keepID, []string{srcID}, "")
	require.NoError(t, err)
	assert.False(t, rows[srcID].IsSoftDeleted(), "a src still holding its PIDs must not be soft-deleted")
	assert.Equal(t, 0, res.MergedSrcCount)
	assert.NotEmpty(t, res.Errors)
}

// A1#5 ext IDs + journal, on a real store: the src's PID moves to the keep,
// the merge is journaled, and UndoCombine puts back the src, its file and PID.
func TestMergeSplitBookCluster_ReassignsExternalIDsAndIsUndoable(t *testing.T) {
	store := newConcurrentTestStore(t)
	keepID, srcID := ulid.Make().String(), ulid.Make().String()
	pid := "PID-SPLIT-" + ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Keep", Format: "mp3", FilePath: "/tmp/split/k"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: srcID, Title: "Keep Part 2", Format: "mp3", FilePath: "/tmp/split/s"})
	require.NoError(t, err)
	fileID := ulid.Make().String()
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: fileID, BookID: srcID, FilePath: "/tmp/split/s/02.mp3", Format: "mp3", Duration: 60, TrackNumber: 2}))
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: pid, BookID: srcID}))

	res, err := MergeSplitBookCluster(store, keepID, []string{srcID}, "Keep (Complete)")
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.NotEmpty(t, res.JournalID, "the merge must be journaled")

	owner, err := store.GetBookByExternalID("itunes", pid)
	require.NoError(t, err)
	assert.Equal(t, keepID, owner, "the src's PID must follow its audio to the keep")
	src, err := store.GetBookByID(srcID)
	require.NoError(t, err)
	assert.True(t, src.IsSoftDeleted())

	ms := merge.NewService(store)
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, SplitBookMergeJournalOrigin, j.Origin)
	assert.Equal(t, merge.CombineJournalApplied, j.Status)

	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	src, err = store.GetBookByID(srcID)
	require.NoError(t, err)
	assert.False(t, src.IsSoftDeleted(), "undo restores the src")
	f, err := store.GetBookFileByID(srcID, fileID)
	require.NoError(t, err)
	assert.NotNil(t, f, "undo moves the file back to the src")
	owner, err = store.GetBookByExternalID("itunes", pid)
	require.NoError(t, err)
	assert.Equal(t, srcID, owner, "undo moves the PID back")
	keep, err := store.GetBookByID(keepID)
	require.NoError(t, err)
	assert.Equal(t, "Keep", keep.Title, "undo rolls back the suggested title")
}
