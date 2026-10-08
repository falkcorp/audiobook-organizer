// file: internal/itunes/service/reimport_never_moves_files_test.go
// version: 2.0.0
// guid: 6c2f1e8a-9b4d-4f3a-8e71-2d5c0a9b7f14
// last-edited: 2026-10-08

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
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Owner decision 2026-10-07, "Sync never moves files", carried over to import
// when the incremental sync was removed (2026-10-08): a re-import that matches
// an existing book never moves a file and never changes a stored FilePath.
// The link writes only the book's iTunes fields; the book's FilePath and every
// book_file row, including its FilePath and ITunesPath, stay as they were.
func TestReimport_MatchedFileKeepsOrganizedPath(t *testing.T) {
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
	runImportLibrary(t, imp, lib)

	got, err := store.GetBookFileByPID(sourceFieldsPID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, organized, got.FilePath, "a re-import must never change a matched row's FilePath")
	assert.Equal(t, "m4b", got.Format)
	assert.Equal(t, staleITunesPath, got.ITunesPath, "a link writes no book_file row")

	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	assert.Len(t, files, 1, "no second row at the iTunes location")

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, itunesFile, book.FilePath, "a re-import must not touch the book's FilePath")
	require.NotNil(t, book.ITunesRating)
	assert.Equal(t, 60, *book.ITunesRating, "the link still refreshed the iTunes fields")
	assert.Len(t, allBooks(t, store), 1, "the re-import must link, not add a book")
}

// Importing the same library twice adds nothing the second time: every album
// links to the book the first run created (by its iTunes ID), and the second
// run's linked count equals the album count. Synthetic library only.
func TestExecute_ImportSameLibraryTwice_AddsNoBooks(t *testing.T) {
	store := newRegressionStore(t)
	dir := t.TempDir()

	type album struct{ title, author string }
	albums := []album{
		{"Synthetic Book One", "Author One"},
		{"Synthetic Book Two", "Author Two"},
		{"Synthetic Book Three", "Author Three"},
	}
	const tracksPerAlbum = 2

	var tracksXML bytes.Buffer
	trackID := 0
	for ai, a := range albums {
		albumDir := filepath.Join(dir, "Music", a.author, a.title)
		require.NoError(t, os.MkdirAll(albumDir, 0o755))
		for ti := 1; ti <= tracksPerAlbum; ti++ {
			trackID++
			p := filepath.Join(albumDir, fmt.Sprintf("part%d.m4b", ti))
			require.NoError(t, os.WriteFile(p, bytes.Repeat([]byte{byte('a' + ai)}, 256*ti), 0o644))
			fmt.Fprintf(&tracksXML, `
		<key>%d</key>
		<dict>
			<key>Track ID</key><integer>%d</integer>
			<key>Persistent ID</key><string>SYNTH%02d%02d</string>
			<key>Name</key><string>%s Part %d</string>
			<key>Album</key><string>%s</string>
			<key>Artist</key><string>%s</string>
			<key>Genre</key><string>Audiobook</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Track Number</key><integer>%d</integer>
			<key>Track Count</key><integer>%d</integer>
			<key>Play Count</key><integer>%d</integer>
			<key>Location</key><string>%s</string>
		</dict>`, trackID, trackID, ai, ti, a.title, ti, a.title, a.author, ti, tracksPerAlbum, ai+1, itunes.EncodeLocation(p))
		}
	}
	xmlPath := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>`+tracksXML.String()+`
	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>`), 0o644))

	imp := newImporter(Deps{Store: store, Config: Config{}})
	req := ImportRequest{LibraryPath: xmlPath, ImportMode: "import", PreserveLocation: true}

	require.NoError(t, imp.Execute(context.Background(), "op-first", req, logger.New("test")))
	first := imp.GetStatus("op-first")
	require.Equal(t, len(albums), first.Imported, "the first run adds one book per album (errors: %v)", first.Errors)
	require.Equal(t, 0, first.Linked)
	booksAfterFirst := allBooks(t, store)
	require.Len(t, booksAfterFirst, len(albums))
	pathsAfterFirst := map[string]string{}
	for _, b := range booksAfterFirst {
		pathsAfterFirst[b.ID] = b.FilePath
	}

	require.NoError(t, imp.Execute(context.Background(), "op-second", req, logger.New("test")))
	second := imp.GetStatus("op-second")
	assert.Equal(t, 0, second.Imported, "the second run must add no books (errors: %v)", second.Errors)
	assert.Equal(t, len(albums), second.Linked, "every album links to the book the first run created")
	assert.Equal(t, 0, second.Skipped)
	assert.Equal(t, 0, second.Failed, "errors: %v", second.Errors)

	booksAfterSecond := allBooks(t, store)
	require.Len(t, booksAfterSecond, len(albums), "the second run created a duplicate")
	for _, b := range booksAfterSecond {
		assert.Equal(t, pathsAfterFirst[b.ID], b.FilePath, "a re-import must not change a book's FilePath")
	}
}

// countingOrganizer records every organize call. A re-import must make none
// for a book it only linked.
type countingOrganizer struct {
	mu    sync.Mutex
	calls []string
}

func (o *countingOrganizer) OrganizeSingleFile(book *database.Book) (*organizer.Landing, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, book.ID)
	return nil, fmt.Errorf("organize called for %s", book.ID)
}

func (o *countingOrganizer) OrganizeBookDirectory(book *database.Book, _ []database.BookFile) (*organizer.Landing, error) {
	return o.OrganizeSingleFile(book)
}

// A re-import in organize mode organizes only the books it created. Until
// 2026-10-08 the organize phase swept every book in "imported" state with an
// iTunes import source, so it organized -- copied, and repointed the FilePath
// of -- a book the re-import had only linked.
func TestExecute_ReimportInOrganizeModeLeavesLinkedBookAlone(t *testing.T) {
	store := newRegressionStore(t)
	dir := t.TempDir()
	trackPath := filepath.Join(dir, "linked.m4b")
	require.NoError(t, os.WriteFile(trackPath, bytes.Repeat([]byte("l"), 512), 0o644))
	pid := "REIMPORT_ORGANIZE_PID"
	xmlPath := writeXMLWithAudiobook(t, dir, "Linked Book", "Author L", pid, trackPath)

	linked, err := store.CreateBook(&database.Book{
		Title: "Linked Book", FilePath: trackPath, Format: "m4b",
		LibraryState: new("imported"), ITunesImportSource: new(xmlPath),
	})
	require.NoError(t, err)

	org := &countingOrganizer{}
	imp := newImporter(Deps{Store: store, Config: Config{}})
	imp.organizerFactory = func() BookOrganizer { return org }
	req := ImportRequest{LibraryPath: xmlPath, ImportMode: "organize"}
	require.NoError(t, imp.Execute(context.Background(), "op-reimport-organize", req, logger.New("test")))

	snap := imp.GetStatus("op-reimport-organize")
	require.Equal(t, 1, snap.Linked, "errors: %v", snap.Errors)
	require.Equal(t, 0, snap.Imported)
	assert.Empty(t, org.calls, "a linked book must not be organized by a re-import")

	got, err := store.GetBookByID(linked.ID)
	require.NoError(t, err)
	assert.Equal(t, trackPath, got.FilePath, "a re-import must not change a linked book's FilePath")
	require.NotNil(t, got.LibraryState)
	assert.Equal(t, "imported", *got.LibraryState)
}
