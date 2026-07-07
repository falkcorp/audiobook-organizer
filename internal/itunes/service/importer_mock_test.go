// file: internal/itunes/service/importer_mock_test.go
// version: 1.1.0
// guid: e7f1a2b3-4c5d-6e7f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-07-07

package itunesservice

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockImporter constructs a minimal *Importer backed by a mock store.
func newMockImporter(store database.Store) *Importer {
	return &Importer{store: store, cfg: Config{}}
}

// ---------------------------------------------------------------------------
// GetStatus tests
// ---------------------------------------------------------------------------

func TestGetStatus_Empty(t *testing.T) {
	imp := newMockImporter(nil)

	snap := imp.GetStatus("nonexistent-op")

	require.NotNil(t, snap)
	assert.Equal(t, 0, snap.Total)
	assert.Equal(t, 0, snap.Processed)
	assert.Equal(t, 0, snap.Imported)
	assert.Equal(t, 0, snap.Skipped)
	assert.Equal(t, 0, snap.Linked)
	assert.Equal(t, 0, snap.Failed)
	assert.Nil(t, snap.Errors)
}

func TestGetStatus_AfterSet(t *testing.T) {
	imp := newMockImporter(nil)

	// Manually write to statusMap (white-box).
	opID := "test-op-123"
	s := imp.statusMap.load(opID)
	s.mu.Lock()
	s.Total = 50
	s.Processed = 20
	s.Imported = 15
	s.Skipped = 3
	s.Linked = 2
	s.Failed = 1
	s.Errors = []string{"some error"}
	s.mu.Unlock()

	snap := imp.GetStatus(opID)

	require.NotNil(t, snap)
	assert.Equal(t, 50, snap.Total)
	assert.Equal(t, 20, snap.Processed)
	assert.Equal(t, 15, snap.Imported)
	assert.Equal(t, 3, snap.Skipped)
	assert.Equal(t, 2, snap.Linked)
	assert.Equal(t, 1, snap.Failed)
	assert.Equal(t, []string{"some error"}, snap.Errors)
}

// ---------------------------------------------------------------------------
// GetStatusBulk tests
// ---------------------------------------------------------------------------

func TestGetStatusBulk_Mixed(t *testing.T) {
	imp := newMockImporter(nil)

	// Populate one known op.
	knownID := "op-known"
	s := imp.statusMap.load(knownID)
	s.mu.Lock()
	s.Total = 10
	s.Imported = 7
	s.mu.Unlock()

	unknownID := "op-unknown"

	result := imp.GetStatusBulk([]string{knownID, unknownID})

	require.Len(t, result, 2)

	known := result[knownID]
	require.NotNil(t, known)
	assert.Equal(t, 10, known.Total)
	assert.Equal(t, 7, known.Imported)

	unknown := result[unknownID]
	require.NotNil(t, unknown, "unknown ID should return zero-value snapshot, not nil")
	assert.Equal(t, 0, unknown.Total)
}

// ---------------------------------------------------------------------------
// CollectITLUpdatesWithBookIDs tests
// ---------------------------------------------------------------------------

func TestCollectITLUpdatesWithBookIDs_Empty(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(100000, 0).Return([]database.BookCore{}, nil)

	imp := newMockImporter(m)
	updates, bookIDs := imp.CollectITLUpdatesWithBookIDs()

	assert.Empty(t, updates)
	assert.Empty(t, bookIDs)
}

