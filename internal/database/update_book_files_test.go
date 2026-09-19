// file: internal/database/update_book_files_test.go
// version: 1.0.0
// guid: b98e5f12-df7c-4238-b237-e32604a7d218
// last-edited: 2026-09-19

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
)

func seedUpdateBookFilesBook(t *testing.T, s *PebbleStore, n int) (string, []*BookFile) {
	t.Helper()
	book, err := s.CreateBook(&Book{Title: "Many Parts", FilePath: "/lib/many"})
	require.NoError(t, err)
	rows := make([]*BookFile, 0, n)
	for i := range n {
		f := &BookFile{
			BookID:      book.ID,
			FilePath:    fmt.Sprintf("/lib/many/part%03d.mp3", i),
			TrackNumber: i + 1,
			FileSize:    2_000_000,
			Duration:    50,
		}
		require.NoError(t, s.CreateBookFile(f))
		rows = append(rows, f)
	}
	return book.ID, rows
}

// UpdateBookFiles writes every row by ID and recomputes the book ONCE, where n
// UpdateBookFile calls recompute it n times (each re-reading all n rows).
func TestUpdateBookFiles_RecomputesOncePerBook(t *testing.T) {
	s := setupTestPebbleStore(t)
	const n = 25
	bookID, rows := seedUpdateBookFilesBook(t, s, n)
	for _, r := range rows {
		r.Duration = 100
	}

	logs := aggtest.Capture(t)
	var calls []int
	written, err := s.UpdateBookFiles(context.Background(), rows, func(done int) { calls = append(calls, done) })
	require.NoError(t, err)
	require.Equal(t, n, written)
	require.Len(t, calls, n, "afterRow runs once per row")
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID))

	book, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book.Duration)
	require.Equal(t, 100*n, *book.Duration)
}

// A cancel between rows stops the writes, keeps the rows already written, and
// still recomputes the book so its totals agree with its committed rows.
func TestUpdateBookFiles_CancelStopsBetweenRowsAndRecomputesWritten(t *testing.T) {
	s := setupTestPebbleStore(t)
	const n, stopAfter = 10, 4
	bookID, rows := seedUpdateBookFilesBook(t, s, n)
	for _, r := range rows {
		r.Duration = 100
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := aggtest.Capture(t)
	written, err := s.UpdateBookFiles(ctx, rows, func(done int) {
		if done == stopAfter {
			cancel()
		}
	})
	require.True(t, errors.Is(err, context.Canceled), "got %v", err)
	require.Equal(t, stopAfter, written)
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID))

	files, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	changed := 0
	for _, f := range files {
		if f.Duration == 100 {
			changed++
		}
	}
	require.Equal(t, stopAfter, changed)
	book, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book.Duration)
	require.Equal(t, stopAfter*100+(n-stopAfter)*50, *book.Duration)
}
