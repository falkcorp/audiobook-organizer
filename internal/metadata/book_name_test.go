// file: internal/metadata/book_name_test.go
// version: 1.0.0
// guid: fd994992-1a70-4204-b1cc-72148bda319a
// last-edited: 2026-10-05

package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The shapes of the 2026-10-05 census's filename-pattern probe (20 whole
// books that recorded no match), with made-up titles and people. Each row
// says what the book's own name, series slot and credits are.
func TestParseBookName_CensusShapes(t *testing.T) {
	known := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, x := range names {
				if x == n {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name             string
		raw              string
		ev               NameEvidence
		title, search    string
		author, narrator string
		year, suffix     string
		series, position string
		shapes           []string
	}{
		{
			name: "leading year with a one-word name", raw: "2018 - Glasswake",
			ev:    NameEvidence{Authors: []string{"Mara Quill"}},
			title: "Glasswake", search: "Glasswake", year: "2018", shapes: []string{ShapeLeadingYear},
		},
		{
			name: "one-word series slot with a trailing track number", raw: "Driftworld 24 - The Seventh Lantern - 01",
			ev:    NameEvidence{Authors: []string{"Dorian Vex"}},
			title: "Driftworld 24 - The Seventh Lantern", search: "The Seventh Lantern",
			series: "Driftworld", position: "24", suffix: "01", shapes: []string{ShapeTrackSuffix, ShapeSeriesSlot},
		},
		{
			name: "author segment, then a labelled slot alone, then the placeholder", raw: "Kestrel Moon - Copper Regent Book 03 - Unknown Author",
			ev:    NameEvidence{Authors: []string{"Kestrel Moon"}},
			title: "Copper Regent Book 03", search: "Copper Regent Book 03", author: "Kestrel Moon",
			series: "Copper Regent", position: "03",
			shapes: []string{ShapePlaceholderAuthor, ShapeLeadingAuthor, ShapeSeriesSlot},
		},
		{
			name: "placeholder only", raw: "Hammer Fall Rising - Unknown Author",
			title: "Hammer Fall Rising", search: "Hammer Fall Rising", shapes: []string{ShapePlaceholderAuthor},
		},
		{
			name: "series book slot, sub-series, book name", raw: "Saga of Embers Book 05 - Frostvale Trilogy - Rivers of Ash",
			ev:    NameEvidence{Authors: []string{"R. T. Ashby"}},
			title: "Saga of Embers Book 05 - Frostvale Trilogy - Rivers of Ash", search: "Rivers of Ash",
			series: "Saga of Embers", position: "05", shapes: []string{ShapeSeriesSlot, ShapeSubseries},
		},
		{
			name: "book one named for its series", raw: "The Hollow Wood 01 - The Hollow Wood",
			title: "The Hollow Wood 01 - The Hollow Wood", search: "The Hollow Wood",
			series: "The Hollow Wood", position: "01", shapes: []string{ShapeSeriesSlot},
		},
		{
			name: "unbracketed rip tail with narrator", raw: "1987 - The Last Orbit (Avers) 64k 12.07.23 {345mb}",
			title: "The Last Orbit", search: "The Last Orbit", narrator: "Avers", year: "1987",
			shapes: []string{ShapeRipTail, ShapeLeadingYear},
		},
		{
			name: "series - n - name", raw: "Squad K - 3 - The Night Shift",
			title: "Squad K - 3 - The Night Shift", search: "The Night Shift", series: "Squad K", position: "3",
			shapes: []string{ShapeSeriesSlot},
		},
		{
			name: "hyphenated series word", raw: "Semi-Trained Cadet 06 - Recruit",
			title: "Semi-Trained Cadet 06 - Recruit", search: "Recruit", series: "Semi-Trained Cadet", position: "06",
			shapes: []string{ShapeSeriesSlot},
		},
		{
			name: "single-word author segment known to the book", raw: "Quillfeather - The One Who Hunts Storms 03 (Ada Penn)",
			ev:    NameEvidence{Authors: []string{"Quillfeather"}},
			title: "The One Who Hunts Storms 03 (Ada Penn)", search: "The One Who Hunts Storms 03 (Ada Penn)",
			author: "Quillfeather", shapes: []string{ShapeLeadingAuthor},
		},
		{
			name: "initials author segment and trailing tags", raw: "J.K. Marlow - Freighter for Hire 02 [44m4] [Fixed]",
			ev:    NameEvidence{Authors: []string{"J. K. Marlow"}},
			title: "Freighter for Hire 02", search: "Freighter for Hire 02", author: "J.K. Marlow",
			shapes: []string{ShapeTrailingTag, ShapeTrailingTag, ShapeLeadingAuthor},
		},
		{
			name: "leading year then a subtitle", raw: "2005 - Raven Song: A Novel of the Reach",
			title: "Raven Song: A Novel of the Reach", search: "Raven Song: A Novel of the Reach", year: "2005",
			shapes: []string{ShapeLeadingYear},
		},
		{
			name: "transposed: title - author", raw: "The Paper Garden - Mara Quill",
			ev:    NameEvidence{Authors: []string{"Mara Quill"}},
			title: "The Paper Garden", search: "The Paper Garden", author: "Mara Quill", shapes: []string{ShapeTrailingAuthor},
		},
		{
			name: "authority-known author segment", raw: "Dorian Vex - Ironclad - The Long Watch",
			ev:    NameEvidence{IsKnownAuthor: known("Dorian Vex")},
			title: "Ironclad - The Long Watch", search: "Ironclad - The Long Watch", author: "Dorian Vex",
			shapes: []string{ShapeLeadingAuthor},
		},
		{
			name: "author segment named by an ancestor folder", raw: "Dorian Vex - Ironclad 02 - The Long Watch",
			ev:    NameEvidence{Path: "/srv/library/Dorian Vex/Ironclad/Dorian Vex - Ironclad 02 - The Long Watch/book.m4b"},
			title: "Ironclad 02 - The Long Watch", search: "The Long Watch", author: "Dorian Vex",
			series: "Ironclad", position: "02", shapes: []string{ShapeLeadingAuthor, ShapeSeriesSlot},
		},
		{
			name: "explicit count suffix", raw: "The Glass Tower (1 of 3)",
			title: "The Glass Tower", search: "The Glass Tower", suffix: "1 of 3", shapes: []string{ShapeTrackSuffix},
		},
		{
			name: "folder parser: any author-shaped trailing segment", raw: "The Long Watch - Dorian Vex",
			ev:    NameEvidence{FolderName: true},
			title: "The Long Watch", search: "The Long Watch", author: "Dorian Vex", shapes: []string{ShapeTrailingAuthor},
		},
		{
			name: "read by credit and placeholder", raw: "Cold Harbor - read by Ada Penn - Unknown Author",
			title: "Cold Harbor", search: "Cold Harbor", narrator: "Ada Penn",
			shapes: []string{ShapePlaceholderAuthor, ShapeNarratorCredit},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseBookName(tc.raw, tc.ev)
			assert.Equal(t, tc.title, got.Title, "title")
			assert.Equal(t, tc.search, got.SearchName(), "search name")
			assert.Equal(t, tc.author, got.Author, "author")
			assert.Equal(t, tc.narrator, got.Narrator, "narrator")
			assert.Equal(t, tc.year, got.Year, "year")
			assert.Equal(t, tc.suffix, got.Suffix, "suffix")
			assert.Equal(t, tc.series, got.Series, "series")
			assert.Equal(t, tc.position, got.Position, "position")
			assert.Equal(t, tc.shapes, got.Shapes, "shapes")
		})
	}
}

