// file: internal/metafetch/search_fanout_round7_probe_test.go
// version: 1.1.0
// guid: 5681c68e-3c68-4920-971d-5dbd61de2aef
// last-edited: 2026-10-01

package metafetch

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3641 round-6 re-review (6c16e1f65). B1: the book's stored ASIN is a
// position-less sibling's. The sibling does not agree with the book
// (ownASINAgrees), so the ASIN earns it no multiplier at all; the right
// answer, which carries no ASIN, ranks above it. Bulk apply takes
// Candidates[0], and the apply gate cannot refute a sibling that names no
// number, so the order IS the decision. The sibling is served first, so a
// tie fails too (the sort is stable).
func TestSearchFanoutProbe_SiblingASINDoesNotOutrankTheBook(t *testing.T) {
	for _, tc := range []struct{ book, author, sibling, right string }{
		{"The Final Four", "Some Author", "The Final", "The Final Four"},
		{"Rogue Ascension VIII", raAuthor, "Rogue Ascension", "Rogue Ascension 8"},
		{"The Witcher 4", "Andrzej Sapkowski", "The Witcher", "The Witcher 4"},
	} {
		t.Run(tc.book, func(t *testing.T) {
			sibASIN := "B00SIBLNG1"
			sib := metadata.BookMetadata{Title: tc.sibling, Author: tc.author, DurationSec: 36500, CoverURL: "c", ASIN: sibASIN}
			right := metadata.BookMetadata{Title: tc.right, Author: tc.author, DurationSec: 36200, CoverURL: "c"}
			b := probeBook(tc.book, 36000)
			b.ASIN = &sibASIN
			svc := fanoutHarness(t, b, serving(sib, right))
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			require.NotEmpty(t, resp.Results)
			assert.Equal(t, tc.right, resp.Results[0].Title, "%v", resultTitles(resp))
			for _, c := range resp.Results {
				if c.Title != tc.sibling || c.ScoreBreakdown == nil {
					continue
				}
				for _, s := range c.ScoreBreakdown.Steps {
					if s.ID == "asin_match" {
						assert.Equal(t, 1.0, s.Operand, "a disagreeing own-ASIN answer earns no ASIN multiplier")
					}
				}
			}
		})
	}
}

// S1: the book's name, with no position, is not strong for a book that has
// one: a name a series' siblings share ("Midlife Magic" as a sub-series
// tagline, "Murder" in many titles) says nothing about which number. The
// answers carry the author and run within 15% (but not 2%), so before the fix
// nothing but positionNamed's name exemption separated them from strong.
func TestSearchFanoutProbe_NameWithoutPositionIsNotStrong(t *testing.T) {
	const author = "Ann Author"
	for _, tc := range []struct{ book, answer string }{
		{"Hemlock Hollow 8: Midlife Magic", "Hemlock Hollow: Midlife Magic"},
		{"Hemlock Hollow 8: Midlife Magic", "Midlife Magic"},
		{"Hemlock Hollow 8: Murder", "Murder at the Hollow"},
	} {
		t.Run(tc.book+" vs "+tc.answer, func(t *testing.T) {
			src := serving(metadata.BookMetadata{Title: tc.answer, Author: author, DurationSec: 37500, CoverURL: "c"})
			svc := fanoutHarness(t, probeBook(tc.book, 36000), src)
			_, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Greater(t, src.callCount(), 1)

			p := parseSearchTitle(tc.book, author, "")
			c := newStrongCriteria(p, p.Title, tc.book, "", author, 36000)
			assert.False(t, c.positionNamed(metadata.BookMetadata{Title: tc.answer, DurationSec: 37500}))
			// Within positionOverrideTolerance the name still vouches: the
			// provider-numbering case only the runtime can tell.
			assert.True(t, c.positionNamed(metadata.BookMetadata{Title: tc.answer, DurationSec: 36300}))
		})
	}
}

