// file: internal/database/retired_shell_test.go
// version: 1.1.0
// guid: 0f7e3a19-6c2b-4d85-a914-b8e5c1d02f73
// last-edited: 2026-10-29

package database

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

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

// twoReads exposes only the two reads of retiredShellReader, so retiredRowsHeld
// takes its generic two-read path instead of the store's own count.
type twoReads struct{ ps *PebbleStore }

func (r twoReads) GetRetiredVersionsFromBook(id string) ([]BookVersion, error) {
	return r.ps.GetRetiredVersionsFromBook(id)
}

func (r twoReads) GetRetiredBookFiles(id string) ([]BookFile, error) {
	return r.ps.GetRetiredBookFiles(id)
}

// CountRetiredRowsHeldFor is the same number the generic two-read count gives,
// for a book with no versions, a shell with rows under two holders, a shell
// whose version was emptied, and a holder.
func TestCountRetiredRowsHeldFor_MatchesTheGenericCount(t *testing.T) {
	fx := newRetiredFixture(t)
	a := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/count/01.mp3", FileHash: "h-a", TrackNumber: 1})
	b := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/count/02.mp3", FileHash: "h-b", TrackNumber: 2})
	require.NoError(t, fx.retire(t, a, b))
	// A live row on the holder is not counted, and neither is a retired row of
	// another version.
	fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/Gotham/count/holder.mp3", FileHash: "h-live", TrackNumber: 1})

	second, err := fx.ps.CreateBook(&Book{Title: "Year Two", FilePath: "/lib/Gotham/count/second"})
	require.NoError(t, err)
	v2, err := fx.ps.CreateBookVersion(&BookVersion{
		BookID: second.ID, Status: BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: fx.shell.ID, RetireOpID: "op-2", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	c := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/count/03.mp3", FileHash: "h-c", TrackNumber: 3})
	require.NoError(t, fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{c.ID}, SourceBookID: fx.shell.ID}}, second.ID, v2.ID))

	check := func(label, id string, want int) {
		t.Helper()
		got, err := fx.ps.CountRetiredRowsHeldFor(id)
		require.NoError(t, err, label)
		require.Equal(t, want, got, label+": narrow count")
		generic, err := retiredRowsHeld(twoReads{fx.ps}, id)
		require.NoError(t, err, label)
		require.Equal(t, want, generic, label+": generic count")
		viaStore, err := RetiredRowsHeldFor(fx.ps, id)
		require.NoError(t, err, label)
		require.Equal(t, want, viaStore, label+": through RetiredRowsHeldFor")
	}
	check("shell with rows under two holders", fx.shell.ID, 3)
	check("holder", fx.holder.ID, 0)
	check("a book with no versions", second.ID, 0)
	check("an unknown book", "no-such-book", 0)
	check("empty id", "", 0)

	// Empty one version: the other still counts.
	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{c.ID}, SourceBookID: second.ID}}, fx.shell.ID, v2.ID, nil))
	check("one version emptied", fx.shell.ID, 2)
	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{a.ID, b.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
	check("every version emptied", fx.shell.ID, 0)
	vs, err := fx.ps.GetRetiredVersionsFromBook(fx.shell.ID)
	require.NoError(t, err)
	require.Len(t, vs, 2, "the emptied versions are still retired versions of the shell")
}

// failingVersions embeds the Pebble store, so the store's own narrow count is
// promoted onto it, and overrides the version read to fail.
type failingVersions struct{ *PebbleStore }

func (failingVersions) GetRetiredVersionsFromBook(string) ([]BookVersion, error) {
	return nil, errors.New("injected version read failure")
}

// A decorator's override of the retired reads is honoured: the narrow count is
// taken only by the bare Pebble store, not by a type that embeds it, so a
// failing (or filtering) wrapper is not bypassed and the check fails closed.
func TestRetiredRowsHeldFor_HonoursADecoratorsOverride(t *testing.T) {
	fx, _ := shellFixture(t)
	_, err := RetiredRowsHeldFor(failingVersions{fx.ps}, fx.shell.ID)
	require.Error(t, err)
	require.ErrorContains(t, err, "injected version read failure")
	err = RefuseRetiredShell(failingVersions{fx.ps}, fx.shell.ID, "purge")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBookIsRetiredShell, "an unreadable store is not proof of a shell")
}
