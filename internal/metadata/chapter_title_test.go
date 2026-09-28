// file: internal/metadata/chapter_title_test.go
// version: 1.1.0
// guid: 0cc97d6f-e2ee-42d6-8233-ed4cec61f5ab
// last-edited: 2026-09-28

package metadata

import "testing"

func TestChapterTitleFromDirectory(t *testing.T) {
	cases := []struct {
		path, title string
		want        string
		wantOK      bool
	}{
		{"/lib/Christopher Paolini/Eldest/98.mp3", "98", "Eldest", true},
		{"/lib/Kevin J. Anderson/Scattered Suns/Chapter 12.mp3", "Chapter 12", "Scattered Suns", true},
		{"/lib/A/Work/Part 3 of 12.mp3", "Part 3 of 12", "Work", true},
		{"/lib/A/Work/CD1.mp3", "CD1", "Work", true},
		// A real title is left alone.
		{"/lib/A/Work/The Real Title.mp3", "The Real Title", "", false},
		{"/lib/A/Work/Catch-22.mp3", "Catch-22", "", false},
		// N3: a year-like number is a title.
		{"/lib/George Orwell/Classics/1984.mp3", "1984", "", false},
		// N3: a disc/part folder names no work; the folder above it does.
		{"/lib/A/Work/Disc 2/07.mp3", "07", "Work", true},
		{"/lib/A/Work/CD1/07.mp3", "07", "Work", true},
		// ...but only one such level is skipped, and never into a root.
		{"/downloads/CD1/07.mp3", "07", "", false},
		// A generic parent offers nothing better.
		{"/downloads/07.mp3", "07", "", false},
		// An author-folder layout: the parent is the author, not a work.
		{"/books/itunes/iTunes Media/Audiobooks/Bruce Sentar/01.mp3", "01", "", false},
		{"/books/Audiobooks/Bruce Sentar/01.mp3", "01", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, _, ok := ChapterTitleFromDirectory(tc.path, tc.title)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("ChapterTitleFromDirectory(%q, %q) = (%q, %v), want (%q, %v)",
					tc.path, tc.title, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestExtractFromFilename_ChapterFilesAndNoSeriesFromPrefix is task E: a
// chapter-numbered file takes its title from its folder and its author from the
// folder above, and a "X - Y" name with no recognisable author never records X
// as a series.
func TestExtractFromFilename_ChapterFilesAndNoSeriesFromPrefix(t *testing.T) {
	cases := []struct {
		path                  string
		wantTitle, wantArtist string
		wantSeries            string
		checkArtist           bool
	}{
		{path: "/lib/Christopher Paolini/Eldest/98.mp3", wantTitle: "Eldest", wantArtist: "Christopher Paolini", checkArtist: true},
		{path: "/lib/Kevin J. Anderson/Scattered Suns/Chapter 12.mp3", wantTitle: "Scattered Suns", wantArtist: "Kevin J. Anderson", checkArtist: true},
		// Used to record Series "Eldest" and title "02".
		{path: "/lib/Christopher Paolini/Eldest/Eldest - 02.mp3", wantTitle: "Eldest", wantArtist: "Christopher Paolini", checkArtist: true},
		// No author parses out of this name, so the fallback branch runs and
		// the prefix is the series: neither end is a chapter position (S8).
		{path: "/lib/x/the lost city - a tale of old.mp3", wantTitle: "a tale of old", wantSeries: "the lost city"},
		// The review's "The Stormlight Archive - The Way of Kings" never reaches
		// this branch: authorname.ParseFilenameForAuthor reads the capitalised
		// left side as an author (on main as well). The lowercase name takes
		// the no-author branch this rule governs.
		{path: "/lib/x/the stormlight archive - the way of kings.mp3", wantTitle: "the way of kings", wantSeries: "the stormlight archive"},
		// ...and a chapter-only end still drops it.
		{path: "/lib/x/02 - Eldest.mp3", wantTitle: "Eldest"},
		// N3: a disc folder is skipped for the work above it.
		{path: "/lib/Christopher Paolini/Eldest/CD1/03.mp3", wantTitle: "Eldest", wantArtist: "Christopher Paolini", checkArtist: true},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			m := extractFromFilename(tc.path)
			if tc.wantTitle != "" && m.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", m.Title, tc.wantTitle)
			}
			if tc.checkArtist && m.Artist != tc.wantArtist {
				t.Errorf("artist = %q, want %q", m.Artist, tc.wantArtist)
			}
			if m.Series != tc.wantSeries {
				t.Errorf("series = %q, want %q", m.Series, tc.wantSeries)
			}
		})
	}
}

func TestIsChapterOnlyTitle_YearLikeNumbersAreTitles(t *testing.T) {
	for title, want := range map[string]bool{
		"": true, "98": true, "007": true, "0012": true, "Chapter 12": true, "CD1": true,
		"1984": false, "2001": false, "1776": false, "Dune": false,
	} {
		if got := IsChapterOnlyTitle(title); got != want {
			t.Errorf("IsChapterOnlyTitle(%q) = %v, want %v", title, got, want)
		}
	}
}
