// file: internal/itunes/service/import_corruption_regression_test.go
// version: 1.0.0
// guid: 4e9a1c7b-8d23-4f5e-b6a0-2c7d9e1f3b58
// last-edited: 2026-09-13
//
// Regression tests for the 2026-09-13 iTunes import audit: each one runs the
// real code path against a real PebbleStore and asserts the stored data.

package itunesservice

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const concurrentNarrator = "Narrator Set Concurrently"

func newRegressionStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func allBooks(t *testing.T, store *database.PebbleStore) []database.BookCore {
	t.Helper()
	books, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	return books
}

func setNarrator(t *testing.T, store database.Store, id string) {
	t.Helper()
	_, err := store.ModifyBook(id, func(b *database.Book) error {
		b.Narrator = new(concurrentNarrator)
		return nil
	})
	require.NoError(t, err)
}

// Finding 1: a PID miss must not attach the album to a book that merely
// shares its title.
func TestSyncLibrary_TitleCollisionDoesNotAttachToEitherBook(t *testing.T) {
	imp, store := newSourceFieldsImporter(t)
	filePath := sourceFieldsAudioFile(t)
	var ids []string
	for _, p := range []string{"/lib/a/Some Book.m4b", "/lib/b/Some Book.m4b"} {
		b, err := store.CreateBook(&database.Book{Title: "Some Book", FilePath: p, Format: "m4b"})
		require.NoError(t, err)
		ids = append(ids, b.ID)
	}

	lib := sourceFieldsLibrary(filePath, itunes.XMLSourceFields(), itunes.Track{PlayCount: 9, Rating: 60})
	require.NoError(t, imp.syncLibrary(context.Background(), lib, filepath.Join(t.TempDir(), "lib"), nil, nil, logger.New("test")))

	for _, id := range ids {
		got, err := store.GetBookByID(id)
		require.NoError(t, err)
		assert.Nil(t, got.ITunesPersistentID, "book %s only shares a title; it must not get the PID", id)
		assert.Nil(t, got.ITunesPlayCount, "book %s must not get the play count", id)
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		assert.Empty(t, files, "book %s must not get the album's files", id)
	}
}

// Finding 1: a path held by two books is ambiguous -- neither is picked and
// no third book is created for the path.
func TestSyncLibrary_PathHeldByTwoBooksIsSkipped(t *testing.T) {
	imp, store := newSourceFieldsImporter(t)
	filePath := sourceFieldsAudioFile(t)
	var ids []string
	for _, title := range []string{"Copy One", "Copy Two"} {
		b, err := store.CreateBook(&database.Book{Title: title, FilePath: filePath, Format: "m4b"})
		require.NoError(t, err)
		ids = append(ids, b.ID)
	}

	lib := sourceFieldsLibrary(filePath, itunes.XMLSourceFields(), itunes.Track{PlayCount: 9, Rating: 60})
	require.NoError(t, imp.syncLibrary(context.Background(), lib, filepath.Join(t.TempDir(), "lib"), nil, nil, logger.New("test")))

	for _, id := range ids {
		got, err := store.GetBookByID(id)
		require.NoError(t, err)
		assert.Nil(t, got.ITunesPersistentID, "book %s shares the path with another book; it must not get the PID", id)
		assert.Nil(t, got.ITunesRating, "book %s must not get the rating", id)
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		assert.Empty(t, files, "book %s must not get the album's files", id)
	}
	assert.Len(t, allBooks(t, store), 2, "an ambiguous path must not create a third book")
}

type recordingEnqueuer struct {
	mu  sync.Mutex
	ids []string
}

func (e *recordingEnqueuer) Enqueue(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ids = append(e.ids, id)
}
func (e *recordingEnqueuer) EnqueueAdd(itunes.ITLNewTrack) {}
func (e *recordingEnqueuer) EnqueueRemove(string)          {}

