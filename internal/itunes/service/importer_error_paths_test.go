// file: internal/itunes/service/importer_error_paths_test.go
// version: 1.1.1
// guid: a7c3f2e1-4d8b-4e6a-9f0c-2b5d7e3a8c1f
// last-edited: 2026-07-07

// Package itunesservice - error and edge-case tests for importer.go (TODO 4.13d).
//
// Covers:
//   1. Disabled-mode (NewDisabled service; Importer is nil — early return callers).
//   2. Corrupt ITL — malformed XML bytes → Execute returns error, no partial state.
//   3. Concurrent sync — back-to-back Sync calls do not panic or corrupt state.
//   4. Empty library — already covered in importer_execute_test.go (noted here).
//   5. External-ID collision — tombstoned PID is skipped; already-mapped PID is linked.
//   6. Partial write — CreateBook store failure → execution continues, status updated.
//   7. Position sync race — Sync GetAllBooks failure → error returned.
//   8. Cover-art missing — buildBookFromAlbumGroup with no CoverURL crash guard.

package itunesservice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 1. Disabled-mode: NewDisabled() returns a Service with nil Importer.
//    Public Importer methods on a Service constructed via New(disabled) must
//    not panic — callers are expected to gate on service.Importer != nil or
//    service.Enabled(). This test verifies the construction invariant and that
//    GetStatus on a freshly-constructed real Importer returns a zero snapshot.
// ---------------------------------------------------------------------------

func TestDisabledService_ImporterIsNil(t *testing.T) {
	svc := NewDisabled()
	assert.False(t, svc.Enabled(), "disabled service must report Enabled()==false")
	assert.Nil(t, svc.Importer, "disabled service must have nil Importer")
}

func TestDisabledService_ViaNewWithDisabledConfig(t *testing.T) {
	svc, err := New(Deps{
		Store:  dbmocks.NewMockStore(t),
		Config: Config{Enabled: false},
	})
	require.NoError(t, err)
	assert.False(t, svc.Enabled())
	assert.Nil(t, svc.Importer, "disabled service (via New) must have nil Importer")
}

func TestGetStatus_ZeroOnFreshImporter(t *testing.T) {
	// GetStatus on a never-used op ID returns an empty snapshot, not nil.
	imp := newMockImporter(nil)
	snap := imp.GetStatus("brand-new-op-id")
	require.NotNil(t, snap)
	assert.Zero(t, snap.Total)
	assert.Zero(t, snap.Imported)
}

// ---------------------------------------------------------------------------
// 2. Corrupt ITL — malformed bytes → Execute returns error without storing
//    any books.  The corrupt file test verifies that recordImportError is
//    called and that ClearState is invoked (operation cleaned up).
// ---------------------------------------------------------------------------

func TestExecute_CorruptXML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	corruptPath := filepath.Join(dir, "corrupt.xml")
	// Write some garbage that is NOT valid iTunes Library XML.
	require.NoError(t, os.WriteFile(corruptPath, bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 64), 0o644))

	m := dbmocks.NewMockStore(t)
	// Execute always calls SaveParams + LoadCheckpoint before attempting to parse.
	m.EXPECT().SaveOperationParams("op-corrupt", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-corrupt").Return(nil, nil).Once()
	// On parse failure ClearState (DeleteOperationState) is called.
	m.EXPECT().DeleteOperationState("op-corrupt").Return(nil).Once()
	// CreateBook must NOT be called for a failed parse.
	// (testify mock enforces unexpected calls → test would fail)

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-corrupt", ImportRequest{
		LibraryPath: corruptPath,
		ImportMode:  "import",
	}, log)

	require.Error(t, err, "corrupt XML must return an error from Execute")
	assert.Contains(t, err.Error(), "failed to parse library")
}

