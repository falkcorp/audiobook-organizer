// file: internal/database/pebble_store_bookfile_patch_test.go
// version: 1.0.0
// guid: 1a7e4c92-8d35-4b6f-9e20-6f3b8a5d1c47
// last-edited: 2026-09-13

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func seedPatchFile(t *testing.T, s *PebbleStore) *BookFile {
	t.Helper()
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/lib/T"})
	require.NoError(t, err)
	f := &BookFile{BookID: b.ID, FilePath: "/lib/T/01.mp3", Format: "mp3", Duration: 100, TrackNumber: 1, DiscNumber: 1}
	require.NoError(t, s.CreateBookFile(f))
	return f
}

// A column written by another writer after the caller's view of the row
// (enrich-book-files' Duration) survives a track edit: only the named field
// is set, on a fresh read.
func TestPatchBookFileFields_KeepsOtherWritersColumns(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)

	enriched := *f
	enriched.Duration = 555
	require.NoError(t, s.UpdateBookFile(f.ID, &enriched))

	track := 7
	before, after, err := s.PatchBookFileFields(f.BookID, f.ID, BookFileFieldPatch{TrackNumber: &track})
	require.NoError(t, err)
	require.Equal(t, 1, before.TrackNumber)
	require.Equal(t, 7, after.TrackNumber)

	got, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)
	require.Equal(t, 7, got.TrackNumber)
	require.Equal(t, 555, got.Duration, "the concurrent writer's Duration must not be reverted")
	require.Equal(t, 1, got.DiscNumber)
}

// An unchanged value writes nothing: UpdatedAt stays as stored.
func TestPatchBookFileFields_NoChangeNoWrite(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)
	stored, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)

	track := 1
	before, after, err := s.PatchBookFileFields(f.BookID, f.ID, BookFileFieldPatch{TrackNumber: &track})
	require.NoError(t, err)
	require.Equal(t, before, after)

	got, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)
	require.True(t, got.UpdatedAt.Equal(stored.UpdatedAt), "no-op patch rewrote the row")
}

// A failed precondition writes nothing and reports ErrBookFileChangedSince.
func TestPatchBookFileFields_PreconditionMismatch(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)

	want, expect := 9, 5
	_, _, err := s.PatchBookFileFields(f.BookID, f.ID, BookFileFieldPatch{DiscNumber: &want, IfDiscNumber: &expect})
	require.True(t, errors.Is(err, ErrBookFileChangedSince), "got %v", err)

	got, err := s.GetBookFileByID(f.BookID, f.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.DiscNumber)

	expect = 1
	_, after, err := s.PatchBookFileFields(f.BookID, f.ID, BookFileFieldPatch{DiscNumber: &want, IfDiscNumber: &expect})
	require.NoError(t, err)
	require.Equal(t, 9, after.DiscNumber)
}

func TestPatchBookFileFields_MissingRow(t *testing.T) {
	s := setupTestPebbleStore(t)
	track := 2
	before, after, err := s.PatchBookFileFields("nobook", "nofile", BookFileFieldPatch{TrackNumber: &track})
	require.NoError(t, err)
	require.Nil(t, before)
	require.Nil(t, after)
}
