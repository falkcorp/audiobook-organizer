// file: internal/merge/trash_restore_retired_test.go
// version: 1.0.0
// guid: 7a1e5d93-2c48-4f06-b3a9-0d6c82f1e574
// last-edited: 2026-10-23

package merge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// retireShellInto retires the file row of the book shell (seeded by vptest as
// bf-<shell>) into holder and returns the retired version.
func retireShellInto(t *testing.T, f *vptest.Fixture, holder, shell string) *database.BookVersion {
	t.Helper()
	retiredAt := time.Now()
	ver, err := f.S.CreateBookVersion(&database.BookVersion{
		BookID: holder, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-" + shell, RetiredAt: &retiredAt,
	})
	require.NoError(t, err)
	require.NoError(t, f.S.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{"bf-" + shell}, SourceBookID: shell}}, holder, ver.ID))
	return ver
}

// Restoring a trashed holder of retired versions keeps its retired versions
// exactly as they are, and decides the restored row's library state and ABS
// listability from its LIVE file rows only.
//
// This is the one reader the retired-versions census listed as "include retired
// rows" that must NOT: the file list RestoreFromTrash reads feeds
// RestoreLibraryStateFromTrash and RestoredRowIsABSListable, which demote a row
// to "imported" and keep its merge redirect when ANY row sits outside the
// library folder. A retired row is a version of another book and routinely does
// (iTunes paths, /newbooks), so counting it would take a holder whose own audio
// is organized out of ABS the moment it is restored. The retired rows need no
// carrying: they are keyed under the holder and a restore never moves a row.
//
// The control is the same holder with no retired version, which must come back
// "organized"; without it the assertion could pass because nothing was
// organized to begin with.
func TestRestoreFromTrash_HolderKeepsRetiredVersionsAndStaysOrganized(t *testing.T) {
	f := combineUndoFixture(t)
	holder := f.Book(t, vptest.Spec{ID: "holder", Primary: "nil"})
	shell := f.Book(t, vptest.Spec{ID: "shell", Outside: true, Primary: "nil"})
	ver := retireShellInto(t, f, holder, shell)
	retiredBefore, err := f.S.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, retiredBefore, 1, "fixture: one retired row under the holder")
	require.False(t, retiredBefore[0].Missing)

	control := f.Book(t, vptest.Spec{ID: "control", Primary: "nil"})

	f.SoftDelete(t, holder)
	f.SoftDelete(t, control)

	res, err := RestoreFromTrash(f.S, holder, nil, nil)
	require.NoError(t, err)
	require.True(t, res.Restored)
	ctl, err := RestoreFromTrash(f.S, control, nil, nil)
	require.NoError(t, err)
	require.True(t, ctl.Restored)

	require.NotNil(t, ctl.Book.LibraryState)
	require.Equal(t, "organized", *ctl.Book.LibraryState, "control: a trashed organized book with no retired version comes back organized")
	require.NotNil(t, res.Book.LibraryState)
	require.Equal(t, "organized", *res.Book.LibraryState,
		"a retired row outside the library folder must not demote a holder whose own file is organized")

	// The retired version is carried through the restore untouched.
	retiredAfter, err := f.S.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, retiredAfter, 1)
	require.Equal(t, retiredBefore[0].ID, retiredAfter[0].ID)
	require.True(t, retiredAfter[0].Retired)
	require.Equal(t, ver.ID, retiredAfter[0].VersionID)
	gotVer, err := f.S.GetBookVersion(ver.ID)
	require.NoError(t, err)
	require.Equal(t, database.BookVersionStatusRetired, gotVer.Status)
	require.Equal(t, holder, gotVer.BookID)
	live, err := f.S.GetBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, live, 1, "the retired row is still not one of the holder's own files")
}
