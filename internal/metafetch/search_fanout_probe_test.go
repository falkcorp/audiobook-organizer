// file: internal/metafetch/search_fanout_probe_test.go
// version: 1.2.0
// guid: 8bda3876-c00e-45e0-aae3-0f7e50356f5e
// last-edited: 2026-10-01

package metafetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	for _, tc := range []struct {
		name     string
		err      error
		answered bool
	}{
		{"JSON 404", &metadata.ProviderStatusError{Provider: metadata.SourceIDAudible, Status: 404, Body: `{"error":"not found"}`}, true},
		{"empty product", metadata.ErrASINNotFound, true},
		// S5: a proxy's or an edge's HTML 404 never reached the provider.
		{"HTML 404", &metadata.ProviderStatusError{Provider: metadata.SourceIDAudible, Status: 404, Body: "<html>404 Not Found</html>"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			own := "B000NOSUCH"
			book := reacherBook(0)
			book.ASIN = &own
			svc := fanoutHarness(t, book, &fakeProvider{id: metadata.SourceIDAudible, name: "Audible"})
			svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
				return nil, tc.err
			}
			resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
			require.NoError(t, err)
			if tc.answered {
				assert.Empty(t, resp.SourcesFailed)
				assert.Contains(t, resp.SourcesAnswered, "Audible")
				assert.NoError(t, noSourceAnswered(resp))
			} else {
				assert.Contains(t, resp.SourcesFailed, "Audible")
				assert.NotContains(t, resp.SourcesAnswered, "Audible")
			}
		})
	}
}

// S5: Audnexus is asked the English stores first, and only when all three
// say "not here" the rest -- so the narrowing loses no recall against the
// full region list.
func TestLookupASIN_AudnexusFallsBackToTheOtherRegions(t *testing.T) {
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Query().Get("region") == "de" && r.URL.Path == "/books/B000GERMAN" {
			_, _ = w.Write([]byte(`{"asin":"B000GERMAN","title":"Das Buch"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404}`))
	}))
	defer server.Close()
	prev := config.AppConfig.MetadataSources
	t.Cleanup(func() { config.AppConfig.MetadataSources = prev })
	config.AppConfig.MetadataSources = []config.MetadataSource{{ID: metadata.SourceIDAudnexus, Enabled: true, BaseURL: server.URL}}
	metadata.DefaultThrottleRegistry().RecordSuccess(metadata.SourceIDAudnexus, time.Now())

	svc := NewService(&database.MockStore{})
	got, err := svc.lookupASIN(context.Background(), nil, metadata.SourceIDAudnexus, "B000GERMAN")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Das Buch", got.Title)
	assert.EqualValues(t, 6, asked.Load(), `"", uk, au, ca, in, then de`)

	asked.Store(0)
	got, err = svc.lookupASIN(context.Background(), nil, metadata.SourceIDAudnexus, "B000NOWHERE")
	require.NoError(t, err, "every region said not here: an answer, not a failure")
	assert.Nil(t, got)
	assert.EqualValues(t, maxAudnexusRequests, asked.Load(), "every region asked once")
}

// B1: a stored ASIN that is a SIBLING's ("Rogue Ascension 7" stored on book
// 8, an earlier bad match) is not ranked first, not strong, and not exempt
// from the position drop.
func TestSearchFanoutProbe_SiblingOwnASIN(t *testing.T) {
	const title, author = "Rogue Ascension 8: A Progression LitRPG", "Hunter Mythos"
	sibASIN := "B00SIBLNG7"
	sib := metadata.BookMetadata{Title: "Rogue Ascension 7: A Progression LitRPG", Author: author, Series: "Rogue Ascension",
		SeriesPosition: "7", DurationSec: 50000, CoverURL: "c", ASIN: sibASIN}
	right := metadata.BookMetadata{Title: title, Author: author, Series: "Rogue Ascension", SeriesPosition: "8",
		DurationSec: 36100, CoverURL: "c", ASIN: "B00RIGHTB8"}
	book := func() *database.Book {
		b := probeBook(title, 36000)
		b.ASIN = &sibASIN
		return b
	}

	src := serving(sib, right)
	resp := probeSearch(t, book(), author, src)
	require.NotEmpty(t, resp.Results)
	assert.Equal(t, "B00RIGHTB8", resp.Results[0].ASIN)
	assert.NotContains(t, resultTitles(resp), sib.Title)

	src = serving(sib)
	resp = probeSearch(t, book(), author, src)
	assert.Empty(t, resp.Results)
	assert.Greater(t, src.callCount(), 1)

	// A sibling running within 15% of the book agrees on runtime, which
	// siblings often do: carrying the book's ASIN does not excuse its other
	// position, so it is neither re-added to the pool nor strong.
	near := sib
	near.DurationSec = 38000
	src = serving(near)
	resp = probeSearch(t, book(), author, src)
	assert.Empty(t, resp.Results)
	assert.Greater(t, src.callCount(), 1)

	// The direct lookup of the stored ASIN answers with the sibling the store
	// has under it. It is dropped like any pooled sibling: never a candidate,
	// never ranked first by the ASIN tier.
	src = serving(right)
	svc := fanoutHarness(t, book(), src)
	svc.asinLookupOverride = func(_ context.Context, _, asin string) (*metadata.BookMetadata, error) {
		if asin == sibASIN {
			r := near
			return &r, nil
		}
		return nil, nil
	}
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", author, "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{title}, resultTitles(resp))
}

