// file: internal/database/pebble_store_bookfile_modify_test.go
// version: 1.2.0
// guid: 0c8e5b31-7a92-4d6f-b3e4-9f1a2c6d8e57
// last-edited: 2026-10-26

package database

import (
	"errors"
	"fmt"
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

// ModifyBookFileDeferAggregates writes exactly what ModifyBookFile writes but
// leaves the book's aggregates to the caller: N writes cause no recompute at
// all, where ModifyBookFile causes one per write (the O(files^2) cost a
// 400-chapter book paid per stored hash). The caller's one RecomputeBookAggregates
// afterwards is then the book's only one.
func TestModifyBookFileDeferAggregates_DoesNotRecomputeUntilTheCallerDoes(t *testing.T) {
	s := setupTestPebbleStore(t)
	b, err := s.CreateBook(&Book{Title: "The Long Halloween", FilePath: "/lib/Gotham/The Long Halloween"})
	require.NoError(t, err)
	const n = 5
	ids := make([]string, n)
	for i := range ids {
		f := &BookFile{BookID: b.ID, FilePath: fmt.Sprintf("/lib/Gotham/The Long Halloween/%02d.mp3", i+1), Format: "mp3", Duration: 100, FileSize: 1000}
		require.NoError(t, s.CreateBookFile(f))
		ids[i] = f.ID
	}
	setHash := func(hash string) func(*BookFile) error {
		return func(row *BookFile) error { row.FileHash = hash; return nil }
	}

	readLogs := captureAggregateLogs(t)
	for i, id := range ids {
		row, err := s.ModifyBookFileDeferAggregates(b.ID, id, setHash(fmt.Sprintf("hash-%d", i)))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("hash-%d", i), row.FileHash)
	}
	require.Zero(t, countAggregateInvocations(readLogs(), b.ID),
		"the deferring variant must not recompute the book's aggregates")
	for i, id := range ids {
		got, gerr := s.GetBookFileByID(b.ID, id)
		require.NoError(t, gerr)
		require.Equal(t, fmt.Sprintf("hash-%d", i), got.FileHash, "the write itself must land, index and all")
		byHash, herr := s.GetBookBySegmentFileHash(fmt.Sprintf("hash-%d", i))
		require.NoError(t, herr)
		require.NotNil(t, byHash, "the hash's secondary index must be written")
		require.Equal(t, b.ID, byHash.ID)
	}

	require.NoError(t, s.RecomputeBookAggregates(b.ID))
	require.Equal(t, 1, countAggregateInvocations(readLogs(), b.ID), "the caller's one recompute is the book's only one")

	// The recomputing variant is the contrast: one recompute per write.
	readLogs = captureAggregateLogs(t)
	for i, id := range ids {
		_, err := s.ModifyBookFile(b.ID, id, setHash(fmt.Sprintf("again-%d", i)))
		require.NoError(t, err)
	}
	require.Equal(t, n, countAggregateInvocations(readLogs(), b.ID), "ModifyBookFile recomputes on every write")
}

// The deferring variant keeps ModifyBookFile's contract: a skip or an error
// writes nothing, a missing row is (nil, nil), and the identity cannot change.
func TestModifyBookFileDeferAggregates_KeepsTheModifyContract(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)
	before, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)

	boom := errors.New("boom")
	for _, ret := range []error{ErrSkipBookFileWrite, boom} {
		_, err := s.ModifyBookFileDeferAggregates(f.BookID, f.ID, func(row *BookFile) error {
			row.FileHash = "never"
			return ret
		})
		if ret == boom {
			require.ErrorIs(t, err, boom)
		} else {
			require.NoError(t, err)
		}
		got, gerr := s.GetBookFileByID(f.BookID, f.ID)
		require.NoError(t, gerr)
		require.Equal(t, before.FileHash, got.FileHash)
		require.Equal(t, before.UpdatedAt, got.UpdatedAt)
	}

	row, err := s.ModifyBookFileDeferAggregates(f.BookID, "no-such-file", func(*BookFile) error { t.Fatal("fn ran for a missing row"); return nil })
	require.NoError(t, err)
	require.Nil(t, row)

	_, err = s.ModifyBookFileDeferAggregates(f.BookID, f.ID, func(row *BookFile) error { row.BookID = "other"; return nil })
	require.Error(t, err, "changing BookID must be refused")
}

// CONS-18: a millisecond Duration written through either modify variant is
// stored as seconds, the same as through CreateBookFile and UpdateBookFile. The
// repair lives in updateBookFileOver, which both variants (and every other
// row-rewriting path) commit through. 3,600,000 for a 40 MB file implies 88 bps
// as seconds and 88 kbps as milliseconds, so the heuristic fires.
func TestModifyBookFile_NormalisesAMillisecondDurationToSeconds(t *testing.T) {
	variants := map[string]func(s *PebbleStore, bookID, fileID string, fn func(*BookFile) error) (*BookFile, error){
		"ModifyBookFile":                (*PebbleStore).ModifyBookFile,
		"ModifyBookFileDeferAggregates": (*PebbleStore).ModifyBookFileDeferAggregates,
	}
	for name, modify := range variants {
		t.Run(name, func(t *testing.T) {
			s := setupTestPebbleStore(t)
			b, err := s.CreateBook(&Book{Title: "Batman: Year One", FilePath: "/lib/Gotham/Batman Year One"})
			require.NoError(t, err)
			f := &BookFile{BookID: b.ID, FilePath: "/lib/Gotham/Batman Year One/01.m4b", Format: "m4b", Duration: 3600, FileSize: 40_000_000}
			require.NoError(t, s.CreateBookFile(f))

			row, err := modify(s, b.ID, f.ID, func(r *BookFile) error { r.Duration = 3_600_000; return nil })
			require.NoError(t, err)
			require.Equal(t, 3600, row.Duration, "the returned row is what was stored")
			got, err := s.GetBookFileByID(b.ID, f.ID)
			require.NoError(t, err)
			require.Equal(t, 3600, got.Duration, "a millisecond duration must be stored as seconds")

			// A value that is already seconds is never divided again.
			_, err = modify(s, b.ID, f.ID, func(r *BookFile) error { r.Title = "Batman: Year One, Chapter 1"; return nil })
			require.NoError(t, err)
			got, err = s.GetBookFileByID(b.ID, f.ID)
			require.NoError(t, err)
			require.Equal(t, 3600, got.Duration)
		})
	}
}