func TestCollectITLUpdatesWithBookIDs_SkipsNonPrimary(t *testing.T) {
	pid := "AABBCCDDEEFF0011"
	path := "/mnt/books/book.m4b"
	notPrimary := false

	book := database.Book{
		ID:                 "book-1",
		Title:              "Non-Primary Book",
		IsPrimaryVersion:   &notPrimary,
		ITunesPersistentID: &pid,
		ITunesPath:         &path,
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(100000, 0).Return([]database.BookCore{book.Core()}, nil)

	imp := newMockImporter(m)
	updates, bookIDs := imp.CollectITLUpdatesWithBookIDs()

	assert.Empty(t, updates, "non-primary books should be skipped")
	assert.Empty(t, bookIDs)
}

func TestCollectITLUpdatesWithBookIDs_BookLevel(t *testing.T) {
	// Books without BookFiles produce no location updates now that
	// Book.ITunesPath is deprecated; location is tracked on BookFile.
	pid := "DEADBEEFCAFEBABE"
	path := "/mnt/books/greatbook.m4b"
	isPrimary := true

	book := database.Book{
		ID:                 "book-2",
		Title:              "Great Book",
		IsPrimaryVersion:   &isPrimary,
		ITunesPersistentID: &pid,
		ITunesPath:         &path,
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(100000, 0).Return([]database.BookCore{book.Core()}, nil)
	m.EXPECT().GetBookFiles("book-2").Return(nil, nil)

	imp := newMockImporter(m)
	updates, bookIDs := imp.CollectITLUpdatesWithBookIDs()

	// No BookFiles → no location updates (deprecated Book.ITunesPath not used).
	assert.Empty(t, updates)
	assert.Empty(t, bookIDs)
}

func TestCollectITLUpdatesWithBookIDs_FileLevel(t *testing.T) {
	isPrimary := true
	emptyPID := ""
	emptyPath := ""

	book := database.Book{
		ID:                 "book-3",
		Title:              "Multi-File Book",
		IsPrimaryVersion:   &isPrimary,
		ITunesPersistentID: &emptyPID,
		ITunesPath:         &emptyPath,
	}

	files := []database.BookFile{
		{
			// TASK-006 / SPEC §1b: f.ITunesPath holds a native Windows path; the
			// collector normalizes it through LocationPair (Unix paths would be
			// unmappable and skipped).
			ID:                 "file-1",
			BookID:             "book-3",
			ITunesPersistentID: "PID111",
			ITunesPath:         `W:\books\multi\track1.m4b`,
		},
		{
			ID:                 "file-2",
			BookID:             "book-3",
			ITunesPersistentID: "PID222",
			ITunesPath:         `W:\books\multi\track2.m4b`,
		},
		{
			// File without PID — should be skipped.
			ID:     "file-3",
			BookID: "book-3",
		},
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(100000, 0).Return([]database.BookCore{book.Core()}, nil)
	m.EXPECT().GetBookFiles("book-3").Return(files, nil)

	imp := newMockImporter(m)
	updates, bookIDs := imp.CollectITLUpdatesWithBookIDs()

	require.Len(t, updates, 2)
	pids := []string{updates[0].PersistentID, updates[1].PersistentID}
	assert.Contains(t, pids, "PID111")
	assert.Contains(t, pids, "PID222")
	require.Len(t, bookIDs, 1)
	assert.Equal(t, "book-3", bookIDs[0])
}

// ---------------------------------------------------------------------------
// DiscoverLibraryPath tests
// ---------------------------------------------------------------------------

func TestDiscoverLibraryPath_FromConfig(t *testing.T) {
	// DiscoverLibraryPath scans books for ITunesImportSource.
	// "from config" means we return via a book that has the source set.
	source := "/mnt/bigdata/books/itunes/iTunes Library.xml"
	isPrimary := true

	book := database.Book{
		ID:                 "book-src",
		IsPrimaryVersion:   &isPrimary,
		ITunesImportSource: func() *string { s := source; return &s }(),
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(100, 0).Return([]database.BookCore{book.Core()}, nil)

	imp := newMockImporter(m)
	result := imp.DiscoverLibraryPath()

	assert.Equal(t, source, result)
}

func TestDiscoverLibraryPath_Empty(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(100, 0).Return([]database.BookCore{}, nil)

	imp := newMockImporter(m)
	result := imp.DiscoverLibraryPath()

	assert.Equal(t, "", result)
}

// ---------------------------------------------------------------------------
// remapWindowsPath tests
// ---------------------------------------------------------------------------

func TestRemapWindowsPath_WithMapping(t *testing.T) {
	opts := itunes.ImportOptions{
		PathMappings: []itunes.PathMapping{
			{From: "C:/Users/jdfalk/Music/iTunes/", To: "/mnt/itunes/"},
		},
	}

	// Windows path that matches the mapping.
	windowsPath := "C:/Users/jdfalk/Music/iTunes/Audiobooks/Book.m4b"
	result := remapWindowsPath(windowsPath, opts)
	assert.Equal(t, "/mnt/itunes/Audiobooks/Book.m4b", result)

	// Windows path with no match — returned unchanged.
	unmapped := "D:/OtherDrive/file.m4b"
	result2 := remapWindowsPath(unmapped, opts)
	assert.Equal(t, unmapped, result2)
}

func TestRemapWindowsPath_NonWindows(t *testing.T) {
	opts := itunes.ImportOptions{
		PathMappings: []itunes.PathMapping{
			{From: "C:/", To: "/mnt/"},
		},
	}

	// Unix path — no drive letter colon at index 1.
	unixPath := "/mnt/books/audiobooks/great-book.m4b"
	result := remapWindowsPath(unixPath, opts)
	assert.Equal(t, unixPath, result)
}

// ---------------------------------------------------------------------------
// toITunesPathMappings tests
// ---------------------------------------------------------------------------

func TestToITunesPathMappings(t *testing.T) {
	src := []PathMapping{
		{From: "C:/iTunes/", To: "/mnt/itunes/"},
		{From: "D:/Books/", To: "/mnt/books/"},
	}

	result := toITunesPathMappings(src)

	require.Len(t, result, 2)
	assert.Equal(t, "C:/iTunes/", result[0].From)
	assert.Equal(t, "/mnt/itunes/", result[0].To)
	assert.Equal(t, "D:/Books/", result[1].From)
	assert.Equal(t, "/mnt/books/", result[1].To)
}

func TestToITunesPathMappings_Empty(t *testing.T) {
	result := toITunesPathMappings(nil)
	assert.Empty(t, result)
}

// Ensure the mock store used in tests above satisfies the service Store interface.
var _ Store = (*dbmocks.MockStore)(nil)

// Ensure fmt is used (it may be transitively referenced).
var _ = fmt.Sprintf