// TestExecute_NonXMLBinary_ReturnsError tests that a file containing random
// binary content (not XML at all) causes Execute to fail at parse time.
func TestExecute_NonXMLBinary_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "binary.xml")
	// Null bytes + random noise: definitely not XML.
	require.NoError(t, os.WriteFile(binPath, bytes.Repeat([]byte{0x00, 0xff, 0x80, 0x01}, 32), 0o644))

	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-binary", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-binary").Return(nil, nil).Once()
	m.EXPECT().DeleteOperationState("op-binary").Return(nil).Once()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-binary", ImportRequest{
		LibraryPath: binPath,
		ImportMode:  "import",
	}, log)

	require.Error(t, err, "binary file must return an error from Execute")
}

func TestSync_CorruptXML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.xml")
	require.NoError(t, os.WriteFile(badPath, []byte("<not valid plist"), 0o644))

	imp := &Importer{cfg: Config{}}
	log := logger.New("test")
	err := imp.Sync(context.Background(), badPath, nil, nil, log)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse library")
}

// ---------------------------------------------------------------------------
// 3. Concurrent sync — two Sync calls that both succeed must not corrupt
//    shared Importer state.  We use a library with zero audiobooks so both
//    return early and this is purely a concurrency/no-panic guard.
// ---------------------------------------------------------------------------

func TestSync_Concurrent_NoPanic(t *testing.T) {
	xmlContent := validEmptyXML()
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(xmlContent), 0o644))

	imp := &Importer{cfg: Config{}}
	log := logger.New("test")

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = imp.Sync(context.Background(), xmlPath, nil, nil, log)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "concurrent Sync %d should not error", i)
	}
}

// ---------------------------------------------------------------------------
// 5a. External-ID collision: tombstoned PID → track is skipped (no CreateBook).
// ---------------------------------------------------------------------------

func TestExecute_TombstonedPID_Skipped(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "chapter.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("a"), 512), 0o644))

	pid := "TOMBSTONE_PID_001"
	xmlPath := writeXMLWithAudiobook(t, dir, "Audiobook A", "Author A", pid, trackPath)

	// assignAuthorAndSeries is called before the tombstone check.
	authorRecord := &database.Author{ID: 1, Name: "Author A"}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-tombstone", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-tombstone").Return(nil, nil).Once()
	// assignAuthorAndSeries: look up / create author + series.
	m.EXPECT().GetAuthorByName(mock.Anything).Return(authorRecord, nil).Maybe()
	m.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().CreateSeries(mock.Anything, mock.Anything).Return(&database.Series{ID: 1, Name: "Audiobook A"}, nil).Maybe()
	// tombstone check: returns true → skip
	m.EXPECT().IsExternalIDTombstoned("itunes", pid).Return(true, nil).Once()
	// ClearState and fingerprint at the end
	m.EXPECT().DeleteOperationState("op-tombstone").Return(nil).Once()
	m.EXPECT().SaveLibraryFingerprint(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-tombstone", ImportRequest{
		LibraryPath: xmlPath,
		ImportMode:  "import",
	}, log)

	assert.NoError(t, err)
	// CreateBook must not have been called (testify mock enforces this).
}

// ---------------------------------------------------------------------------
// 5b. External-ID collision: already-mapped PID → linkITunesMetadata called,
//     no new book created.
// ---------------------------------------------------------------------------

