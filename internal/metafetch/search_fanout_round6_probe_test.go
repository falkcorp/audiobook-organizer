// file: internal/metafetch/search_fanout_round6_probe_test.go
// version: 1.0.0
// guid: 5d0c7e3a-91b4-4f26-8a1e-2c6b9f04d7e3
// last-edited: 2026-10-01

package metafetch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3641 round-5 re-review (deefb0999). S1: a sibling answering in the shape
// Google Books and Open Library use -- the series name for a title, NO
// series_position, no number -- names no position at all, so no position
// gate could refute it. It is not strong (positionNamed), by its author or by
// the book's stored ASIN, in every form the book's title writes its number.
var s1Cases = []struct{ book, author, sibling, right string }{
	{"Rogue Ascension VIII", raAuthor, "Rogue Ascension", "Rogue Ascension 8"},
	{"Rogue Ascension Eight", raAuthor, "Rogue Ascension", "Rogue Ascension 8"},
	{"Rogue Ascension 8", raAuthor, "Rogue Ascension", "Rogue Ascension 8"},
	{"The Witcher 4", "Andrzej Sapkowski", "The Witcher", "The Witcher 4"},
	{"The Final Four", "Some Author", "The Final", "The Final Four"},
}

func TestSearchFanoutProbe_NoPositionSiblingIsNeverStrong(t *testing.T) {
	for _, tc := range s1Cases {
		for _, dur := range []int{0, 36000} {
			t.Run(tc.book, func(t *testing.T) {
				sib := metadata.BookMetadata{Title: tc.sibling, Author: tc.author, DurationSec: 36500, CoverURL: "c"}
				src := serving(sib)
				svc := fanoutHarness(t, probeBook(tc.book, dur), src)
				_, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, "Some Reader", "", SearchOptions{})
				require.NoError(t, err)
				assert.Greater(t, src.callCount(), 1, "dur %d: a position-less sibling never stops the search", dur)
			})
		}
	}
}

// The own-ASIN leg of S1: the stored ASIN is the position-less sibling's,
// within 15% of the book's runtime. It does not agree with the book
// (ownASINAgrees), so it neither stops the search nor takes the first-place
// tier; it keeps only the x2.0 ASIN multiplier.
func TestSearchFanoutProbe_NoPositionSiblingOwnASIN(t *testing.T) {
	for _, tc := range s1Cases {
		t.Run(tc.book, func(t *testing.T) {
			sibASIN := "B00SIBLNG1"
			sib := metadata.BookMetadata{Title: tc.sibling, Author: tc.author, DurationSec: 36500, CoverURL: "c", ASIN: sibASIN}
			b := probeBook(tc.book, 36000)
			b.ASIN = &sibASIN
			src := serving(sib)
			svc := fanoutHarness(t, b, src)
			_, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Greater(t, src.callCount(), 1, "the stored sibling ASIN never stops the search")

			p := parseSearchTitle(tc.book, tc.author, "")
			c := newStrongCriteria(p, p.Title, tc.book, sibASIN, tc.author, 36000)
			assert.False(t, c.ownASINAgrees(sib))
			right := metadata.BookMetadata{Title: tc.right, Author: tc.author, DurationSec: 36500, ASIN: sibASIN}
			assert.True(t, c.ownASINAgrees(right), "the book itself under its ASIN still agrees")
		})
	}
}

