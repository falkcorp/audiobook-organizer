// file: internal/dedup/split_book_merge_test.go
// version: 1.0.0
// guid: 7c4e1a92-6d3b-4f18-b0a5-2e9c7d514af3
// last-edited: 2026-07-16

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSplitMergeMock builds a MockStore modelling a 1-keep, 2-src cluster where
// each src owns exactly one file. moveFailSrc names the src whose
// MoveBookFilesToBook call should fail (empty = all moves succeed). It records
// which book IDs were soft-deleted (UpdateBook with MarkedForDeletion) or
// hard-deleted (DeleteBook).
func newSplitMergeMock(src1, src2, moveFailSrc string, softDeleted, hardDeleted map[string]bool) *database.MockStore {
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, Title: "book " + id}, nil
	}
	m.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case src1:
			return []database.BookFile{{ID: "f-" + src1, BookID: src1, Duration: 100}}, nil
		case src2:
			return []database.BookFile{{ID: "f-" + src2, BookID: src2, Duration: 200}}, nil
		default: // keep: files land here after moves; irrelevant to this test
			return nil, nil
		}
	}
	m.MoveBookFilesToBookFunc = func(_ []string, sourceBookID, _ string) error {
		if sourceBookID == moveFailSrc {
			return assert.AnError
		}
		return nil
	}
	m.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		if book != nil && book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			softDeleted[id] = true
		}
		return book, nil
	}
	m.DeleteBookFunc = func(id string) error {
		hardDeleted[id] = true
		return nil
	}
	return m
}

// A src whose file-move fails must NOT be soft/hard-deleted — deleting it while
// it still owns its file would orphan that audio (a deleted book referenced by
// live BookFile rows). Regression for the split-book merge orphan bug.
func TestMergeSplitBookCluster_FailedMoveSrcNotDeleted(t *testing.T) {
	const keepID, src1, src2 = "K", "S1", "S2"
	softDeleted := map[string]bool{}
	hardDeleted := map[string]bool{}
	store := newSplitMergeMock(src1, src2, src2 /* S2 move fails */, softDeleted, hardDeleted)

	result, err := MergeSplitBookCluster(store, keepID, []string{src1, src2}, "")
	require.NoError(t, err)

	// S1 moved cleanly → soft-deleted. S2's move failed → left intact.
	assert.True(t, softDeleted[src1], "src with successful move should be soft-deleted")
	assert.False(t, softDeleted[src2], "src with FAILED move must NOT be soft-deleted (would orphan its file)")
	assert.False(t, hardDeleted[src2], "src with FAILED move must NOT be hard-deleted either")

	assert.Equal(t, 1, result.MergedSrcCount, "only the cleanly-moved src counts as merged")
	assert.Equal(t, 1, result.FilesMoved, "only src1's file moved")
	require.NotEmpty(t, result.Errors, "the failed move must be reported")
}

// When every move succeeds, all srcs are soft-deleted (happy path unchanged).
func TestMergeSplitBookCluster_AllMovesSucceed_AllDeleted(t *testing.T) {
	const keepID, src1, src2 = "K", "S1", "S2"
	softDeleted := map[string]bool{}
	hardDeleted := map[string]bool{}
	store := newSplitMergeMock(src1, src2, "" /* no failure */, softDeleted, hardDeleted)

	result, err := MergeSplitBookCluster(store, keepID, []string{src1, src2}, "")
	require.NoError(t, err)

	assert.True(t, softDeleted[src1])
	assert.True(t, softDeleted[src2])
	assert.Equal(t, 2, result.MergedSrcCount)
	assert.Equal(t, 2, result.FilesMoved)
	assert.Empty(t, result.Errors)
}

// A src that already has zero files is safe to delete (nothing to orphan).
func TestMergeSplitBookCluster_EmptySrcStillDeleted(t *testing.T) {
	const keepID, src1 = "K", "S1"
	softDeleted := map[string]bool{}
	hardDeleted := map[string]bool{}
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id}, nil
	}
	m.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil } // empty everywhere
	m.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		if book != nil && book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			softDeleted[id] = true
		}
		return book, nil
	}
	m.DeleteBookFunc = func(id string) error { hardDeleted[id] = true; return nil }

	result, err := MergeSplitBookCluster(m, keepID, []string{src1}, "")
	require.NoError(t, err)
	assert.True(t, softDeleted[src1], "an already-empty src is safe to soft-delete")
	assert.Equal(t, 1, result.MergedSrcCount)
	assert.Equal(t, 0, result.FilesMoved)
}
