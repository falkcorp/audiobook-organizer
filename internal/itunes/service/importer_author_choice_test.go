// file: internal/itunes/service/importer_author_choice_test.go
// version: 1.0.0
// guid: 4a1d8e63-9f2b-4c75-b3e0-7d6c1f9a2b58
// last-edited: 2026-09-14

package itunesservice

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Album Artist is the author (owner decision 2026-09-14). The author is chosen
// in assignAuthorAndSeries; the narrator is set by buildBookFromAlbumGroup.
// Each case runs both.
var authorChoiceCases = []struct {
	name         string
	albumArtist  string
	artist       string
	wantAuthor   string
	wantNarrator string // "" means no narrator is set
}{
	{name: "only Artist", artist: "Solo Author", wantAuthor: "Solo Author"},
	{name: "Album Artist equals Artist", albumArtist: "Same Person", artist: "Same Person", wantAuthor: "Same Person"},
	{name: "Album Artist differs from Artist", albumArtist: "Book Author", artist: "Book Narrator", wantAuthor: "Book Author", wantNarrator: "Book Narrator"},
}

func TestAssignAuthorAndSeries_AlbumArtistIsAuthor(t *testing.T) {
	for _, tc := range authorChoiceCases {
		t.Run(tc.name, func(t *testing.T) {
			m := dbmocks.NewMockStore(t)
			m.EXPECT().GetAuthorByName(tc.wantAuthor).Return(nil, nil).Once()
			m.EXPECT().CreateAuthor(tc.wantAuthor).Return(&database.Author{ID: 42, Name: tc.wantAuthor}, nil).Once()

			imp := newMockImporter(m)
			book := &database.Book{}
			imp.assignAuthorAndSeries(book, &itunes.Track{AlbumArtist: tc.albumArtist, Artist: tc.artist})

			require.NotNil(t, book.AuthorID)
			assert.Equal(t, 42, *book.AuthorID)
		})
	}
}

func TestBuildBookFromAlbumGroup_NarratorFromArtistWhenAlbumArtistDiffers(t *testing.T) {
	for _, tc := range authorChoiceCases {
		t.Run(tc.name, func(t *testing.T) {
			trackPath := filepath.Join(t.TempDir(), "track.m4b")
			require.NoError(t, os.WriteFile(trackPath, []byte("not a real m4b"), 0o644))
			track := &itunes.Track{
				Location:     itunes.EncodeLocation(trackPath),
				Name:         "Chapter 1",
				AlbumArtist:  tc.albumArtist,
				Artist:       tc.artist,
				PersistentID: "AUTHORCHOICE01",
				TotalTime:    10000,
				Kind:         "Audiobook",
			}
			imp := newTestImporter()
			group := albumGroup{key: "k", tracks: []*itunes.Track{track}}
			book, err := imp.buildBookFromAlbumGroup(group, "/fake/library.xml", itunes.ImportOptions{}, itunes.XMLSourceFields())
			require.NoError(t, err)
			if tc.wantNarrator == "" {
				assert.Nil(t, book.Narrator)
				return
			}
			require.NotNil(t, book.Narrator)
			assert.Equal(t, tc.wantNarrator, *book.Narrator)
		})
	}
}