// S2: a stored ASIN's answer agrees with the book only when its title says
// nothing the book's does not (titleSubset) or it runs within
// positionOverrideTolerance. Siblings of one series run alike, so 15% is no
// evidence: "Sword Art Online Progressive 8" is not "Sword Art Online, Vol. 8".
func TestSearchFanoutProbe_SiblingASINWithinFifteenPercentIsNotStrong(t *testing.T) {
	const title, author, asin = "Sword Art Online, Vol. 8", "Reki Kawahara", "B0PROGRES8"
	prog := metadata.BookMetadata{Title: "Sword Art Online Progressive 8", Author: author,
		SeriesPosition: "8", DurationSec: 37500, CoverURL: "c", ASIN: asin}
	b := probeBook(title, 36000)
	b.ASIN = func() *string { s := asin; return &s }()
	src := serving(prog)
	svc := fanoutHarness(t, b, src)
	_, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Greater(t, src.callCount(), 1, "a sibling's ASIN 4%% off the runtime never stops the search")

	p := parseSearchTitle(title, author, "")
	c := newStrongCriteria(p, p.Title, title, asin, author, 36000)
	assert.False(t, c.ownASINAgrees(prog))
	// Its own series restating the extra word does not excuse it: the book
	// names its series, and "Progressive" is another one.
	withSeries := prog
	withSeries.Series = "Sword Art Online Progressive"
	assert.False(t, c.ownASINAgrees(withSeries))
	src = serving(withSeries)
	_, err = fanoutHarness(t, b, src).SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Greater(t, src.callCount(), 1)
	exact := prog
	exact.DurationSec = 36300
	assert.True(t, c.ownASINAgrees(exact), "within 2%% the runtime still vouches")
	own := metadata.BookMetadata{Title: "Sword Art Online, Vol. 8", Author: author, SeriesPosition: "8", DurationSec: 40000, ASIN: asin}
	assert.True(t, c.ownASINAgrees(own), "the book's own title still vouches whatever the runtime")
}

// S3: a title with no position of its own ("Overlord") takes the book's
// stored series sequence for the strong gates: an answer the provider numbers
// otherwise is not strong unless its runtime is within
// positionOverrideTolerance. The stored number never drops an answer from the
// pool -- it may come from an earlier bad match.
func TestSearchFanoutProbe_StoredSequenceGatesStrength(t *testing.T) {
	const title, author = "Overlord", "Kugane Maruyama"
	seq := 8
	book := func() *database.Book { b := probeBook(title, 36000); b.SeriesSequence = &seq; return b }

	first := metadata.BookMetadata{Title: "Overlord", Author: author, Series: "Overlord", SeriesPosition: "1", DurationSec: 37500, CoverURL: "c"}
	src := serving(first)
	resp, err := fanoutHarness(t, book(), src).SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Greater(t, src.callCount(), 1, "book 1 is never strong for stored sequence 8")
	assert.Contains(t, resultTitles(resp), "Overlord", "the stored sequence does not drop it from the pool")

	eighth := first
	eighth.SeriesPosition = "8"
	src = serving(eighth)
	_, err = fanoutHarness(t, book(), src).SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, src.callCount(), "the stored sequence's own number is still strong")

	numbered := metadata.BookMetadata{Title: "Overlord 8", Author: author, DurationSec: 37500, CoverURL: "c"}
	src = serving(numbered)
	_, err = fanoutHarness(t, book(), src).SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, src.callCount(), "a title carrying the stored number fits it")
}

// N1: a series_position naming a range or a list ("8-10", "8, 9, 10") is an
// omnibus, not book 8: it names no single position, so it neither satisfies
// nor refutes one. seqnum.ParsePosition reads "8-10" as 10; normPosition read
// it as 8.
func TestNormPosition_RangeIsNoSinglePosition(t *testing.T) {
	for _, in := range []string{"8-10", "8–10", "8, 9, 10", "8 & 9", "8/9", "Books 8 - 10"} {
		assert.Empty(t, normPosition(in), "%q", in)
	}
	for in, want := range map[string]string{"8": "8", "08": "8", "8.5": "8.5", "Book 8": "8", "#8": "8"} {
		assert.Equal(t, want, normPosition(in), "%q", in)
	}

	const title, asin = "Rogue Ascension 8", "B0BOXSET01"
	box := metadata.BookMetadata{Title: "Rogue Ascension Box Set", Author: raAuthor, SeriesPosition: "8-10",
		DurationSec: 36300, CoverURL: "c", ASIN: asin}
	p := parseSearchTitle(title, raAuthor, "")
	c := newStrongCriteria(p, p.Title, title, asin, raAuthor, 36000)
	assert.False(t, c.positionNamed(box))
	assert.False(t, c.ownASINAgrees(box))
}

// N2: a decimal position ("2.5", a novella between books) is a series slot
// like any other.
func TestParseSearchTitle_DecimalPosition(t *testing.T) {
	p := parseSearchTitle("Stormlight Archive 2.5: Edgedancer", "Brandon Sanderson", "")
	assert.Equal(t, "2.5", p.Position)
	assert.Equal(t, "Edgedancer", p.Name)
	assert.Equal(t, "Stormlight Archive", p.Series)
	assert.Equal(t, "Edgedancer", p.Title)

	c := newStrongCriteria(p, p.Title, "Stormlight Archive 2.5: Edgedancer", "", "Brandon Sanderson", 0)
	assert.True(t, c.positionConflicts(metadata.BookMetadata{Title: "Stormlight Archive 3: Oathbringer"}))
	assert.False(t, c.positionConflicts(metadata.BookMetadata{Title: "Edgedancer", SeriesPosition: "2.5"}))
}

