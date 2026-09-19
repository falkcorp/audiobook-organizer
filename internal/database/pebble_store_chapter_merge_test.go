// file: internal/database/pebble_store_chapter_merge_test.go
// version: 1.0.0
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

// TestMergeChapterBooks_FileRowIsNeverAbsentFromBothBooks pins the atomicity of
// the chapter-merge re-parent. MergeChapterBooks used to delete each
// book_file:<src>:<id> key in its own Sync commit and only then call
// CreateBookFile for book_file:<primary>:<id>. Between the two the row existed
// under neither book, so a crash or a CreateBookFile error in that gap lost the
// row outright, and the maintenance job logged and moved on to the next group.
//
// The observer reads every source first and the primary second. With a single
// atomic batch a file can only move from "under the source" to "under the
// primary" between those reads, never vanish, so every observation must account
// for every file. With the old delete-then-create loop the observer catches the
// gap.
func TestMergeChapterBooks_FileRowIsNeverAbsentFromBothBooks(t *testing.T) {
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

	err := s.MergeChapterBooks(primary, srcIDs, "Book", 3600)
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
		b, err := s.GetBookByID(src)
		require.NoError(t, err)
		require.NotNil(t, b.MergedIntoBookID, "source %s must be flagged as merged", src)
		require.Equal(t, primary, *b.MergedIntoBookID)
	}
}
