// file: internal/metadata/chapter_title_test.go
// version: 1.7.0
// guid: 0cc97d6f-e2ee-42d6-8233-ed4cec61f5ab
// last-edited: 2026-09-29

package metadata

import "testing"

// WorkFolderTitle reads the work folder from a file path or a directory
// path (a multi-file book's FilePath). A directory used to be read as a file
// path: ".../Terry Pratchett/Mort" named "Terry Pratchett", or nothing.
func TestWorkFolderTitle(t *testing.T) {
	cases := []struct {
		path   string
		want   string
		wantOK bool
	}{
		{"/library/Terry Pratchett/Mort/Mort.m4b", "Mort", true},
		{"/library/Terry Pratchett/Mort", "Mort", true},
		{"/library/Terry Pratchett/Mort/", "Mort", true},
		{"/library/Terry Pratchett/Mort/CD1/01.mp3", "Mort", true},
		{"/library/Terry Pratchett/Mort/CD1", "Mort", true},
		{"/library/Unknown Author/Mort", "Mort", true},
		{"/library/Unknown Author/Mort/01.mp3", "Mort", true},
		{"/library/Brandon Sanderson/Book 1.5", "Book 1.5", true},
		{"/library/Terry Pratchett", "", false}, // an author folder under the library root
		{"/library/Unknown Author", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := WorkFolderTitle(tc.path)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("WorkFolderTitle(%q) = %q, %v; want %q, %v", tc.path, got, ok, tc.want, tc.wantOK)
		}
	}
	// The scanner's title recovery is unchanged: a folder under the
	// placeholder author folder is still not offered there.
	if got, _, ok := ChapterTitleFromDirectory("/library/Unknown Author/Mort/01.mp3", "01"); ok {
		t.Errorf("ChapterTitleFromDirectory under Unknown Author = %q, want no title (scanner behaviour unchanged)", got)
	}
}

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
		// Lowercase form; the capitalised one is pinned in
		// TestExtractFromFilename_SeriesTitleIsNotAnAuthor.
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