func TestExecute_ExistingPID_LinkedNotCreated(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "chapter.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("b"), 512), 0o644))

	pid := "EXISTING_PID_002"
	existingBookID := "existing-book-id-99"
	existingBook := &database.Book{
		ID:    existingBookID,
		Title: "Audiobook B",
	}
	xmlPath := writeXMLWithAudiobook(t, dir, "Audiobook B", "Author B", pid, trackPath)

	authorRecord := &database.Author{ID: 2, Name: "Author B"}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-existing-pid", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-existing-pid").Return(nil, nil).Once()
	// assignAuthorAndSeries: look up author + series.
	m.EXPECT().GetAuthorByName(mock.Anything).Return(authorRecord, nil).Maybe()
	m.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().CreateSeries(mock.Anything, mock.Anything).Return(&database.Series{ID: 2, Name: "Audiobook B"}, nil).Maybe()
	// PID not tombstoned.
	m.EXPECT().IsExternalIDTombstoned("itunes", pid).Return(false, nil).Once()
	// PID already mapped → returns existing book ID.
	m.EXPECT().GetBookByExternalID("itunes", pid).Return(existingBookID, nil).Once()
	// Fetch the existing book for linkITunesMetadata.
	m.EXPECT().GetBookByID(existingBookID).Return(existingBook, nil).Once()
	// linkITunesMetadata calls UpdateBook on the existing book (sets VG + isPrimary).
	m.EXPECT().UpdateBook(existingBookID, mock.Anything).Return(existingBook, nil).Once()
	// End of Execute: ClearState + fingerprint.
	m.EXPECT().DeleteOperationState("op-existing-pid").Return(nil).Once()
	m.EXPECT().SaveLibraryFingerprint(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-existing-pid", ImportRequest{
		LibraryPath: xmlPath,
		ImportMode:  "import",
	}, log)

	assert.NoError(t, err)

	// Linked counter must be 1.
	snap := imp.GetStatus("op-existing-pid")
	assert.Equal(t, 1, snap.Linked, "one book should have been linked via PID")
	assert.Equal(t, 0, snap.Imported, "no new books should have been imported")
}

// ---------------------------------------------------------------------------
// 5c. SkipDuplicates: existing book at same file path → linked, not created.
// ---------------------------------------------------------------------------

func TestExecute_SkipDuplicates_ExistingPath_Linked(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "dup-chapter.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("c"), 512), 0o644))

	pid := "NO_PID_SKIP_DUP"
	existingBook := &database.Book{ID: "dup-book-id", Title: "Audiobook C"}
	xmlPath := writeXMLWithAudiobook(t, dir, "Audiobook C", "Author C", pid, trackPath)

	authorRecord := &database.Author{ID: 3, Name: "Author C"}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-skip-dup", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-skip-dup").Return(nil, nil).Once()
	// assignAuthorAndSeries: look up author + series.
	m.EXPECT().GetAuthorByName(mock.Anything).Return(authorRecord, nil).Maybe()
	m.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().CreateSeries(mock.Anything, mock.Anything).Return(&database.Series{ID: 3, Name: "Audiobook C"}, nil).Maybe()
	m.EXPECT().IsExternalIDTombstoned("itunes", pid).Return(false, nil).Once()
	// PID not yet in external_id_map.
	m.EXPECT().GetBookByExternalID("itunes", pid).Return("", fmt.Errorf("not found")).Once()
	// SkipDuplicates = true → check file path.
	m.EXPECT().GetBookByFilePath(mock.Anything).Return(existingBook, nil).Once()
	// linkITunesMetadata: update the existing book (sets VG + isPrimary etc.).
	m.EXPECT().UpdateBook("dup-book-id", mock.Anything).Return(existingBook, nil).Once()
	m.EXPECT().DeleteOperationState("op-skip-dup").Return(nil).Once()
	m.EXPECT().SaveLibraryFingerprint(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-skip-dup", ImportRequest{
		LibraryPath:    xmlPath,
		ImportMode:     "import",
		SkipDuplicates: true,
	}, log)

	assert.NoError(t, err)
	snap := imp.GetStatus("op-skip-dup")
	assert.Equal(t, 1, snap.Linked)
	assert.Equal(t, 0, snap.Imported)
}

// ---------------------------------------------------------------------------
// 6. Partial write — CreateBook fails for one book → execution continues,
//    Failed counter is incremented, no panic.
// ---------------------------------------------------------------------------

