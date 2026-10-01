// file: internal/metafetch/search_fanout_round5_probe_test.go
// version: 1.1.0
// guid: 0b6f2a8e-4c1d-4e7a-9f35-8d2c61e0a4b7
// last-edited: 2026-10-01

package metafetch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3641 round 5 (re-review of 2a4e95749). Every case below was a strong
// match, an ASIN-first rank or an applyable candidate on the wrong book.

const raAuthor = "Hunter Mythos"

// The position formats a title writes book 8 in. Each must parse to 8: with
// no position every position gate is off.
var raBookTitles = []string{
	"Rogue Ascension 8", "Rogue Ascension #8", "Rogue Ascension 08", "Rogue Ascension VIII",
	"Rogue Ascension Eight", "Rogue Ascension, Book Eight", "Rogue Ascension, Book 8",
	"Rogue Ascension 8: A Progression LitRPG",
}

func TestParseSearchTitle_PositionFormats(t *testing.T) {
	for _, tc := range []struct {
		title, pos string
		bare       bool
	}{
		{"Rogue Ascension 8", "8", true},
		{"Rogue Ascension #8", "8", true},
		{"Rogue Ascension 08", "8", true},
		{"Rogue Ascension 8.5", "8.5", true},
		{"Rogue Ascension VIII", "8", true},
		{"Rogue Ascension Eight", "8", true},
		{"Rogue Ascension, Book Eight", "8", false},
		{"Rogue Ascension, Part II", "2", false},
		{"Fahrenheit 451", "451", true},
		{"Apollo 13", "13", true},
		// A digit slot is never displaced by a written number after it.
		{"Hemlock Hollow 8: Book One", "8", false},
		// Not positions.
		{"Who Am I", "", false},
		{"Malcolm X", "", false},
		{"Seven Years in Tibet", "", false},
		{"1984", "", false},
		{"Metro 2033", "", false},
		{"The Book Club", "", false},
	} {
		p := parseSearchTitle(tc.title, raAuthor, "")
		c := newStrongCriteria(p, p.Title, tc.title, "", raAuthor, 0)
		assert.Equal(t, tc.pos, c.position, "%q position", tc.title)
		assert.Equal(t, tc.bare, p.BareSlot, "%q bare", tc.title)
	}
	// "Book Eight" takes the digit slot's path, series and all.
	p := parseSearchTitle("Rogue Ascension, Book Eight", raAuthor, "")
	assert.Equal(t, "Rogue Ascension", p.Series)
	assert.True(t, p.TitleIsSeries)
	// A bare number records no series: no query variant changes.
	p = parseSearchTitle("Rogue Ascension 8", raAuthor, "")
	assert.Empty(t, p.Series)
	assert.Equal(t, "Rogue Ascension 8", p.Title)
}

func TestTitleNumbers_WrittenNumbers(t *testing.T) {
	for title, want := range map[string][]string{
		"Rogue Ascension VIII":        {"8"},
		"Rogue Ascension: Book Seven": {"7"},
		"Rogue Ascension Seven":       {"7"},
		"Part II: The Return":         {"2"},
		"Hemlock Hollow 8: Book One":  {"8", "1"},
		"Seven Years in Tibet":        nil,
		"Who Am I":                    nil,
		"Book I":                      {"1"},
		"The Book Club":               nil,
	} {
		assert.Equal(t, want, titleNumbers(title), "%q", title)
	}
}

// R4-1, own ASIN: the stored ASIN is book 7's. Whatever format book 8's title
// uses, the sibling is never ranked first, never strong, never a result.
func TestSearchFanoutProbe_SiblingOwnASINEveryFormat(t *testing.T) {
	for _, title := range raBookTitles {
		t.Run(title, func(t *testing.T) {
			sibASIN := "B00SIBLNG7"
			sib := metadata.BookMetadata{Title: "Rogue Ascension 7", Author: raAuthor, Series: "Rogue Ascension",
				SeriesPosition: "7", DurationSec: 38000, CoverURL: "c", ASIN: sibASIN}
			right := metadata.BookMetadata{Title: "Rogue Ascension 8", Author: raAuthor, Series: "Rogue Ascension",
				SeriesPosition: "8", DurationSec: 36100, CoverURL: "c", ASIN: "B00RIGHTB8"}
			b := probeBook(title, 36000)
			b.ASIN = &sibASIN
			resp := probeSearch(t, b, raAuthor, serving(sib, right))
			require.NotEmpty(t, resp.Results)
			assert.Equal(t, "B00RIGHTB8", resp.Results[0].ASIN)
			assert.NotContains(t, resultTitles(resp), sib.Title)
		})
	}
}

