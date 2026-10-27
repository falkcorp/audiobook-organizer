// file: internal/merge/trash_restore_retired_test.go
// version: 1.1.1
// guid: 7a1e5d93-2c48-4f06-b3a9-0d6c82f1e574
// last-edited: 2026-10-27

package merge

import (
	"errors"
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
// rows" that must NOT: the file list RestoreFromTrashLocked reads feeds
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

	res, err := restoreFromTrash(f.S, holder, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, res.Restored)
	ctl, err := restoreFromTrash(f.S, control, nil, nil, nil)
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

// retiredLookupDownStore makes the retired-version lookup fail, the way a store
// that cannot read retired rows does.
type retiredLookupDownStore struct{ *database.PebbleStore }

func (retiredLookupDownStore) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, errors.New("injected lookup failure")
}

// Restoring a trashed SHELL (a book whose files were retired into a holder) is
// refused with database.ErrBookIsRetiredShell and writes nothing: the shell
// stays in the trash and its retired rows stay under the holder. Without the
// refusal the restore wrote library_state=imported on a book with no live
// files, which ABS never lists, and took it out of the trash.
//
// Controls: a trashed book with no retired version restores; so does the same
// shell once its retired version holds no rows (they were un-retired), so the
// refusal is specific to a version that still holds rows. A failing lookup
// refuses too (fail closed) rather than restoring blind.
func TestRestoreFromTrash_RefusesRetiredShell(t *testing.T) {
	f := combineUndoFixture(t)
	holder := f.Book(t, vptest.Spec{ID: "holder", Primary: "nil"})
	shell := f.Book(t, vptest.Spec{ID: "shell", Primary: "nil"})
	control := f.Book(t, vptest.Spec{ID: "control", Primary: "nil"})
	ver := retireShellInto(t, f, holder, shell)
	f.SoftDelete(t, shell)
	f.SoftDelete(t, control)

	res, err := restoreFromTrash(f.S, shell, nil, nil, nil)
	require.ErrorIs(t, err, database.ErrBookIsRetiredShell)
	// The message must not send the user to a control that does not work yet
	// ("Restore as separate book" is disabled and its route answers 501): it
	// says what to do instead and that the files are safe.
	require.NotContains(t, err.Error(), "Restore as separate book")
	require.Contains(t, err.Error(), "leave it in the trash")
	require.Contains(t, err.Error(), "not available yet")
	require.False(t, res.Restored)
	got, err := f.S.GetBookByID(shell)
	require.NoError(t, err)
	require.True(t, database.IsInTrash(got), "a refused restore leaves the shell in the trash")
	retired, err := f.S.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, retired, 1, "the retired row stays under the holder")

	// Fail closed: the same call with the lookup down is an error, not a restore.
	_, err = restoreFromTrash(retiredLookupDownStore{f.S}, shell, nil, nil, nil)
	require.Error(t, err)
	require.False(t, errors.Is(err, database.ErrBookIsRetiredShell))
	got, err = f.S.GetBookByID(shell)
	require.NoError(t, err)
	require.True(t, database.IsInTrash(got))

	// Control 1: a trashed book that is nobody's shell restores.
	ctl, err := restoreFromTrash(f.S, control, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, ctl.Restored)

	// Control 2: with the version's rows un-retired the shell is restorable.
	require.NoError(t, f.S.UnretireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{"bf-" + shell}, SourceBookID: holder}}, shell, ver.ID, nil))
	again, err := restoreFromTrash(f.S, shell, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, again.Restored)
}
