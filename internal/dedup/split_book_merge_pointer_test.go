// file: internal/dedup/split_book_merge_pointer_test.go
// version: 2.0.1
// guid: 0c7e2f4a-9b13-4d58-a6e1-3f82d5b7c904
// last-edited: 2026-11-09

package dedup

import (
	"errors"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// hookCluster seeds a keep and n srcs on a real store, each with one file
// row, and returns their ids (keep first).
func hookCluster(t *testing.T, store database.Store, n int) (keep string, srcs []string, files map[string]string) {
	t.Helper()
	files = map[string]string{}
	mk := func(title string, track int) string {
		id := ulid.Make().String()
		dir := "/tmp/split-hooks/" + id
		_, err := store.CreateBook(&database.Book{ID: id, Title: title, Format: "mp3", FilePath: dir + "/01.mp3"})
		require.NoError(t, err)
		fid := ulid.Make().String()
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: fid, BookID: id, FilePath: dir + "/01.mp3", Format: "mp3", Duration: database.Secs(60), TrackNumber: track}))
		files[id] = fid
		return id
	}
	keep = mk("Keep 01", 1)
	for i := range n {
		srcs = append(srcs, mk("Keep part", i+2))
	}
	return keep, srcs, files
}

func hookBook(t *testing.T, store database.Store, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

// failPointerStore refuses to journal the undo-pointer row (an operation
// change), the way a ledger that cannot be written would.
type failPointerStore struct{ database.Store }

func (failPointerStore) CreateOperationChange(*database.OperationChange) error {
	return errors.New("pointer not journaled")
}

// The merge journals its undo pointer after the Pending journal is written and
// before any mutation. When that write fails the merge is refused with nothing
// mutated, and the journal is rewritten empty so an undo of it restores nothing.
func TestMergeSplitBookCluster_PointerWriteFailureRefusesBeforeAnyMutation(t *testing.T) {
	store := newConcurrentTestStore(t)
	keep, srcs, files := hookCluster(t, store, 2)
	res, err := MergeSplitBookClusterWithOptions(failPointerStore{store}, keep, srcs, "Keep (Complete)", SplitMergeOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pointer not journaled")
	assert.Nil(t, res)
	for _, src := range srcs {
		assert.False(t, hookBook(t, store, src).IsSoftDeleted())
		f, ferr := store.GetBookFileByID(src, files[src])
		require.NoError(t, ferr)
		assert.NotNil(t, f)
	}
	assert.Equal(t, "Keep 01", hookBook(t, store, keep).Title)
}

// A merge that journals its pointer reports the operation id it used: the
// caller's OperationID when given, so reverting that operation undoes the merge.
func TestMergeSplitBookCluster_ReportsTheOperationItJournaledUnder(t *testing.T) {
	store := newConcurrentTestStore(t)
	keep, srcs, _ := hookCluster(t, store, 2)
	res, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "", SplitMergeOptions{OperationID: "op-gotham-merge"})
	require.NoError(t, err)
	assert.Equal(t, "op-gotham-merge", res.OperationID)
	assert.Equal(t, 2, res.MergedSrcCount)
	j, err := merge.NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, merge.CombineJournalApplied, j.Status)
}