// R4-1, author path: book 7 titled only by the series, explicit position 7,
// 5.5% off the runtime. Not strong (the search goes on) and not pooled.
func TestSearchFanoutProbe_SeriesTitledSiblingEveryFormat(t *testing.T) {
	sib := metadata.BookMetadata{Title: "Rogue Ascension", Author: raAuthor, Series: "Rogue Ascension",
		SeriesPosition: "7", DurationSec: 38000, CoverURL: "c"}
	for _, title := range raBookTitles {
		t.Run(title, func(t *testing.T) {
			src := serving(sib)
			svc := fanoutHarness(t, probeBook(title, 36000), src)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", raAuthor, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Greater(t, src.callCount(), 1, "book 7 is never strong for book 8")
			assert.Empty(t, resp.Results)
		})
	}
}

// R4-1, a correctly parsed book 8 against a sibling whose title writes its
// number as a numeral or a word and that has no explicit position. It carries
// the book's (wrong) stored ASIN: not ranked first, not strong, not pooled.
func TestSearchFanoutProbe_WrittenNumberSiblingOwnASIN(t *testing.T) {
	for _, sibTitle := range []string{"Rogue Ascension VII", "Rogue Ascension: Book Seven", "Rogue Ascension Seven"} {
		t.Run(sibTitle, func(t *testing.T) {
			sibASIN := "B00SIBLNG7"
			sib := metadata.BookMetadata{Title: sibTitle, Author: raAuthor, Series: "Rogue Ascension",
				DurationSec: 38000, CoverURL: "c", ASIN: sibASIN}
			b := probeBook("Rogue Ascension, Book 8", 36000)
			b.ASIN = &sibASIN
			src := serving(sib)
			svc := fanoutHarness(t, b, src)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", raAuthor, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Greater(t, src.callCount(), 1)
			assert.Empty(t, resp.Results)
		})
	}
}

// R4-1, the direct own-ASIN lookup (service_search.go): the store answers the
// book's stored ASIN with book 7, in every way a title can say 7. It is
// dropped; book 8 from the title search is the only result.
func TestSearchFanoutProbe_DirectLookupSiblingEveryFormat(t *testing.T) {
	siblings := []metadata.BookMetadata{
		{Title: "Rogue Ascension 7"},
		{Title: "Rogue Ascension #7"},
		{Title: "Rogue Ascension VII"},
		{Title: "Rogue Ascension: Book Seven"},
		{Title: "Rogue Ascension", SeriesPosition: "7"},
	}
	for _, title := range raBookTitles {
		for _, s := range siblings {
			t.Run(title+" / "+s.Title+" "+s.SeriesPosition, func(t *testing.T) {
				sibASIN := "B00SIBLNG7"
				far := s
				far.Author, far.Series, far.DurationSec, far.CoverURL, far.ASIN = raAuthor, "Rogue Ascension", 50000, "c", sibASIN
				right := metadata.BookMetadata{Title: "Rogue Ascension 8", Author: raAuthor, Series: "Rogue Ascension",
					SeriesPosition: "8", DurationSec: 36100, CoverURL: "c", ASIN: "B00RIGHTB8"}
				b := probeBook(title, 36000)
				b.ASIN = &sibASIN
				svc := fanoutHarness(t, b, serving(right))
				svc.asinLookupOverride = func(_ context.Context, _, asin string) (*metadata.BookMetadata, error) {
					if asin == sibASIN {
						r := far
						return &r, nil
					}
					return nil, nil
				}
				resp, err := svc.SearchMetadataForBookWithOptions("b1", "", raAuthor, "", "", SearchOptions{})
				require.NoError(t, err)
				assert.Equal(t, []string{"Rogue Ascension 8"}, resultTitles(resp))
			})
		}
	}
}

// R4-1 fix (2): a title with numbers but no parsed position ("Metro 2033":
// four digits are never a slot). A provider's explicit series_position that
// is none of the title's numbers is not strong unless the runtime is within
// 2%; it stays in the pool.
func TestSearchFanoutProbe_ExplicitPositionOutsideTheTitlesNumbers(t *testing.T) {
	const title, author = "Metro 2033", "Dmitry Glukhovsky"
	other := metadata.BookMetadata{Title: title, Author: author, Series: "Metro", SeriesPosition: "2", DurationSec: 34000, CoverURL: "c"}
	src := serving(other)
	svc := fanoutHarness(t, probeBook(title, 36000), src)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Greater(t, src.callCount(), 1, "series_position 2 is not strong for a title numbered 2033")
	assert.Equal(t, []string{title}, resultTitles(resp), "kept: the provider may number it differently")

	same := other
	same.SeriesPosition = ""
	src = serving(same)
	svc = fanoutHarness(t, probeBook(title, 36000), src)
	_, err = svc.SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, src.callCount(), "without the other position it is strong")

	// The own-ASIN rule asks the same.
	p := parseSearchTitle(title, author, "")
	c := newStrongCriteria(p, p.Title, title, "B0METRO001", author, 36000)
	other.ASIN = "B0METRO001"
	assert.False(t, c.ownASINAgrees(other))
	other.DurationSec = 36300
	assert.True(t, c.ownASINAgrees(other), "within 2%: the provider's numbering differs")
}

