// file: internal/scanner/extract_info_chapter_title_test.go
// version: 1.3.0
// guid: 1887ad95-0bf8-4bb7-87f5-cf52026d1289
// last-edited: 2026-09-28

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// TestExtractInfoFromPath_ChapterFilesAndNoSeriesFromPrefix is task E on the
// scanner's own filename parser (it diverges from metadata's; both had the
// defect). A chapter-numbered file takes its title from its folder and its
// author from the folder above -- it used to come out as author "Eldest",
// title "98" -- and an "X - Y" name records X as a series only when neither
// end is a bare chapter position ("02 - Eldest" gave Series "02"), while
// "The Stormlight Archive - The Way of Kings" keeps its series (S8 of the
// 2026-09-28 review). A year-like name is a title, and a disc folder is
// skipped for the work folder above it (N3).
func TestExtractInfoFromPath_ChapterFilesAndNoSeriesFromPrefix(t *testing.T) {
	cases := []struct {
		path       string
		wantTitle  string
		wantAuthor string
		wantSeries string
	}{
		{"/lib/Christopher Paolini/Eldest/98.mp3", "Eldest", "Christopher Paolini", ""},
		{"/lib/Kevin J. Anderson/Scattered Suns/Chapter 12.mp3", "Scattered Suns", "Kevin J. Anderson", ""},
		{"/lib/Christopher Paolini/Eldest/Eldest - 02.mp3", "Eldest", "Christopher Paolini", ""},
		{"/lib/Christopher Paolini/Eldest/02 - Eldest.mp3", "Eldest", "", ""},
		{"/lib/x/the lost city - a tale of old.mp3", "a tale of old", "", "the lost city"},
		// The lowercase form; the capitalised one is pinned in
		// TestExtractInfoFromPath_SeriesTitleIsNotAnAuthor below.
		{"/lib/x/the stormlight archive - the way of kings.mp3", "the way of kings", "", "the stormlight archive"},
		// A real title is untouched.
		{"/lib/Frank Herbert/Dune/Dune.mp3", "Dune", "", ""},
		// N3: a year-like number is a title, not a chapter.
		{"/lib/George Orwell/Classics/1984.mp3", "1984", "", ""},
		// N3: a disc folder names no work; the folder above it does.
		{"/lib/Christopher Paolini/Eldest/CD1/03.mp3", "Eldest", "Christopher Paolini", ""},
		// An author-folder layout: the parent is the author, so it must not
		// become the title; the bare number is kept as it is.
		{"/books/itunes/iTunes Media/Audiobooks/Bruce Sentar/01.mp3", "01", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			b := &Book{FilePath: tc.path}
			extractInfoFromPath(b)
			if b.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", b.Title, tc.wantTitle)
			}
			if tc.wantAuthor != "" && b.Author != tc.wantAuthor {
				t.Errorf("author = %q, want %q", b.Author, tc.wantAuthor)
			}
			if b.Series != tc.wantSeries {
				t.Errorf("series = %q, want %q", b.Series, tc.wantSeries)
			}
		})
	}
}