func TestExecute_CreateBookFails_ContinuesAndCountsFailed(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "fail-chapter.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("d"), 512), 0o644))

	pid := "FAIL_CREATE_PID"
	xmlPath := writeXMLWithAudiobook(t, dir, "Audiobook D", "Author D", pid, trackPath)

	storeErr := fmt.Errorf("disk full: cannot create book")
	authorRecord := &database.Author{ID: 4, Name: "Author D"}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().SaveOperationParams("op-fail-create", mock.Anything).Return(nil).Once()
	m.EXPECT().GetOperationState("op-fail-create").Return(nil, nil).Once()
	// assignAuthorAndSeries runs before PID check and before CreateBook.
	m.EXPECT().GetAuthorByName(mock.Anything).Return(authorRecord, nil).Maybe()
	m.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().CreateSeries(mock.Anything, mock.Anything).Return(&database.Series{ID: 4, Name: "Audiobook D"}, nil).Maybe()
	m.EXPECT().IsExternalIDTombstoned("itunes", pid).Return(false, nil).Once()
	m.EXPECT().GetBookByExternalID("itunes", pid).Return("", fmt.Errorf("not found")).Once()
	// CreateBook returns an error — simulates a disk/DB failure mid-import.
	m.EXPECT().CreateBook(mock.Anything).Return(nil, storeErr).Once()
	m.EXPECT().DeleteOperationState("op-fail-create").Return(nil).Once()
	m.EXPECT().SaveLibraryFingerprint(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	log := logger.New("test")
	err := imp.Execute(context.Background(), "op-fail-create", ImportRequest{
		LibraryPath: xmlPath,
		ImportMode:  "import",
	}, log)

	// Execute itself does NOT return an error when a single book save fails —
	// it logs the error and continues to the next album group.
	assert.NoError(t, err)

	snap := imp.GetStatus("op-fail-create")
	assert.Equal(t, 1, snap.Failed, "failed counter should be 1")
	assert.Equal(t, 0, snap.Imported, "no book should have been imported")
}

// ---------------------------------------------------------------------------
// 7. Sync GetAllBooks failure → Sync returns error.
// ---------------------------------------------------------------------------

func TestSync_GetAllBooksFails_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "sync-chapter.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("e"), 512), 0o644))

	pid := "SYNC_PID_001"
	xmlPath := writeXMLWithAudiobook(t, dir, "Sync Book", "Sync Author", pid, trackPath)

	m := dbmocks.NewMockStore(t)
	// After parsing and grouping, Sync calls GetAllBooksCore for the PID index.
	m.EXPECT().GetAllBooksCore(100000, 0).Return(nil, fmt.Errorf("database connection lost")).Once()
	// No deferred iTunes updates (ITLWriteBackEnabled = false).

	imp := newImporter(Deps{Store: m, Config: Config{ITLWriteBackEnabled: false}})
	log := logger.New("test")
	err := imp.Sync(context.Background(), xmlPath, nil, nil, log)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load books for index")
}

// ---------------------------------------------------------------------------
// 8. Cover-art missing: a track with no embedded cover → buildBookFromAlbumGroup
//    returns a valid book with empty CoverURL (nil), no panic.
// ---------------------------------------------------------------------------

func TestBuildBookFromAlbumGroup_NoCoverArt_NoCrash(t *testing.T) {
	dir := t.TempDir()
	// Write a minimal (non-audio) file — cover extraction will simply fail.
	trackPath := filepath.Join(dir, "no-cover.m4b")
	require.NoError(t, os.WriteFile(trackPath, []byte("not a real m4b"), 0o644))

	track := &itunes.Track{
		Location:     itunes.EncodeLocation(trackPath),
		Name:         "Chapter 1",
		Album:        "No Cover Book",
		Artist:       "No Cover Author",
		PersistentID: "NOCOVER001",
		TotalTime:    10000,
		Kind:         "Audiobook",
	}

	imp := newTestImporter()
	group := albumGroup{key: "No Cover Author|No Cover Book", tracks: []*itunes.Track{track}}
	book, err := imp.buildBookFromAlbumGroup(group, "/fake/library.xml", itunes.ImportOptions{})

	require.NoError(t, err)
	assert.Equal(t, "No Cover Book", book.Title)
	// CoverURL should be nil when extraction fails — not an empty string that
	// could cause a bad API response.
	assert.Nil(t, book.CoverURL, "CoverURL must be nil when no cover art is found")
}

// ---------------------------------------------------------------------------
// buildBookFromAlbumGroup — empty group (no tracks) → error path.
// ---------------------------------------------------------------------------

