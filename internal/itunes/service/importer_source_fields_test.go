// file: internal/itunes/service/importer_source_fields_test.go
// version: 1.0.0
// guid: 0dfe3afe-fcd9-49ee-a139-7a5fd7ad56c9
// last-edited: 2026-09-11

package itunesservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the fix for an ITL-sourced sync zeroing stored bookmarks.
// ParseITLAsLibrary decodes no bookmark, so every ITL track arrives with
// Bookmark == 0, and before itunes.SourceFields the existing-book update path
// in Sync wrote that 0 over whatever the book had. The Library values here use
// the same capability sets the real parsers declare; source_fields_test.go in
// package itunes pins the parsers to those sets.

const sourceFieldsPID = "A1B2C3D4E5F60718"

var (
	seededLastPlayed = time.Unix(1_700_000_000, 0).UTC()
	laterPlayed      = time.Unix(1_750_000_000, 0).UTC()
)

const seededBookmarkMs int64 = 4_321_000

func newSourceFieldsImporter(t *testing.T) (*Importer, *database.PebbleStore) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return &Importer{store: store, cfg: Config{}}, store
}

// sourceFieldsAudioFile creates a real file, because buildBookFromAlbumGroup
// stats the decoded track location.
func sourceFieldsAudioFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Audiobooks", "Some Author", "Some Book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("audio"), 0o644))
	return path
}

// sourceFieldsLibrary wraps one audiobook track in a Library declaring carries.
// Only the playback/rating fields of track are taken from the caller.
func sourceFieldsLibrary(filePath string, carries itunes.SourceFields, track itunes.Track) *itunes.Library {
	track.TrackID = 1
	track.PersistentID = sourceFieldsPID
	track.Name = "Some Book"
	track.Album = "Some Book"
	track.Artist = "Some Author"
	track.Kind = "Audiobook"
	track.Location = itunes.EncodeLocation(filePath)
	return &itunes.Library{Tracks: map[string]*itunes.Track{"1": &track}, Carries: carries}
}

// seedSyncedBook stores a book an earlier XML sync already populated with a
// bookmark, play count and last-played date.
func seedSyncedBook(t *testing.T, store *database.PebbleStore, filePath string) string {
	t.Helper()
	lastPlayed := seededLastPlayed
	created, err := store.CreateBook(&database.Book{
		Title:              "Some Book",
		FilePath:           filePath,
		Format:             "m4b",
		ITunesPersistentID: new(sourceFieldsPID),
		ITunesBookmark:     new(seededBookmarkMs),
		ITunesPlayCount:    new(7),
		ITunesLastPlayed:   &lastPlayed,
	})
	require.NoError(t, err)
	return created.ID
}

type playbackWant struct {
	bookmark   int64
	playCount  int
	lastPlayed time.Time
}

func runSyncAndRead(t *testing.T, carries itunes.SourceFields, track itunes.Track) *database.Book {
	t.Helper()
	imp, store := newSourceFieldsImporter(t)
	filePath := sourceFieldsAudioFile(t)
	bookID := seedSyncedBook(t, store, filePath)

	lib := sourceFieldsLibrary(filePath, carries, track)
	require.NoError(t, imp.syncLibrary(context.Background(), lib, filepath.Join(t.TempDir(), "lib"), nil, nil, logger.New("test")))

	got, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, got)
	// Rating is carried by every source and the seeded book has none, so a
	// rating of 60 proves UpdateBook actually ran. Without it, "the bookmark
	// survived" could just mean nothing was written at all.
	require.NotNil(t, got.ITunesRating, "sync must have written the book")
	require.Equal(t, 60, *got.ITunesRating, "sync must have written the book")
	return got
}

func assertPlayback(t *testing.T, got *database.Book, want playbackWant) {
	t.Helper()
	require.NotNil(t, got.ITunesBookmark)
	assert.Equal(t, want.bookmark, *got.ITunesBookmark, "ITunesBookmark")
	require.NotNil(t, got.ITunesPlayCount)
	assert.Equal(t, want.playCount, *got.ITunesPlayCount, "ITunesPlayCount")
	require.NotNil(t, got.ITunesLastPlayed)
	assert.True(t, got.ITunesLastPlayed.Equal(want.lastPlayed),
		"ITunesLastPlayed = %v, want %v", got.ITunesLastPlayed, want.lastPlayed)
}