// Shapes that must NOT be read: each keeps its title as written.
func TestParseBookName_Negatives(t *testing.T) {
	for _, raw := range []string{
		"1984",                     // a year that is the whole title
		"2001: A Space Odyssey",    // a colon is a real subtitle
		"Fahrenheit 451: A Novel",  // a colon after a number is not a slot
		"Catch-22",                 // unspaced hyphen
		"The Witcher - 4",          // a trailing number with no slot before it may be the position
		"Apollo 13",                // a bare trailing number alone is a title
		"Dune - Frank Herbert",     // an unknown trailing name stays for a search
		"15 - Harry and the Fox",   // a leading number that is not a year
		"Book 3 - The Silver Gate", // a slot word is no series
		"96 Hours",                 // number-leading title
		"The Way of Kings, Part 1", // a split edition's part is the product
		"Golden Son (Part 1)",      // likewise
		"Rogue Lawyer - 001",       // a part suffix with no slot is left to the search ladder
	} {
		got := ParseBookName(raw, NameEvidence{})
		assert.Equal(t, raw, got.Title, raw)
		assert.Empty(t, got.Author, raw)
		assert.Empty(t, got.Year, raw)
		assert.Empty(t, got.Suffix, raw)
	}
	// Empty and blank input parse to themselves without panicking.
	assert.Equal(t, "", ParseBookName("", NameEvidence{}).Title)
	assert.Equal(t, "", ParseBookName("   ", NameEvidence{Path: "/srv/a/b.m4b"}).Title)
	// Never empties a title.
	got := ParseBookName("2018 - Unknown Author", NameEvidence{})
	assert.Equal(t, "2018 - Unknown Author", got.Title)
	assert.Equal(t, "Mara Quill", ParseBookName("Mara Quill", NameEvidence{Authors: []string{"Mara Quill"}}).Title)
}

