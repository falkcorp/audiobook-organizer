// file: internal/dedup/split_book_merge_hooks_test.go
// version: 1.0.0
// guid: 0c7e2f4a-9b13-4d58-a6e1-3f82d5b7c904
// last-edited: 2026-10-12

package dedup

import (
	"errors"
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
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
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: fid, BookID: id, FilePath: dir + "/01.mp3", Format: "mp3", Duration: 60, TrackNumber: track}))
		files[id] = fid
		return id
	}
	keep = mk("Keep 01", 1)
	for i := 0; i < n; i++ {
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

// BeforeSource stopping mid-cluster: the srcs already absorbed stay merged,
// the stopping src and every later one are untouched and out of the journal,
// no optional write (title) is made, the retired primary still hands its
// version group on, the journal is finalized, and undoing it restores
// exactly what was absorbed.
func TestMergeSplitBookCluster_BeforeSourceStopsMidCluster(t *testing.T) {
	store := newConcurrentTestStore(t)
	keep, srcs, files := hookCluster(t, store, 3)

	stop := errors.New("lease lost")
	var seen []string
	res, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "Keep (Complete)", SplitMergeOptions{
		BeforeSource: func(src string) error {
			seen = append(seen, src)
			if src == srcs[1] {
				return stop
			}
			return nil
		},
	})
	require.NoError(t, err, "a stop after the journal is reported in the result")
	require.NotNil(t, res)
	assert.Equal(t, srcs[:2], seen, "the hook runs before each src, and nothing runs after it stops")
	assert.Equal(t, 1, res.MergedSrcCount)
	assert.Equal(t, srcs[1:], res.NotAttempted)
	assert.Contains(t, res.Errors[0], "lease lost")

	assert.True(t, hookBook(t, store, srcs[0]).IsSoftDeleted(), "the src absorbed before the stop stays merged")
	f, err := store.GetBookFileByID(keep, files[srcs[0]])
	require.NoError(t, err)
	assert.NotNil(t, f, "its file is on the keep")
	for _, src := range srcs[1:] {
		assert.False(t, hookBook(t, store, src).IsSoftDeleted(), "src %s is not attempted", src)
		f, err := store.GetBookFileByID(src, files[src])
		require.NoError(t, err)
		assert.NotNil(t, f, "src %s keeps its file", src)
	}
	assert.Equal(t, "Keep 01", hookBook(t, store, keep).Title, "no optional write after a stop")

	ms := merge.NewService(store)
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, merge.CombineJournalApplied, j.Status)
	require.Len(t, j.Absorbed, 1, "only the absorbed src is journaled")
	assert.Equal(t, srcs[0], j.Absorbed[0].BookID)

	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	assert.False(t, hookBook(t, store, srcs[0]).IsSoftDeleted(), "undo restores the absorbed src")
	f, err = store.GetBookFileByID(srcs[0], files[srcs[0]])
	require.NoError(t, err)
	assert.NotNil(t, f, "undo moves its file back")
}

