// file: internal/itunes/service/importer_author_choice_test.go
// version: 1.2.1
// guid: 4a1d8e63-9f2b-4c75-b3e0-7d6c1f9a2b58
// last-edited: 2026-10-04

package itunesservice

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// expectTitles lets the shared resolver read the (empty) title index.
func expectTitles(m *dbmocks.MockStore) {
	m.EXPECT().GetAllSeries().Return(nil, nil).Maybe()
	m.EXPECT().GetAllBooksCore(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().FindAuthorByAlias(mock.Anything).Return(nil, nil).Maybe()
}

// A credit the splitter will not split whose pieces are existing authors is
// not created as one combined author row (authorcredit.ErrCombinedCredit):
// the book is left without an author, and the mock fails on any CreateAuthor.
func TestAssignAuthorAndSeries_CombinedOfExistingAuthorsIsNotCreated(t *testing.T) {
	// Four existing authors: all linked in credit order, nothing created
	// (#3729 review B1: authors are an ordered credit list; the part cap
	// guards only NEW authors).
	m := dbmocks.NewMockStore(t)
	expectTitles(m)
	known := map[string]int{"Amy Adams": 1, "Ben Brown": 2, "Cat Cole": 3, "Dan Dorn": 4}
	m.EXPECT().GetAuthorByName(mock.Anything).RunAndReturn(func(n string) (*database.Author, error) {
		if id, ok := known[n]; ok {
			return &database.Author{ID: id, Name: n}, nil
		}
		return nil, nil
	})

	imp := newMockImporter(m)
	book := &database.Book{}
	imp.assignAuthorAndSeries(book, &itunes.Track{AlbumArtist: "Amy Adams, Ben Brown, Cat Cole, Dan Dorn"})
	require.NotNil(t, book.AuthorID)
	assert.Equal(t, 1, *book.AuthorID)
	require.Len(t, book.Authors, 4)
	for i, ba := range book.Authors {
		assert.Equal(t, i+1, ba.AuthorID)
		assert.Equal(t, i, ba.Position)
	}
}

// #3729 review B1 repro: a part that is also a series name ("Michael
// Anderle") does not stop two existing authors from being linked.
func TestAssignAuthorAndSeries_ExistingAuthorNamedLikeASeriesIsLinked(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllSeries().Return([]database.Series{{ID: 9, Name: "Michael Anderle"}}, nil).Maybe()
	m.EXPECT().GetAllBooksCore(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.EXPECT().FindAuthorByAlias(mock.Anything).Return(nil, nil).Maybe()
	known := map[string]int{"Michael Anderle": 1, "Craig Martelle": 2}
	m.EXPECT().GetAuthorByName(mock.Anything).RunAndReturn(func(n string) (*database.Author, error) {
		if id, ok := known[n]; ok {
			return &database.Author{ID: id, Name: n}, nil
		}
		return nil, nil
	})

	imp := newMockImporter(m)
	book := &database.Book{}
	imp.assignAuthorAndSeries(book, &itunes.Track{AlbumArtist: "Michael Anderle, Craig Martelle"})
	require.NotNil(t, book.AuthorID)
	assert.Equal(t, 1, *book.AuthorID)
	require.Len(t, book.Authors, 2)
	assert.Equal(t, 2, book.Authors[1].AuthorID)
}

// A single-word pen name that is an author already splits with its co-author
// (owner decision 2026-10-04); nothing is created.
func TestAssignAuthorAndSeries_SingleWordPenNameThatExistsSplits(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	expectTitles(m)
	known := map[string]int{"Shirtaloon": 1, "Travis Deverell": 2}
	m.EXPECT().GetAuthorByName(mock.Anything).RunAndReturn(func(n string) (*database.Author, error) {
		if id, ok := known[n]; ok {
			return &database.Author{ID: id, Name: n}, nil
		}
		return nil, nil
	})

	imp := newMockImporter(m)
	book := &database.Book{}
	imp.assignAuthorAndSeries(book, &itunes.Track{AlbumArtist: "Shirtaloon, Travis Deverell"})
	require.NotNil(t, book.AuthorID)
	assert.Equal(t, 1, *book.AuthorID)
	require.Len(t, book.Authors, 2)
	assert.Equal(t, 2, book.Authors[1].AuthorID)
}

// A credit of two existing authors credits each, in order; nothing created.
func TestAssignAuthorAndSeries_SplittableCreditCreditsEachPerson(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	expectTitles(m)
	m.EXPECT().GetAuthorByName("J. N. Chaney, Jonathan P. Brazee").Return(nil, nil)
	m.EXPECT().GetAuthorByName("J. N. Chaney").Return(&database.Author{ID: 5, Name: "J. N. Chaney"}, nil)
	m.EXPECT().GetAuthorByName("Jonathan P. Brazee").Return(&database.Author{ID: 6, Name: "Jonathan P. Brazee"}, nil)

	imp := newMockImporter(m)
	book := &database.Book{}
	imp.assignAuthorAndSeries(book, &itunes.Track{AlbumArtist: "J.N. Chaney, Jonathan P. Brazee"})
	require.NotNil(t, book.AuthorID)
	assert.Equal(t, 5, *book.AuthorID)
	require.Len(t, book.Authors, 2)
	assert.Equal(t, 6, book.Authors[1].AuthorID)
	assert.Equal(t, 1, book.Authors[1].Position)
}

// SF4: the iTunes path no longer creates part authors. A bracketed series tag
// and a credit with one unknown person both keep the whole artist string;
// "Dragon Born" and "Jonathan P. Brazee" are never created.
func TestAssignAuthorAndSeries_NoPartAuthorsCreated(t *testing.T) {
	for _, tc := range []struct{ artist string }{{"Dante King (Dragon Born)"}, {"J.N. Chaney, Jonathan P. Brazee"}} {
		t.Run(tc.artist, func(t *testing.T) {
			m := dbmocks.NewMockStore(t)
			expectTitles(m)
			m.EXPECT().GetAuthorByName(mock.Anything).RunAndReturn(func(n string) (*database.Author, error) {
				if n == "J. N. Chaney" {
					return &database.Author{ID: 5, Name: n}, nil
				}
				return nil, nil
			})
			var created []string
			m.EXPECT().CreateAuthor(mock.Anything).RunAndReturn(func(n string) (*database.Author, error) {
				created = append(created, n)
				return &database.Author{ID: 9, Name: n}, nil
			}).Once()
			imp := newMockImporter(m)
			book := &database.Book{}
			imp.assignAuthorAndSeries(book, &itunes.Track{AlbumArtist: tc.artist})
			require.NotNil(t, book.AuthorID)
			assert.Equal(t, 9, *book.AuthorID)
			require.Len(t, created, 1)
			assert.NotContains(t, []string{"Dragon Born", "Jonathan P. Brazee", "Dante King"}, created[0])
		})
	}
}