// R4-1 fix (1): an own-ASIN answer whose title carries a number the book's
// does not is not this book, whatever its runtime.
func TestStrongCriteria_OwnASINNeedsTheNumbersToFit(t *testing.T) {
	const title = "Hyperion"
	p := parseSearchTitle(title, "Dan Simmons", "")
	c := newStrongCriteria(p, p.Title, title, "B0HYPERION", "Dan Simmons", 72000)
	r := metadata.BookMetadata{Title: "Hyperion 2", Author: "Dan Simmons", ASIN: "B0HYPERION", DurationSec: 71000}
	assert.False(t, c.ownASINAgrees(r))
	r.Title = "Hyperion"
	assert.True(t, c.ownASINAgrees(r))
}

// A bare number may be the name's own: "Fahrenheit 451" with a provider's
// series_position 1 is still Fahrenheit 451, so it is kept and, inside the
// runtime tolerance, strong.
func TestSearchFanoutProbe_BareNumberInTheNameIsNotAPosition(t *testing.T) {
	const title, author = "Fahrenheit 451", "Ray Bradbury"
	right := metadata.BookMetadata{Title: title, Author: author, SeriesPosition: "1", DurationSec: 35000, CoverURL: "c"}
	src := serving(right)
	svc := fanoutHarness(t, probeBook(title, 36000), src)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "Some Reader", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{title}, resultTitles(resp))
	assert.Equal(t, 1, src.callCount())
}

// R4-2 and R4-3: the person leg. Each pair was strong; none names the same
// person. A narrator hint gives the fan-out a second round, so "not strong"
// shows as Audible asked again.
func TestSearchFanoutProbe_PersonNamesRound5(t *testing.T) {
	for _, tc := range []struct {
		title, book, answer string
		strong              bool
	}{
		{"Gone", "Michael Grant, Jr.", "Michael Connelly, Jr.", false},
		{"Gone", "Grant, Michael", "Connelly, Michael", false},
		{"Gone", "Michael Grant, editor", "Michael Connelly, editor", false},
		{"Gone", "Michael Grant, PhD", "Michael Connelly, Ph.D.", false},
		{"Duma Key", "Stephen King", "Owen King", false},
		// The same person, written differently.
		{"Gone", "Grant, Michael", "Michael Grant", true},
		{"Gone", "Michael Grant, Jr.", "Michael Grant", true},
		{"The Hobbit", "J.R.R. Tolkien", "John Ronald Reuel Tolkien", true},
		{"The Hobbit", "Tolkien", "J. R. R. Tolkien", true},
	} {
		t.Run(tc.book+" vs "+tc.answer, func(t *testing.T) {
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

func TestPersonNames_Round5(t *testing.T) {
	for in, want := range map[string][][]string{
		"Michael Grant, Jr.":        {{"michael", "grant"}},
		"Grant, Michael":            {{"michael", "grant"}},
		"Lee Child, Jeff Harding":   {{"lee", "child"}, {"jeff", "harding"}},
		"Michael Grant, editor":     {{"michael", "grant"}},
		"Michael Connelly, Ph.D.":   {{"michael", "connelly"}},
		"Various Authors":           nil,
		"Full Cast; Lee Child":      {{"lee", "child"}},
		"Child, Lee; Harding, Jeff": {{"lee", "child"}, {"jeff", "harding"}},
	} {
		assert.Equal(t, want, personNames(in), "%q", in)
	}
}

// R4-2: a placeholder credit names nobody. The search itself drops one as an
// author hint (SearchAuthorHint), so this is the person match the accept
// paths and the strong check run on answers' credits.
func TestSharesPerson_PlaceholdersNameNobody(t *testing.T) {
	for _, name := range []string{"Various Authors", "Various", "Full Cast", "Anonymous", "Unknown Author"} {
		assert.False(t, sharesPerson(name, name), "%q", name)
	}
	p := parseSearchTitle("Gone", "", "")
	c := newStrongCriteria(p, p.Title, "Gone", "", "Various Authors", 0)
	assert.False(t, c.matches(metadata.BookMetadata{Title: "Gone", Author: "Various Authors"}))
}

// R4-4(c): a row an earlier search version wrote is filtered on read however
// it was asked -- a user-typed query's legacy fingerprint, which the book's
// title never reproduces, and a row with no fingerprint at all -- and a row
// this version wrote (fingerprintPrefix) is returned as stored.
func TestGetCachedCandidates_EveryOldRowIsFiltered(t *testing.T) {
	f := newVerdictFixture(t)
	const title = "Rogue Ascension 8"
	b, err := f.store.CreateBook(&database.Book{Title: title, FilePath: "/lib/ra/book.m4b"})
	require.NoError(t, err)
	book := f.book(b.ID)
	typed := f.mfs.resolveSearchInputs(book, "Rogue Ascension book eight", "", "")
	own := f.mfs.resolveSearchInputs(book, book.Title, "", "")
	cand := func(title, pos string) json.RawMessage {
		raw, merr := json.Marshal(MetadataCandidate{Title: title, Author: raAuthor, Series: "Rogue Ascension", SeriesPosition: pos})
		require.NoError(t, merr)
		return raw
	}
	sib, right := cand("Rogue Ascension 7", "7"), cand(title, "8")
	for name, fp := range map[string]string{
		"typed-query legacy": typed.legacyFingerprint(book.Title),
		"no fingerprint":     "",
		"own legacy":         own.legacyFingerprint(book.Title),
	} {
		require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: b.ID, FetchedAt: time.Now().UTC(), SearchFingerprint: fp, Candidates: []json.RawMessage{sib, right},
		}))
		entry, _, gerr := f.mfs.GetCachedCandidates(b.ID)
		require.NoError(t, gerr)
		assert.Equal(t, []json.RawMessage{right}, entry.Candidates, name)
	}
	cur := own.fingerprint(book.Title)
	assert.True(t, isCurrentFingerprint(cur))
	assert.False(t, isCurrentFingerprint(own.legacyFingerprint(book.Title)))
	require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: time.Now().UTC(), SearchFingerprint: cur, Candidates: []json.RawMessage{sib, right},
	}))
	entry, _, err := f.mfs.GetCachedCandidates(b.ID)
	require.NoError(t, err)
	assert.Len(t, entry.Candidates, 2, "this version's own row is not re-filtered")
}

