// file: internal/metafetch/search_fanout_probe_test.go
// version: 1.0.0
// guid: 8bda3876-c00e-45e0-aae3-0f7e50356f5e
// last-edited: 2026-10-01

package metafetch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The #3641 re-review probes: every one runs through the real fan-out, with a
// provider that returns BOTH the right book and its sibling, and one that
// returns ONLY the sibling. "Strong" (the fan-out stops) shows as Audible
// being asked exactly once.

func probeBook(title string, durationSec int) *database.Book {
	b := &database.Book{ID: "b1", Title: title}
	if durationSec > 0 {
		b.Duration = &durationSec
	}
	return b
}

// serving returns an Audible fake that answers every question with answers.
func serving(answers ...metadata.BookMetadata) *fakeProvider {
	return &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
		return answers
	}}
}

func probeSearch(t *testing.T, book *database.Book, author string, src *fakeProvider) *SearchMetadataResponse {
	t.Helper()
	svc := fanoutHarness(t, book, src)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "", "", SearchOptions{})
	require.NoError(t, err)
	return resp
}

func resultTitles(resp *SearchMetadataResponse) []string {
	var out []string
	for _, c := range resp.Results {
		out = append(out, c.Title)
	}
	return out
}

// B-1: a one-word series ("Witcher 4") still anchors the book's name, so the
// right book survives a provider that numbers it differently (#6), and an
// exact runtime makes it strong.
func TestSearchFanoutProbe_WitcherProviderNumbering(t *testing.T) {
	const title, author = "Witcher 4: The Tower of the Swallow", "Andrzej Sapkowski"
	tower := metadata.BookMetadata{Title: "The Tower of the Swallow", Author: author, Series: "The Witcher",
		SeriesPosition: "6", DurationSec: 50000, CoverURL: "c"}
	contempt := metadata.BookMetadata{Title: "Time of Contempt", Author: author, Series: "The Witcher",
		SeriesPosition: "4", DurationSec: 36000, CoverURL: "c"}

	t.Run("both, exact runtime", func(t *testing.T) {
		src := serving(contempt, tower)
		resp := probeSearch(t, probeBook(title, 50000), author, src)
		require.NotEmpty(t, resp.Results)
		assert.Equal(t, tower.Title, resp.Results[0].Title)
		assert.Equal(t, 1, src.callCount(), "a runtime-exact answer is strong")
	})
	t.Run("both, no runtime", func(t *testing.T) {
		src := serving(contempt, tower)
		resp := probeSearch(t, probeBook(title, 0), author, src)
		assert.Contains(t, resultTitles(resp), tower.Title, "the provider's #6 carries the book's name; it is not dropped")
		assert.Greater(t, src.callCount(), 1, "with no runtime, an answer the provider numbers #6 is never strong for 4")
	})
	t.Run("only the right book, no runtime", func(t *testing.T) {
		src := serving(tower)
		resp := probeSearch(t, probeBook(title, 0), author, src)
		assert.Equal(t, []string{tower.Title}, resultTitles(resp))
		assert.Greater(t, src.callCount(), 1)
	})
	t.Run("only the sibling, exact runtime", func(t *testing.T) {
		src := serving(contempt)
		probeSearch(t, probeBook(title, 50000), author, src)
		assert.Greater(t, src.callCount(), 1, "a sibling 28% off the runtime is never strong")
	})
}

// B-1, the runtime leg: the provider numbers the book #9 (it counts a
// prequel), its title says 8, and there is no book name past the tagline to
// vouch. Only a runtime within positionOverrideTolerance keeps it; a sibling
// that merely shares the series runtime band (8%) does not get that pass.
func TestSearchFanoutProbe_ExactRuntimeOutweighsProviderNumbering(t *testing.T) {
	const title, author = "Rogue Ascension 8: A Progression LitRPG", "Hunter Mythos"
	right := metadata.BookMetadata{Title: title, Author: author, Series: "Rogue Ascension", SeriesPosition: "9",
		DurationSec: 36100, CoverURL: "c"}
	sib := metadata.BookMetadata{Title: "A Progression LitRPG", Author: author, Series: "Rogue Ascension", SeriesPosition: "7",
		DurationSec: 39000, CoverURL: "c"}

	src := serving(sib, right)
	resp := probeSearch(t, probeBook(title, 36000), author, src)
	assert.Equal(t, []string{title}, resultTitles(resp))
	assert.Equal(t, 1, src.callCount(), "runtime-exact and the book's own title: strong")

	src = serving(sib)
	resp = probeSearch(t, probeBook(title, 36000), author, src)
	assert.Empty(t, resp.Results)
	assert.Greater(t, src.callCount(), 1)
}

