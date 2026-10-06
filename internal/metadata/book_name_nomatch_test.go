// file: internal/metadata/book_name_nomatch_test.go
// version: 1.0.0
// guid: 60025c82-bf73-40fd-a024-8a2df56a858b
// last-edited: 2026-10-06

package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Shapes from the 2026-10-05 no-match census (732 books a forced candidate
// re-fetch left unmatched), with made-up titles. Each is a genuine parse
// rule, so it lives in ParseBookName: a search and a new import read the
// name the same way.
func TestParseBookName_NomatchCensusShapes(t *testing.T) {
	cases := []struct {
		name, raw, path     string
		title, year, suffix string
		wantShape           string
	}{
		// The organizer's own file template, "NNN - <Title> - read by
		// <narrator>", with the narrator placeholder it once wrote: the
		// zero-padded leading field is the track, not part of the title.
		{name: "own template", raw: "001 - Some Example Saga, Book 2 (Unabridged) - read by narrator",
			title: "Some Example Saga, Book 2 (Unabridged)", suffix: "001", wantShape: ShapeTrackLead},
		{name: "own template, two digits", raw: "07 - Harbor Lights - read by narrator",
			title: "Harbor Lights", suffix: "07", wantShape: ShapeTrackLead},
		// An unpadded number is the track only in the per-track folder
		// named for it.
		{name: "own template, unpadded in its track folder", raw: "110 - Harbor Lights - read by narrator",
			path:  "/srv/example-organizer/Harbor Lights/110/110 - Harbor Lights - read by narrator.mp3",
			title: "Harbor Lights", suffix: "110", wantShape: ShapeTrackLead},
		// HTML entities a tag or a scraped name carried in.
		{name: "html entity", raw: "Fish &amp; Chips, Volume II", title: "Fish & Chips, Volume II"},
		{name: "numeric entity", raw: "Salt &#38; Pepper", title: "Salt & Pepper"},
		// Filesystem-safe stand-ins for a colon.
		{name: "modifier-letter colon", raw: "The Example of Doubt꞉ Some Detective, Book 3", title: "The Example of Doubt: Some Detective, Book 3"},
		{name: "underscore colon", raw: "Wasteland Tales 2_ More Example Stories", title: "Wasteland Tales 2: More Example Stories"},
		{name: "underscore colon, cut subtitle", raw: "Example Magic_", title: "Example Magic"},
		// A trailing release year in parentheses is no part of a catalog
		// title (a release tag behind it is removed first).
		{name: "year in parentheses", raw: "Meeting Point (2017) [Example World 3]", title: "Meeting Point", year: "2017", wantShape: ShapeYearParen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseBookName(tc.raw, NameEvidence{Path: tc.path})
			assert.Equal(t, tc.title, got.Title, "title")
			assert.Equal(t, tc.year, got.Year, "year")
			assert.Equal(t, tc.suffix, got.Suffix, "suffix")
			assert.Empty(t, got.Author, "author")
			if tc.wantShape != "" {
				assert.True(t, got.Has(tc.wantShape), "shape %s in %v", tc.wantShape, got.Shapes)
			}
		})
	}
}

// Shapes these rules must NOT read.
func TestParseBookName_NomatchShapeNegatives(t *testing.T) {
	for _, tc := range []struct{ raw, path, title string }{
		// A leading number with no "read by" trailer: by shape alone it may
		// be the title's.
		{raw: "001 - Harbor Lights", title: "001 - Harbor Lights"},
		{raw: "15 - Harry and the Fox", title: "15 - Harry and the Fox"},
		// An unpadded number outside its own track folder is the title's.
		{raw: "101 - Dalmatians - read by narrator", title: "101 - Dalmatians"},
		{raw: "101 - Dalmatians - read by narrator", path: "/srv/example-organizer/Dodie Smith/101 - Dalmatians.mp3", title: "101 - Dalmatians"},
		// A year inside the title is the title's.
		{raw: "Blade (1998) Revisited", title: "Blade (1998) Revisited"},
		{raw: "(2017)", title: "(2017)"},
		// A legacy entity with no semicolon is text ("&not" in "&notes").
		{raw: "Fish &notes; and Chips", title: "Fish &notes; and Chips"},
		{raw: "Fish &notes and Chips", title: "Fish &notes and Chips"},
		{raw: "Fish & Chips", title: "Fish & Chips"},
		// An underscore inside a word.
		{raw: "snake_case_title", title: "snake_case_title"},
	} {
		got := ParseBookName(tc.raw, NameEvidence{Path: tc.path})
		assert.Equal(t, tc.title, got.Title, tc.raw)
		assert.False(t, got.Has(ShapeTrackLead), tc.raw)
	}
	assert.Equal(t, "", ParseBookName("2018 - Blueshift - read by narrator", NameEvidence{}).Suffix, "a year is read as one")
}

// A bare file-copy name ("copy1") is no title to search by: a catalog
// answers it with whatever it ranks first.
func TestIsUnsearchableTitle_BareCopyName(t *testing.T) {
	for _, s := range []string{"copy1", "copy2", "Copy 3", "_copy12"} {
		assert.True(t, IsUnsearchableTitle(s), s)
	}
	for _, s := range []string{"Copycat", "The Copy", "Copy Shop 2"} {
		assert.False(t, IsUnsearchableTitle(s), s)
	}
}