// The scanner's folder parse reads the same shapes (made-up paths): it used
// to title a book by the text before the first " - ", so "2018 - Glasswake"
// was titled "2018" and "Driftworld 24 - The Seventh Lantern - 01"
// "Driftworld 24".
func TestExtractMetadataFromFolder_FilenameShapes(t *testing.T) {
	for _, tc := range []struct {
		path, title, series string
		pos                 int
		authors             []string
	}{
		{"/srv/library/Mara Quill/2018/2018 - Glasswake", "Glasswake", "", 0, []string{"Mara Quill"}},
		{"/srv/library/Dorian Vex/Driftworld/Driftworld 24 - The Seventh Lantern - 01", "The Seventh Lantern", "Driftworld", 24, []string{"Dorian Vex"}},
		{"/srv/library/Kestrel Moon/Kestrel Moon - Copper Regent/Kestrel Moon - Copper Regent Book 03 - Unknown Author", "Copper Regent Book 03", "Copper Regent", 3, []string{"Kestrel Moon"}},
		{"/srv/library/incoming/The Long Watch - Dorian Vex", "The Long Watch", "", 0, []string{"Dorian Vex"}},
	} {
		fm, err := ExtractMetadataFromFolder(tc.path)
		assert.NoError(t, err)
		assert.Equal(t, tc.title, fm.Title, tc.path)
		assert.Equal(t, tc.series, fm.SeriesName, tc.path)
		assert.Equal(t, tc.pos, fm.SeriesPosition, tc.path)
		assert.Equal(t, tc.authors, fm.Authors, tc.path)
	}
}

// Folder-name regressions found by running the parser over the library's
// paths (made-up titles): a slot followed by a tagline names no book, a
// trailing part number keeps the folder's first field, an author-looking
// folder above a "<title> - <tagline>" folder is not an author credit, and a
// kept title keeps its own dashes.
func TestExtractMetadataFromFolder_FolderRegressions(t *testing.T) {
	for _, tc := range []struct{ path, title string }{
		{"/srv/library/Mara Quill/Tower Climber 2 - A Daopocalypse Fantasy", "Tower Climber 2"},
		{"/srv/library/Mara Quill/The Paper Garden/The Paper Garden - 4", "The Paper Garden"},
		{"/srv/library/Ember Healer/Ember Healer - A LitRPG Adventure", "Ember Healer"},
		{"/srv/library/incoming/Glass Tower – A Story", "Glass Tower – A Story"},
		{"/srv/library/Mara Quill/Saving Honor/Saving Honor - 02 - Claimed by Night", "Claimed by Night"},
	} {
		fm, err := ExtractMetadataFromFolder(tc.path)
		assert.NoError(t, err)
		assert.Equal(t, tc.title, fm.Title, tc.path)
	}
}