// R4-4(b): what GetCachedCandidates costs per row. A row this version wrote
// is the cache read alone; an older row adds the book read, the author
// links, input resolution and the candidate decode.
func BenchmarkGetCachedCandidates(b *testing.B) {
	store, err := database.NewPebbleStore(b.TempDir())
	require.NoError(b, err)
	require.NoError(b, database.RunMigrations(store))
	b.Cleanup(func() { _ = store.Close() })
	mfs := NewService(store)
	bk, err := store.CreateBook(&database.Book{Title: "Rogue Ascension 8", FilePath: "/lib/ra/book.m4b"})
	require.NoError(b, err)
	in := mfs.resolveSearchInputs(bk, bk.Title, "", "")
	var cands []json.RawMessage
	for i := 0; i < 10; i++ {
		raw, merr := json.Marshal(MetadataCandidate{Title: "Rogue Ascension 8", Author: raAuthor, Series: "Rogue Ascension", SeriesPosition: "8"})
		require.NoError(b, merr)
		cands = append(cands, raw)
	}
	for name, fp := range map[string]string{"current": in.fingerprint(bk.Title), "legacy": in.legacyFingerprint(bk.Title)} {
		b.Run(name, func(b *testing.B) {
			require.NoError(b, store.PutMetadataCache(&database.MetadataCandidateCache{
				BookID: bk.ID, FetchedAt: time.Now().UTC(), SearchFingerprint: fp, Candidates: cands,
			}))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, gerr := mfs.GetCachedCandidates(bk.ID); gerr != nil {
					b.Fatal(gerr)
				}
			}
		})
	}
}

