// file: internal/metafetch/search_filename_titles_test.go
// version: 1.0.0
// guid: c97eea70-d007-4405-bff2-d7f992c312a3
// last-edited: 2026-10-05

package metafetch

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// audibleCatalog is a fake Audible shaped like the real catalog search: the
// product title and subtitle are separate fields, series and position are
// structured, and a keyword query answers only products whose title,
// subtitle and author carry EVERY query token -- so "2018 - Blueshift" finds
// nothing while "Blueshift" does, the behaviour the 2026-10-05 census
// measured against api.audible.com. ASINs are made up.
func audibleCatalog(products ...metadata.BookMetadata) *fakeProvider {
	tok := regexp.MustCompile(`[\pL\pN]+`)
	tokens := func(s string) []string { return tok.FindAllString(strings.ToLower(s), -1) }
	return &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
		var out []metadata.BookMetadata
		for _, p := range products {
			have := map[string]bool{}
			for _, w := range tokens(p.Title + " " + p.Subtitle + " " + p.Author) {
				have[w] = true
			}
			ok := true
			for _, w := range tokens(title) {
				if !have[w] {
					ok = false
					break
				}
			}
			if ok && author != "" && !sharesPerson(p.Author, author) {
				ok = false
			}
			if ok {
				out = append(out, p)
			}
		}
		return out
	}}
}

var (
	blueshift = metadata.BookMetadata{Title: "Blueshift", Subtitle: "Example Fleet, Book 1", Author: "Joshua Dalzelle",
		Narrator: "Reader One", Series: "Example Fleet", SeriesPosition: "1", ASIN: "B0TEST0001", DurationSec: 9 * 3600, CoverURL: "c"}
	blueshiftOther = metadata.BookMetadata{Title: "Blueshift", Author: "Someone Else", Narrator: "Reader Two",
		ASIN: "B0TEST0002", DurationSec: 7 * 3600, CoverURL: "c"}
	fifthElephant = metadata.BookMetadata{Title: "The Fifth Elephant", Subtitle: "Discworld, Book 24", Author: "Terry Pratchett",
		Narrator: "Reader Three", Series: "Discworld", SeriesPosition: "24", ASIN: "B0TEST0024", DurationSec: 11 * 3600, CoverURL: "c"}
	colourOfMagic = metadata.BookMetadata{Title: "The Colour of Magic", Subtitle: "Discworld, Book 1", Author: "Terry Pratchett",
		Narrator: "Reader Three", Series: "Discworld", SeriesPosition: "1", ASIN: "B0TEST0101", DurationSec: 8 * 3600, CoverURL: "c"}
)

// The two census books Audible finds directly but the app recorded no match
// for (op 01M3ZC9K). Root cause: the PARSER, not a filter and not the
// 4-variant cap. parseSearchTitle never produced the bare name:
//   - "2018 - Blueshift": the year-free variant needs two significant words
//     after the year, and the subtitle head it built instead was "2018";
//   - "Discworld 24 - The Fifth Elephant - 01": the " - 01" track suffix stayed
//     on the name, and a one-word series is never split before its name.
//
// Before the fix only 3-4 variants were built (under the cap) and none was
// the name, so no filter ever saw the right answer.
func TestSearchFanout_FilenameTitlesFindTheBook(t *testing.T) {
	for _, tc := range []struct {
		title, author, wantQuery, wantASIN string
	}{
		{"2018 - Blueshift", "Joshua Dalzelle", "Blueshift", "B0TEST0001"},
		{"Discworld 24 - The Fifth Elephant - 01", "Terry Pratchett", "The Fifth Elephant", "B0TEST0024"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			audible := audibleCatalog(blueshift, blueshiftOther, fifthElephant, colourOfMagic)
			svc := fanoutHarness(t, &database.Book{ID: "b1", Title: tc.title}, audible)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", tc.title, tc.author, "", "", SearchOptions{})
			require.NoError(t, err)
			require.NotEmpty(t, resp.Results, "the book must be found")
			assert.Equal(t, tc.wantASIN, resp.Results[0].ASIN)
			assert.Equal(t, [2]string{tc.wantQuery, tc.author}, audible.calls[0], "the first question is the bare name + author")
		})
	}
}

