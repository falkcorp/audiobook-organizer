// file: internal/metafetch/search_variants_test.go
// version: 1.2.0
// guid: f16af23a-770c-4bcf-8317-0c7f7724ee42
// last-edited: 2026-10-01

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The prod titles that got 0 or wrong results under the stop-at-first-hit
// ladder (2026-10-01), parsed into the book's own name and its slots.
func TestParseSearchTitle_ProdFailures(t *testing.T) {
	cases := []struct {
		raw, author, narrator string
		want                  parsedTitle
	}{
		{raw: "Magma Heart - Unknown Author", author: "Plum Parrot",
			want: parsedTitle{Title: "Magma Heart"}},
		{raw: "read by Cathfach (Erryn's World)",
			want: parsedTitle{Title: "Erryn's World", Narrator: "Cathfach"}},
		{raw: "read by Solomon Ignis (Reborn a Hero)",
			want: parsedTitle{Title: "Reborn a Hero", Narrator: "Solomon Ignis"}},
		{raw: "Jack Reacher 17: A Wanted Man (Jeff Harding)", author: "Lee Child",
			want: parsedTitle{Title: "A Wanted Man", Series: "Jack Reacher", Position: "17", Narrator: "Jeff Harding", NameSplit: true, SlotHead: "Jack Reacher", Name: "A Wanted Man"}},
		{raw: "Jack Reacher 17: A Wanted Man (Jeff Harding)", author: "Lee Child", narrator: "Jeff Harding",
			want: parsedTitle{Title: "A Wanted Man", Series: "Jack Reacher", Position: "17", Narrator: "Jeff Harding", NameSplit: true, SlotHead: "Jack Reacher", Name: "A Wanted Man"}},
		{raw: "The Witcher - 4 - The Tower of the Swallow", author: "Andrzej Sapkowski",
			want: parsedTitle{Title: "The Tower of the Swallow", Series: "The Witcher", Position: "4", NameSplit: true, SlotHead: "The Witcher", Name: "The Tower of the Swallow"}},
		{raw: "Saving Supervillains, Book 5 - Bruce Sentar",
			want: parsedTitle{Title: "Saving Supervillains", Series: "Saving Supervillains", Position: "5", Author: "Bruce Sentar", TitleIsSeries: true, SlotHead: "Saving Supervillains"}},
		{raw: "Drudge Match - Unknown Author",
			want: parsedTitle{Title: "Drudge Match"}},
		{raw: "2010 The Stainless Steel Rat Returns - Unknown Author", author: "Harry Harrison",
			want: parsedTitle{Title: "2010 The Stainless Steel Rat Returns", YearFree: "The Stainless Steel Rat Returns"}},
		{raw: "Mayor of Mythos: An Isekai LitRPG Fantasy (Unabridged)",
			want: parsedTitle{Title: "Mayor of Mythos: An Isekai LitRPG Fantasy", Short: "Mayor of Mythos"}},
		// The review's blockers (2026-10-01): a genre tagline after a series
		// slot is not the book's name, and a one-word "series" with a number
		// is a title. The number is still the position answers must name.
		{raw: "Rogue Ascension 8: A Progression LitRPG", author: "Hunter Mythos",
			want: parsedTitle{Title: "Rogue Ascension 8: A Progression LitRPG", Series: "Rogue Ascension", Position: "8", Short: "Rogue Ascension 8", SlotHead: "Rogue Ascension"}},
		{raw: "Rogue Ascension, Book 8: A LitRPG Adventure",
			want: parsedTitle{Title: "Rogue Ascension", Series: "Rogue Ascension", Position: "8", TitleIsSeries: true, SlotHead: "Rogue Ascension"}},
		{raw: "Fahrenheit 451: A Novel",
			want: parsedTitle{Title: "Fahrenheit 451: A Novel", Position: "451", Short: "Fahrenheit 451", SlotHead: "Fahrenheit"}},
		{raw: "Catch 22: A Novel",
			want: parsedTitle{Title: "Catch 22: A Novel", Position: "22", Short: "Catch 22", SlotHead: "Catch"}},
		{raw: "Area 51: An Uncensored History",
			want: parsedTitle{Title: "Area 51: An Uncensored History", Position: "51", Short: "Area 51", SlotHead: "Area", Name: "An Uncensored History"}},
		{raw: "Apollo 8: The Thrilling Story of the First Mission to the Moon",
			want: parsedTitle{Title: "Apollo 8: The Thrilling Story of the First Mission to the Moon", Position: "8", Short: "Apollo 8", SlotHead: "Apollo", Name: "The Thrilling Story of the First Mission to the Moon"}},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			assert.Equal(t, tc.want, parseSearchTitle(tc.raw, tc.author, tc.narrator))
		})
	}
}