// B2: with a runtime of our own, a runtime within 15% is not enough: a
// sequel, a companion or an abridgement runs close to the book. Its title
// must say nothing the book's does not (titleSubset). The book's own answer
// is still strong. A narrator hint gives the fan-out a second round, so a
// strong answer shows as Audible asked once.
func TestSearchFanoutProbe_RuntimeNeedsTheTitleToo(t *testing.T) {
	for _, tc := range []struct{ title, author, other string }{
		{"Hyperion", "Dan Simmons", "The Fall of Hyperion"},
		{"Foundation", "Isaac Asimov", "Foundation and Empire"},
		{"Artemis Fowl", "Eoin Colfer", "Artemis Fowl: The Arctic Incident"},
		{"The Expanse", "James S. A. Corey", "The Expanse: Caliban's War"},
		{"Shogun", "James Clavell", "Shogun (Abridged)"},
		// "2" is too short for SignificantWords: only numbersFit sees it.
		{"Hyperion", "Dan Simmons", "Hyperion 2"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			other := metadata.BookMetadata{Title: tc.other, Author: tc.author, DurationSec: 41000, CoverURL: "c"}
			right := metadata.BookMetadata{Title: tc.title, Author: tc.author, DurationSec: 36200, CoverURL: "c"}

			src := serving(other)
			svc := fanoutHarness(t, probeBook(tc.title, 36000), src)
			_, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Greater(t, src.callCount(), 1, "%q is never strong for %q", tc.other, tc.title)

			src = serving(other, right)
			svc = fanoutHarness(t, probeBook(tc.title, 36000), src)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, "Some Reader", "", SearchOptions{})
			require.NoError(t, err)
			assert.Equal(t, 1, src.callCount(), "the book itself is strong")
			require.NotEmpty(t, resp.Results)
			assert.Equal(t, tc.title, resp.Results[0].Title)
		})
	}
}

// B3: the book's name keeps a sibling with another explicit position in the
// pool (the provider may number differently), but never makes it strong
// unless its runtime is within positionOverrideTolerance.
func TestSearchFanoutProbe_ExplicitOtherPositionIsNotStrong(t *testing.T) {
	const title, author = "Hemlock Hollow 8: A Hemlock Whodunit", "Ivy Quill"
	sib := metadata.BookMetadata{Title: "A Hemlock Whodunit", Author: author, Series: "Hemlock Hollow", SeriesPosition: "7",
		DurationSec: 39600, CoverURL: "c"}
	src := serving(sib)
	probeSearch(t, probeBook(title, 36000), author, src)
	assert.Greater(t, src.callCount(), 1, "#7 at 10% off is not strong for 8")

	exact := sib
	exact.DurationSec = 36300
	src = serving(exact)
	probeSearch(t, probeBook(title, 36000), author, src)
	assert.Equal(t, 1, src.callCount(), "#7 at 0.8% off: the provider numbers it differently")
}

