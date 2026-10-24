// file: internal/repairs/writer_repoint_retired_test.go
// version: 1.1.0
// guid: 7e1b4c92-5d38-4a06-b8f1-3c9a02d6e415
// last-edited: 2026-10-24

package repairs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredPathFiles answers like the production store: the plain path getter
// skips a retired row, the include-retired twin does not. The embedded reader
// is nil and never called; only the two path getters are implemented.
type retiredPathFiles struct {
	*memFiles
	database.RetiredBookFileReader
}

func (r retiredPathFiles) GetBookFileByPath(path string) (*database.BookFile, error) {
	f, err := r.memFiles.GetBookFileByPath(path)
	if f != nil && f.Retired {
		return nil, err
	}
	return f, err
}

func (r retiredPathFiles) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	return r.memFiles.GetBookFileByPath(path)
}

// A retired row that holds the target path's single-owner key is journaled as
// the key's owner. The plain getter cannot see it, so before this the repoint's
// journal named no owner and a revert had nobody to hand the key back to. The
// revert that reads it back is TestRevertRepoint_KeyGoesBackToARetiredOwner
// (plugins/maintenance).
func TestRepointBookFile_JournalsRetiredKeyOwner(t *testing.T) {
	files := &memFiles{rows: map[string]*database.BookFile{
		"f1": {ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b"},
		"r1": {ID: "r1", BookID: "holder", FilePath: "/lib/new.m4b", Retired: true, VersionID: "v1"},
	}}
	journal := &creditFake{credits: map[string][]database.BookAuthor{}}
	w := NewWriter(newMemStore(), newMemStore(), "fx", "bulk_update", "rp-").
		WithJournal(retiredPathFiles{memFiles: files}, journal, "op-1")

	err := w.RepointBookFile("b1", "f1", database.BookFileLocation{Path: "/lib/old.m4b"}, database.BookFileLocation{Path: "/lib/new.m4b"})
	require.NoError(t, err)
	require.Len(t, journal.journal, 1)
	to, err := database.DecodeBookFileLocation(journal.journal[0].NewValue)
	require.NoError(t, err)
	require.Equal(t, "r1", to.KeyOwnerRow)
	require.Equal(t, "holder", to.KeyOwnerBook)
}

// A store without the retired reader falls back to the plain getter, so a live
// key owner is still journaled exactly as before.
func TestRepointBookFile_PlainStoreStillJournalsLiveKeyOwner(t *testing.T) {
	files := &memFiles{rows: map[string]*database.BookFile{
		"f1": {ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b"},
		"f2": {ID: "f2", BookID: "b2", FilePath: "/lib/new.m4b"},
	}}
	journal := &creditFake{credits: map[string][]database.BookAuthor{}}
	w := NewWriter(newMemStore(), newMemStore(), "fx", "bulk_update", "rp-").WithJournal(files, journal, "op-1")

	require.NoError(t, w.RepointBookFile("b1", "f1", database.BookFileLocation{Path: "/lib/old.m4b"}, database.BookFileLocation{Path: "/lib/new.m4b"}))
	require.Len(t, journal.journal, 1)
	to, err := database.DecodeBookFileLocation(journal.journal[0].NewValue)
	require.NoError(t, err)
	require.Equal(t, "f2", to.KeyOwnerRow)
	require.Equal(t, "b2", to.KeyOwnerBook)
}