// Round-7 review, R1: the direct ASIN lookup's score floor. A score <= 0
// means the title gave no search words, so the stored ASIN is the only
// evidence; the floor is withheld only on positive sibling evidence, never on
// a mere lack of agreement (main's floor was unconditional).
func TestSearchFanoutProbe_DirectASINFloorNeedsSiblingEvidence(t *testing.T) {
	const asin = "B0OWNBOOK1"
	lookupScore := func(t *testing.T, seq int, dur int, answer metadata.BookMetadata) (float64, bool) {
		t.Helper()
		b := probeBook("", dur)
		b.ASIN = func() *string { s := asin; return &s }()
		if seq > 0 {
			b.SeriesSequence = &seq
		}
		svc := fanoutHarness(t, b, serving())
		svc.asinLookupOverride = func(_ context.Context, _, a string) (*metadata.BookMetadata, error) {
			if a == asin {
				r := answer
				return &r, nil
			}
			return nil, nil
		}
		resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "", "", "", SearchOptions{})
		require.NoError(t, err)
		for _, c := range resp.Results {
			if c.ASIN == asin {
				return c.Score, true
			}
		}
		return 0, false
	}
	own := metadata.BookMetadata{Title: "Some Great Book", ASIN: asin, DurationSec: 36000, CoverURL: "c"}

	t.Run("no runtime", func(t *testing.T) {
		score, ok := lookupScore(t, 0, 0, own)
		require.True(t, ok)
		assert.GreaterOrEqual(t, score, 1.0)
	})
	t.Run("exact runtime, stored sequence 1", func(t *testing.T) {
		score, ok := lookupScore(t, 1, 36000, own)
		require.True(t, ok)
		assert.GreaterOrEqual(t, score, 1.0)
	})
	t.Run("control: another explicit position gets no floor", func(t *testing.T) {
		sib := own
		sib.SeriesPosition, sib.DurationSec = "1", 30000
		score, ok := lookupScore(t, 8, 36000, sib)
		require.True(t, ok, "it stays a candidate: the stored sequence never drops one")
		assert.Less(t, score, 1.0, "a sibling the stored sequence refutes is not floored")
	})
}

// Round-7 review, R2: with a stored sequence, a runtime within
// positionOverrideTolerance names the book whatever a provider numbers it,
// and an answer with no series_position is judged by the title's own numbers.
func TestStrongCriteria_StoredSequenceRuntimeExcuse(t *testing.T) {
	const asin = "B0OWNBOOK2"
	criteria := func(title string, seq string) strongCriteria {
		p := parseSearchTitle(title, "Ann Author", "")
		p.StoredPosition = seq
		return newStrongCriteria(p, p.Title, title, asin, "Ann Author", 36000)
	}
	tower := criteria("The Tower of the Swallow", "4")
	assert.True(t, tower.ownASINAgrees(metadata.BookMetadata{Title: "The Tower of the Swallow", SeriesPosition: "6", DurationSec: 36100, ASIN: asin}))
	assert.False(t, tower.ownASINAgrees(metadata.BookMetadata{Title: "The Tower of the Swallow", SeriesPosition: "6", DurationSec: 40000, ASIN: asin}),
		"another explicit position outside 2% is still not this book")

	dune := criteria("Dune", "1")
	assert.True(t, dune.ownASINAgrees(metadata.BookMetadata{Title: "Dune", DurationSec: 36100, ASIN: asin}))
	assert.True(t, dune.positionNamed(metadata.BookMetadata{Title: "Dune", DurationSec: 40000}),
		"no series_position names no other position")
}

// Round-7 review, NIT: the stored-sequence branch of explicitPositionConflicts
// excuses a title that carries the book's numbers, as the no-position branch
// does.
func TestStrongCriteria_StoredSequenceCarriesNumbers(t *testing.T) {
	p := parseSearchTitle("Overlord", "Ann Author", "")
	p.StoredPosition = "8"
	c := newStrongCriteria(p, p.Title, "Overlord", "", "Ann Author", 36000)
	assert.False(t, c.explicitPositionConflicts(metadata.BookMetadata{Title: "Overlord 8", SeriesPosition: "1", DurationSec: 40000}))
	assert.True(t, c.explicitPositionConflicts(metadata.BookMetadata{Title: "Overlord", SeriesPosition: "1", DurationSec: 40000}))

	p = parseSearchTitle("Metro 2033", "Ann Author", "")
	p.StoredPosition = "1"
	c = newStrongCriteria(p, p.Title, "Metro 2033", "", "Ann Author", 36000)
	assert.False(t, c.explicitPositionConflicts(metadata.BookMetadata{Title: "Metro 2033", SeriesPosition: "3", DurationSec: 40000}))
	assert.True(t, c.explicitPositionConflicts(metadata.BookMetadata{Title: "Metro 2034", SeriesPosition: "3", DurationSec: 40000}))
}