// Recall: a position written as a numeral or a word is the same position as
// the digit, in either direction. The book "Rogue Ascension VIII" is the
// provider's "Rogue Ascension 8", and the book "Rogue Ascension 8" is the
// provider's "Rogue Ascension Eight". A written number is checked as a number
// (numbersFit), never demanded as a title word (titleAgrees) nor counted as an
// extra one (titleSubset), so the answer is strong in round 1.
func TestSearchFanoutProbe_WrittenNumberIsTheSamePosition(t *testing.T) {
	for _, tc := range []struct{ book, answer string }{
		{"Rogue Ascension VIII", "Rogue Ascension 8"},
		{"Rogue Ascension Eight", "Rogue Ascension 8"},
		{"Rogue Ascension 8", "Rogue Ascension Eight"},
		{"Rogue Ascension 8", "Rogue Ascension VIII"},
		{"Rogue Ascension, Book 8", "Rogue Ascension, Book Eight"},
	} {
		t.Run(tc.book+" vs "+tc.answer, func(t *testing.T) {
			right := metadata.BookMetadata{Title: tc.answer, Author: raAuthor, Series: "Rogue Ascension",
				SeriesPosition: "8", DurationSec: 36100, CoverURL: "c"}
			src := serving(right)
			svc := fanoutHarness(t, probeBook(tc.book, 36000), src)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", raAuthor, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Equal(t, []string{tc.answer}, resultTitles(resp))
			assert.Equal(t, 1, src.callCount(), "the same position written another way is strong")
		})
	}
}

// The position gates of ownASINAgrees and matches, where numbersFit and the explicit-
// position rule cannot see the sibling: every number in its title is one of
// the book's own (the 7 is the name's), and it gives no series_position. Only
// the series stated beside 7 (positionConflicts) says it is book 7. The pool
// pass drops it before the fan-out ever asks ownASINAgrees, so it is pinned
// here.
func TestStrongCriteria_OwnASINNeedsNoOtherPosition(t *testing.T) {
	const raw = "Rogue Ascension 8: The 7 Gates"
	p := parseSearchTitle(raw, raAuthor, "")
	c := newStrongCriteria(p, p.Title, raw, "B00SIBLNG7", raAuthor, 36000)
	sib := metadata.BookMetadata{Title: "Rogue Ascension 7: The 7 Gates", Author: raAuthor, ASIN: "B00SIBLNG7", DurationSec: 38000}
	require.True(t, c.numbersFit(sib.Title))
	require.False(t, c.explicitPositionConflicts(sib))
	assert.False(t, c.ownASINAgrees(sib))
	// The author path's position gate (matches), for the same reason.
	require.True(t, c.titleSubset(sib))
	assert.False(t, c.matches(sib))
	right := metadata.BookMetadata{Title: raw, Author: raAuthor, ASIN: "B00SIBLNG7", DurationSec: 36500}
	assert.True(t, c.ownASINAgrees(right))
	assert.True(t, c.matches(right))
}

// R4-4(c), carry-over: an empty refetch carries the previous row's candidates
// forward, and a row this version did not write is filtered first whatever
// its fingerprint -- one with no fingerprint, or one stamped by other inputs
// -- not only one matching this search's legacy fingerprint. What survives
// the filter passed this version's rules, so the row takes this search's
// stamp (only an exact legacy match keeps the legacy stamp, for the quota).
func TestCacheSearchResponse_CarryFiltersEveryOldRow(t *testing.T) {
	f := newVerdictFixture(t)
	const title = "Rogue Ascension 8"
	b, err := f.store.CreateBook(&database.Book{Title: title, FilePath: "/lib/ra/book.m4b"})
	require.NoError(t, err)
	book := f.book(b.ID)
	own := f.mfs.resolveSearchInputs(book, book.Title, "", "")
	typed := f.mfs.resolveSearchInputs(book, "Rogue Ascension book eight", "", "")
	legacy, current := own.legacyFingerprint(book.Title), own.fingerprint(book.Title)
	cand := func(title, pos string) json.RawMessage {
		raw, merr := json.Marshal(MetadataCandidate{Title: title, Author: raAuthor, Series: "Rogue Ascension", SeriesPosition: pos})
		require.NoError(t, merr)
		return raw
	}
	sib, right := cand("Rogue Ascension 7", "7"), cand(title, "8")
	p := parseSearchTitle(title, raAuthor, "")
	c := newStrongCriteria(p, p.Title, title, "", raAuthor, 0)
	for name, fp := range map[string]string{
		"no fingerprint":   "",
		"other inputs' fp": typed.legacyFingerprint(book.Title),
	} {
		require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: b.ID, FetchedAt: time.Now().UTC(), SourceHash: hashSearchInputs(b.ID, book.Title, "", "", ""),
			SearchFingerprint: fp, Candidates: []json.RawMessage{sib, right},
		}))
		got := f.mfs.cacheSearchResponse(b.ID, book.Title, "", "", "", &SearchMetadataResponse{
			InputFingerprint: current, LegacyFingerprint: legacy, SourcesAnswered: []string{"A"}, carryFilter: c.filterCarried,
		})
		assert.Equal(t, []json.RawMessage{right}, got.Candidates, name)
		assert.Equal(t, current, got.SearchFingerprint, name)
	}
}