func TestBuildBookFromAlbumGroup_EmptyGroup_Error(t *testing.T) {
	imp := newTestImporter()
	group := albumGroup{key: "empty|group", tracks: nil}
	_, err := imp.buildBookFromAlbumGroup(group, "/lib.xml", itunes.ImportOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tracks")
}

// ---------------------------------------------------------------------------
// buildBookFromAlbumGroup — decode fails (no file on disk) → error returned.
// ---------------------------------------------------------------------------

func TestBuildBookFromAlbumGroup_FileNotOnDisk_Error(t *testing.T) {
	imp := newTestImporter()
	track := &itunes.Track{
		// Valid URL-encoded path but the file doesn't exist on disk.
		Location:     "file:///does/not/exist/chapter.m4b",
		Name:         "Missing Chapter",
		Album:        "Ghost Book",
		PersistentID: "GHOST001",
	}
	group := albumGroup{key: "Author|Ghost Book", tracks: []*itunes.Track{track}}
	_, err := imp.buildBookFromAlbumGroup(group, "/lib.xml", itunes.ImportOptions{})
	require.Error(t, err, "missing file on disk should return an error")
}

// ---------------------------------------------------------------------------
// linkITunesMetadata — already-linked book (PID already set) → UpdateBook still
// called if other fields differ; verifies no double-assignment panic.
// ---------------------------------------------------------------------------

func TestLinkITunesMetadata_AlreadyLinked_UpdatesCalled(t *testing.T) {
	pid := "ALREADY_LINKED"
	existing := &database.Book{
		ID:                 "book-linked",
		Title:              "Linked Book",
		ITunesPersistentID: &pid, // already set
	}
	importBook := &database.Book{
		Title:              "Linked Book",
		ITunesPersistentID: &pid,
		ITunesPlayCount:    intPtrLocal(5),
	}

	m := dbmocks.NewMockStore(t)
	// PlayCount differs, so changed=true → UpdateBook is expected.
	m.EXPECT().UpdateBook("book-linked", mock.Anything).Return(existing, nil).Once()

	imp := newMockImporter(m)
	log := logger.New("test")
	imp.linkITunesMetadata(existing, importBook, &itunes.Track{}, log)
	// Assertions via mock expectation above.
}

// ---------------------------------------------------------------------------
// linkITunesMetadata — nothing changes → UpdateBook NOT called.
// ---------------------------------------------------------------------------

func TestLinkITunesMetadata_NothingChanged_NoUpdate(t *testing.T) {
	pid := "NO_CHANGE_PID"
	pc := 3
	existing := &database.Book{
		ID:                 "book-nochange",
		Title:              "Static Book",
		ITunesPersistentID: &pid,
		ITunesPlayCount:    &pc,
	}
	// All fields already match — import brings nothing new.
	importBook := &database.Book{
		ITunesPersistentID: &pid,
		ITunesPlayCount:    &pc,
	}
	// VersionGroupID and IsPrimaryVersion: set them so the changed guard stays false.
	vgID := "vg-existing"
	isPrimary := true
	existing.VersionGroupID = &vgID
	existing.IsPrimaryVersion = &isPrimary

	m := dbmocks.NewMockStore(t)
	// No EXPECT for UpdateBook — if called, testify will fail the test.

	imp := newMockImporter(m)
	log := logger.New("test")
	imp.linkITunesMetadata(existing, importBook, &itunes.Track{}, log)
}

// ---------------------------------------------------------------------------
// linkAsVersion — creates a version link for an existing primary book.
// ---------------------------------------------------------------------------

func TestLinkAsVersion_CreatesVersionBook(t *testing.T) {
	existingVGID := "vg-primary-001"
	isPrimary := true
	existing := &database.Book{
		ID:               "book-primary",
		Title:            "Primary Book",
		VersionGroupID:   &existingVGID,
		IsPrimaryVersion: &isPrimary,
	}
	importBook := &database.Book{
		Title: "Version Book",
	}
	createdVersion := &database.Book{ID: "book-version", Title: "Version Book"}

	m := dbmocks.NewMockStore(t)
	// linkAsVersion: existing already has a VG → no initial UpdateBook.
	// CreateBook for the new version.
	m.EXPECT().CreateBook(mock.Anything).Return(createdVersion, nil).Once()
	// linkITunesMetadata: existing.VersionGroupID already set + IsPrimaryVersion=true
	// + all other iTunes fields nil on both books → changed=false → no UpdateBook.
	// (No EXPECT for UpdateBook — testify would fail if it were called.)

	imp := newMockImporter(m)
	log := logger.New("test")
	imp.linkAsVersion(existing, importBook, &itunes.Track{}, log)

	// The import book must have been given the existing book's version group.
	assert.Equal(t, existingVGID, *importBook.VersionGroupID)
	assert.NotNil(t, importBook.IsPrimaryVersion)
	assert.False(t, *importBook.IsPrimaryVersion)
}

// ---------------------------------------------------------------------------
// linkAsVersion — existing book has no VersionGroupID → one is created.
// ---------------------------------------------------------------------------

func TestLinkAsVersion_ExistingHasNoVGID_CreatesVGID(t *testing.T) {
	existing := &database.Book{
		ID:    "book-no-vg",
		Title: "No VG Book",
		// VersionGroupID is nil
	}
	importBook := &database.Book{Title: "New Version"}
	createdVersion := &database.Book{ID: "book-new-v", Title: "New Version"}

	m := dbmocks.NewMockStore(t)
	// First UpdateBook call: linkAsVersion sets VG + isPrimary on existing.
	m.EXPECT().UpdateBook("book-no-vg", mock.Anything).Return(existing, nil).Once()
	// CreateBook for the version.
	m.EXPECT().CreateBook(mock.Anything).Return(createdVersion, nil).Once()
	// linkITunesMetadata: after the VG is set on existing, changed=false →
	// no second UpdateBook call.

	imp := newMockImporter(m)
	log := logger.New("test")
	imp.linkAsVersion(existing, importBook, &itunes.Track{}, log)

	// existing must now have a VersionGroupID.
	require.NotNil(t, existing.VersionGroupID)
	assert.NotEmpty(t, *existing.VersionGroupID)
}

// ---------------------------------------------------------------------------
// organizeOneBook — nil book → error.
// ---------------------------------------------------------------------------

func TestOrganizeOneBook_NilBook_Error(t *testing.T) {
	imp := newTestImporter()
	log := logger.New("test")
	err := imp.organizeOneBook(nil, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// ---------------------------------------------------------------------------
// organizeOneBook — nil organizerFactory → error.
// ---------------------------------------------------------------------------

func TestOrganizeOneBook_NoFactory_Error(t *testing.T) {
	imp := &Importer{organizerFactory: nil}
	log := logger.New("test")
	book := &database.Book{ID: "b1", Title: "Some Book", FilePath: "/mnt/books/b.m4b"}
	err := imp.organizeOneBook(book, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

// ---------------------------------------------------------------------------
// organizeOneBook — multi-file (merged) book routes to OrganizeBookDirectory
// instead of the single-file OrganizeBook path (CONS-FRAG-2).
// ---------------------------------------------------------------------------

// fakeDirOrganizer is a minimal BookOrganizer whose OrganizeBook always fails
// (mirroring the real Organizer's directory-path refusal) and whose
// OrganizeBookDirectory succeeds, recording the segment paths it was called
// with so the test can assert routing.
type fakeDirOrganizer struct {
	calledDirectory bool
	segmentPaths    []string
	targetDir       string
	pathMap         map[string]string
}

func (f *fakeDirOrganizer) OrganizeBook(book *database.Book) (string, string, error) {
	return "", "", fmt.Errorf("file_path %s is a directory but single-file organize was requested", book.FilePath)
}

func (f *fakeDirOrganizer) OrganizeBookDirectory(book *database.Book, segmentPaths []string) (string, map[string]string, error) {
	f.calledDirectory = true
	f.segmentPaths = segmentPaths
	return f.targetDir, f.pathMap, nil
}

func TestOrganizeOneBook_MultiFile_RoutesToOrganizeBookDirectory(t *testing.T) {
	book := &database.Book{ID: "book-multi", Title: "Multi File Book", FilePath: "/mnt/books/imported/multi-file-book"}
	files := []database.BookFile{
		{ID: "f1", BookID: book.ID, FilePath: "/mnt/books/imported/multi-file-book/track1.mp3"},
		{ID: "f2", BookID: book.ID, FilePath: "/mnt/books/imported/multi-file-book/track2.mp3"},
	}

	targetDir := "/mnt/books/organized/Multi File Book"
	pathMap := map[string]string{
		files[0].FilePath: targetDir + "/track1.mp3",
		files[1].FilePath: targetDir + "/track2.mp3",
	}
	fake := &fakeDirOrganizer{targetDir: targetDir, pathMap: pathMap}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookFiles(book.ID).Return(files, nil)
	m.EXPECT().UpdateBookFile("f1", mock.MatchedBy(func(bf *database.BookFile) bool {
		return bf.FilePath == pathMap[files[0].FilePath]
	})).Return(nil)
	m.EXPECT().UpdateBookFile("f2", mock.MatchedBy(func(bf *database.BookFile) bool {
		return bf.FilePath == pathMap[files[1].FilePath]
	})).Return(nil)

	imp := newMockImporter(m)
	imp.organizerFactory = func() BookOrganizer { return fake }

	log := logger.New("test")
	err := imp.organizeOneBook(book, log)
	require.NoError(t, err)

	assert.True(t, fake.calledDirectory, "expected OrganizeBookDirectory to be called for a multi-file book")
	assert.ElementsMatch(t, []string{files[0].FilePath, files[1].FilePath}, fake.segmentPaths)
	assert.Equal(t, targetDir, book.FilePath, "book.FilePath should be updated to the new organized directory")
}

// TestOrganizeOneBook_SingleFile_StillUsesOrganizeBook verifies that this
// change is strictly additive: books with 0 or 1 BookFile continue to use
// the existing single-file OrganizeBook path unchanged.
func TestOrganizeOneBook_SingleFile_StillUsesOrganizeBook(t *testing.T) {
	book := &database.Book{ID: "book-single", Title: "Single File Book", FilePath: "/mnt/books/imported/single.m4b"}
	files := []database.BookFile{
		{ID: "f1", BookID: book.ID, FilePath: book.FilePath},
	}

	fake := &fakeDirOrganizer{}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookFiles(book.ID).Return(files, nil)

	imp := newMockImporter(m)
	imp.organizerFactory = func() BookOrganizer { return fake }

	log := logger.New("test")
	err := imp.organizeOneBook(book, log)

	// The fake's OrganizeBook always errors (mirroring the real directory
	// refusal); a single-file book must still hit that path, so the error
	// must propagate and OrganizeBookDirectory must never be called.
	require.Error(t, err)
	assert.False(t, fake.calledDirectory, "single-file book must not route through OrganizeBookDirectory")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// validEmptyXML builds a minimal iTunes Library XML with no audiobook tracks.
func validEmptyXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key><dict/>
	<key>Playlists</key><array/>
</dict>
</plist>`
}

// writeXMLWithAudiobook writes a valid iTunes XML with a single audiobook track
// that maps the given persistent ID to the given on-disk file path, and returns
// the XML file path.
func writeXMLWithAudiobook(t *testing.T, dir, albumTitle, artist, pid, trackFilePath string) string {
	t.Helper()
	location := itunes.EncodeLocation(trackFilePath)
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>101</key>
		<dict>
			<key>Track ID</key><integer>101</integer>
			<key>Persistent ID</key><string>%s</string>
			<key>Name</key><string>Chapter 1</string>
			<key>Artist</key><string>%s</string>
			<key>Album</key><string>%s</string>
			<key>Genre</key><string>Audiobook</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Total Time</key><integer>60000</integer>
			<key>Track Number</key><integer>1</integer>
			<key>Disc Number</key><integer>1</integer>
			<key>Location</key><string>%s</string>
		</dict>
	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>`, pid, artist, albumTitle, location)

	xmlPath := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(xml), 0o644))
	return xmlPath
}