// A trailing " - 01" is a track, not the book's number. The series-number
// tiebreak read the RAW title's trailing number, so it expected position 1:
// the edition that carries the book's real position (24) was penalised as
// "another book of the series" and an edition with no series data outranked
// it. The tiebreak now reads the title less what metadata.ParseBookName
// removed, so it expects 24.
func TestSearchFanout_TrackSuffixIsNotTheSeriesNumber(t *testing.T) {
	bare := metadata.BookMetadata{Title: "The Fifth Elephant", Author: "Terry Pratchett", Narrator: "Reader Four",
		ASIN: "B0TEST0201", DurationSec: 11 * 3600, CoverURL: "c"}
	audible := audibleCatalog(bare, fifthElephant)
	title := "Discworld 24 - The Fifth Elephant - 01"
	svc := fanoutHarness(t, &database.Book{ID: "b1", Title: title}, audible)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", title, "Terry Pratchett", "", "", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "B0TEST0024", resp.Results[0].ASIN, "the edition at the book's own position ranks first")
}

// The searched title changes for a reparsed book, and with it the
// fingerprint a cached "every provider has nothing" verdict is keyed on, so
// the batch fetch re-asks exactly the books the parser now reads differently
// -- no searchInputVersion bump, which would re-ask every cached book.
func TestResolveSearchInputs_ReparsedTitleChangesFingerprint(t *testing.T) {
	for _, title := range []string{"2018 - Blueshift", "Discworld 24 - The Fifth Elephant - 01"} {
		book := &database.Book{ID: "b1", Title: title}
		svc := fanoutHarness(t, book)
		in := svc.resolveSearchInputs(book, title, "Some Author", "")
		old := in
		old.title = title
		assert.NotEqual(t, title, in.title)
		assert.NotEqual(t, old.fingerprint(title), in.fingerprint(title), title)
	}
	// A title the parser leaves alone keeps its fingerprint.
	book := &database.Book{ID: "b1", Title: "A Plain Title"}
	svc := fanoutHarness(t, book)
	in := svc.resolveSearchInputs(book, book.Title, "Some Author", "")
	assert.Equal(t, "A Plain Title", in.title)
}

// An author that is the title itself is junk ("Hammer Fall Rising - Unknown
// Author" filed with author "Hammer Fall Rising"): narrowing by it finds
// nothing, so it is dropped and the title is asked alone.
func TestResolveSearchInputs_AuthorEqualToTitleIsDropped(t *testing.T) {
	title := "Hammer Fall Rising - Unknown Author"
	book := &database.Book{ID: "b1", Title: title}
	svc := fanoutHarness(t, book)
	in := svc.resolveSearchInputs(book, title, "Hammer Fall Rising", "")
	assert.Equal(t, "Hammer Fall Rising", in.title)
	assert.Empty(t, in.author)
	assert.Empty(t, in.bookAuthor)
}

// Every census shape (made-up titles): the book's bare name, with the author,
// is among the questions the capped ladder asks.
func TestBuildQueryVariants_FilenameShapesAskTheName(t *testing.T) {
	for _, tc := range []struct{ raw, author, want string }{
		{"2018 - Glasswake", "Mara Quill", "Glasswake"},
		{"Driftworld 24 - The Seventh Lantern - 01", "Dorian Vex", "The Seventh Lantern"},
		{"Saga of Embers Book 05 - Frostvale Trilogy - Rivers of Ash", "R. T. Ashby", "Rivers of Ash"},
		{"The Hollow Wood 01 - The Hollow Wood", "Ada Penn", "The Hollow Wood"},
		{"1987 - The Last Orbit (Avers) 64k 12.07.23 {345mb}", "Gene Holt", "The Last Orbit"},
		{"Squad K - 3 - The Night Shift", "Dorian Vex", "The Night Shift"},
		{"Semi-Trained Cadet 06 - Recruit", "Ada Penn", "Recruit"},
		{"Kestrel Moon - Copper Regent Book 03 - Unknown Author", "Kestrel Moon", "Copper Regent"},
		{"J.K. Marlow - Freighter for Hire 02 [44m4] [Fixed]", "J. K. Marlow", "Freighter for Hire 02"},
		{"2005 - Raven Song: A Novel of the Reach", "Mara Quill", "Raven Song"},
		{"Lantern Corps 13 - The Quiet Lords", "Gene Holt", "The Quiet Lords"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			p := parseSearchTitle(tc.raw, tc.author, "")
			vs := buildQueryVariants(p, stripChapterFromTitle(tc.raw), tc.raw, tc.author, "")
			require.LessOrEqual(t, len(vs), maxQueryVariants)
			assert.Contains(t, variantPairs(vs), [2]string{tc.want, tc.author})
		})
	}
}