// Titles that must come through untouched: a number that IS the title, a year
// glued to a subtitle, a trailing dash field that is the book, a series in
// parentheses. Title, series and position are pinned for every case.
func TestParseSearchTitle_Negatives(t *testing.T) {
	cases := []struct{ raw, title, series, position string }{
		{raw: "1984", title: "1984"},
		{raw: "11/22/63", title: "11/22/63"},
		{raw: "2001: A Space Odyssey", title: "2001: A Space Odyssey"},
		{raw: "Metro 2034", title: "Metro 2034"},
		{raw: "Dune", title: "Dune"},
		{raw: "The Long Cosmos (Long Earth Saga)", title: "The Long Cosmos (Long Earth Saga)"},
		{raw: "Star Wars - Thrawn", title: "Star Wars - Thrawn"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			p := parseSearchTitle(tc.raw, "", "")
			assert.Equal(t, tc.title, p.Title)
			assert.Equal(t, tc.series, p.Series)
			assert.Equal(t, tc.position, p.Position)
			assert.Empty(t, p.YearFree)
			assert.Empty(t, p.Narrator)
			assert.Empty(t, p.Author)
			assert.False(t, p.TitleIsSeries)
			assert.False(t, p.NameSplit)
		})
	}
}

func variantPairs(vs []queryVariant) [][2]string {
	out := make([][2]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, [2]string{v.Title, v.Author})
	}
	return out
}