// What positionNamed lets through: the book's own position (explicit or in
// the title), and the book's own name with no position. Not a runtime alone,
// even within 2%: a sibling of the series can run as long.
func TestStrongCriteria_PositionNamed(t *testing.T) {
	const book = "Rogue Ascension VIII"
	p := parseSearchTitle(book, raAuthor, "")
	c := newStrongCriteria(p, p.Title, book, "", raAuthor, 36000)
	for _, r := range []metadata.BookMetadata{
		{Title: "Rogue Ascension", SeriesPosition: "8", DurationSec: 37000},
		{Title: "Rogue Ascension 8", DurationSec: 37000},
		{Title: "Rogue Ascension Eight", DurationSec: 37000},
	} {
		assert.True(t, c.positionNamed(r), "%+v", r)
	}
	for _, r := range []metadata.BookMetadata{
		{Title: "Rogue Ascension", DurationSec: 37000},
		{Title: "Rogue Ascension", DurationSec: 36000},
		{Title: "Rogue Ascension", SeriesPosition: "7", DurationSec: 36000},
	} {
		assert.False(t, c.positionNamed(r), "%+v", r)
	}

	// No parsed position: the title's own numbers.
	p = parseSearchTitle("The Final Four", "Some Author", "")
	c = newStrongCriteria(p, p.Title, "The Final Four", "", "Some Author", 0)
	assert.True(t, c.positionNamed(metadata.BookMetadata{Title: "The Final Four"}))
	assert.True(t, c.positionNamed(metadata.BookMetadata{Title: "The Final 4"}))
	assert.False(t, c.positionNamed(metadata.BookMetadata{Title: "The Final"}))

	const named = "Witcher 4: The Tower of the Swallow"
	p = parseSearchTitle(named, "Andrzej Sapkowski", "")
	c = newStrongCriteria(p, p.Title, named, "", "Andrzej Sapkowski", 36000)
	assert.True(t, c.positionNamed(metadata.BookMetadata{Title: "The Tower of the Swallow", DurationSec: 40000}),
		"the book's own name, with no position, is enough")
}

// S3: names written with an honorific, a surname particle or sorted by a
// particled surname are the same person; a shared surname with another given
// name still is not.
func TestSearchFanoutProbe_PersonNamesRound6(t *testing.T) {
	for _, tc := range []struct {
		title, book, answer string
		strong              bool
	}{
		{"The Dispossessed", "Ursula K. Le Guin", "Le Guin, Ursula K.", true},
		{"The Dispossessed", "Ursula K. Le Guin", "Le Guin", true},
		{"The Spy Who Came in from the Cold", "John le Carré", "le Carré, John", true},
		{"Lest Darkness Fall", "L. Sprague de Camp", "de Camp, L. Sprague", true},
		{"Rendezvous with Rama", "Arthur C. Clarke", "Sir Arthur C. Clarke", true},
		{"Rendezvous with Rama", "Arthur C. Clarke", "Dr. Arthur C. Clarke", true},
		{"Duma Key", "Stephen King", "Owen King", false},
		// A particle that ends a name is its surname: "Le" is a common
		// Vietnamese surname, never skipped for the given name before it.
		{"Some Book", "Thanh Le", "T. Le", true},
		{"Some Book", "Thanh Le", "Mary Le", false},
		{"Some Book", "Mary Le", "Mary Smith", false},
		{"The Sentinel", "Lee Child", "Andrew Child", false},
	} {
		t.Run(tc.book+" vs "+tc.answer, func(t *testing.T) {
			assert.Equal(t, tc.strong, sharesPerson(tc.answer, tc.book))
			src := serving(metadata.BookMetadata{Title: tc.title, Author: tc.answer, DurationSec: 34000, CoverURL: "c"})
			svc := fanoutHarness(t, probeBook(tc.title, 36000), src)
			_, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.book, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			if tc.strong {
				assert.Equal(t, 1, src.callCount())
			} else {
				assert.Greater(t, src.callCount(), 1)
			}
		})
	}
}