// A src retired before a stop was its version group's primary: the group is
// still handed on, a reconciling step that runs after a stop.
func TestMergeSplitBookCluster_BeforeSourceStopStillHandsOnARetiredPrimary(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep", Primary: "true"})
	src := f.Book(t, vptest.Spec{ID: "src", Group: "g-src", Primary: "true"})
	later := f.Book(t, vptest.Spec{ID: "later"})
	sibling := f.Book(t, vptest.Spec{ID: "sib", Group: "g-src", Primary: "false"})

	res, err := MergeSplitBookClusterWithOptions(f.S, keep, []string{src, later}, "", SplitMergeOptions{
		BeforeSource: func(id string) error {
			if id == later {
				return errors.New("stop")
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.MergedSrcCount)
	require.Equal(t, []string{later}, res.NotAttempted)
	f.RequireSinglePrimary(t, "g-src", sibling)
}

// A stop before the first src writes nothing but the finalized, empty
// journal; it is still not an error.
func TestMergeSplitBookCluster_BeforeSourceStopsAtFirstSrc(t *testing.T) {
	store := newConcurrentTestStore(t)
	keep, srcs, files := hookCluster(t, store, 2)
	res, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "Keep (Complete)", SplitMergeOptions{
		BeforeSource: func(string) error { return errors.New("fenced") },
	})
	require.NoError(t, err)
	assert.Zero(t, res.MergedSrcCount)
	assert.Zero(t, res.FilesMoved)
	assert.Equal(t, srcs, res.NotAttempted)
	for _, src := range srcs {
		assert.False(t, hookBook(t, store, src).IsSoftDeleted())
		f, err := store.GetBookFileByID(src, files[src])
		require.NoError(t, err)
		assert.NotNil(t, f)
	}
	j, err := merge.NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, merge.CombineJournalApplied, j.Status)
	assert.Empty(t, j.Absorbed)
}

// OnJournal is told the journal id after the Pending journal is written and
// before any mutation; its error refuses the merge with nothing mutated and
// the journal emptied.
func TestMergeSplitBookCluster_OnJournal(t *testing.T) {
	t.Run("called before the first mutation", func(t *testing.T) {
		store := newConcurrentTestStore(t)
		keep, srcs, _ := hookCluster(t, store, 2)
		var got string
		res, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "", SplitMergeOptions{
			OnJournal: func(id string) error {
				got = id
				j, jerr := merge.NewService(store).GetCombineJournal(id)
				require.NoError(t, jerr, "the journal exists when OnJournal runs")
				assert.Equal(t, merge.CombineJournalPending, j.Status)
				for _, src := range srcs {
					assert.False(t, hookBook(t, store, src).IsSoftDeleted(), "nothing is mutated yet")
				}
				return nil
			},
		})
		require.NoError(t, err)
		assert.Equal(t, res.JournalID, got)
		assert.Equal(t, 2, res.MergedSrcCount)
	})
	t.Run("an error refuses with nothing mutated", func(t *testing.T) {
		store := newConcurrentTestStore(t)
		keep, srcs, files := hookCluster(t, store, 2)
		var id string
		res, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "Keep (Complete)", SplitMergeOptions{
			OnJournal: func(j string) error { id = j; return errors.New("pointer not journaled") },
		})
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
		j, jerr := merge.NewService(store).GetCombineJournal(id)
		require.NoError(t, jerr)
		assert.Empty(t, j.Absorbed, "the refused journal lists no src")
		assert.Empty(t, j.SurvivorOwnFiles)
		assert.Equal(t, merge.CombineJournalApplied, j.Status)
	})
}

// AcquireLock replaces the bare lock: its error refuses the merge with
// nothing written and the lock not held.
func TestMergeSplitBookCluster_AcquireLock(t *testing.T) {
	store := newConcurrentTestStore(t)
	keep, srcs, _ := hookCluster(t, store, 2)
	_, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "", SplitMergeOptions{
		AcquireLock: func(_, _ func()) error { return errors.New("stand-down lost") },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stand-down lost")
	for _, src := range srcs {
		assert.False(t, hookBook(t, store, src).IsSoftDeleted())
	}
	free := make(chan struct{})
	go func() {
		merge.LockMergeRMW()
		merge.UnlockMergeRMW()
		close(free)
	}()
	select {
	case <-free:
	case <-time.After(10 * time.Second):
		t.Fatal("the merge lock is still held after a refused acquire")
	}

	took := false
	res, err := MergeSplitBookClusterWithOptions(store, keep, srcs, "", SplitMergeOptions{
		AcquireLock: func(lock, _ func()) error { lock(); took = true; return nil },
	})
	require.NoError(t, err)
	assert.True(t, took)
	assert.Equal(t, 2, res.MergedSrcCount)
}