// Finding 2: one finish adds one play, however many times the sync runs.
func TestPushPositions_FinishedBookBumpsPlayCountOnce(t *testing.T) {
	store := setupSyncTestStore(t)
	pid := "POS_PID_1"
	book, err := store.CreateBook(&database.Book{
		Title: "Finished", FilePath: "/tmp/finished.m4b", Format: "m4b",
		ITunesPersistentID: &pid, ITunesPlayCount: new(3),
	})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "seg-1", BookID: book.ID, FilePath: "/tmp/finished.m4b", Duration: 3600}))
	require.NoError(t, store.SetUserPosition(adminUserID, book.ID, "seg-1", 3599))
	_, err = readstatus.SetManualStatus(store, adminUserID, book.ID, database.UserBookStatusFinished)
	require.NoError(t, err)

	enq := &recordingEnqueuer{}
	ps := newPositionSync(store, enq)
	for range 3 {
		assert.Equal(t, 1, ps.pushPositions())
	}

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ITunesPlayCount)
	assert.Equal(t, 4, *got.ITunesPlayCount, "three sync runs over one finish must add exactly one play")
	require.NotNil(t, got.ITunesBookmark)
	assert.Equal(t, int64(3599000), *got.ITunesBookmark)
	assert.NotNil(t, got.ITunesPlayCountBumpedAt)
	assert.Len(t, enq.ids, 3, "the bookmark is still pushed on every run")
}

// raceOrganizer stands in for the file copy: while it "copies", another
// writer changes the book, which the organize write must not revert.
type raceOrganizer struct {
	t       *testing.T
	store   database.Store
	landing string
}

func (o *raceOrganizer) OrganizeSingleFile(book *database.Book) (*organizer.Landing, error) {
	setNarrator(o.t, o.store, book.ID)
	return &organizer.Landing{Path: o.landing, Created: []string{o.landing}}, nil
}

func (o *raceOrganizer) OrganizeBookDirectory(*database.Book, []database.BookFile) (*organizer.Landing, error) {
	return nil, os.ErrInvalid
}

