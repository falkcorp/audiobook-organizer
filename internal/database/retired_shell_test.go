// file: internal/database/retired_shell_test.go
// version: 1.0.0
// guid: 0f7e3a19-6c2b-4d85-a914-b8e5c1d02f73
// last-edited: 2026-10-24

package database

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// shellFixture is a retiredFixture whose shell has had its one file retired
// onto the holder, so the shell owns no live row and is a retired shell.
func shellFixture(t *testing.T) (*retiredFixture, *BookFile) {
	t.Helper()
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/shell-guard/01.mp3", FileHash: "shell-guard-hash"})
	require.NoError(t, fx.retire(t, x))
	return fx, x
}

// DeleteBook refuses a retired shell although it owns no live row (the owns-files
// count passes it), leaves it in place, and refuses it again until its version
// holds no row.
func TestDeleteBook_RefusesARetiredShell(t *testing.T) {
	fx, _ := shellFixture(t)

	err := fx.ps.DeleteBook(fx.shell.ID)
	require.ErrorIs(t, err, ErrBookIsRetiredShell)
	require.NotErrorIs(t, err, ErrBookOwnsFiles, "the shell owns no live row; the refusal is the shell's")
	b, gerr := fx.ps.GetBookByID(fx.shell.ID)
	require.NoError(t, gerr)
	require.NotNil(t, b, "a refused delete leaves the book")
	rows, gerr := fx.ps.GetRetiredBookFiles(fx.holder.ID)
	require.NoError(t, gerr)
	require.Len(t, rows, 1)

	// A book with no retired version still deletes as before.
	other, cerr := fx.ps.CreateBook(&Book{Title: "Plain", FilePath: "/lib/ret/plain"})
	require.NoError(t, cerr)
	require.NoError(t, fx.ps.DeleteBook(other.ID))
}

// Once the retired rows are put back (the version holds none) the book is no
// shell and DeleteBook's other guard answers for it.
func TestDeleteBook_ShellStopsBeingOneWhenItsRowsComeBack(t *testing.T) {
	fx, x := shellFixture(t)
	require.ErrorIs(t, fx.ps.DeleteBook(fx.shell.ID), ErrBookIsRetiredShell)

	require.NoError(t, fx.ps.UnretireBookFiles([]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}},
		fx.shell.ID, fx.version.ID, map[string]string{}))
	err := fx.ps.DeleteBook(fx.shell.ID)
	require.ErrorIs(t, err, ErrBookOwnsFiles, "its row is live under it again")
	require.NotErrorIs(t, err, ErrBookIsRetiredShell)
}

// The bypass is the seam of the journaled purge: with it set the same delete
// goes through, and the retired rows stay under the holder (nothing deletes a
// book_file row).
func TestDeleteBook_BypassOptionLetsTheJournaledPurgeDeleteAShell(t *testing.T) {
	fx, _ := shellFixture(t)

	require.NoError(t, fx.ps.deleteBook(fx.shell.ID, deleteBookOpts{allowRetiredShell: true}))
	b, err := fx.ps.GetBookByID(fx.shell.ID)
	require.NoError(t, err)
	require.Nil(t, b)
	rows, err := fx.ps.GetRetiredBookFiles(fx.holder.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "no book_file row is deleted")
}

