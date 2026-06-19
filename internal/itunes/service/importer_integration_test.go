// file: internal/itunes/service/importer_integration_test.go
// version: 1.1.0
// guid: 8f2e4a1b-7c3d-4f9b-a0e5-3d6c2f8b5a7e
// last-edited: 2026-06-19

package itunesservice

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLibraryPath(t *testing.T) string {
	t.Helper()
	root := testutil.FindRepoRoot(t)
	return filepath.Join(root, "internal", "itunes", "testdata", "test_library.xml")
}

// newIntegrationImporter returns a *Importer wired with the global integration store.
func newIntegrationImporter() *Importer {
	return &Importer{store: database.GetGlobalStore(), cfg: Config{}}
}

// TestITunesImport_ParseAndGroupTestLibrary parses the real test library XML
// and verifies groupTracksByAlbum filters and groups correctly.
func TestITunesImport_ParseAndGroupTestLibrary(t *testing.T) {
	library, err := itunes.ParseLibrary(testLibraryPath(t))
	require.NoError(t, err)
	require.NotNil(t, library)

	imp := newTestImporter()
	groups := imp.groupTracksByAlbum(library)

	// Test library: 9 tracks total; track 300 (Queen) is music, not audiobook.
	// Audiobook groups: Hobbit, Dune, Art of War, Moby Dick (3), Pride (2)
	require.Len(t, groups, 5, "should have 5 audiobook groups (Queen filtered out)")

	byKey := make(map[string]albumGroup)
	for _, g := range groups {
		byKey[g.key] = g
	}

	moby, ok := byKey["Herman Melville|Moby Dick"]
	require.True(t, ok, "Moby Dick group should exist")
	assert.Len(t, moby.tracks, 3)
	assert.Equal(t, 1, moby.tracks[0].TrackNumber)
	assert.Equal(t, 2, moby.tracks[1].TrackNumber)
	assert.Equal(t, 3, moby.tracks[2].TrackNumber)

	pride, ok := byKey["Jane Austen|Pride and Prejudice"]
	require.True(t, ok, "Pride and Prejudice group should exist")
	assert.Len(t, pride.tracks, 2)

	_, ok = byKey["J.R.R. Tolkien|Middle-earth, Book 1"]
	assert.True(t, ok, "Hobbit group should exist")

	_, ok = byKey["Frank Herbert|Dune Chronicles"]
	assert.True(t, ok, "Dune group should exist")

	_, ok = byKey["Sun Tzu|The Art of War"]
	assert.True(t, ok, "Art of War group should exist")
}

// TestITunesImport_BuildAndSaveBooks parses the test library, groups tracks,
// builds books with temp files, and saves to real SQLite.
func TestITunesImport_BuildAndSaveBooks(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	library, err := itunes.ParseLibrary(testLibraryPath(t))
	require.NoError(t, err)

	imp := newIntegrationImporter()
	groups := imp.groupTracksByAlbum(library)
	require.NotEmpty(t, groups)

	tempAudioDir := filepath.Join(env.TempDir, "audiobooks")
	require.NoError(t, os.MkdirAll(tempAudioDir, 0755))

	opts := itunes.ImportOptions{
		PathMappings: []itunes.PathMapping{
			{From: "file://localhost/Users/testuser/Music/iTunes", To: "file://localhost" + tempAudioDir},
		},
	}

	for _, track := range library.Tracks {
		if !itunes.IsAudiobook(track) {
			continue
		}
		remapped := opts.RemapPath(track.Location)
		decoded, err := itunes.DecodeLocation(remapped)
		require.NoError(t, err, "decode location for track %d", track.TrackID)
		require.NoError(t, os.MkdirAll(filepath.Dir(decoded), 0755))
		require.NoError(t, os.WriteFile(decoded, []byte("fake-audio-data"), 0644))
	}

	var savedBooks []*database.Book
	for _, group := range groups {
		book, err := imp.buildBookFromAlbumGroup(group, testLibraryPath(t), opts)
		require.NoError(t, err, "build book for group %s", group.key)

		created, err := database.GetGlobalStore().CreateBook(book)
		require.NoError(t, err, "save book %s", book.Title)
		savedBooks = append(savedBooks, created)
	}

	assert.Len(t, savedBooks, 5)

	for _, book := range savedBooks {
		fetched, err := database.GetGlobalStore().GetBookByID(book.ID)
		require.NoError(t, err)
		require.NotNil(t, fetched)
		assert.Equal(t, book.Title, fetched.Title)
	}

	for _, book := range savedBooks {
		if book.Title == "Moby Dick" {
			require.NotNil(t, book.Duration)
			assert.Equal(t, 10200, *book.Duration, "Moby Dick should have summed duration")
			require.NotNil(t, book.FileSize)
			assert.Equal(t, int64(50000000+45000000+48000000), *book.FileSize, "Moby Dick should have summed file size")
		}
	}

	for _, book := range savedBooks {
		if book.Title == "Pride and Prejudice" {
			require.NotNil(t, book.Duration)
			assert.Equal(t, 10200, *book.Duration)
		}
	}
}