// B4: the person leg is the same AUTHOR -- full name or surname -- not a
// shared word, and never the narrator alone.
func TestSearchFanoutProbe_PersonIsTheAuthor(t *testing.T) {
	for _, tc := range []struct {
		name, title, author, narrator string
		other                         metadata.BookMetadata
	}{
		{"shared first name", "Gone", "Michael Grant", "Some Reader",
			metadata.BookMetadata{Title: "Gone", Author: "Michael Connelly", DurationSec: 36000, CoverURL: "c"}},
		{"shared first name 2", "The Witness", "Nora Roberts", "Some Reader",
			metadata.BookMetadata{Title: "The Witness", Author: "Nora Ephron", DurationSec: 36000, CoverURL: "c"}},
		{"narrator only", "The Stand", "Stephen King", "Grover Gardner",
			metadata.BookMetadata{Title: "The Stand", Author: "Ken Follett", Narrator: "Grover Gardner", DurationSec: 36000, CoverURL: "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := serving(tc.other)
			svc := fanoutHarness(t, probeBook(tc.title, 36000), src)
			_, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, tc.narrator, "", SearchOptions{})
			require.NoError(t, err)
			assert.Greater(t, src.callCount(), 1, "%s's %q is never strong for %s's", tc.other.Author, tc.title, tc.author)

			right := metadata.BookMetadata{Title: tc.title, Author: tc.author, DurationSec: 36000, CoverURL: "c"}
			src = serving(tc.other, right)
			svc = fanoutHarness(t, probeBook(tc.title, 36000), src)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", tc.author, tc.narrator, "", SearchOptions{})
			require.NoError(t, err)
			assert.Equal(t, 1, src.callCount())
			require.NotEmpty(t, resp.Results)
			assert.Equal(t, tc.author, resp.Results[0].Author)
		})
	}
}

func TestSharesPerson(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"Lee Child", "Lee Child", true},
		{"Lee Child, Jeff Harding", "Jeff Harding", true},
		{"L. Child", "Lee Child", true},
		{"James S. A. Corey", "James S.A. Corey", true},
		{"Michael Connelly", "Michael Grant", false},
		{"Nora Ephron", "Nora Roberts", false},
		{"Martin Luther King Jr.", "Martin Luther King", true},
		{"Jack Reacher", "", false},
	} {
		assert.Equal(t, tc.want, sharesPerson(tc.a, tc.b), "%q vs %q", tc.a, tc.b)
	}
}

// S6: candidates the version "1" ladder cached are filtered by this
// version's position rules wherever they are read or carried, so a sibling
// the old ladder pooled is never applyable -- without refetching anything.
func TestLegacyCandidatesAreFiltered(t *testing.T) {
	f := newVerdictFixture(t)
	const title = "Rogue Ascension 8: A Progression LitRPG"
	b, err := f.store.CreateBook(&database.Book{Title: title, FilePath: "/lib/ra/book.m4b"})
	require.NoError(t, err)
	book := f.book(b.ID)
	in := f.mfs.resolveSearchInputs(book, book.Title, "", "")
	legacy, current := in.legacyFingerprint(book.Title), in.fingerprint(book.Title)
	cand := func(title, pos string) json.RawMessage {
		raw, merr := json.Marshal(MetadataCandidate{Title: title, Author: "Hunter Mythos", Series: "Rogue Ascension", SeriesPosition: pos})
		require.NoError(t, merr)
		return raw
	}
	sib, right := cand("Rogue Ascension 7: A Progression LitRPG", "7"), cand(title, "8")
	put := func(fp string, cands ...json.RawMessage) {
		require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: b.ID, FetchedAt: time.Now().UTC(), SourceHash: hashSearchInputs(b.ID, book.Title, "", "", ""),
			SearchFingerprint: fp, Candidates: cands,
		}))
	}

	// Read for apply.
	put(legacy, sib, right)
	entry, _, err := f.mfs.GetCachedCandidates(b.ID)
	require.NoError(t, err)
	assert.Equal(t, []json.RawMessage{right}, entry.Candidates)
	stored, err := f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	assert.Len(t, stored.Candidates, 2, "the stored row is not rewritten")
	// A current-version row is this version's own answer: not re-filtered.
	put(current, sib, right)
	entry, _, err = f.mfs.GetCachedCandidates(b.ID)
	require.NoError(t, err)
	assert.Len(t, entry.Candidates, 2)

	// The batch verdict: filtered, and re-asked only when nothing survives.
	put(legacy, sib, right)
	entry, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "")
	assert.Equal(t, BatchVerdictFreshCandidates, verdict)
	assert.Equal(t, []json.RawMessage{right}, entry.Candidates)
	put(legacy, sib)
	_, verdict, _ = f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, "")
	assert.Equal(t, BatchVerdictNone, verdict)

	// Carried over an empty refetch.
	put(legacy, sib, right)
	p := parseSearchTitle(title, "", "")
	c := newStrongCriteria(p, p.Title, title, "", "", 0)
	got := f.mfs.cacheSearchResponse(b.ID, book.Title, "", "", "", &SearchMetadataResponse{
		InputFingerprint: current, LegacyFingerprint: legacy, SourcesAnswered: []string{"A"}, carryFilter: c.filterCarried,
	})
	assert.Equal(t, []json.RawMessage{right}, got.Candidates)
	assert.Equal(t, legacy, got.SearchFingerprint)
	put(legacy, sib)
	got = f.mfs.cacheSearchResponse(b.ID, book.Title, "", "", "", &SearchMetadataResponse{
		InputFingerprint: current, LegacyFingerprint: legacy, SourcesAnswered: []string{"A"}, carryFilter: c.filterCarried,
	})
	assert.Empty(t, got.Candidates, "nothing worth carrying: the row is this search's empty answer")
	assert.Equal(t, current, got.SearchFingerprint)
}

