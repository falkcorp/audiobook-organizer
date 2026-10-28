// file: internal/versions/purge_owner_spelling_test.go
// version: 1.0.0
// guid: 4c9e2b7a-1d53-4f08-a6e4-8b0d3c71f952
// last-edited: 2026-10-28

package versions

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The owner checks (another book_file row, another book row) match path
// STRINGS exactly, as the stores do. A library reached through a symlinked root
// has two spellings of every file, and a row written with the other spelling
// from the one the purge holds is an owner the check must still find. Fixtures
// are Batman titles and characters; the symlinked root is a pair of folders
// under t.TempDir().

// symlinkedLibrary returns a real library folder (tank) and a symlink to it
// (mnt), both under a canonical temp directory (so tank is exactly what
// canonPath returns for a path through mnt).
func symlinkedLibrary(t *testing.T) (tank, mnt string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tank = filepath.Join(base, "tank")
	if err := os.MkdirAll(tank, 0o755); err != nil {
		t.Fatal(err)
	}
	mnt = filepath.Join(base, "mnt")
	if err := os.Symlink(tank, mnt); err != nil {
		t.Fatal(err)
	}
	return tank, mnt
}

// retireRowAt gives the book a retired row naming path: a row of a shell book
// that was retired into the holder (the shape of a merged-away book's files).
func retireRowAt(t *testing.T, store *database.PebbleStore, holderID, path string) {
	t.Helper()
	shell, err := store.CreateBook(&database.Book{Title: "Shell Of Harvey Dent", FilePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	row := &database.BookFile{BookID: shell.ID, FilePath: path, TrackNumber: 1}
	if err := store.CreateBookFile(row); err != nil {
		t.Fatal(err)
	}
	retiredAt := time.Now()
	rv, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &retiredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holderID, rv.ID); err != nil {
		t.Fatal(err)
	}
}

// spellingFixture is a servedFixture whose book folder is reached through the
// symlinked root: bookLink is the spelling the book (and so the slot walk) uses,
// bookReal the other one.
type spellingFixture struct {
	*servedFixture
	bookLink, bookReal string
}

func newSpellingFixture(t *testing.T) *spellingFixture {
	t.Helper()
	f := newServedFixture(t)
	tank, mnt := symlinkedLibrary(t)
	sf := &spellingFixture{
		servedFixture: f,
		bookLink:      filepath.Join(mnt, "Gotham", "Batman Hush"),
		bookReal:      filepath.Join(tank, "Gotham", "Batman Hush"),
	}
	writeServed(t, filepath.Join(sf.bookReal, "hush.m4b"))
	f.setBookFilePath(filepath.Join(sf.bookLink, "hush.m4b"))
	return sf
}

// slotFile is a file in the version's slot, written once, as both spellings.
func (sf *spellingFixture) slotFile(v *database.BookVersion, name string) (link, real string) {
	sf.t.Helper()
	real = filepath.Join(VersionSlotDir(sf.bookReal, v.ID), name)
	writeServed(sf.t, real)
	return filepath.Join(VersionSlotDir(sf.bookLink, v.ID), name), real
}

func (sf *spellingFixture) purge(v *database.BookVersion) error {
	sf.t.Helper()
	got, err := sf.store.GetBookVersion(v.ID)
	if err != nil || got == nil {
		sf.t.Fatalf("get version: %v", err)
	}
	return PurgeVersion(sf.store, got, testGuard)
}

func (sf *spellingFixture) requirePurgedWithFileKept(v *database.BookVersion, kept string) {
	sf.t.Helper()
	if err := sf.purge(v); err != nil {
		sf.t.Fatalf("PurgeVersion: %v", err)
	}
	sf.requireAllOnDisk(kept)
	if got := sf.status(v.ID); got != database.BookVersionStatusInactivePurged {
		sf.t.Fatalf("status = %s, want inactive_purged (the version is purged, the file stays)", got)
	}
}

// SF1: the version's row is stored with the symlink spelling, a retired row names
// the same file with the real one. The lookup on the spelling the purge holds
// finds nothing; the owner is found only on the resolved spelling.
func TestPurgeKeepsFile_RetiredRowUsesTheOtherSpelling(t *testing.T) {
	sf := newSpellingFixture(t)
	active := sf.version(database.BookVersionStatusActive, 10*day)
	sf.row(active.ID, sf.book.FilePath, 1)
	alt := sf.version(database.BookVersionStatusAlt, 30*day)
	link, real := sf.slotFile(alt, "hush-alt.m4b")
	sf.row(alt.ID, link, 1)
	retireRowAt(t, sf.store, sf.book.ID, real)
	sf.trashLikeHandler(alt.ID)

	sf.requirePurgedWithFileKept(alt, real)
}

// SF1: the own book is in the trash, so it serves nothing and the active
// version's row is an owner like any other row. It names the file with the other
// spelling.
func TestPurgeKeepsFile_ActiveVersionRowUsesTheOtherSpelling_OwnBookTrashed(t *testing.T) {
	sf := newSpellingFixture(t)
	active := sf.version(database.BookVersionStatusActive, 10*day)
	alt := sf.version(database.BookVersionStatusAlt, 30*day)
	link, real := sf.slotFile(alt, "hush-alt.m4b")
	sf.row(active.ID, real, 2)
	sf.row(alt.ID, link, 1)
	sf.trashLikeHandler(alt.ID)
	sf.trashBook()

	sf.requirePurgedWithFileKept(alt, real)
}

// SF1: a book in the trash whose FilePath is the real spelling of the file
// (otherBooksNamingPath, which asks the book rows rather than the file rows).
func TestPurgeKeepsFile_TrashedBookFilePathUsesTheOtherSpelling(t *testing.T) {
	sf := newSpellingFixture(t)
	alt := sf.version(database.BookVersionStatusAlt, 30*day)
	link, real := sf.slotFile(alt, "hush-alt.m4b")
	sf.row(alt.ID, link, 1)
	yes := true
	if _, err := sf.store.CreateBook(&database.Book{ID: "knightfall", Title: "Batman: Knightfall", FilePath: real, MarkedForDeletion: &yes}); err != nil {
		t.Fatal(err)
	}
	sf.trashLikeHandler(alt.ID)
	sf.trashBook()

	sf.requirePurgedWithFileKept(alt, real)
}

// Control: with nothing naming the file under either spelling, the purge removes
// it, so the three tests above fail for the owner and not for a fixture reason.
func TestPurgeRemovesFile_NoOwnerUnderEitherSpelling(t *testing.T) {
	sf := newSpellingFixture(t)
	alt := sf.version(database.BookVersionStatusAlt, 30*day)
	link, real := sf.slotFile(alt, "hush-alt.m4b")
	sf.row(alt.ID, link, 1)
	sf.trashLikeHandler(alt.ID)

	if err := sf.purge(alt); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	if onDisk(real) {
		t.Fatal("a file nothing else names was kept")
	}
}

// NIT a: a file the slot walk reaches through the symlinked root and a row
// names through the real path is one target, not two. The active version's row
// serves it, so the purge is refused and the refusal names it once.
func TestPurgeTargetsAreDedupedAcrossSpellings(t *testing.T) {
	sf := newSpellingFixture(t)
	active := sf.version(database.BookVersionStatusActive, 10*day)
	alt := sf.version(database.BookVersionStatusAlt, 30*day)
	_, real := sf.slotFile(alt, "hush-alt.m4b")
	sf.row(active.ID, real, 2)
	sf.row(alt.ID, real, 1) // the row spells it the real way; the slot walk the link way
	sf.trashLikeHandler(alt.ID)

	err := sf.purge(alt)
	var served *BookServesFilesError
	if !errors.As(err, &served) {
		t.Fatalf("PurgeVersion = %v, want *BookServesFilesError", err)
	}
	if len(served.Paths) != 1 {
		t.Fatalf("served paths = %v, want the one file named once", served.Paths)
	}
	sf.requireAllOnDisk(real)
}

// SF4: the own book is in the trash, the version's row names a file OUTSIDE its
// slot, and only a retired row (of the same book, another version) names it too:
// the file is kept.
func TestPurgeVersion_KeepsAFileOutsideTheSlotOnlyARetiredRowNames(t *testing.T) {
	f := newSharedPathFixture(t)
	outside := filepath.Join(t.TempDir(), "Gotham", "outside.m4b")
	writeServed(t, outside)
	if err := f.store.CreateBookFile(&database.BookFile{
		BookID: f.holderID, FilePath: outside, VersionID: f.trashed.ID, TrackNumber: 30,
	}); err != nil {
		t.Fatal(err)
	}
	retireRowAt(t, f.store, f.holderID, outside)
	f.trashHolder(t) // after the retire: a retire into a book in the trash is refused

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, outside)
	requireGone(t, f.ownOnly)
}