// B-1, a second shape: Discworld's sub-series numbering.
func TestSearchFanoutProbe_DiscworldSubSeriesNumbering(t *testing.T) {
	const title, author = "Discworld 12: Witches Abroad", "Terry Pratchett"
	witches := metadata.BookMetadata{Title: "Witches Abroad", Author: author, Series: "Discworld - Witches",
		SeriesPosition: "3", DurationSec: 30000, CoverURL: "c"}
	reaper := metadata.BookMetadata{Title: "Reaper Man", Author: author, Series: "Discworld",
		SeriesPosition: "12", DurationSec: 28000, CoverURL: "c"}

	for _, dur := range []int{0, 30000} {
		src := serving(reaper, witches)
		resp := probeSearch(t, probeBook(title, dur), author, src)
		require.NotEmpty(t, resp.Results, "runtime %d", dur)
		assert.Equal(t, witches.Title, resp.Results[0].Title, "runtime %d", dur)
	}
	src := serving(reaper)
	probeSearch(t, probeBook(title, 0), author, src)
	assert.Greater(t, src.callCount(), 1, "a sibling without the book's name is never strong")
}

// B-2: taglines the genre vocabulary does not know. The sibling's title names
// the series beside another number, so it is another book, whatever "name"
// it shares; it is never pooled and never strong.
func TestSearchFanoutProbe_UnknownTaglines(t *testing.T) {
	const author = "Ivy Quill"
	for _, tagline := range []string{"A Cozy Mystery", "A LitRPG Saga", "The Saga", "A Love Story", "A Tragedy",
		"A Christmas Novella", "Book One", "Adventures"} {
		t.Run(tagline, func(t *testing.T) {
			title := "Hemlock Hollow 8: " + tagline
			right := metadata.BookMetadata{Title: title, Author: author, Series: "Hemlock Hollow", SeriesPosition: "8", CoverURL: "c"}
			sib := metadata.BookMetadata{Title: "Hemlock Hollow 7: " + tagline, Author: author, Series: "Hemlock Hollow",
				SeriesPosition: "7", CoverURL: "c"}

			src := serving(sib, right)
			resp := probeSearch(t, probeBook(title, 0), author, src)
			assert.Equal(t, []string{title}, resultTitles(resp))

			src = serving(sib)
			resp = probeSearch(t, probeBook(title, 0), author, src)
			assert.Empty(t, resp.Results, "book 7 is never pooled for book 8")
			assert.Greater(t, src.callCount(), 1, "book 7 is never strong for book 8")
		})
	}
}

// B-2, the pool check: two answers carrying the "name" at different
// positions show it to be the series' tagline, so it vouches for neither,
// and the one at another position is dropped -- even when its title is the
// bare tagline.
func TestSearchFanoutProbe_SharedNameIsATagline(t *testing.T) {
	const title, author = "Hemlock Hollow 8: A Cozy Mystery", "Ivy Quill"
	right := metadata.BookMetadata{Title: "A Cozy Mystery", Author: author, Series: "Hemlock Hollow", SeriesPosition: "8", CoverURL: "c"}
	sib := metadata.BookMetadata{Title: "A Cozy Mystery", Author: author + " ", Series: "Hemlock Hollow", SeriesPosition: "7", CoverURL: "c"}
	src := serving(sib, right)
	resp := probeSearch(t, probeBook(title, 0), author, src)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "8", resp.Results[0].SeriesPosition)
}

