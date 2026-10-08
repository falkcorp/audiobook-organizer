// file: internal/itunes/service/sync_never_moves_files_test.go
// version: 1.0.0
// guid: 6c2f1e8a-9b4d-4f3a-8e71-2d5c0a9b7f14
// last-edited: 2026-10-07

package itunesservice

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Owner decision 2026-10-07: "Sync never moves files." A sync that matches an
// existing book_file by PID must not write the iTunes location over the row's
// FilePath (it used to: FilePath is bfUpsertOwned, so an organized book was
// pointed back at its old iTunes file). The iTunes location lands in
// ITunesPath only.
func TestSyncLibrary_MatchedFileKeepsOrganizedPath(t *testing.T) {
	imp, store := newSourceFieldsImporter(t)
	itunesFile := sourceFieldsAudioFile(t)
	bookID := seedSyncedBook(t, store, itunesFile)

	organized := filepath.Join(t.TempDir(), "library", "Some Author", "Some Book", "Some Book.m4b")
	staleITunesPath := "file:///old/location/Some%20Book.m4b"
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		BookID:             bookID,
		FilePath:           organized,
		ITunesPath:         staleITunesPath,
		ITunesPersistentID: sourceFieldsPID,
		Format:             "m4b",
	}))

	lib := sourceFieldsLibrary(itunesFile, itunes.SourceFields{}, itunes.Track{Rating: 60, Name: "Some Book"})
	lib.Tracks["1"].TrackNumber = 3
	require.NoError(t, imp.syncLibrary(context.Background(), lib, filepath.Join(t.TempDir(), "lib"), nil, nil, logger.New("test")))

	got, err := store.GetBookFileByPID(sourceFieldsPID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, organized, got.FilePath, "sync must never change a matched row's FilePath")
	assert.Equal(t, "m4b", got.Format)
	assert.Equal(t, itunes.EncodeLocation(itunesFile), got.ITunesPath, "sync records the iTunes location in ITunesPath")
	assert.Equal(t, 3, got.TrackNumber, "sync still fills metadata on the matched row")

	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	assert.Len(t, files, 1, "no second row at the iTunes location")

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, itunesFile, book.FilePath, "sync must not touch the book's FilePath")
}

// A track with no existing book_file is still created at its iTunes location.
func TestSyncLibrary_NewTrackStillCreatesRow(t *testing.T) {
	imp, store := newSourceFieldsImporter(t)
	itunesFile := sourceFieldsAudioFile(t)
	bookID := seedSyncedBook(t, store, itunesFile)

	lib := sourceFieldsLibrary(itunesFile, itunes.SourceFields{}, itunes.Track{Rating: 60})
	require.NoError(t, imp.syncLibrary(context.Background(), lib, filepath.Join(t.TempDir(), "lib"), nil, nil, logger.New("test")))

	got, err := store.GetBookFileByPID(sourceFieldsPID)
	require.NoError(t, err)
	require.NotNil(t, got, "new track must create its book_file row")
	assert.Equal(t, bookID, got.BookID)
	assert.Equal(t, itunesFile, got.FilePath)
	assert.Equal(t, itunes.EncodeLocation(itunesFile), got.ITunesPath)
}
