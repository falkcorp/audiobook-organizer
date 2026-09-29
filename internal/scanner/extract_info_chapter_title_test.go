// file: internal/scanner/extract_info_chapter_title_test.go
// version: 1.2.0
// guid: 1887ad95-0bf8-4bb7-87f5-cf52026d1289
// last-edited: 2026-09-28

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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
// "The Stormlight Archive - The Way of Kings.mp3" in a folder that names no
// author. authorname.ParseFilenameForAuthor used to read the capitalised left
// side as the AUTHOR, so the book got a bogus author and no series; and when the
// folder itself was the series, authorname.ExtractAuthorFromDirectory would
// have handed the same bogus author back as the fallback.
//
// wantSeriesName/wantPosition are what resolveSeriesID persists: it runs
// metadata.StripSeriesContamination over this series before writing, which is
// where "Discworld 01" becomes series "Discworld" at position 1.
func TestExtractInfoFromPath_SeriesTitleIsNotAnAuthor(t *testing.T) {
	cases := []struct {
		path                       string
		wantAuthor, wantTitle      string
		wantSeries, wantSeriesName string
		wantPosition               string
	}{
		{"/lib/import/The Stormlight Archive - The Way of Kings.mp3", "", "The Way of Kings", "The Stormlight Archive", "The Stormlight Archive", ""},
		// The folder is the series: the directory fallback must not take it.
		{"/lib/The Stormlight Archive/The Stormlight Archive - The Way of Kings.mp3", "", "The Way of Kings", "The Stormlight Archive", "The Stormlight Archive", ""},
		{"/lib/import/Brandon Sanderson - The Way of Kings.mp3", "Brandon Sanderson", "The Way of Kings", "", "", ""},
		{"/lib/import/Sanderson, Brandon - Mistborn.mp3", "Sanderson, Brandon", "Mistborn", "", "", ""},
		{"/lib/import/Wheel of Time 01 - The Eye of the World.mp3", "", "The Eye of the World", "Wheel of Time 01", "Wheel of Time", "1"},
		{"/lib/import/A Song of Ice and Fire - A Game of Thrones.mp3", "", "A Game of Thrones", "A Song of Ice and Fire", "A Song of Ice and Fire", ""},
		{"/lib/import/J.R.R. Tolkien - The Hobbit.mp3", "J.R.R. Tolkien", "The Hobbit", "", "", ""},
		{"/lib/import/Discworld 01 - The Colour of Magic.mp3", "", "The Colour of Magic", "Discworld 01", "Discworld", "1"},
		{"/lib/import/The Dark Tower - The Gunslinger.mp3", "", "The Gunslinger", "The Dark Tower", "The Dark Tower", ""},
		{"/lib/import/A J Finn - The Woman in the Window.mp3", "A J Finn", "The Woman in the Window", "", "", ""},
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
			c := metadata.StripSeriesContamination(b.Series, "")
			name := c.Name
			if name == "" {
				name = b.Series
			}
			if name != tc.wantSeriesName || c.Position != tc.wantPosition {
				t.Errorf("persisted series = (%q, pos %q), want (%q, pos %q)",
					name, c.Position, tc.wantSeriesName, tc.wantPosition)
			}
		})
	}
}