// TestITunesImport_AuthorAndSeriesCreation tests assignAuthorAndSeries with real SQLite.
func TestITunesImport_AuthorAndSeriesCreation(t *testing.T) {
	_, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	imp := newIntegrationImporter()

	book1 := &database.Book{Title: "Dune"}
	track1 := &itunes.Track{
		Artist: "Frank Herbert",
		Album:  "Dune Chronicles",
	}
	imp.assignAuthorAndSeries(book1, track1)

	require.NotNil(t, book1.AuthorID, "AuthorID should be set")
	author, err := database.GetGlobalStore().GetAuthorByName("Frank Herbert")
	require.NoError(t, err)
	require.NotNil(t, author)
	assert.Equal(t, *book1.AuthorID, author.ID)

	require.NotNil(t, book1.SeriesID, "SeriesID should be set")
	series, err := database.GetGlobalStore().GetSeriesByName("Dune Chronicles", book1.AuthorID)
	require.NoError(t, err)
	require.NotNil(t, series)
	assert.Equal(t, *book1.SeriesID, series.ID)

	book2 := &database.Book{Title: "Dune Messiah"}
	track2 := &itunes.Track{
		Artist: "Frank Herbert",
		Album:  "Dune Messiah",
	}
	imp.assignAuthorAndSeries(book2, track2)

	require.NotNil(t, book2.AuthorID)
	assert.Equal(t, *book1.AuthorID, *book2.AuthorID, "same author should be reused")

	book3 := &database.Book{Title: "The Hobbit"}
	track3 := &itunes.Track{
		Artist: "J.R.R. Tolkien",
		Album:  "Middle-earth, Book 1",
	}
	imp.assignAuthorAndSeries(book3, track3)

	require.NotNil(t, book3.SeriesID)
	seriesMiddle, err := database.GetGlobalStore().GetSeriesByName("Middle-earth", book3.AuthorID)
	require.NoError(t, err)
	require.NotNil(t, seriesMiddle)
	assert.Equal(t, *book3.SeriesID, seriesMiddle.ID)
}

// TestITunesImport_AuthorDedup verifies that two tracks by the same artist
// result in a single author record in the database.
func TestITunesImport_AuthorDedup(t *testing.T) {
	_, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	imp := newIntegrationImporter()

	bookA := &database.Book{Title: "Book A"}
	trackA := &itunes.Track{Artist: "Isaac Asimov", Album: "Foundation"}
	imp.assignAuthorAndSeries(bookA, trackA)

	bookB := &database.Book{Title: "Book B"}
	trackB := &itunes.Track{Artist: "Isaac Asimov", Album: "I, Robot"}
	imp.assignAuthorAndSeries(bookB, trackB)

	require.NotNil(t, bookA.AuthorID)
	require.NotNil(t, bookB.AuthorID)
	assert.Equal(t, *bookA.AuthorID, *bookB.AuthorID, "both books should share the same author ID")

	authors, err := database.GetGlobalStore().GetAllAuthors()
	require.NoError(t, err)
	count := 0
	for _, a := range authors {
		if a.Name == "Isaac Asimov" {
			count++
		}
	}
	assert.Equal(t, 1, count, "should have exactly one Isaac Asimov author record")
}

// TestITunesImport_SeriesExtraction tests extractSeriesName and verifies
// series records are created in the DB.
func TestITunesImport_SeriesExtraction(t *testing.T) {
	_, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	tests := []struct {
		album          string
		expectedSeries string
	}{
		{"Middle-earth, Book 1", "Middle-earth"},
		{"The Dark Tower: Book 3", "The Dark Tower"},
		{"Wheel of Time - Book 5", "Wheel of Time"},
		{"Dune Chronicles", "Dune Chronicles"},
		{"", ""},
	}

	for _, tc := range tests {
		result := extractSeriesName(tc.album)
		assert.Equal(t, tc.expectedSeries, result, "extractSeriesName(%q)", tc.album)
	}

	imp := newIntegrationImporter()
	book := &database.Book{Title: "Test Book"}
	track := &itunes.Track{
		Artist: "Test Author",
		Album:  "Middle-earth, Book 1",
	}
	imp.assignAuthorAndSeries(book, track)

	require.NotNil(t, book.SeriesID)
	series, err := database.GetGlobalStore().GetSeriesByID(*book.SeriesID)
	require.NoError(t, err)
	require.NotNil(t, series)
	assert.Equal(t, "Middle-earth", series.Name)

	book2 := &database.Book{Title: "Test Book 2"}
	track2 := &itunes.Track{
		Artist: "Test Author",
		Album:  "Middle-earth, Book 2",
	}
	imp.assignAuthorAndSeries(book2, track2)
	require.NotNil(t, book2.SeriesID)
	assert.Equal(t, *book.SeriesID, *book2.SeriesID, "same series name should yield same series ID")
}

