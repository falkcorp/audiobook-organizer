// file: internal/database/pid_uniqueness_test.go
// version: 1.0.0
// guid: 4d7c2e91-8a63-4b05-9f18-2e6c1a4b7d33
// last-edited: 2026-07-23

package database

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreateBookFilePIDUniquenessTransfer verifies the write-time invariant: when
// a new book_file is minted with a PID already held by another row (the
// version-split copy pattern), ownership TRANSFERS to the new row and the prior
// owner's PID is cleared — with no audio file touched. This is the forward fix for
// the duplicate-PID ("shared_skipped") anomaly.
func TestCreateBookFilePIDUniquenessTransfer(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	defer store.Close()

	const pid = "ABCDEF1234567890"

	orig, err := store.CreateBook(&Book{Title: "Original", FilePath: "/old/book"})
	require.NoError(t, err)
	organized, err := store.CreateBook(&Book{Title: "Organized", FilePath: "/organized/book"})
	require.NoError(t, err)

	// Original import row owns the PID.
	f1 := &BookFile{BookID: orig.ID, FilePath: "/old/book/01.m4b", ITunesPersistentID: pid}
	require.NoError(t, store.CreateBookFile(f1))

	got, err := store.GetBookFileByPID(pid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, f1.ID, got.ID, "prior owner should hold the PID before the copy")

	// Version-split copy: the organizer mints a NEW row carrying the SAME PID.
	f2 := &BookFile{BookID: organized.ID, FilePath: "/organized/book/01.m4b", ITunesPersistentID: pid}
	require.NoError(t, store.CreateBookFile(f2))

	// The PID index now points at the NEW (organized) row.
	got, err = store.GetBookFileByPID(pid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, f2.ID, got.ID, "PID should transfer to the new organized row")
	require.NotEqual(t, f1.ID, f2.ID)

	// The prior owner's PID field is cleared — exactly one row carries the PID.
	origFiles, err := store.GetBookFiles(orig.ID)
	require.NoError(t, err)
	require.Len(t, origFiles, 1)
	require.Empty(t, origFiles[0].ITunesPersistentID, "prior owner PID must be cleared (no duplicate)")

	// The prior owner's audio file path is untouched (no file-level data loss).
	require.Equal(t, "/old/book/01.m4b", origFiles[0].FilePath)
}

// TestCreateBookFileSamePIDSameRowNoop verifies re-creating the same row (same ID
// + same PID) does not clear itself — the guard is scoped to a DIFFERENT prior row.
func TestCreateBookFileSamePIDSameRowNoop(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	defer store.Close()

	const pid = "0011223344556677"
	b, err := store.CreateBook(&Book{Title: "B", FilePath: "/b"})
	require.NoError(t, err)

	f := &BookFile{BookID: b.ID, FilePath: "/b/01.m4b", ITunesPersistentID: pid}
	require.NoError(t, store.CreateBookFile(f))
	// Re-create with the SAME explicit ID → prior.ID == file.ID → no clear.
	require.NoError(t, store.CreateBookFile(f))

	got, err := store.GetBookFileByPID(pid)
	require.NoError(t, err)
	require.NotNil(t, got, "PID must still be owned after re-create of the same row")
	require.Equal(t, f.ID, got.ID)
}
