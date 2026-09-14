// file: internal/database/pebble_store_bookfile_modify_test.go
// version: 1.0.0
// guid: 0c8e5b31-7a92-4d6f-b3e4-9f1a2c6d8e57
// last-edited: 2026-09-14

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// fn sees the row as currently stored, not a caller's earlier read: a change
// committed after the caller's read is visible to the precondition.
func TestModifyBookFile_CallbackSeesCurrentRow(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)
	staleRead := *f

	moved := *f
	moved.FilePath = "/lib/Other/01.mp3"
	require.NoError(t, s.UpdateBookFile(f.ID, &moved))

	var seen string
	_, err := s.ModifyBookFile(f.BookID, f.ID, func(row *BookFile) error {
		seen = row.FilePath
		if row.FilePath != staleRead.FilePath {
			return ErrSkipBookFileWrite
		}
		row.FilePath = "/lib/Clobbered/01.mp3"
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "/lib/Other/01.mp3", seen)

	got, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)
	require.Equal(t, "/lib/Other/01.mp3", got.FilePath, "the concurrent change was overwritten")
}

// A path change goes through UpdateBookFile's commit, so the path index moves
// with it and every other column is kept.
func TestModifyBookFile_PathChangeRewritesIndex(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)

	after, err := s.ModifyBookFile(f.BookID, f.ID, func(row *BookFile) error {
		row.FilePath = "/lib/New/01.mp3"
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "/lib/New/01.mp3", after.FilePath)

	byNew, err := s.GetBookFileByPath("/lib/New/01.mp3")
	require.NoError(t, err)
	require.NotNil(t, byNew)
	require.Equal(t, f.ID, byNew.ID)
	require.Equal(t, 100.0, float64(byNew.Duration))
	byOld, err := s.GetBookFileByPath("/lib/T/01.mp3")
	require.NoError(t, err)
	require.Nil(t, byOld, "stale path index left behind")
}

func TestModifyBookFile_SkipAndErrorWriteNothing(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)
	before, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)

	boom := errors.New("boom")
	for _, ret := range []error{ErrSkipBookFileWrite, boom} {
		_, err := s.ModifyBookFile(f.BookID, f.ID, func(row *BookFile) error {
			row.FilePath = "/lib/Nope/01.mp3"
			return ret
		})
		if ret == boom {
			require.ErrorIs(t, err, boom)
		} else {
			require.NoError(t, err)
		}
		got, gerr := s.GetBookFileByID(f.BookID, f.ID)
		require.NoError(t, gerr)
		require.Equal(t, before.FilePath, got.FilePath)
		require.Equal(t, before.UpdatedAt, got.UpdatedAt)
	}

	row, err := s.ModifyBookFile(f.BookID, "no-such-file", func(*BookFile) error { t.Fatal("fn ran for a missing row"); return nil })
	require.NoError(t, err)
	require.Nil(t, row)

	_, err = s.ModifyBookFile(f.BookID, f.ID, func(row *BookFile) error { row.BookID = "other"; return nil })
	require.Error(t, err, "changing BookID must be refused")
}
