// file: internal/plugins/maintenance/itunes_clone_rollback_retired_test.go
// version: 1.0.0
// guid: b7c3e920-4d15-4a8f-9e62-f0a1d8c5b374
// last-edited: 2026-10-24

package maintenance

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireRowOnto creates a live row for shellID at path (or retires an existing
// one when rowID is given) and retires it onto holderID under a retired version
// made from shellID.
func retireRowOnto(t *testing.T, s *database.PebbleStore, shellID, holderID, rowID, path string) {
	t.Helper()
	if path != "" {
		require.NoError(t, s.CreateBookFile(&database.BookFile{ID: rowID, BookID: shellID, FilePath: path, FileSize: 10}))
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	vid := "rv-" + shellID + "-" + rowID
	_, err := s.CreateBookVersion(&database.BookVersion{ID: vid, BookID: holderID, Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: shellID, RetireOpID: "op-test", RetiredAt: &at})
	require.NoError(t, err)
	require.NoError(t, s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rowID}, SourceBookID: shellID}}, holderID, vid))
}

// A rollback does not delete a clone the retired-versions repair has since
// turned into a shell (its rows re-keyed onto the source as a retired
// version): deleting the book drops the only record of where those versions
// came from. It is refused before anything is written: the clone, its file and
// the clone record stay, and the source is not touched.
func TestITunesClone_RollbackRefusesARetiredShellClone(t *testing.T) {
	f := newICFixture(t)
	cloneID, _ := applyCloneWithProgress(t, f)
	rows, err := f.s.GetBookFiles(cloneID)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	clonePath := rows[0].FilePath
	for _, r := range rows {
		retireRowOnto(t, f.s, cloneID, "T", r.ID, "")
	}
	srcBefore, err := f.s.GetBookByID("T")
	require.NoError(t, err)

	g := icGroup(t, f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeFailed, g.Outcome)
	require.Contains(t, g.Error, "retired version")
	clone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.NotNil(t, clone, "the clone book is not deleted")
	require.FileExists(t, clonePath, "the file a retired row names stays")
	rec, err := (&icRunner{store: icStore{OpsStore: f.s}}).loadRecord("vg-t")
	require.NoError(t, err)
	require.NotNil(t, rec, "the record is kept")
	srcAfter, err := f.s.GetBookByID("T")
	require.NoError(t, err)
	require.Equal(t, srcBefore.LibraryState, srcAfter.LibraryState, "the source's state was not restored")
}

// A clone file a retired row (of any other book) names is left on disk by the
// rollback's file removal, and the group says so; the clone's own file, which
// nothing else names, is still removed (the control).
func TestITunesClone_RollbackKeepsAFileARetiredRowNames(t *testing.T) {
	f := newICFixture(t)
	cloneID, _ := applyCloneWithProgress(t, f)
	rows, err := f.s.GetBookFiles(cloneID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	clonePath := rows[0].FilePath

	// Another book's row at the same path, retired onto "T".
	_, err = f.s.CreateBook(&database.Book{ID: "O", Title: "Other", FilePath: filepath.Join(f.root, "other")})
	require.NoError(t, err)
	retireRowOnto(t, f.s, "O", "T", "o-row", clonePath)

	g := icGroup(t, f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeRolled, g.Outcome, g.Error)
	require.Contains(t, g.Error, "clone files kept")
	require.FileExists(t, clonePath, "the file a retired row names is not removed")
}