// A provider credits the accented spelling ("Zoë Brontë") the book's tags
// lack ("Zoe Bronte"): the same person. The person checks (sharesPerson:
// the title-only variant's personRequired, keepAnchored, the strong
// criteria) compared raw letters and dropped the right answer -- one of the
// census books Audible finds.
func TestSharesPerson_FoldsAccents(t *testing.T) {
	assert.True(t, sharesPerson("Zoë Brontë, Ada Penn", "Zoe Bronte"))
	assert.True(t, sharesPerson("Zoe Bronte", "Zoë Brontë"))
	assert.False(t, sharesPerson("Zoë Brontë", "Zoe Bront"))
}

// The strong criteria took the book's own numbers from the literal title, so
// a stripped track suffix (" - 01") counted as one of them: "The Fifth
// Elephant, Part 1" carried no number the book lacked (numbersFit), the name
// excused its position, and the split part stayed in the pool as book 24's
// candidate. The numbers now come from the title less what
// metadata.ParseBookName removed. Only the split part is in the catalog, so
// the series-tagline detector (two answers carrying the name at different
// positions) cannot be what drops it.
func TestSearchFanout_TrackSuffixIsNotOneOfTheBooksNumbers(t *testing.T) {
	part := metadata.BookMetadata{Title: "The Fifth Elephant, Part 1", Author: "Terry Pratchett", Narrator: "Reader Three",
		ASIN: "B0TEST0201", DurationSec: 5 * 3600, CoverURL: "c"}
	audible := audibleCatalog(part)
	title := "Discworld 24 - The Fifth Elephant - 01"
	svc := fanoutHarness(t, &database.Book{ID: "b1", Title: title}, audible)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", title, "Terry Pratchett", "", "", SearchOptions{})
	require.NoError(t, err)
	for _, r := range resp.Results {
		assert.NotEqual(t, "B0TEST0201", r.ASIN, "an answer numbered 1 is not book 24")
	}
}

// With the junk author dropped no person vouches for an answer, and the bulk
// fetch applies the top candidate with no score floor. So a title-only answer
// stands only when its title is exactly the book's and every answer names one
// author: two authors' books of that title are ambiguous, and an answer with
// more words is another book.
func TestSearchFanout_AuthorEqualToTitleNeedsAnUnambiguousAnswer(t *testing.T) {
	title := "Hammer Fall Rising - Unknown Author"
	one := metadata.BookMetadata{Title: "Hammer Fall Rising", Author: "Gene Holt", ASIN: "B0TEST0401", CoverURL: "c"}
	other := metadata.BookMetadata{Title: "Hammer Fall Rising", Author: "Ada Penn", ASIN: "B0TEST0402", CoverURL: "c"}
	longer := metadata.BookMetadata{Title: "Hammer Fall Rising Again", Author: "Gene Holt", ASIN: "B0TEST0403", CoverURL: "c"}

	search := func(products ...metadata.BookMetadata) []MetadataCandidate {
		t.Helper()
		book := &database.Book{ID: "b1", Title: title}
		svc := fanoutHarness(t, book, audibleCatalog(products...))
		resp, err := svc.SearchMetadataForBookWithOptions("b1", title, "Hammer Fall Rising", "", "", SearchOptions{})
		require.NoError(t, err)
		return resp.Results
	}
	got := search(one, longer)
	require.Len(t, got, 1)
	assert.Equal(t, "B0TEST0401", got[0].ASIN, "one author, exact title: kept")
	assert.Empty(t, search(one, other), "two authors' books of the title: ambiguous, none kept")
}