// TestITunesImport_MultiTrackBookSegments tests creating BookFiles for
// multi-track albums.
func TestITunesImport_MultiTrackBookSegments(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	library, err := itunes.ParseLibrary(testLibraryPath(t))
	require.NoError(t, err)

	imp := newIntegrationImporter()
	groups := imp.groupTracksByAlbum(library)

	var mobyGroup *albumGroup
	for i, g := range groups {
		if g.key == "Herman Melville|Moby Dick" {
			mobyGroup = &groups[i]
			break
		}
	}
	require.NotNil(t, mobyGroup, "Moby Dick group should exist")
	require.Len(t, mobyGroup.tracks, 3)

	tempDir := filepath.Join(env.TempDir, "audiobooks")
	opts := itunes.ImportOptions{
		PathMappings: []itunes.PathMapping{
			{From: "file://localhost/Users/testuser/Music/iTunes", To: "file://localhost" + tempDir},
		},
	}

	for _, track := range mobyGroup.tracks {
		remapped := opts.RemapPath(track.Location)
		decoded, err := itunes.DecodeLocation(remapped)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(decoded), 0755))
		require.NoError(t, os.WriteFile(decoded, []byte("fake-audio"), 0644))
	}

	book, err := imp.buildBookFromAlbumGroup(*mobyGroup, testLibraryPath(t), opts)
	require.NoError(t, err)

	created, err := database.GetGlobalStore().CreateBook(book)
	require.NoError(t, err)

	for i, track := range mobyGroup.tracks {
		trackLoc := opts.RemapPath(track.Location)
		trackPath, err := itunes.DecodeLocation(trackLoc)
		require.NoError(t, err)

		bf := &database.BookFile{
			ID:          fmt.Sprintf("bf-%d", i),
			BookID:      created.ID,
			FilePath:    trackPath,
			Format:      "m4b",
			FileSize:    int64(track.Size),
			Duration:    trackDurationSeconds(track), // CONS-16: seconds, not raw ms
			TrackNumber: track.TrackNumber,
			TrackCount:  len(mobyGroup.tracks),
		}
		require.NoError(t, database.GetGlobalStore().CreateBookFile(bf))
	}

	files, err := database.GetGlobalStore().GetBookFiles(created.ID)
	require.NoError(t, err)
	require.Len(t, files, 3)

	// Index expected per-track seconds by track number so we can assert each
	// stored BookFile.Duration is in seconds (CONS-16 regression guard).
	wantSecByTrack := make(map[int]int, len(mobyGroup.tracks))
	for _, track := range mobyGroup.tracks {
		wantSecByTrack[track.TrackNumber] = int(track.TotalTime / 1000)
	}
	for _, bf := range files {
		assert.Equal(t, len(mobyGroup.tracks), bf.TrackCount)
		assert.NotZero(t, bf.Duration)
		assert.Equal(t, wantSecByTrack[bf.TrackNumber], bf.Duration,
			"BookFile.Duration must be seconds, not milliseconds")
	}
}

// TestITunesImport_PlaylistTagExtraction tests ExtractPlaylistTags against
// the test library's playlists.
func TestITunesImport_PlaylistTagExtraction(t *testing.T) {
	library, err := itunes.ParseLibrary(testLibraryPath(t))
	require.NoError(t, err)

	tags100 := itunes.ExtractPlaylistTags(100, library.Playlists)
	assert.Contains(t, tags100, "sci-fi favorites", "track 100 should have sci-fi favorites tag")
	assert.NotContains(t, tags100, "audiobooks", "built-in Audiobooks should be filtered")
	assert.NotContains(t, tags100, "music", "Music playlist should be filtered")

	tags200 := itunes.ExtractPlaylistTags(200, library.Playlists)
	assert.Contains(t, tags200, "sci-fi favorites")
	assert.NotContains(t, tags200, "audiobooks")

	tags400 := itunes.ExtractPlaylistTags(400, library.Playlists)
	assert.NotContains(t, tags400, "audiobooks")
	assert.NotContains(t, tags400, "recently added")
	assert.Empty(t, tags400, "Art of War should have no user playlist tags")

	tags300 := itunes.ExtractPlaylistTags(300, library.Playlists)
	assert.Empty(t, tags300)
}