// TestExtractFromFilename_SeriesTitleIsNotAnAuthor is metadata's end-to-end pin
// for "The Stormlight Archive - The Way of Kings": the capitalised left side was
// read as the AUTHOR by authorname.ParseFilenameForAuthor, so the file got a
// bogus artist and no series. A work-named side is now never an author, and the
// directory fallback applies the same rule to a series-named folder.
func TestExtractFromFilename_SeriesTitleIsNotAnAuthor(t *testing.T) {
	cases := []struct {
		path                              string
		wantArtist, wantTitle, wantSeries string
	}{
		{"/lib/import/The Stormlight Archive - The Way of Kings.mp3", "", "The Way of Kings", "The Stormlight Archive"},
		{"/lib/The Stormlight Archive/The Stormlight Archive - The Way of Kings.mp3", "", "The Way of Kings", "The Stormlight Archive"},
		{"/lib/import/Brandon Sanderson - The Way of Kings.mp3", "Brandon Sanderson", "The Way of Kings", ""},
		{"/lib/import/Sanderson, Brandon - Mistborn.mp3", "Sanderson, Brandon", "Mistborn", ""},
		{"/lib/import/Wheel of Time 01 - The Eye of the World.mp3", "", "The Eye of the World", "Wheel of Time 01"},
		{"/lib/import/A Song of Ice and Fire - A Game of Thrones.mp3", "", "A Game of Thrones", "A Song of Ice and Fire"},
		{"/lib/import/J.R.R. Tolkien - The Hobbit.mp3", "J.R.R. Tolkien", "The Hobbit", ""},
		{"/lib/import/Discworld 01 - The Colour of Magic.mp3", "", "The Colour of Magic", "Discworld 01"},
		{"/lib/import/A J Finn - The Woman in the Window.mp3", "A J Finn", "The Woman in the Window", ""},
		{"/lib/import/The Expanse 01 - Leviathan Wakes.mp3", "", "Leviathan Wakes", "The Expanse 01"},
		{"/lib/import/Mistborn Book 1 - Brandon Sanderson.mp3", "Brandon Sanderson", "Mistborn Book 1", ""},
		// Refused credits are neither series nor title.
		{"/lib/import/An Na - A Step from Heaven.mp3", "", "A Step from Heaven", ""},
		{"/lib/import/A Step from Heaven - An Na.mp3", "", "A Step from Heaven", ""},
		{"/lib/import/The Arbinger Institute - Leadership and Self-Deception.mp3", "", "Leadership and Self-Deception", ""},
		{"/lib/import/The Dark Tower - The Gunslinger.mp3", "", "The Dark Tower - The Gunslinger", ""},
		// "Saga" is a surname, not a series word.
		{"/lib/import/Memories of Silk and Straw - Junichi Saga.mp3", "Junichi Saga", "Memories of Silk and Straw", ""},
		{"/lib/import/Junichi Saga - Memories of Silk and Straw.mp3", "Junichi Saga", "Memories of Silk and Straw", ""},
		// KNOWN LIMIT: the same shape as "The Stand - Stephen King".
		{"/lib/import/The Hunger Games - Catching Fire.mp3", "Catching Fire", "The Hunger Games", ""},
		// Round-3 review rows.
		{"/lib/import/Mistborn Book 1 - The Final Empire.mp3", "", "The Final Empire", "Mistborn Book 1"},
		{"/lib/import/The Hobbit - Chapter 01.mp3", "", "The Hobbit", ""},
		{"/lib/import/The Hobbit - Part 1.mp3", "", "The Hobbit", ""},
		{"/lib/import/The Hobbit - 01.mp3", "", "The Hobbit", ""},
		{"/lib/import/The Dark Tower 01 - Stephen King.mp3", "", "Stephen King", "The Dark Tower 01"},
		{"/lib/import/The Dark Tower 01 - King, Stephen.mp3", "King, Stephen", "The Dark Tower 01", ""},
		{"/lib/Terry Pratchett/Discworld 01/Discworld 01 - Terry Pratchett.mp3", "Terry Pratchett", "Discworld 01", ""},
		{"/lib/James S. A. Corey/The Expanse/Leviathan Wakes/The Expanse 01 - Leviathan Wakes.mp3", "", "Leviathan Wakes", "The Expanse 01"},
		{"/lib/Leviathan Wakes/The Expanse 01 - Leviathan Wakes.mp3", "", "Leviathan Wakes", "The Expanse 01"},
		{"/lib/import/The Book Thief - Markus Zusak.mp3", "Markus Zusak", "The Book Thief", ""},
		{"/lib/Neil Gaiman/Good Omens/Good Omens.mp3", "Neil Gaiman", "Good Omens", ""},
		// Round-5 review rows.
		{"/lib/Stephen King/Stephen King - The Stand/01.mp3", "Stephen King", "Stephen King - The Stand", ""},
		{"/lib/Stephen King/Stephen King Collection/01.mp3", "Stephen King", "Stephen King Collection", ""},
		{"/lib/Stephen King/Stephen King Short Stories/01.mp3", "Stephen King", "Stephen King Short Stories", ""},
		{"/lib/Stephen King/Stephen King Short Stories/Stephen King Short Stories.mp3", "Stephen King", "Stephen King Short Stories", ""},
		{"/lib/Brandon Sanderson/Brandon Sanderson Mistborn/01.mp3", "Brandon Sanderson", "Brandon Sanderson Mistborn", ""},
		{"/lib/Lee Child/Lee Child Jack Reacher 01 Killing Floor/01.mp3", "Lee Child", "Lee Child Jack Reacher 01 Killing Floor", ""},
		{"/lib/import/Heinlein 01 - Robert A Heinlein.mp3", "Robert A Heinlein", "Heinlein 01", ""},
		{"/lib/Stephen King/Stephen King The Stand/Stephen King The Stand.mp3", "Stephen King", "Stephen King The Stand", ""},
		{"/lib/Harry Potter/Harry Potter and the Goblet of Fire/01.mp3", "", "Harry Potter and the Goblet of Fire", ""},
		{"/lib/import/Discworld 08 - Guards, Guards.mp3", "", "Guards, Guards", "Discworld 08"},
		// Round-4 review rows.
		{"/lib/Harry Potter/Harry Potter and the Goblet of Fire/Harry Potter and the Goblet of Fire.mp3", "", "Harry Potter and the Goblet of Fire", ""},
		{"/lib/Harry Potter/Harry Potter and the Goblet of Fire/01.mp3", "", "Harry Potter and the Goblet of Fire", ""},
		{"/lib/Science Fiction/Good Omens/Good Omens.mp3", "", "Good Omens", ""},
		{"/lib/Lee Child/Killing Floor/Killing Floor.mp3", "Lee Child", "Killing Floor", ""},
		{"/lib/Jack Reacher/Killing Floor/Killing Floor.mp3", "Jack Reacher", "Killing Floor", ""},
		{"/lib/Stephen King/Stephen King.mp3", "", "Stephen King", ""},
		{"/lib/import/Stormlight 02 - Words Of Radiance.mp3", "", "Words Of Radiance", "Stormlight 02"},
		{"/lib/import/Dune 03 - Children Of Dune.mp3", "", "Children Of Dune", "Dune 03"},
		{"/lib/import/Bill Hodges 01 - Mr. Mercedes.mp3", "", "Mr. Mercedes", "Bill Hodges 01"},
		{"/lib/import/Mistborn 01 - Brandon Sanderson (Unabridged).mp3", "Brandon Sanderson (Unabridged)", "Mistborn 01", ""},
		{"/lib/import/Good Omens 01 - Neil Gaiman & Terry Pratchett.mp3", "Neil Gaiman & Terry Pratchett", "Good Omens 01", ""},
		{"/lib/import/Discworld 01 - J. R. R. Tolkien.mp3", "J. R. R. Tolkien", "Discworld 01", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			m := extractFromFilename(tc.path)
			if m.Artist != tc.wantArtist || m.Title != tc.wantTitle || m.Series != tc.wantSeries {
				t.Errorf("got (artist %q, title %q, series %q), want (%q, %q, %q)",
					m.Artist, m.Title, m.Series, tc.wantArtist, tc.wantTitle, tc.wantSeries)
			}
		})
	}
}

