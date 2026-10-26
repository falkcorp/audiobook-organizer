// file: internal/database/bookfile_batch_modify_test.go
// version: 1.1.0
// guid: 2e8a4c60-b9d1-4375-8f12-6d0a3b7c9e54
// last-edited: 2026-10-25

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
)

func fileIDsOf(rows []*BookFile) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// N rows of one book written through ModifyBookFiles recompute the book ONCE,
// where N ModifyBookFile calls recompute it N times. The 1-recompute assertion
// is the point; the control below proves the counter would see N.
func TestModifyBookFiles_RecomputesOncePerBook(t *testing.T) {
	s := setupTestPebbleStore(t)
	const n = 25
	bookID, rows := seedUpdateBookFilesBook(t, s, n)

	logs := aggtest.Capture(t)
	changed, err := s.ModifyBookFiles(context.Background(), bookID, fileIDsOf(rows), func(r *BookFile) error {
		r.Missing = true
		return nil
	})
	require.NoError(t, err)
	require.Len(t, changed, n)
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID), "one recompute for the whole book")

	files, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, n)
	for _, f := range files {
		require.True(t, f.Missing, "row %s", f.ID)
	}
}

// Control for the test above: the per-row path really does recompute per row,
// so a counter of 1 above cannot be a counter that never counts.
func TestModifyBookFiles_ControlPerRowModifyRecomputesEveryRow(t *testing.T) {
	s := setupTestPebbleStore(t)
	const n = 6
	bookID, rows := seedUpdateBookFilesBook(t, s, n)

	logs := aggtest.Capture(t)
	for _, r := range rows {
		_, err := s.ModifyBookFile(bookID, r.ID, func(row *BookFile) error { row.Missing = true; return nil })
		require.NoError(t, err)
	}
	require.Equal(t, n, countAggregateInvocations(logs(), bookID))
}

// fn is the under-lock precondition: a row it skips is not written and is not
// reported, and a batch in which nothing is written recomputes nothing.
func TestModifyBookFiles_SkippedRowsNotWrittenNorRecomputed(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID, rows := seedUpdateBookFilesBook(t, s, 4)

	logs := aggtest.Capture(t)
	changed, err := s.ModifyBookFiles(context.Background(), bookID, fileIDsOf(rows), func(*BookFile) error {
		return ErrSkipBookFileWrite
	})
	require.NoError(t, err)
	require.Empty(t, changed)
	require.Zero(t, countAggregateInvocations(logs(), bookID))

	// Half skipped: only the written half is reported, one recompute.
	want := map[string]bool{rows[0].ID: true, rows[2].ID: true}
	changed, err = s.ModifyBookFiles(context.Background(), bookID, fileIDsOf(rows), func(r *BookFile) error {
		if !want[r.ID] {
			return ErrSkipBookFileWrite
		}
		r.Missing = true
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{rows[0].ID, rows[2].ID}, changed)
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID))
	files, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	for _, f := range files {
		require.Equal(t, want[f.ID], f.Missing, "row %s", f.ID)
	}
}

// A callback error on one row is joined into the result, that row is not
// reported, and the remaining rows are still written and recomputed once.
func TestModifyBookFiles_RowErrorDoesNotStopTheBatch(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID, rows := seedUpdateBookFilesBook(t, s, 3)
	boom := errors.New("boom")

	logs := aggtest.Capture(t)
	changed, err := s.ModifyBookFiles(context.Background(), bookID, fileIDsOf(rows), func(r *BookFile) error {
		if r.ID == rows[1].ID {
			return boom
		}
		r.Missing = true
		return nil
	})
	require.ErrorIs(t, err, boom)
	require.ElementsMatch(t, []string{rows[0].ID, rows[2].ID}, changed)
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID))
}

// A callback that changes the row's identity is refused for that row.
func TestModifyBookFiles_IdentityChangeRefused(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID, rows := seedUpdateBookFilesBook(t, s, 1)
	changed, err := s.ModifyBookFiles(context.Background(), bookID, fileIDsOf(rows), func(r *BookFile) error {
		r.BookID = "other"
		return nil
	})
	require.Error(t, err)
	require.Empty(t, changed)
}

// A cancelled context stops between rows; rows already written keep a recompute.
func TestModifyBookFiles_CancelStopsAndRecomputesWritten(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID, rows := seedUpdateBookFilesBook(t, s, 6)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := 0

	logs := aggtest.Capture(t)
	changed, err := s.ModifyBookFiles(ctx, bookID, fileIDsOf(rows), func(r *BookFile) error {
		seen++
		if seen == 2 {
			cancel()
		}
		r.Missing = true
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, changed, 2)
	require.Equal(t, 1, countAggregateInvocations(logs(), bookID))
}

// An id with no stored row is ignored, not an error.
func TestModifyBookFiles_UnknownIDIgnored(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID, _ := seedUpdateBookFilesBook(t, s, 1)
	changed, err := s.ModifyBookFiles(context.Background(), bookID, []string{"no-such-file"}, func(r *BookFile) error {
		t.Fatal("fn must not run for a row that does not exist")
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, changed)
}

// The hash-absent marker is written and cleared through the same batch call the
// backfill job uses, and a batch that only touches the marker does not disturb
// Missing (the superseded/missing flag other readers own).
func TestModifyBookFiles_HashAbsentMarkerRoundTrip(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID, rows := seedUpdateBookFilesBook(t, s, 3)
	ids := fileIDsOf(rows)
	now := time.Now().UTC().Truncate(time.Second)

	changed, err := s.ModifyBookFiles(context.Background(), bookID, ids, func(r *BookFile) error {
		r.HashAbsentAt = &now
		return nil
	})
	require.NoError(t, err)
	require.Len(t, changed, 3)
	files, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	for _, f := range files {
		require.NotNil(t, f.HashAbsentAt, "marker written on %s", f.ID)
		require.True(t, f.HashAbsentAt.Equal(now))
		require.False(t, f.Missing, "writing the marker must not set Missing on %s", f.ID)
	}

	changed, err = s.ModifyBookFiles(context.Background(), bookID, ids, func(r *BookFile) error {
		r.HashAbsentAt = nil
		return nil
	})
	require.NoError(t, err)
	require.Len(t, changed, 3)
	files, err = s.GetBookFiles(bookID)
	require.NoError(t, err)
	for _, f := range files {
		require.Nil(t, f.HashAbsentAt, "marker cleared on %s", f.ID)
	}
}

// MockStore.ModifyBookFiles must keep going after a failing row and join the
// errors, as the Pebble implementation does (a caller's per-row error
// accounting would otherwise be tested against a different contract).
func TestMockStoreModifyBookFiles_ContinuesPastRowError(t *testing.T) {
	boom := errors.New("boom")
	rows := []BookFile{{ID: "f1", BookID: "b"}, {ID: "f2", BookID: "b"}, {ID: "f3", BookID: "b"}}
	var written []string
	m := &MockStore{
		GetBookFilesFunc: func(string) ([]BookFile, error) { return append([]BookFile(nil), rows...), nil },
		UpdateBookFileFunc: func(id string, _ *BookFile) error {
			if id == "f2" {
				return boom
			}
			written = append(written, id)
			return nil
		},
	}
	changed, err := m.ModifyBookFiles(context.Background(), "b", []string{"f1", "f2", "f3"}, func(r *BookFile) error {
		now := time.Now()
		r.HashAbsentAt = &now
		return nil
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, []string{"f1", "f3"}, changed)
	require.Equal(t, []string{"f1", "f3"}, written)
}
