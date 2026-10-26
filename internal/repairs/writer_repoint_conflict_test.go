// file: internal/repairs/writer_repoint_conflict_test.go
// version: 1.0.0
// guid: 25efa569-e8e9-4a4f-a9e6-552f22bd5863
// last-edited: 2026-10-26

package repairs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// failingModifyFiles is a book_file store whose ModifyBookFile runs the
// callback (so the write is refused AFTER the writer journaled it) and then
// fails with err, writing nothing.
type failingModifyFiles struct {
	*memFiles
	err error
}

func (f *failingModifyFiles) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	cp := database.BookFile{}
	if r, _ := f.memFiles.GetBookFileByID(bookID, fileID); r != nil {
		cp = *r
	}
	if err := fn(&cp); err != nil {
		return nil, err
	}
	return nil, f.err
}

func repointFixture(t *testing.T, modifyErr error) (*Writer, *creditFake, *memFiles, database.BookFileLocation, database.BookFileLocation) {
	t.Helper()
	files := &memFiles{rows: map[string]*database.BookFile{
		"bf-1": {ID: "bf-1", BookID: "b1", FilePath: "/lib/Gotham/Year One/01.m4b", Missing: true},
	}}
	journal := &creditFake{credits: map[string][]database.BookAuthor{}}
	w := NewWriter(newMemStore(), newMemStore(), "junk", "bulk_update", "rp-").
		WithJournal(&failingModifyFiles{memFiles: files, err: modifyErr}, journal, "op-1")
	from := database.BookFileLocation{Path: "/lib/Gotham/Year One/01.m4b", Missing: true}
	to := database.BookFileLocation{Path: "/lib/Gotham/Year One/Rebuilt/01.m4b"}
	return w, journal, files, from, to
}

// The store giving up on a row that other writers kept changing
// (database.ErrBookFileConflict) wrote nothing, so the row the writer journaled
// first is voided: a live row would claim a repoint that never happened.
func TestWriter_RepointBookFile_ConflictVoidsItsJournalRow(t *testing.T) {
	w, journal, files, from, to := repointFixture(t, &database.BookFileConflictError{BookID: "b1", FileID: "bf-1", Attempts: 5})

	err := w.RepointBookFile("b1", "bf-1", from, to)
	require.ErrorIs(t, err, database.ErrBookFileConflict)
	require.Equal(t, from.Path, files.rows["bf-1"].FilePath, "nothing was written")
	require.Len(t, journal.journal, 1)
	require.True(t, journal.journal[0].Voided, "the row for a repoint that did not happen is voided")
	require.NotNil(t, journal.journal[0].RevertedAt)
	require.Zero(t, w.writes.Load())
}

// An error that does not prove nothing was written keeps its row: JOURNAL
// FIRST, a commit of unknown outcome may have been applied.
func TestWriter_RepointBookFile_UnknownErrorKeepsItsJournalRow(t *testing.T) {
	w, journal, _, from, to := repointFixture(t, errors.New("disk on fire"))

	require.Error(t, w.RepointBookFile("b1", "bf-1", from, to))
	require.Len(t, journal.journal, 1)
	require.False(t, journal.journal[0].Voided, "an unproven write's row stands")
}