// S-3: with no runtime of our own, a box set or an omnibus carrying every
// word of the book's title is not strong (its title says more than the
// book's); the book itself is.
func TestSearchFanoutProbe_BoxSetIsNotStrong(t *testing.T) {
	const author = "Lee Child"
	box := metadata.BookMetadata{Title: "Jack Reacher, Books 17-19: A Wanted Man, Never Go Back, Personal", Author: author,
		Series: "Jack Reacher", SeriesPosition: "17", CoverURL: "c"}
	omnibus := metadata.BookMetadata{Title: "A Wanted Man / Never Go Back / Personal", Author: author, CoverURL: "c"}
	right := metadata.BookMetadata{Title: "A Wanted Man", Author: author, Narrator: "Jeff Harding", Series: "Jack Reacher",
		SeriesPosition: "17", CoverURL: "c"}

	src := serving(box, omnibus)
	probeSearch(t, reacherBook(0), author, src)
	assert.Greater(t, src.callCount(), 1, "a box set or omnibus never stops the fan-out")

	src = serving(box, omnibus, right)
	resp := probeSearch(t, reacherBook(0), author, src)
	assert.Equal(t, 1, src.callCount(), "the book itself is strong")
	require.NotEmpty(t, resp.Results)
	assert.Equal(t, right.Title, resp.Results[0].Title)
}

// S-1: the title as written is anchored on the parsed title's words, so a
// same-author "Frost Heart" answering "Magma Heart - Unknown Author" asked
// as written is never pooled.
func TestSearchFanoutProbe_LiteralVariantIsAnchored(t *testing.T) {
	const literal = "Magma Heart - Unknown Author"
	asked := false
	src := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, _ string) []metadata.BookMetadata {
		if title == literal {
			asked = true
			return []metadata.BookMetadata{{Title: "Frost Heart", Author: "Plum Parrot", CoverURL: "c"}}
		}
		return nil
	}}
	resp := probeSearch(t, probeBook(literal, 0), "Plum Parrot", src)
	require.True(t, asked, "the literal title is asked")
	assert.Empty(t, resp.Results)
}

// S-2: an ASIN no store has (a 404, an empty product) is the provider
// answering "no such book", not failing: the search is answered and the book
// is not re-asked forever.
func TestSearchFanoutProbe_NotFoundASINIsAnswered(t *testing.T) {
	for name, notFound := range map[string]error{
		"404":           &metadata.ProviderStatusError{Provider: metadata.SourceIDAudible, Status: 404},
		"empty product": metadata.ErrASINNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			own := "B000NOSUCH"
			book := reacherBook(0)
			book.ASIN = &own
			svc := fanoutHarness(t, book, &fakeProvider{id: metadata.SourceIDAudible, name: "Audible"})
			svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
				return nil, notFound
			}
			resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
			require.NoError(t, err)
			assert.Empty(t, resp.SourcesFailed)
			assert.Contains(t, resp.SourcesAnswered, "Audible")
			assert.NoError(t, noSourceAnswered(resp))
		})
	}
}

// matches' own position gate: the fan-out filters the pool before it asks
// matches, so this gate is tested directly. Book 7 at 2.7% of book 8's
// runtime agrees within strongRuntimeTolerance but not
// positionOverrideTolerance, and its title states the series beside 7.
func TestStrongCriteria_PositionConflictIsNeverStrong(t *testing.T) {
	const raw, author = "Rogue Ascension 8: A Progression LitRPG", "Hunter Mythos"
	p := parseSearchTitle(raw, author, "")
	c := newStrongCriteria(p, p.Title, raw, "", author, 36000)
	sib := metadata.BookMetadata{Title: "Rogue Ascension 7: A Progression LitRPG", Author: author, DurationSec: 37000}
	assert.False(t, c.matches(sib))
	sib.Title = "A Progression LitRPG"
	sib.SeriesPosition = "7"
	assert.False(t, c.matches(sib), "an explicit other position with no name of the book's to vouch")
	right := metadata.BookMetadata{Title: "Rogue Ascension 8: A Progression LitRPG", Author: author, SeriesPosition: "8", DurationSec: 36500}
	assert.True(t, c.matches(right))
}
