// file: internal/merge/itunes_guard_test.go
// version: 1.1.0
// guid: 8d025d9c-5d1a-4c6a-b6d3-6c88a9739dd6
// last-edited: 2026-09-13

package merge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Neutral example roots. The guard's protected roots are the folder holding
// itunes.library_read_path and itunes.media_root.
const (
	testITunesLibFolder = "/srv/library/iTunes"
	testITunesMedia     = "/srv/media/Audiobooks"
)

var testITunesRoots = []string{testITunesLibFolder, testITunesMedia}

// withITunesConfig swaps the global iTunes config for one test.
func withITunesConfig(t *testing.T, fn func(c *config.ITunesConfig)) {
	t.Helper()
	prev := config.Snapshot().ITunes
	config.Mutate(func(c *config.Config) { fn(&c.ITunes) })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.ITunes = prev }) })
}

type fakeGuardStore struct {
	books    map[string]*database.Book
	files    map[string][]database.BookFile
	bookErr  error
	filesErr error
}

func (f *fakeGuardStore) GetBookByID(id string) (*database.Book, error) {
	if f.bookErr != nil {
		return nil, f.bookErr
	}
	return f.books[id], nil
}

func (f *fakeGuardStore) GetBookFiles(id string) ([]database.BookFile, error) {
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	return f.files[id], nil
}

// oneFileStore: book "safe" has a file outside every root; book "probe" has a
// single book_file at probePath (and no FilePath of its own).
func oneFileStore(probePath string) *fakeGuardStore {
	return &fakeGuardStore{
		books: map[string]*database.Book{
			"safe":  {ID: "safe", FilePath: "/srv/managed/Safe/safe.m4b"},
			"probe": {ID: "probe"},
		},
		files: map[string][]database.BookFile{
			"safe":  {{ID: "f1", BookID: "safe", FilePath: "/srv/managed/Safe/safe.m4b"}},
			"probe": {{ID: "f2", BookID: "probe", FilePath: probePath}},
		},
	}
}

func TestGuardITunesProtected_PathTable(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		roots   []string
		refused bool
	}{
		{"inside media root", testITunesMedia + "/Author/Book.m4b", testITunesRoots, true},
		{"inside library-file folder", testITunesLibFolder + "/iTunes Media/Book.m4b", testITunesRoots, true},
		{"the root itself", testITunesMedia, testITunesRoots, true},
		{"outside every root", "/srv/managed/Author/Book.m4b", testITunesRoots, false},
		{"sibling folder sharing the prefix", testITunesMedia + "2/Author/Book.m4b", testITunesRoots, false},
		{"sibling of a books/itunes root", "/data/books/itunes2/Book.m4b", []string{"/data/books/itunes"}, false},
		{"dot-dot climbs out of the root", testITunesMedia + "/../Other/Book.m4b", testITunesRoots, false},
		{"dot-dot climbs into the root", "/srv/managed/../media/Audiobooks/Book.m4b", testITunesRoots, true},
		{"relative path cannot be proven outside", "Audiobooks/Author/Book.m4b", testITunesRoots, true},
		{"frozen books/itunes segment with no configured roots", "/mnt/pool/books/itunes/Book.m4b", nil, true},
		{"empty config, ordinary path", "/srv/managed/Author/Book.m4b", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardITunesProtected(oneFileStore(tc.path), []string{"safe", "probe"}, tc.roots)
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrITunesProtected)
			var pe *ITunesProtectedError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, "probe", pe.BookID)
			assert.Equal(t, tc.path, pe.Path)
			assert.True(t, IsRefusal(err))
		})
	}
}

// The P0 shape: a book with FilePath set and no book_file rows. Checking only
// book_file rows would let it through.
func TestGuardITunesProtected_ChecksBookFilePathWithoutRows(t *testing.T) {
	s := &fakeGuardStore{books: map[string]*database.Book{
		"shell": {ID: "shell", FilePath: testITunesMedia + "/Author/Book.m4b"},
	}}
	err := guardITunesProtected(s, []string{"shell"}, testITunesRoots)
	require.ErrorIs(t, err, ErrITunesProtected)
}

// Fail closed: an unreadable book or file list is a refusal, never a pass.
func TestGuardITunesProtected_FailsClosedOnReadError(t *testing.T) {
	boom := errors.New("store unavailable")

	s := oneFileStore("/srv/managed/x.m4b")
	s.filesErr = boom
	err := guardITunesProtected(s, []string{"safe"}, testITunesRoots)
	require.ErrorIs(t, err, ErrITunesProtected)
	require.ErrorIs(t, err, boom, "the store failure must stay visible")

	s = oneFileStore("/srv/managed/x.m4b")
	s.bookErr = boom
	err = guardITunesProtected(s, []string{"safe"}, testITunesRoots)
	require.ErrorIs(t, err, ErrITunesProtected)
	require.ErrorIs(t, err, boom)
}

func TestGuardITunesProtected_MissingBookIsLeftToCaller(t *testing.T) {
	require.NoError(t, guardITunesProtected(&fakeGuardStore{}, []string{"gone"}, testITunesRoots))
}