// SF2: the own live book comes to serve a slot file (its FilePath is re-pointed)
// after the read-only pass said nothing serves it and before the unlink. The
// guard is asked again at the unlink and must see the book as it is then.
func TestPurgeUnlinkTimeGuardSeesTheBookAsItIsAfterTheClaim(t *testing.T) {
	f := newServedFixture(t)
	v, slotFile := f.altWithSlotFile("alt.m4b")
	f.trashLikeHandler(v.ID)
	store := &claimHookStore{PebbleStore: f.store, hook: func() { f.setBookFilePath(slotFile) }}

	got, _ := f.store.GetBookVersion(v.ID)
	if err := PurgeVersion(store, got, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	if !store.done {
		t.Fatal("the claim hook never ran, so the test proves nothing")
	}
	f.requireAllOnDisk(slotFile)
}

// failBooksAfterClaimStore makes GetBookByID fail once the version is claimed.
type failBooksAfterClaimStore struct {
	claimHookStore
	failing atomic.Bool
}

var errBookGoneMidPurge = errors.New("injected: book unreadable after the claim")

func (s *failBooksAfterClaimStore) GetBookByID(id string) (*database.Book, error) {
	if s.failing.Load() {
		return nil, errBookGoneMidPurge
	}
	return s.PebbleStore.GetBookByID(id)
}

// SF2: when the guard cannot be rebuilt after the claim, nothing is unlinked,
// the error says why, and the version stays purging so the next night retries.
func TestPurgeRebuildErrorAfterClaimUnlinksNothing(t *testing.T) {
	f := newServedFixture(t)
	v, slotFile := f.altWithSlotFile("alt.m4b")
	f.trashLikeHandler(v.ID)
	store := &failBooksAfterClaimStore{}
	store.claimHookStore = claimHookStore{PebbleStore: f.store, hook: func() { store.failing.Store(true) }}

	got, _ := f.store.GetBookVersion(v.ID)
	err := PurgeVersion(store, got, testGuard)
	if !errors.Is(err, errBookGoneMidPurge) {
		t.Fatalf("PurgeVersion = %v, want an error wrapping %v", err, errBookGoneMidPurge)
	}
	f.requireAllOnDisk(slotFile)
	if st := f.status(v.ID); st != database.BookVersionStatusPurging {
		t.Fatalf("status = %s, want purging (claimed, retried next night)", st)
	}
}

// goneBookStore reports one book's row as gone (a hard delete that landed while
// its versions' rows were still there).
type goneBookStore struct {
	*database.PebbleStore
	id string
}

func (s goneBookStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.id {
		return nil, nil
	}
	return s.PebbleStore.GetBookByID(id)
}

// NIT d: the version's book row is gone, so it serves nothing, and another LIVE
// book serves one of the version's files. That file alone is what the refusal
// names: the version's other file, which nothing serves, is not "served by the
// own book" just because the own book's row is missing.
func TestPurgeKeepsFileAnotherLiveBookServes_WhenOwnBookRowIsGone(t *testing.T) {
	f := newServedFixture(t)
	shared := filepath.Join(t.TempDir(), "deluge", "shared.m4b")
	writeServed(t, shared)
	f.otherBook("other", shared, true, true)
	unshared := filepath.Join(t.TempDir(), "Gotham", "unshared.m4b")
	writeServed(t, unshared)
	v := f.version(database.BookVersionStatusAlt, 30*day)
	f.row(v.ID, shared, 1)
	f.row(v.ID, unshared, 2)
	f.trashLikeHandler(v.ID)

	got, _ := f.store.GetBookVersion(v.ID)
	err := PurgeVersion(goneBookStore{f.store, f.book.ID}, got, testGuard)
	var served *BookServesFilesError
	if !errors.As(err, &served) {
		t.Fatalf("PurgeVersion = %v, want *BookServesFilesError", err)
	}
	if len(served.Paths) != 1 || served.Paths[0] != shared {
		t.Fatalf("served paths = %v, want only %s", served.Paths, shared)
	}
	f.requireAllOnDisk(shared, unshared)
	if st := f.status(v.ID); st != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash", st)
	}
}