// Variant generation for every prod failure title: the cleaned title is asked
// first with the author, the narrator is asked as the author, and the cap holds.
func TestBuildQueryVariants_ProdFailures(t *testing.T) {
	cases := []struct {
		raw, author, narrator string
		want                  [][2]string
	}{
		{raw: "Magma Heart - Unknown Author", author: "Plum Parrot",
			want: [][2]string{{"Magma Heart", "Plum Parrot"}, {"Magma Heart - Unknown Author", "Plum Parrot"}, {"Magma Heart", ""}}},
		{raw: "read by Cathfach (Erryn's World)",
			want: [][2]string{{"Erryn's World", "Cathfach"}, {"read by Cathfach (Erryn's World)", "Cathfach"}, {"Erryn's World", ""}}},
		{raw: "read by Solomon Ignis (Reborn a Hero)",
			want: [][2]string{{"Reborn a Hero", "Solomon Ignis"}, {"read by Solomon Ignis (Reborn a Hero)", "Solomon Ignis"}, {"Reborn a Hero", ""}}},
		{raw: "Jack Reacher 17: A Wanted Man (Jeff Harding)", author: "Lee Child",
			want: [][2]string{{"A Wanted Man", "Lee Child"}, {"A Wanted Man", "Jeff Harding"}, {"Jack Reacher 17: A Wanted Man (Jeff Harding)", "Lee Child"}, {"Jack Reacher", "Lee Child"}}},
		{raw: "The Witcher - 4 - The Tower of the Swallow", author: "Andrzej Sapkowski", narrator: "Peter Kenny",
			want: [][2]string{{"The Tower of the Swallow", "Andrzej Sapkowski"}, {"The Tower of the Swallow", "Peter Kenny"}, {"The Witcher - 4 - The Tower of the Swallow", "Andrzej Sapkowski"}, {"The Witcher", "Andrzej Sapkowski"}}},
		{raw: "Saving Supervillains, Book 5 - Bruce Sentar",
			want: [][2]string{{"Saving Supervillains", "Bruce Sentar"}, {"Saving Supervillains, Book 5 - Bruce Sentar", "Bruce Sentar"}, {"Saving Supervillains", ""}}},
		{raw: "Drudge Match - Unknown Author",
			want: [][2]string{{"Drudge Match", ""}, {"Drudge Match - Unknown Author", ""}}},
		{raw: "2010 The Stainless Steel Rat Returns - Unknown Author", author: "Harry Harrison",
			want: [][2]string{{"2010 The Stainless Steel Rat Returns", "Harry Harrison"}, {"2010 The Stainless Steel Rat Returns - Unknown Author", "Harry Harrison"}, {"The Stainless Steel Rat Returns", "Harry Harrison"}, {"2010 The Stainless Steel Rat Returns", ""}}},
		{raw: "Mayor of Mythos: An Isekai LitRPG Fantasy (Unabridged)", author: "Eric Vall",
			want: [][2]string{{"Mayor of Mythos: An Isekai LitRPG Fantasy", "Eric Vall"}, {"Mayor of Mythos", "Eric Vall"}, {"Mayor of Mythos: An Isekai LitRPG Fantasy", ""}}},
		{raw: "Rogue Ascension 8: A Progression LitRPG", author: "Hunter Mythos",
			want: [][2]string{{"Rogue Ascension 8: A Progression LitRPG", "Hunter Mythos"}, {"Rogue Ascension 8", "Hunter Mythos"}, {"Rogue Ascension", "Hunter Mythos"}, {"Rogue Ascension 8: A Progression LitRPG", ""}}},
		{raw: "Fahrenheit 451: A Novel", author: "Ray Bradbury",
			want: [][2]string{{"Fahrenheit 451: A Novel", "Ray Bradbury"}, {"Fahrenheit 451", "Ray Bradbury"}, {"Fahrenheit 451: A Novel", ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			p := parseSearchTitle(tc.raw, tc.author, tc.narrator)
			author, narrator := tc.author, tc.narrator
			if author == "" {
				author = p.Author
			}
			if narrator == "" {
				narrator = p.Narrator
			}
			got := buildQueryVariants(p, stripChapterFromTitle(tc.raw), tc.raw, author, narrator)
			assert.Equal(t, tc.want, variantPairs(got))
			assert.LessOrEqual(t, len(got), maxQueryVariants)
		})
	}
}

// A slot-only title ("<series>, Book 5") keeps only answers in that series at
// that position: a query by series name answers with every sibling.
func TestQueryVariant_SlotOnlyTitleRefusesSiblings(t *testing.T) {
	p := parseSearchTitle("Saving Supervillains, Book 5 - Bruce Sentar", "", "")
	vs := buildQueryVariants(p, "Saving Supervillains", "Saving Supervillains, Book 5 - Bruce Sentar", "Bruce Sentar", "")
	results := []metadata.BookMetadata{
		{Title: "Saving Supervillains 4", Author: "Bruce Sentar", Series: "Saving Supervillains", SeriesPosition: "4"},
		{Title: "Saving Supervillains 5", Author: "Bruce Sentar", Series: "Saving Supervillains", SeriesPosition: "5"},
		{Title: "Saving Supervillains", Author: "Bruce Sentar"},
	}
	kept := vs[0].accept(results, "Bruce Sentar")
	if assert.Len(t, kept, 1) {
		assert.Equal(t, "5", kept[0].SeriesPosition)
	}
}

func TestCandidateSeen_DedupesByIDOrTitleAuthor(t *testing.T) {
	s := candidateSeen{}
	assert.True(t, s.add(metadata.BookMetadata{Title: "Dune", Author: "Frank Herbert", ASIN: "B002V1OF70"}))
	assert.False(t, s.add(metadata.BookMetadata{Title: "Dune (Unabridged)", Author: "Herbert", ASIN: "b002v1of70"}), "same ASIN")
	assert.False(t, s.add(metadata.BookMetadata{Title: "dune", Author: "frank  herbert"}), "same normalized title+author")
	assert.True(t, s.add(metadata.BookMetadata{Title: "Dune Messiah", Author: "Frank Herbert", ISBN13: "978-0-593-09823-5"}))
	assert.False(t, s.add(metadata.BookMetadata{Title: "Dune Messiah: Book Two", Author: "F. Herbert", ISBN13: "9780593098235"}), "same ISBN")
}

func TestMaxSearchCallsPerBook(t *testing.T) {
	assert.Equal(t, maxQueryVariants+1, MaxSearchCallsPerBook(metadata.SourceIDAudible), "4 variants + the own-ASIN lookup")
	assert.Equal(t, maxOpenLibraryAsks, MaxSearchCallsPerBook(metadata.SourceIDOpenLibrary))
	assert.Equal(t, 1, MaxSearchCallsPerBook(metadata.SourceIDGoogleBooks))
	assert.Equal(t, 1, MaxSearchCallsPerBook(metadata.SourceIDHardcover))
	assert.Equal(t, maxAudnexusRegions, MaxSearchCallsPerBook(metadata.SourceIDAudnexus), "one lookup, at most 3 regions")
	assert.Len(t, audnexusSearchRegions, maxAudnexusRegions)
}