func TestITunesProtectedRoots(t *testing.T) {
	roots, err := ITunesProtectedRoots(config.ITunesConfig{
		LibraryReadPath: testITunesLibFolder + "/iTunes Library.xml",
		MediaRoot:       testITunesMedia + "/",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{testITunesLibFolder, testITunesMedia}, roots)

	// Empty config with sync OFF: no configured roots, merges allowed (the
	// books/itunes/ segment match still applies).
	roots, err = ITunesProtectedRoots(config.ITunesConfig{})
	require.NoError(t, err)
	assert.Empty(t, roots)

	// Empty config with sync ON: refuse — a live library at an unknown place.
	_, err = ITunesProtectedRoots(config.ITunesConfig{SyncEnabled: true})
	require.ErrorIs(t, err, ErrITunesProtected)
	assert.True(t, IsRefusal(err))
}

func TestGuardITunesProtected_SyncOnEmptyPathsRefusesEveryMerge(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) {
		c.SyncEnabled = true
		c.LibraryReadPath = ""
		c.MediaRoot = ""
	})
	err := GuardITunesProtected(oneFileStore("/srv/managed/x.m4b"), []string{"safe"})
	require.ErrorIs(t, err, ErrITunesProtected)
}

// --- entry points: refusal with no writes ---------------------------------

// writeTrapStore forwards reads to a real store and fails the test on any
// write a merge could make.
type writeTrapStore struct {
	database.Store
	t *testing.T
}

func (w *writeTrapStore) trip(op string) error {
	w.t.Errorf("%s reached the store after an iTunes refusal", op)
	return fmt.Errorf("write trap: %s", op)
}
func (w *writeTrapStore) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, w.trip("UpdateBook")
}
func (w *writeTrapStore) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, w.trip("ModifyBook")
}
func (w *writeTrapStore) DeleteBook(string) error { return w.trip("DeleteBook") }
func (w *writeTrapStore) RecomputeBookAggregates(string) error {
	return w.trip("RecomputeBookAggregates")
}
func (w *writeTrapStore) CreateBookFile(*database.BookFile) error {
	return w.trip("CreateBookFile")
}
func (w *writeTrapStore) MoveBookFilesToBook([]string, string, string) error {
	return w.trip("MoveBookFilesToBook")
}
func (w *writeTrapStore) MoveBookFilesToBookBulk([]database.BookFileMove, string) error {
	return w.trip("MoveBookFilesToBookBulk")
}
func (w *writeTrapStore) SetBookAuthors(string, []database.BookAuthor) error {
	return w.trip("SetBookAuthors")
}
func (w *writeTrapStore) ReassignExternalIDs(string, string) error {
	return w.trip("ReassignExternalIDs")
}

func seedAt(t *testing.T, store database.Store, title, path string) *database.Book {
	t.Helper()
	b := &database.Book{ID: ulid.Make().String(), Title: title, Format: "m4b", FilePath: path}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: b.ID, FilePath: path, Format: "m4b",
	}))
	return b
}

func assertUntouched(t *testing.T, store database.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		assert.False(t, b.IsSoftDeleted(), "book %s must not be merged away", id)
		assert.Nil(t, b.VersionGroupID, "book %s must not join a version group", id)
	}
}

func TestMergeBooks_RefusesITunesParticipantWithoutWriting(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	store := setupTestStore(t)
	itunesBook := seedAt(t, store, "In iTunes", testITunesMedia+"/Author/Book.m4b")
	managed := seedAt(t, store, "Managed", "/srv/managed/Author/Book.m4b")

	svc := NewService(&writeTrapStore{Store: store, t: t})
	for _, primary := range []string{"", itunesBook.ID, managed.ID} {
		_, err := svc.MergeBooks([]string{itunesBook.ID, managed.ID}, primary)
		require.ErrorIs(t, err, ErrITunesProtected, "primary=%q", primary)
		var pe *ITunesProtectedError
		require.ErrorAs(t, err, &pe)
		assert.Equal(t, itunesBook.ID, pe.BookID)
	}
	assertUntouched(t, store, itunesBook.ID, managed.ID)
}

func TestCombineBooks_RefusesITunesParticipantWithoutWriting(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.LibraryReadPath = testITunesLibFolder + "/iTunes Library.xml" })
	store := setupTestStore(t)
	itunesBook := seedAt(t, store, "In iTunes", testITunesLibFolder+"/iTunes Media/Audiobooks/Book.m4b")
	managed := seedAt(t, store, "Managed", "/srv/managed/Author/Book.m4b")

	svc := NewService(&writeTrapStore{Store: store, t: t})
	for _, primary := range []string{itunesBook.ID, managed.ID} {
		_, err := svc.CombineBooks([]string{itunesBook.ID, managed.ID}, primary, nil)
		require.ErrorIs(t, err, ErrITunesProtected, "primary=%q", primary)
	}
	assertUntouched(t, store, itunesBook.ID, managed.ID)
}