func TestSyncLibrary_UncarriedPlaybackFieldsArePreserved(t *testing.T) {
	cases := []struct {
		name    string
		carries itunes.SourceFields
		track   itunes.Track
		want    playbackWant
	}{
		{
			// What ParseITLAsLibrary produces: Bookmark is always 0 and an
			// undecoded last-played date is 0.
			name:    "ITL source keeps bookmark, play count and last-played",
			carries: itunes.ITLSourceFields(),
			track:   itunes.Track{PlayCount: 7, Rating: 60},
			want:    playbackWant{bookmark: seededBookmarkMs, playCount: 7, lastPlayed: seededLastPlayed},
		},
		{
			// ITL does carry play count (mhit offset 76), so a changed count
			// must still land. Guards against the flag being too broad.
			name:    "ITL source still updates the play count it carries",
			carries: itunes.ITLSourceFields(),
			track:   itunes.Track{PlayCount: 9, PlayDate: laterPlayed.Unix(), Rating: 60},
			want:    playbackWant{bookmark: seededBookmarkMs, playCount: 9, lastPlayed: laterPlayed},
		},
		{
			// A Library whose constructor never declared its capabilities
			// fails closed: none of the three fields is written.
			name:    "undeclared source writes no playback field",
			carries: itunes.SourceFields{},
			track:   itunes.Track{PlayCount: 0, PlayDate: laterPlayed.Unix(), Rating: 60},
			want:    playbackWant{bookmark: seededBookmarkMs, playCount: 7, lastPlayed: seededLastPlayed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runSyncAndRead(t, tc.carries, tc.track)
			assertPlayback(t, got, tc.want)
		})
	}
}

func TestSyncLibrary_XMLSourceWritesPlaybackFields(t *testing.T) {
	cases := []struct {
		name  string
		track itunes.Track
		want  playbackWant
	}{
		{
			name:  "new values are written",
			track: itunes.Track{Bookmark: 99_000, PlayCount: 8, PlayDate: laterPlayed.Unix(), Rating: 60},
			want:  playbackWant{bookmark: 99_000, playCount: 8, lastPlayed: laterPlayed},
		},
		{
			// iTunes XML drops the Bookmark key once a book is finished or
			// its bookmark is cleared; that decodes to 0 and is a real reset.
			// Play count 0 is likewise written. Play Date 0 never cleared
			// last-played before this fix and still does not.
			name:  "genuine zero from XML is a reset",
			track: itunes.Track{Bookmark: 0, PlayCount: 0, PlayDate: 0, Rating: 60},
			want:  playbackWant{bookmark: 0, playCount: 0, lastPlayed: seededLastPlayed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runSyncAndRead(t, itunes.XMLSourceFields(), tc.track)
			assertPlayback(t, got, tc.want)
		})
	}
}

func TestSyncLibrary_ITLInsertLeavesBookmarkUnset(t *testing.T) {
	imp, store := newSourceFieldsImporter(t)
	filePath := sourceFieldsAudioFile(t)

	lib := sourceFieldsLibrary(filePath, itunes.ITLSourceFields(), itunes.Track{PlayCount: 3, Rating: 20})
	require.NoError(t, imp.syncLibrary(context.Background(), lib, filepath.Join(t.TempDir(), "lib"), nil, nil, logger.New("test")))

	books, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	var bookID string
	for _, b := range books {
		if b.ITunesPersistentID != nil && *b.ITunesPersistentID == sourceFieldsPID {
			bookID = b.ID
		}
	}
	require.NotEmpty(t, bookID, "sync must have inserted the book")

	got, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, got)
	// nil, not &0: linkITunesMetadata only fills nil fields, so &0 would stop
	// a later XML import from supplying the real bookmark.
	assert.Nil(t, got.ITunesBookmark, "an ITL insert has no bookmark to record")
	assert.Nil(t, got.ITunesLastPlayed, "no play date was decoded")
	require.NotNil(t, got.ITunesPlayCount, "ITL carries play count")
	assert.Equal(t, 3, *got.ITunesPlayCount)
}
