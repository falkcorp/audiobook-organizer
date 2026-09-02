// file: internal/itunes/service/series_position_writeback_test.go
// version: 1.0.0
// guid: 9d2c8b71-6e34-4a05-8f19-3b7e0c5d24a8
// last-edited: 2026-09-02

package itunesservice

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// assignSeriesFromAlbum drives the real iTunes import path that turns a track's
// Album tag into a series row, and reports the NAME that was actually handed to
// the store plus the row the importer built.
//
// The name is captured off CreateSeries rather than asserted on the input,
// because "the number was stripped" is a claim about what got STORED.
func assignSeriesFromAlbum(t *testing.T, album string, book *database.Book) (*database.Book, string) {
	t.Helper()

	var createdSeriesName string
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAuthorByName(mock.Anything).
		Return(&database.Author{ID: 1, Name: "An Author"}, nil).Maybe()
	m.EXPECT().GetSeriesByName(mock.Anything, mock.Anything).
		Return(nil, nil).Maybe()
	m.EXPECT().CreateSeries(mock.Anything, mock.Anything).
		RunAndReturn(func(name string, authorID *int) (*database.Series, error) {
			createdSeriesName = name
			return &database.Series{ID: 7, Name: name}, nil
		}).Maybe()

	imp := newImporter(Deps{Store: m, Config: Config{}})
	imp.assignAuthorAndSeries(book, &itunes.Track{Artist: "An Author", Album: album})
	return book, createdSeriesName
}

// The iTunes importer stripped the number and returned only the series ID, so
// the position was deleted with nothing recording it.
func TestITunesImport_StoresSeriesNameWithoutNumberAndRecordsPosition(t *testing.T) {
	// ⚠️ Albums here deliberately carry no "," "-" or ":" -- extractSeriesName
	// splits on the first of those and would remove the number before
	// ensureSeriesID ever sees it, making the test pass for the wrong reason.
	tests := []struct {
		name       string
		album      string
		wantSeries string
		wantSeq    int
	}{
		{"hash suffix", "Nameless Sovereign #5", "Nameless Sovereign", 5},
		{"bare trailing number", "Discworld 05", "Discworld", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book, created := assignSeriesFromAlbum(t, tt.album, &database.Book{Title: "A Book"})
			require.Equal(t, tt.wantSeries, created, "series name handed to the store still carries the number")
			require.NotNil(t, book.SeriesSequence,
				"series_sequence was not recorded; the number stripped from %q was DELETED", tt.album)
			require.Equal(t, tt.wantSeq, *book.SeriesSequence)
		})
	}
}

// The bracketed shape is the one exception to "it is a move, not a delete": the
// number comes OUT of the name and is deliberately NOT written into the sequence.
//
// 🔑 This asserts BOTH halves. A test that only checked the name would still pass
// if the sequence write were reintroduced, and that is the whole regression this
// guards: ~180 of the 198 bracketed rows measured on 2026-08-06 were
// shattered-book debris, so the number is ~90% likely to be a wrong position.
// An empty sequence is visible and recoverable; a wrong one is not.
func TestITunesImport_BracketedStripsTheNameButWritesNoSequence(t *testing.T) {
	for _, tc := range []struct{ album, wantSeries string }{
		{"Dragon Born [04]", "Dragon Born"},
		{"The Hollows (7)", "The Hollows"},
	} {
		t.Run(tc.album, func(t *testing.T) {
			book, created := assignSeriesFromAlbum(t, tc.album, &database.Book{Title: "A Book"})
			require.Equal(t, tc.wantSeries, created, "the bracketed number must be removed from the series name")
			require.Nil(t, book.SeriesSequence,
				"a bracketed number must NOT be written into series_sequence; it is ~90%% likely to be shattered-book debris")
		})
	}
}

// A sequence already on the row came from the track's own tags and outranks a
// number recovered from the album name.
func TestITunesImport_DoesNotOverwriteExistingPosition(t *testing.T) {
	existing := 3
	book, created := assignSeriesFromAlbum(t, "Discworld 05",
		&database.Book{Title: "A Book", SeriesSequence: &existing})

	require.Equal(t, "Discworld", created)
	require.NotNil(t, book.SeriesSequence)
	require.Equal(t, 3, *book.SeriesSequence,
		"an existing sequence must never be overwritten")
}

// A clean name is stored verbatim with no sequence invented, and an un-vouched
// number is left alone rather than mangled into "—EIGHTY-SIX".
func TestITunesImport_LeavesCleanAndUnvouchedNamesAlone(t *testing.T) {
	// "86—EIGHTY-SIX" is NOT in this list -- see
	// TestITunesImport_ExtractSeriesNameTruncatesOnHyphen below for why.
	for _, album := range []string{"The Expanse", "08. Battle for the Abyss"} {
		t.Run(album, func(t *testing.T) {
			book, created := assignSeriesFromAlbum(t, album, &database.Book{Title: "A Book"})
			require.Equal(t, album, created, "series name should be stored unchanged")
			require.Nil(t, book.SeriesSequence, "no sequence should be invented")
		})
	}
}

// ⚠️ PRE-EXISTING BUG, documented rather than fixed here because it is a
// different defect from the one this change is about.
//
// extractSeriesName splits the album on the first "," "-" or ":" and keeps the
// left half. An ASCII hyphen ANYWHERE in a series name therefore truncates it:
// "86—EIGHTY-SIX" reaches the normalizer as "86—EIGHTY", already mangled, and
// gets stored that way. That is not the series-position normalizer's doing --
// it leaves what it is handed alone, which this test also proves -- and fixing
// the album parser is a separate change with its own blast radius.
//
// Pinned so the truncation is visible and so nobody reads the shortened name in
// production as evidence that the de-numbering did it.
func TestITunesImport_ExtractSeriesNameTruncatesOnHyphen(t *testing.T) {
	require.Equal(t, "86—EIGHTY", extractSeriesName("86—EIGHTY-SIX"),
		"extractSeriesName no longer truncates on a hyphen; re-check the exclusion above")

	book, created := assignSeriesFromAlbum(t, "86—EIGHTY-SIX", &database.Book{Title: "A Book"})
	require.Equal(t, "86—EIGHTY", created,
		"the normalizer must pass through whatever extractSeriesName handed it, unchanged")
	require.Nil(t, book.SeriesSequence, "no sequence should be invented")
}