// The position gates of matches and ownASINAgrees, asked directly. In the
// fan-out both only ever see answers the position rules already kept (the
// pool pass, and the own-ASIN lookup's own check), so no search can tell
// their gate from its absence; the gate in ownASINAgrees is pinned here.
// Book 7 at 2.7% of book 8's runtime agrees within strongRuntimeTolerance but
// not positionOverrideTolerance, and its title states the series beside 7.
func TestStrongCriteria_PositionConflictIsNeverStrong(t *testing.T) {
	const raw, author = "Rogue Ascension 8: A Progression LitRPG", "Hunter Mythos"
	p := parseSearchTitle(raw, author, "")
	c := newStrongCriteria(p, p.Title, raw, "B00SIBLNG7", author, 36000)
	sib := metadata.BookMetadata{Title: "Rogue Ascension 7: A Progression LitRPG", Author: author, DurationSec: 37000}
	assert.False(t, c.matches(sib))
	sib.ASIN = "B00SIBLNG7"
	assert.False(t, c.ownASINAgrees(sib), "the stored ASIN is a sibling's: a runtime within 15% does not excuse its position")
	sib.ASIN = ""
	sib.Title = "A Progression LitRPG"
	sib.SeriesPosition = "7"
	assert.False(t, c.matches(sib), "an explicit other position with no name of the book's to vouch")
	right := metadata.BookMetadata{Title: "Rogue Ascension 8: A Progression LitRPG", Author: author, SeriesPosition: "8", DurationSec: 36500}
	assert.True(t, c.matches(right))
}

// B1: numbers never vanish from a title comparison. "2" is too short to be
// a significant word, so word coverage alone never sees it.
//
// "Assertions 2" carries the book's own name: the name excuses another
// position only when the answer's title adds no number (positionConflicts).
//
// "The Way of Kings 2" answers the subtitle variant ("The Way of Kings") of a
// book with no position, so only that Exact variant's own number check
// (titleVariant.Nums) can drop it.
func TestSearchFanoutProbe_VariantAnswerKeepsItsNumbers(t *testing.T) {
	src := &answeringSource{name: "audible", answers: map[string][]metadata.BookMetadata{
		"Assertions": {{Title: "Assertions", Author: "Bern Dean"}, {Title: "Assertions 2", Author: "Bern Dean"}},
	}}
	svc := newVariantService(t, "Eternal Dominion, Book 04 - Assertions", src)
	resp, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "Bern Dean", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Assertions"}, resultTitles(resp))

	src = &answeringSource{name: "audible", answers: map[string][]metadata.BookMetadata{
		"The Way of Kings": {{Title: "The Way of Kings", Author: "Brandon Sanderson"},
			{Title: "The Way of Kings 2", Author: "Brandon Sanderson"}},
	}}
	svc = newVariantService(t, "The Way of Kings: Stormlight Archive", src)
	resp, err = svc.searchMetadataForBook(context.Background(), nil, "b1", "", "Brandon Sanderson", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"The Way of Kings"}, resultTitles(resp), "queries %v", src.queries)
}
