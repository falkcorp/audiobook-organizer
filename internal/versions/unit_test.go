// file: internal/versions/unit_test.go
// version: 1.6.1
// guid: 5d2f8a14-3c67-4e09-b1d8-7f4a2e6c9b03
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// CreateIngestVersion — mock-based unit tests
// --------------------------------------------------------------------------

func TestCreateIngestVersion_EmptyBookID(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	_, _, err := CreateIngestVersion(mockStore, IngestVersionParams{
		FilePath: "/tmp/book.m4b",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "book_id and file_path required")
}

func TestCreateIngestVersion_EmptyFilePath(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	_, _, err := CreateIngestVersion(mockStore, IngestVersionParams{
		BookID: "book-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "book_id and file_path required")
}

func TestCreateIngestVersion_StoreErrorOnCreate(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	// Allow-all import path so the path gate passes; this test exercises
	// version-creation error handling, not path validation.
	mockStore.EXPECT().GetAllImportPaths().Return([]database.ImportPath{{Path: "/"}}, nil)
	// The row lookup runs before anything is created (idempotency check).
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)
	// No torrent hash so fingerprint check is skipped.
	// First version — no active version exists.
	mockStore.EXPECT().GetActiveVersionForBook("book-1").Return(nil, errors.New("not found"))
	mockStore.EXPECT().CreateBookVersion(mock.AnythingOfType("*database.BookVersion")).Return(nil, errors.New("db write failed"))

	_, _, err := CreateIngestVersion(mockStore, IngestVersionParams{
		BookID:   "book-1",
		FilePath: "/nonexistent/path.m4b",
		Format:   "m4b",
		Source:   "imported",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create version")
}

// TestCreateIngestVersion_RejectsDisallowedPath verifies the allow-list gate:
// a path outside the configured import paths is rejected before any version
// row is created or file is read (go/path-injection).
func TestCreateIngestVersion_RejectsDisallowedPath(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetAllImportPaths().Return([]database.ImportPath{}, nil)

	_, _, err := CreateIngestVersion(mockStore, IngestVersionParams{
		BookID:   "book-1",
		FilePath: "/etc/passwd",
		Format:   "m4b",
		Source:   "imported",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed")
}

func TestCreateIngestVersion_FingerprintBlocksPurgedTorrent_Mock(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetAllImportPaths().Return([]database.ImportPath{{Path: "/"}}, nil)
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)
	// Torrent hash matches a purged version.
	mockStore.EXPECT().GetBookVersionByTorrentHash("bad-hash").Return(&database.BookVersion{
		ID:     "v-old",
		BookID: "old-book",
		Status: database.BookVersionStatusInactivePurged,
	}, nil)

	_, _, err := CreateIngestVersion(mockStore, IngestVersionParams{
		BookID:      "book-1",
		FilePath:    "/tmp/book.m4b",
		Format:      "m4b",
		Source:      "deluge",
		TorrentHash: "bad-hash",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fingerprint match")
}

func TestCreateIngestVersion_FirstVersionIsActive_Mock(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetAllImportPaths().Return([]database.ImportPath{{Path: "/"}}, nil)
	// No active version exists.
	mockStore.EXPECT().GetActiveVersionForBook("book-1").Return(nil, errors.New("not found"))

	// Expect CreateBookVersion with status=active.
	mockStore.EXPECT().CreateBookVersion(mock.MatchedBy(func(v *database.BookVersion) bool {
		return v.Status == database.BookVersionStatusActive && v.BookID == "book-1"
	})).Return(&database.BookVersion{
		ID: "v-1", BookID: "book-1", Status: database.BookVersionStatusActive,
	}, nil)

	// The file lookup is required, not .Maybe(): it was optional while the
	// linkage lived inside the hash check's else branch, which let the mock
	// pass whether or not the orphaned-version bug was present. It now runs
	// first, before the version is created, and no row is returned, so the
	// version must be reported unlinked.
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)

	ver, linked, err := CreateIngestVersion(mockStore, IngestVersionParams{
		BookID:   "book-1",
		FilePath: "/nonexistent/path.m4b",
		Format:   "m4b",
		Source:   "imported",
	})
	require.NoError(t, err)
	assert.Equal(t, database.BookVersionStatusActive, ver.Status)
	assert.False(t, linked, "no book_file row exists, so nothing can be linked")
}

func TestCreateIngestVersion_SecondVersionIsAlt_Mock(t *testing.T) {
	mockStore := mocks.NewMockStore(t)

	mockStore.EXPECT().GetAllImportPaths().Return([]database.ImportPath{{Path: "/"}}, nil)
	// Active version already exists.
	mockStore.EXPECT().GetActiveVersionForBook("book-1").Return(&database.BookVersion{
		ID: "v-existing", BookID: "book-1", Status: database.BookVersionStatusActive,
	}, nil)

	mockStore.EXPECT().CreateBookVersion(mock.MatchedBy(func(v *database.BookVersion) bool {
		return v.Status == database.BookVersionStatusAlt
	})).Return(&database.BookVersion{
		ID: "v-2", BookID: "book-1", Status: database.BookVersionStatusAlt,
	}, nil)

	// See the note in the sibling test: the file lookup must run.
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, nil)

	ver, _, err := CreateIngestVersion(mockStore, IngestVersionParams{
		BookID:   "book-1",
		FilePath: "/nonexistent/path.mp3",
		Format:   "mp3",
		Source:   "deluge",
	})
	require.NoError(t, err)
	assert.Equal(t, database.BookVersionStatusAlt, ver.Status)
}

// --------------------------------------------------------------------------
// CheckFingerprint — mock-based unit tests
// --------------------------------------------------------------------------

func TestCheckFingerprint_NoMatchEmptyInputs(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	match := CheckFingerprint(mockStore, "", nil)
	assert.False(t, match.Matched)
}

func TestCheckFingerprint_NoMatchUnknownHash(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookVersionByTorrentHash("unknown").Return(nil, errors.New("not found"))

	match := CheckFingerprint(mockStore, "unknown", nil)
	assert.False(t, match.Matched)
}

func TestCheckFingerprint_MatchPurgedTorrent(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookVersionByTorrentHash("purged-hash").Return(&database.BookVersion{
		ID:     "v-1",
		BookID: "b-1",
		Status: database.BookVersionStatusInactivePurged,
	}, nil)

	match := CheckFingerprint(mockStore, "purged-hash", nil)
	require.True(t, match.Matched)
	assert.Equal(t, "torrent_hash", match.MatchType)
	assert.Equal(t, "b-1", match.BookID)
	assert.Equal(t, database.BookVersionStatusInactivePurged, match.Status)
}

func TestCheckFingerprint_ActiveNotBlocked(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBookVersionByTorrentHash("active-hash").Return(&database.BookVersion{
		ID:     "v-1",
		BookID: "b-1",
		Status: database.BookVersionStatusActive,
	}, nil)

	match := CheckFingerprint(mockStore, "active-hash", nil)
	assert.False(t, match.Matched)
}

// --------------------------------------------------------------------------
// CleanupTrashedVersions — mock-based unit tests
// --------------------------------------------------------------------------

func TestCleanupTrashedVersions_EmptyList(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().ListTrashedBookVersions().Return([]database.BookVersion{}, nil)
	// The run also lists the swaps that did not finish (read-only).
	mockStore.EXPECT().ListSwappingBookVersions().Return([]database.BookVersion{}, nil)

	purged, failed, err := cleanupNow(mockStore)
	assert.Equal(t, 0, purged)
	assert.Equal(t, 0, failed)
	assert.NoError(t, err)
}

func TestCleanupTrashedVersions_StoreError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().ListTrashedBookVersions().Return(nil, errors.New("db error"))

	purged, failed, err := cleanupNow(mockStore)
	assert.Equal(t, 0, purged)
	assert.Equal(t, 0, failed, "no version was attempted")
	// A trash that cannot be listed is a failed run, not a successful zero.
	require.Error(t, err)
	assert.ErrorContains(t, err, "db error")
}

// --------------------------------------------------------------------------
// isPurgedOrBlocked (helper)
// --------------------------------------------------------------------------

func TestIsPurgedOrBlocked(t *testing.T) {
	assert.True(t, isPurgedOrBlocked(database.BookVersionStatusInactivePurged))
	assert.True(t, isPurgedOrBlocked(database.BookVersionStatusBlockedForRedownload))
	assert.True(t, isPurgedOrBlocked(database.BookVersionStatusTrash))
	assert.True(t, isPurgedOrBlocked(database.BookVersionStatusPurging))
	assert.False(t, isPurgedOrBlocked(database.BookVersionStatusActive))
	assert.False(t, isPurgedOrBlocked(database.BookVersionStatusAlt))
	assert.False(t, isPurgedOrBlocked(database.BookVersionStatusPending))
	assert.False(t, isPurgedOrBlocked(""))
}