// RevertBookToVersion is a whole-row write: for a retired shell it would bring
// the trash mark back off and clear the merged-into link, so it is refused and
// the row is untouched.
func TestRevertBookToVersion_RefusesARetiredShell(t *testing.T) {
	fx, _ := shellFixture(t)
	title := "Retire Shell edited"
	cur, err := fx.ps.GetBookByID(fx.shell.ID)
	require.NoError(t, err)
	cur.Title = title
	_, err = fx.ps.UpdateBook(fx.shell.ID, cur)
	require.NoError(t, err)
	snaps, err := fx.ps.GetBookSnapshots(fx.shell.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, snaps)

	_, err = fx.ps.RevertBookToVersion(fx.shell.ID, snaps[len(snaps)-1].Timestamp)
	require.ErrorIs(t, err, ErrBookIsRetiredShell)
	after, err := fx.ps.GetBookByID(fx.shell.ID)
	require.NoError(t, err)
	require.Equal(t, title, after.Title, "a refused revert writes nothing")

	// A book that is no shell reverts as before.
	plain, err := fx.ps.CreateBook(&Book{Title: "Plain revert", FilePath: "/lib/ret/plain-revert"})
	require.NoError(t, err)
	plain.Title = "Plain revert edited"
	_, err = fx.ps.UpdateBook(plain.ID, plain)
	require.NoError(t, err)
	psnaps, err := fx.ps.GetBookSnapshots(plain.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, psnaps)
	got, err := fx.ps.RevertBookToVersion(plain.ID, psnaps[len(psnaps)-1].Timestamp)
	require.NoError(t, err)
	require.Equal(t, "Plain revert", got.Title)
}

// bareStore reads retired rows through nothing: no capability, and it does not
// declare that it never holds one.
type bareStore struct{}

// RetiredRowsHeldFor fails closed for a store that cannot read retired rows,
// answers zero only for one that declares it holds none, and reads the real
// store.
func TestRetiredRowsHeldFor_FailsClosedWithoutAReader(t *testing.T) {
	_, err := RetiredRowsHeldFor(bareStore{}, "any")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	err = RefuseRetiredShell(bareStore{}, "any", "purge")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	require.NotErrorIs(t, err, ErrBookIsRetiredShell, "an unreadable store is not a shell")

	n, err := RetiredRowsHeldFor(&MockStore{}, "any")
	require.NoError(t, err)
	require.Zero(t, n)

	fx, _ := shellFixture(t)
	n, err = RetiredRowsHeldFor(fx.ps, fx.shell.ID)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = RetiredRowsHeldFor(fx.ps, fx.holder.ID)
	require.NoError(t, err)
	require.Zero(t, n, "the holder is not a shell")

	// One check behind every caller's refusal: each names its own action and
	// none points at the disabled "restore as a separate book" control.
	for _, action := range []string{"restore", "purge", "discard progress and purge", "hard delete"} {
		err := RefuseRetiredShell(fx.ps, fx.shell.ID, action)
		require.ErrorIs(t, err, ErrBookIsRetiredShell, action)
		require.Contains(t, err.Error(), action)
		require.Contains(t, err.Error(), "leave it in the trash")
		require.Contains(t, err.Error(), "not available yet")
	}
	require.NoError(t, RefuseRetiredShell(fx.ps, fx.holder.ID, "purge"), "the holder is refused by CheckBookOwnsNoFiles, not as a shell")
}

// movingReader reports a version under holder A on its first list and, unless
// flapping, under holder B on every later one, the way a RehomeRetiredVersion
// landing during the count does. A flapping reader alternates on every list so
// no two consecutive reads agree.
type movingReader struct {
	lists    atomic.Int32
	flapping bool
}

func (m *movingReader) GetRetiredVersionsFromBook(string) ([]BookVersion, error) {
	n := m.lists.Add(1)
	holder := "B"
	if n == 1 || (m.flapping && n%2 == 1) {
		holder = "A"
	}
	return []BookVersion{{ID: "v1", BookID: holder}}, nil
}

func (m *movingReader) GetRetiredBookFiles(holder string) ([]BookFile, error) {
	return []BookFile{{ID: "f-" + holder, VersionID: "v1", Retired: true}}, nil
}

// A version that moves holder while its rows are counted is counted again; one
// that never settles is refused, not guessed.
func TestRetiredRowsHeld_RecountsAMovedVersionAndRefusesAnUnsettledOne(t *testing.T) {
	settles := &movingReader{}
	n, err := retiredRowsHeld(settles, "shell")
	require.NoError(t, err)
	require.Equal(t, 1, n, "counted again once the version stopped moving")
	require.Greater(t, int(settles.lists.Load()), 2)

	_, err = retiredRowsHeld(&movingReader{flapping: true}, "shell")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrBookIsRetiredShell))
}
