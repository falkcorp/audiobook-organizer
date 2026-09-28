// file: internal/scanner/extract_info_chapter_title_test.go
// version: 1.0.0
// guid: 1887ad95-0bf8-4bb7-87f5-cf52026d1289
// last-edited: 2026-09-28

package scanner

import "testing"

// TestExtractInfoFromPath_ChapterFilesAndNoSeriesFromPrefix is task E on the
// scanner's own filename parser (it diverges from metadata's; both had the
// defect). A chapter-numbered file takes its title from its folder and its
// author from the folder above -- it used to come out as author "Eldest",
// title "98" -- and an "X - Y" name with no recognisable author never records
// X as a series ("02 - Eldest" gave Series "02").
func TestExtractInfoFromPath_ChapterFilesAndNoSeriesFromPrefix(t *testing.T) {
	cases := []struct {
		path       string
		wantTitle  string
		wantAuthor string
	}{
		{"/lib/Christopher Paolini/Eldest/98.mp3", "Eldest", "Christopher Paolini"},
		{"/lib/Kevin J. Anderson/Scattered Suns/Chapter 12.mp3", "Scattered Suns", "Kevin J. Anderson"},
		{"/lib/Christopher Paolini/Eldest/Eldest - 02.mp3", "Eldest", "Christopher Paolini"},
		{"/lib/x/the lost city - a tale of old.mp3", "a tale of old", ""},
		// A real title is untouched.
		{"/lib/Frank Herbert/Dune/Dune.mp3", "Dune", ""},
		// An author-folder layout: the parent is the author, so it must not
		// become the title. The scanner strips the bare number, leaving "".
		{"/books/itunes/iTunes Media/Audiobooks/Bruce Sentar/01.mp3", "", ""},
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
			if b.Series != "" {
				t.Errorf("series = %q, want none", b.Series)
			}
		})
	}
}
