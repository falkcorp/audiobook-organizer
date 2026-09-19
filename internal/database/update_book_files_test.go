// file: internal/database/update_book_files_test.go
// version: 1.2.0
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
	written, err := s.UpdateBookFiles(context.Background(), rows, func(i int, applied bool) {
		require.True(t, applied)
		calls = append(calls, i)
	})
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
	written, err := s.UpdateBookFiles(ctx, rows, func(i int, _ bool) {
		if i+1 == stopAfter {
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

// A single-row write whose batch was applied but whose fsync failed
// (ErrBookFileDurabilityUnknown) has changed the book's rows, so the book's
// aggregates must follow: bookFileApplied's contract is that the caller does
// its post-commit work, aggregates included, and still returns the error.
// UpdateBookFile and ModifyBookFile skipped the recompute on any error.
func TestSingleRowWrite_DurabilityUnknownStillRecomputes(t *testing.T) {
	cases := map[string]func(s *PebbleStore, f BookFile) error{
		"UpdateBookFile": func(s *PebbleStore, f BookFile) error {
			f.Duration = 300
			return s.UpdateBookFile(f.ID, &f)
		},
		"ModifyBookFile": func(s *PebbleStore, f BookFile) error {
			_, err := s.ModifyBookFile(f.BookID, f.ID, func(cur *BookFile) error {
				cur.Duration = 300
				return nil
			})
			return err
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			s := setupTestPebbleStore(t)
			bookID, rows := seedUpdateBookFilesBook(t, s, 2) // 2 x 50s
			bookFileWALSyncHook = func() error { return errors.New("injected fsync failure") }
			t.Cleanup(func() { bookFileWALSyncHook = nil })

			err := write(s, *rows[0])
			bookFileWALSyncHook = nil
			require.True(t, errors.Is(err, ErrBookFileDurabilityUnknown), "got %v", err)

			book, err := s.GetBookByID(bookID)
			require.NoError(t, err)
			require.NotNil(t, book.Duration)
			require.Equal(t, 350, *book.Duration, "the applied row's new duration must reach the book")
		})
	}
}

// A middle row whose ID was deleted fails on its own: the rows after it are
// still written, the book is recomputed once, and the deleted row is NOT
// brought back.
func TestUpdateBookFiles_DeletedMiddleRowFailsAlone(t *testing.T) {
	s := setupTestPebbleStore(t)
	const n, gone = 5, 2
	bookID, rows := seedUpdateBookFilesBook(t, s, n)
	require.NoError(t, s.DeleteBookFile(rows[gone].ID))
	for _, r := range rows {
		r.Duration = 100
	}

	logs := aggtest.Capture(t)
	var applied []bool
	written, err := s.UpdateBookFiles(context.Background(), rows, func(_ int, ok bool) { applied = append(applied, ok) })
	require.Error(t, err)
	require.Contains(t, err.Error(), rows[gone].ID)
	require.False(t, errors.Is(err, ErrBookAggregatesRecompute))
	require.Equal(t, n-1, written)
	require.Equal(t, []bool{true, true, false, true, true}, applied)
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID))

	files, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, n-1, "the deleted row must not be resurrected")
	for _, f := range files {
		require.NotEqual(t, rows[gone].ID, f.ID)
		require.Equal(t, 100, f.Duration)
	}
	book, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book.Duration)
	require.Equal(t, 100*(n-1), *book.Duration)
}