// TestExtractMetadataFromFolder_SeriesFolderIsNotAnAuthor pins the folder
// parser's side of the same bug: in a <series>/<title>/<disc> layout the series
// folder sits where Pass 3 looks for an author, and "The Stormlight Archive"
// was taken as the author at high confidence.
func TestExtractMetadataFromFolder_SeriesFolderIsNotAnAuthor(t *testing.T) {
	for _, dir := range []string{
		"/lib/books/The Stormlight Archive/The Way of Kings/Disc 1",
		"/lib/books/Dune Chronicles/Children of Dune/Disc 1",
		"/lib/books/The City & The City/Part One/Disc 1",
	} {
		fm, err := ExtractMetadataFromFolder(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if len(fm.Authors) != 0 {
			t.Errorf("%s: authors = %q, want none", dir, fm.Authors)
		}
	}
	// Past a work-named (series) folder the walk takes exactly ONE more step,
	// and accepts it only if person-shaped and not a genre/container folder.
	for _, tc := range []struct {
		dir, wantAuthor, wantSeries string
	}{
		{"/lib/Stephen King/The Dark Tower/The Gunslinger/Disc 1", "Stephen King", "The Dark Tower"},
		{"/lib/Isaac Asimov/The Foundation Series/Foundation/Part 1", "Isaac Asimov", "The Foundation Series"},
		{"/lib/Brandon Sanderson/The Stormlight Archive/The Way of Kings/Disc 1", "Brandon Sanderson", "The Stormlight Archive"},
		{"/mnt/Science Fiction/The Stormlight Archive/The Way of Kings/Disc 1", "", "The Stormlight Archive"},
		{"/srv/Fantasy/Dune Chronicles/Children of Dune/Disc 1", "", "Dune Chronicles"},
		{"/srv/Audiobooks/Dune Chronicles/Children of Dune/Disc 1", "", "Dune Chronicles"},
		// The ORDINARY walk refuses genre folders too, not only the step past
		// a series folder.
		{"/srv/Science Fiction/Mistborn/The Final Empire", "", ""},
		{"/srv/Science Fiction/Mistborn/The Final Empire/Disc 1", "", ""},
		// Stops after the one step: a genre folder past the author is not read.
		{"/mnt/Science Fiction/Stephen King/The Dark Tower/The Gunslinger/Disc 1", "Stephen King", "The Dark Tower"},
	} {
		fm, err := ExtractMetadataFromFolder(tc.dir)
		if err != nil {
			t.Fatal(err)
		}
		gotAuthor := ""
		if len(fm.Authors) > 0 {
			gotAuthor = fm.Authors[0]
		}
		if gotAuthor != tc.wantAuthor || len(fm.Authors) > 1 || fm.SeriesName != tc.wantSeries {
			t.Errorf("%s: authors = %q series = %q, want %q / %q", tc.dir, fm.Authors, fm.SeriesName, tc.wantAuthor, tc.wantSeries)
		}
	}

	fm, err := ExtractMetadataFromFolder("/lib/books/Brandon Sanderson/The Way of Kings/Disc 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.Authors) != 1 || fm.Authors[0] != "Brandon Sanderson" {
		t.Errorf("real author folder: authors = %q, want [Brandon Sanderson]", fm.Authors)
	}
}
