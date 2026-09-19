// file: internal/database/pebble_store_chapter_merge_test.go
// version: 1.1.0
// guid: 3b8f5d21-7c4e-4a96-8e0b-2d6f1a9c7e53
// last-edited: 2026-09-19

package database

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChapterMergeReparent_FileRowIsNeverAbsentFromBothBooks pins the
// atomicity of the re-parent a chapter merge performs. The merge-chapter-groups
// job used PebbleStore.MergeChapterBooks, which once deleted each
// book_file:<src>:<id> key in its own Sync commit and only then created
// book_file:<primary>:<id>; between the two the row existed under neither book.
// #3446 made that path one batch. MergeChapterBooks is gone (2026-09-19): the
// job now merges through dedup.MergeSplitBookCluster, which re-parents each
// source with MoveBookFilesToBook (a one-move MoveBookFilesToBookBulk), so that
// is the call this pins.
//
// The observer reads every source first and the primary second. With an atomic
// move a file can only go from "under the source" to "under the primary"
// between those reads, never vanish, so every observation must account for
// every file.
func TestChapterMergeReparent_FileRowIsNeverAbsentFromBothBooks(t *testing.T) {
	store := setupCoverageDB(t)
	s := store.(*PebbleStore)

	const sources, filesPer = 40, 5
	primary := createTestBook(t, store, "Primary", "/lib/Book/00 - Book.mp3", nil, nil)
	srcIDs := make([]string, 0, sources)
	want := map[string]bool{}
	for i := 1; i <= sources; i++ {
		src := createTestBook(t, store, fmt.Sprintf("Chapter %02d", i), fmt.Sprintf("/lib/Book/%02d - Book.mp3", i), nil, nil)
		srcIDs = append(srcIDs, src)
		for j := 0; j < filesPer; j++ {
			f := &BookFile{BookID: src, FilePath: fmt.Sprintf("/lib/Book/%02d-%d - Book.mp3", i, j), Format: "mp3"}
			require.NoError(t, store.CreateBookFile(f))
			want[f.ID] = true
		}
	}

	var done atomic.Bool
	var observations, gaps atomic.Int64
	var firstGap atomic.Value
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !done.Load() {
			seen := map[string]bool{}
			for _, src := range srcIDs {
				files, err := s.GetBookFiles(src)
				if err != nil {
					t.Error(err)
					return
				}
				for _, f := range files {
					seen[f.ID] = true
				}
			}
			files, err := s.GetBookFiles(primary)
			if err != nil {
				t.Error(err)
				return
			}
			for _, f := range files {
				seen[f.ID] = true
			}
			observations.Add(1)
			for id := range want {
				if !seen[id] {
					gaps.Add(1)
					firstGap.CompareAndSwap(nil, id)
					break
				}
			}
		}
	}()

	var err error
	for _, src := range srcIDs {
		files, gerr := s.GetBookFiles(src)
		require.NoError(t, gerr)
		ids := make([]string, 0, len(files))
		for _, f := range files {
			ids = append(ids, f.ID)
		}
		if err = s.MoveBookFilesToBook(ids, src, primary); err != nil {
			break
		}
	}
	done.Store(true)
	wg.Wait()
	require.NoError(t, err)

	if g := gaps.Load(); g > 0 {
		t.Fatalf("%d of %d observations found a book_file row under neither book (first: %v) — the re-parent is not atomic",
			g, observations.Load(), firstGap.Load())
	}

	files, err := s.GetBookFiles(primary)
	require.NoError(t, err)
	require.Len(t, files, sources*filesPer, "every source file must end under the primary")
	for _, src := range srcIDs {
		left, err := s.GetBookFiles(src)
		require.NoError(t, err)
		require.Empty(t, left, "source %s must be drained", src)
	}
}