// TestExtractInfoFromPath_SeriesTitleIsNotAnAuthor is the end-to-end pin for
// the dash-filename reading in a folder that names no author, through the
// series write: each row with a series is fed to the REAL resolveSeriesID (on
// a mock store) and must look up the stripped name and return the position.
//
// Three failure classes are pinned here:
//   - a series or title filed as the AUTHOR ("The Stormlight Archive - The Way
//     of Kings", "The Expanse 01 - Leviathan Wakes");
//   - a real credit refused as an author and then filed as the SERIES or the
//     TITLE ("An Na - A Step from Heaven", "The Arbinger Institute - ...");
//   - a surname that is also a series word ("Junichi Saga").
//
// wantAuthor "" means NO author, and is asserted, not skipped.
func TestExtractInfoFromPath_SeriesTitleIsNotAnAuthor(t *testing.T) {
	cases := []struct {
		path                       string
		wantAuthor, wantTitle      string
		wantSeries, wantSeriesName string
		wantPosition               int
	}{
		{"/lib/import/The Stormlight Archive - The Way of Kings.mp3", "", "The Way of Kings", "The Stormlight Archive", "The Stormlight Archive", 0},
		// The folder is the series: the directory fallback must not take it.
		{"/lib/The Stormlight Archive/The Stormlight Archive - The Way of Kings.mp3", "", "The Way of Kings", "The Stormlight Archive", "The Stormlight Archive", 0},
		{"/lib/import/Brandon Sanderson - The Way of Kings.mp3", "Brandon Sanderson", "The Way of Kings", "", "", 0},
		{"/lib/import/Sanderson, Brandon - Mistborn.mp3", "Sanderson, Brandon", "Mistborn", "", "", 0},
		{"/lib/import/Wheel of Time 01 - The Eye of the World.mp3", "", "The Eye of the World", "Wheel of Time 01", "Wheel of Time", 1},
		{"/lib/import/A Song of Ice and Fire - A Game of Thrones.mp3", "", "A Game of Thrones", "A Song of Ice and Fire", "A Song of Ice and Fire", 0},
		{"/lib/import/J.R.R. Tolkien - The Hobbit.mp3", "J.R.R. Tolkien", "The Hobbit", "", "", 0},
		{"/lib/import/Discworld 01 - The Colour of Magic.mp3", "", "The Colour of Magic", "Discworld 01", "Discworld", 1},
		{"/lib/import/A J Finn - The Woman in the Window.mp3", "A J Finn", "The Woman in the Window", "", "", 0},
		// A padded volume number marks "Series NN - Title", so the two-word
		// title on the right is not the author.
		{"/lib/import/The Expanse 01 - Leviathan Wakes.mp3", "", "Leviathan Wakes", "The Expanse 01", "The Expanse", 1},
		// A series WORD alone does not: "Title Book N - Author" is common.
		{"/lib/import/Mistborn Book 1 - Brandon Sanderson.mp3", "Brandon Sanderson", "Mistborn Book 1", "", "", 0},
		// Refused credits are neither series nor title.
		{"/lib/import/An Na - A Step from Heaven.mp3", "", "A Step from Heaven", "", "", 0},
		{"/lib/import/A Step from Heaven - An Na.mp3", "", "A Step from Heaven", "", "", 0},
		{"/lib/import/The Arbinger Institute - Leadership and Self-Deception.mp3", "", "Leadership and Self-Deception", "", "", 0},
		// Two article-led person-shaped halves: keep the whole name as the title.
		{"/lib/import/The Dark Tower - The Gunslinger.mp3", "", "The Dark Tower - The Gunslinger", "", "", 0},
		// "Saga" is a surname, not a series word.
		{"/lib/import/Memories of Silk and Straw - Junichi Saga.mp3", "Junichi Saga", "Memories of Silk and Straw", "", "", 0},
		{"/lib/import/Junichi Saga - Memories of Silk and Straw.mp3", "Junichi Saga", "Memories of Silk and Straw", "", "", 0},
		// KNOWN LIMIT, pinned so a change is noticed: this is the same shape as
		// "The Stand - Stephen King" and the text cannot separate them.
		{"/lib/import/The Hunger Games - Catching Fire.mp3", "Catching Fire", "The Hunger Games", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			b := &Book{FilePath: tc.path}
			extractInfoFromPath(b)
			if b.Author != tc.wantAuthor || b.Title != tc.wantTitle || b.Series != tc.wantSeries {
				t.Errorf("got (author %q, title %q, series %q), want (%q, %q, %q)",
					b.Author, b.Title, b.Series, tc.wantAuthor, tc.wantTitle, tc.wantSeries)
			}
			if b.Series == "" {
				return
			}
			store := dbmocks.NewMockStore(t)
			origStore := database.GetGlobalStore()
			database.SetGlobalStore(store)
			SetStore(store)
			t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })
			store.EXPECT().GetSeriesByName(tc.wantSeriesName, (*int)(nil)).
				Return(&database.Series{ID: 7, Name: tc.wantSeriesName}, nil)

			id, pos, err := resolveSeriesID(b.Series, nil)
			if err != nil || id == nil || *id != 7 {
				t.Fatalf("resolveSeriesID(%q) = (%v, %d, %v)", b.Series, id, pos, err)
			}
			if pos != tc.wantPosition {
				t.Errorf("resolveSeriesID(%q) position = %d, want %d", b.Series, pos, tc.wantPosition)
			}
		})
	}
}