func TestPersonNames_Round6(t *testing.T) {
	for in, want := range map[string][][]string{
		"Le Guin, Ursula K.":   {{"ursula", "k", "le", "guin"}},
		"de Camp, L. Sprague":  {{"l", "sprague", "de", "camp"}},
		"Sir Arthur C. Clarke": {{"sir", "arthur", "c", "clarke"}},
		// Two people: neither part is a surname alone.
		"Lee Child, Andrew Child": {{"lee", "child"}, {"andrew", "child"}},
	} {
		assert.Equal(t, want, personNames(in), "%q", in)
	}
	sn, i := surname([]string{"ursula", "k", "le", "guin"})
	assert.Equal(t, "le guin", sn)
	assert.Equal(t, 'u', givenInitial([]string{"ursula", "k", "le", "guin"}, i))
	_, i = surname([]string{"le", "guin"})
	assert.Equal(t, rune(0), givenInitial([]string{"le", "guin"}, i), "a particle never gives an initial")
}

// N2: a junk title's number is not a series position. The junk-title fixer's
// whole input is such titles, and a parsed position would drop every
// candidate a provider numbers otherwise.
func TestParseSearchTitle_JunkTitleHasNoPosition(t *testing.T) {
	for _, title := range []string{"Audiobook 2", "New Recording 4"} {
		require.NotEqual(t, metadata.JunkNone, metadata.ClassifyJunkTitle(title), "%q must be junk", title)
		p := parseSearchTitle(title, "Ann Author", "")
		assert.Empty(t, p.Position, "%q", title)
		assert.False(t, p.BareSlot, "%q", title)
		c := newStrongCriteria(p, p.Title, title, "", "Ann Author", 0)
		assert.False(t, c.explicitPositionConflicts(metadata.BookMetadata{Title: "The Real Title", SeriesPosition: "3"}), "%q", title)
	}
	// Not junk: a real title keeps its position.
	assert.Equal(t, "8", parseSearchTitle("Rogue Ascension 8", raAuthor, "").Position)
}

// N2 through the fan-out: a junk-titled book's candidate at series position 3
// is kept, not dropped as another book of a series the title never named.
func TestSearchFanoutProbe_JunkTitleKeepsNumberedCandidates(t *testing.T) {
	const title, author = "New Recording 4", "Ann Author"
	cand := metadata.BookMetadata{Title: "The Real Title", Author: author, Series: "Some Series", SeriesPosition: "3",
		DurationSec: 36000, CoverURL: "c"}
	resp := probeSearch(t, probeBook(title, 36000), author, serving(cand))
	assert.Contains(t, resultTitles(resp), cand.Title)
}

// N2: a junk title's number is not the book's, so the book's own ASIN answer
// -- its real title, which carries no "4" -- still agrees with it, and stops
// the search in round 1.
func TestSearchFanoutProbe_JunkTitleOwnASINAgrees(t *testing.T) {
	const title, author, asin = "New Recording 4", "Ann Author", "B0REALBOOK"
	own := metadata.BookMetadata{Title: "The Real Title", Author: author, DurationSec: 36100, CoverURL: "c", ASIN: asin}
	b := probeBook(title, 36000)
	b.ASIN = func() *string { s := asin; return &s }()
	src := serving(own)
	svc := fanoutHarness(t, b, src)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Results)
	assert.Equal(t, asin, resp.Results[0].ASIN)
	assert.Equal(t, 1, src.callCount(), "the book's own ASIN, at its runtime, is strong")
}

// N2 on the apply-path read the junk-title fixer uses (GetCachedCandidates):
// an unstamped row cached for a junk-titled book is filtered by the position
// rules, and a candidate numbered 3 survives -- the title's "4" was never the
// book's position.
func TestGetCachedCandidates_JunkTitleKeepsNumberedCandidates(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: "New Recording 4", FilePath: "/lib/x/rec.m4b"})
	require.NoError(t, err)
	raw, err := json.Marshal(MetadataCandidate{Title: "The Real Title", Author: "Ann Author", Series: "Some Series", SeriesPosition: "3"})
	require.NoError(t, err)
	require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: time.Now().UTC(), Candidates: []json.RawMessage{raw},
	}))
	entry, _, err := f.mfs.GetCachedCandidates(b.ID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, []json.RawMessage{raw}, entry.Candidates)
}