// Finding 3: the organize phase writes only what it changed.
func TestOrganizeImportedBooks_KeepsConcurrentEdit(t *testing.T) {
	store := newRegressionStore(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.m4b")
	landing := filepath.Join(dir, "organized", "Book.m4b")
	require.NoError(t, os.WriteFile(src, []byte("audio"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(landing), 0o755))
	require.NoError(t, os.WriteFile(landing, []byte("audio"), 0o644))

	book, err := store.CreateBook(&database.Book{
		Title: "Book", FilePath: src, Format: "m4b",
		LibraryState: new("imported"), ITunesImportSource: new("/lib.xml"),
	})
	require.NoError(t, err)

	org := &raceOrganizer{t: t, store: store, landing: landing}
	imp := &Importer{store: store, organizerFactory: func() BookOrganizer { return org }}
	imp.organizeImportedBooks(context.Background(), &itunesImportStatus{}, logger.New("test"))

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Equal(t, landing, got.FilePath, "organize must record the new path")
	require.NotNil(t, got.LibraryState)
	assert.Equal(t, "organized", *got.LibraryState)
	require.NotNil(t, got.Narrator, "the edit made during the copy was reverted")
	assert.Equal(t, concurrentNarrator, *got.Narrator)
}

// hashRaceStore changes the book between the hash and the write: its
// IsHashBlocked (called after hashing, before the write) edits the row.
type hashRaceStore struct {
	*database.PebbleStore
	t    *testing.T
	once sync.Once
}

func (s *hashRaceStore) IsHashBlocked(hash string) (bool, error) {
	s.once.Do(func() {
		books, err := s.GetAllBooksCore(0, 0)
		require.NoError(s.t, err)
		require.Len(s.t, books, 1)
		setNarrator(s.t, s.PebbleStore, books[0].ID)
	})
	return s.PebbleStore.IsHashBlocked(hash)
}

// Finding 4: hash validation sets only the hash fields on the fresh row.
func TestExecute_HashValidationKeepsConcurrentEdit(t *testing.T) {
	store := &hashRaceStore{PebbleStore: newRegressionStore(t), t: t}
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "hash-race.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("h"), 512), 0o644))
	xmlPath := writeXMLWithAudiobook(t, dir, "Hash Race", "Author H", "HASH_RACE_PID", trackPath)

	imp := newImporter(Deps{Store: store, Config: Config{}})
	require.NoError(t, imp.Execute(context.Background(), "op-hash-race", ImportRequest{
		LibraryPath: xmlPath, ImportMode: "import", SkipDuplicates: true,
	}, logger.New("test")))

	books := allBooks(t, store.PebbleStore)
	require.Len(t, books, 1)
	got, err := store.GetBookByID(books[0].ID)
	require.NoError(t, err)
	require.NotNil(t, got.FileHash, "hash validation must record the hash")
	require.NotNil(t, got.Narrator, "the edit made while hashing was reverted")
	assert.Equal(t, concurrentNarrator, *got.Narrator)
}

// Finding 5: linking an existing non-primary copy leaves its group and
// primary status alone.
func TestExecute_LinkKeepsVersionGroupAndPrimary(t *testing.T) {
	store := newRegressionStore(t)
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "copy.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("v"), 512), 0o644))
	pid := "LINK_PRIMARY_PID"
	xmlPath := writeXMLWithAudiobook(t, dir, "Grouped Book", "Author G", pid, trackPath)

	vg := "vg-existing"
	yes, no := true, false
	primary, err := store.CreateBook(&database.Book{Title: "Grouped Book", FilePath: "/lib/primary.m4b", VersionGroupID: &vg, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	copyBook, err := store.CreateBook(&database.Book{Title: "Grouped Book", FilePath: trackPath, VersionGroupID: &vg, IsPrimaryVersion: &no})
	require.NoError(t, err)
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: pid, BookID: copyBook.ID}))

	imp := newImporter(Deps{Store: store, Config: Config{}})
	require.NoError(t, imp.Execute(context.Background(), "op-link-primary", ImportRequest{LibraryPath: xmlPath, ImportMode: "import"}, logger.New("test")))

	got, err := store.GetBookByID(copyBook.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ITunesPersistentID)
	assert.Equal(t, pid, *got.ITunesPersistentID, "the link must still attach the PID")
	require.NotNil(t, got.VersionGroupID)
	assert.Equal(t, vg, *got.VersionGroupID, "the link must not move the book to another group")
	require.NotNil(t, got.IsPrimaryVersion)
	assert.False(t, *got.IsPrimaryVersion, "the link must not make a second primary in the group")

	other, err := store.GetBookByID(primary.ID)
	require.NoError(t, err)
	assert.True(t, *other.IsPrimaryVersion)
	assert.Len(t, allBooks(t, store), 2)
}

// Finding 6: without SkipDuplicates, an album already in the library by
// path is linked, not created again.
func TestExecute_ReimportByPathLinksWithoutSkipDuplicates(t *testing.T) {
	store := newRegressionStore(t)
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "again.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("r"), 512), 0o644))
	pid := "REIMPORT_PATH_PID"
	xmlPath := writeXMLWithAudiobook(t, dir, "Already Here", "Author R", pid, trackPath)

	existing, err := store.CreateBook(&database.Book{Title: "Already Here", FilePath: trackPath, Format: "m4b"})
	require.NoError(t, err)

	imp := newImporter(Deps{Store: store, Config: Config{}})
	require.NoError(t, imp.Execute(context.Background(), "op-reimport-path", ImportRequest{LibraryPath: xmlPath, ImportMode: "import"}, logger.New("test")))

	books := allBooks(t, store)
	require.Len(t, books, 1, "the re-import created a duplicate book")
	got, err := store.GetBookByID(existing.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ITunesPersistentID)
	assert.Equal(t, pid, *got.ITunesPersistentID)
}

// Finding 6: the per-track PID on book_files also identifies the book.
func TestExecute_ReimportByTrackPIDLinksWithoutSkipDuplicates(t *testing.T) {
	store := newRegressionStore(t)
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "moved.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("m"), 512), 0o644))
	pid := "REIMPORT_FILE_PID"
	xmlPath := writeXMLWithAudiobook(t, dir, "Known By Track", "Author T", pid, trackPath)

	existing, err := store.CreateBook(&database.Book{Title: "Known By Track", FilePath: "/lib/elsewhere", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: existing.ID, FilePath: "/lib/elsewhere/moved.m4b", ITunesPersistentID: pid}))

	imp := newImporter(Deps{Store: store, Config: Config{}})
	require.NoError(t, imp.Execute(context.Background(), "op-reimport-pid", ImportRequest{LibraryPath: xmlPath, ImportMode: "import"}, logger.New("test")))

	require.Len(t, allBooks(t, store), 1, "the re-import created a duplicate book")
	got, err := store.GetBookByID(existing.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ITunesPersistentID)
	assert.Equal(t, pid, *got.ITunesPersistentID)
}
