// file: internal/catalog/match_test.go
// version: 1.0.0
// guid: 5e8a1c37-9d40-4b62-a7f3-0c2d9b6e4815
// last-edited: 2026-10-12

package catalog

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Every fixture is synthetic: "Title 000104", "Author 07", "Series 05".

const (
	testProvider = metadata.SourceIDAudible
	testMarket   = "us"
)

func synthProduct(asin, title, author, authorASIN string) metadata.CatalogProduct {
	return metadata.CatalogProduct{ASIN: asin, Title: title, Language: "english",
		Authors: []metadata.CatalogContributor{{Name: author, ASIN: authorASIN}}}
}

// seedProducts stores products as catalog entries under harvestKey, as a
// harvest would (without the network), and returns the entry ids.
func seedProducts(t *testing.T, st *database.CatalogStore, harvestKey string, ps ...metadata.CatalogProduct) []string {
	t.Helper()
	items := make([]database.CatalogUpsert, len(ps))
	for i, p := range ps {
		items[i] = database.CatalogUpsert{Entry: BuildEntry(p, testProvider, testMarket)}
	}
	res, err := st.UpsertEntries(items, harvestKey)
	require.NoError(t, err)
	return res.IDs
}

// harvestSynthetic runs a real harvest of one author against a counting
// scripted lister (no network), so the catalog under test is built by the
// production write path (entries, indexes and aliases).
func harvestSynthetic(t *testing.T, st *database.CatalogStore, author string, ps []metadata.CatalogProduct) *scriptLister {
	t.Helper()
	l := &scriptLister{list: func(_ string, page, size int) metadata.AuthorPage { return slicePage(ps, page, size) }}
	h := NewHarvester(l, st, Settings{Language: "english", PageSize: 50, Concurrency: 1, OpID: "op-test"})
	state, err := h.HarvestAuthor(context.Background(), ScopeAuthor{Key: HarvestKey(author), Name: author}, nil)
	require.NoError(t, err)
	require.Equal(t, database.CatalogHarvestComplete, state.State)
	return l
}

func titlesOf(r MatchResult) []string {
	out := make([]string, len(r.Candidates))
	for i, c := range r.Candidates {
		out[i] = c.Title
	}
	return out
}

func block(author, authorASIN string, n int, first int) []metadata.CatalogProduct {
	ps := make([]metadata.CatalogProduct, n)
	for i := range ps {
		ps[i] = synthProduct(fmt.Sprintf("B0%s%05d", authorASIN, first+i), fmt.Sprintf("Title %06d", first+i), author, authorASIN)
	}
	return ps
}

// A book whose stored title is series-decorated and which the providers
// cannot answer is found in the harvested catalog of its author with no
// provider call at all. The catalog is built through a real harvest with a
// counting lister; the counter is reset before the match and read after.
func TestMatchBook_SeriesDecoratedTitleZeroProviderCalls(t *testing.T) {
	st, _ := openCatalog(t)
	ps := block("Author 07", "AUTH07", 12, 101)
	ps[3].Series = []metadata.CatalogProductSeries{{Title: "Series 05", Sequence: "3"}}
	l := harvestSynthetic(t, st, "Author 07", ps)
	require.Greater(t, l.pages.Load(), int64(0), "fixture: the harvest itself asks the lister")
	l.pages.Store(0)
	l.lookups.Store(0)

	res, err := MatchBook(st, BookQuestion{
		Titles:   []string{"Series 05, Book 3 - Title 000104", "Title 000104"}, // stored (decorated) and parsed
		Credits:  []string{"Author 07"},
		Series:   "Series 05",
		Position: "3",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 000104"}, titlesOf(res), "the right book only: 000101..000112 differ in their digits")
	assert.Equal(t, metadata.CatalogStageAuthor, res.Stage)
	require.Len(t, res.Origins, 1)
	assert.Equal(t, 0, res.Origins[0].CreditPosition)
	calls := l.pages.Load() + l.lookups.Load()
	t.Logf("provider calls for %d catalog candidate(s): %d", len(res.Candidates), calls)
	assert.Zero(t, calls)
}

// The same name under two provider author ids is two people. Without an id
// local evidence can choose with, the block produces nothing and is counted.
func TestMatchBook_HomonymAuthorNoCrossMatch(t *testing.T) {
	st, _ := openCatalog(t)
	seedProducts(t, st, "author07",
		synthProduct("B0AAA00001", "Title 000201", "Author 07", "AUTHAAA"),
		synthProduct("B0BBB00001", "Title 000202", "Author 07", "AUTHBBB"))
	q := BookQuestion{Titles: []string{"Title 000201"}, Credits: []string{"Author 07"}}

	res, err := MatchBook(st, q)
	require.NoError(t, err)
	assert.Empty(t, res.Candidates, "two author ids, nothing to choose with: no cross-match")
	assert.Equal(t, 1, res.HomonymSkipped)

	q.AuthorASINs = []string{"authaaa"} // the book's author is the first person (case is folded)
	res, err = MatchBook(st, q)
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 000201"}, titlesOf(res))
	assert.Zero(t, res.HomonymSkipped)

	q.AuthorASINs = []string{"AUTHBBB"} // the other person: the block holds no such title
	res, err = MatchBook(st, q)
	require.NoError(t, err)
	assert.Empty(t, res.Candidates)
	assert.Zero(t, res.HomonymSkipped)

	// No ASIN on the question: the harvest's own resolution for the name
	// (CatalogAuthorState, written offline by disambig.go) chooses when it
	// found exactly one author id and saw no conflict.
	q.AuthorASINs = nil
	require.NoError(t, st.PutAuthorState(&database.CatalogAuthorState{Key: "author07", Name: "Author 07", AuthorASINs: []string{"AUTHAAA"}}))
	res, err = MatchBook(st, q)
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 000201"}, titlesOf(res))

	require.NoError(t, st.PutAuthorState(&database.CatalogAuthorState{Key: "author07", Name: "Author 07", AuthorASINs: []string{"AUTHAAA", "AUTHBBB"}, Conflict: true}))
	res, err = MatchBook(st, q)
	require.NoError(t, err)
	assert.Empty(t, res.Candidates, "the library itself holds both people under this name: not a pick")
	assert.Equal(t, 1, res.HomonymSkipped)
}

// Credits are tried in their given order and a candidate names the position
// of the credit whose block found it.
func TestMatchBook_SecondPositionCreditFindsBlock(t *testing.T) {
	st, _ := openCatalog(t)
	seedProducts(t, st, "author08", block("Author 08", "AUTH08", 3, 401)...)
	res, err := MatchBook(st, BookQuestion{
		Titles:  []string{"Title 000402"},
		Credits: []string{"Author 99", "Author 08"}, // the first credit has no block
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 000402"}, titlesOf(res))
	require.Len(t, res.Origins, 1)
	assert.Equal(t, 1, res.Origins[0].CreditPosition)
}

// A credit written differently from the provider's spelling finds its block
// through an alias the harvest wrote: dropped middle name, "Last, First".
// An alias two authors claim names neither.
func TestMatchBook_AliasFindsBlock(t *testing.T) {
	st, _ := openCatalog(t)
	harvestSynthetic(t, st, "Jane Q. Author", block("Jane Q. Author", "AUTHJQA", 4, 501))

	for _, credit := range []string{"Jane Q. Author", "Jane Author", "Author, Jane Q.", "J. Q. Author", "J Author"} {
		res, err := MatchBook(st, BookQuestion{Titles: []string{"Title 000503"}, Credits: []string{credit}})
		require.NoError(t, err, credit)
		assert.Equal(t, []string{"Title 000503"}, titlesOf(res), credit)
	}

	harvestSynthetic(t, st, "Jane R. Author", block("Jane R. Author", "AUTHJRA", 4, 601))
	for _, credit := range []string{"Jane Author", "J Author"} {
		res, err := MatchBook(st, BookQuestion{Titles: []string{"Title 000503", "Title 000603"}, Credits: []string{credit}})
		require.NoError(t, err, credit)
		assert.Empty(t, res.Candidates, "%q is claimed by two authors: it names neither", credit)
	}
	// Their own spellings still hit their own blocks.
	res, err := MatchBook(st, BookQuestion{Titles: []string{"Title 000603"}, Credits: []string{"Jane R. Author"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 000603"}, titlesOf(res))
}

// A block over MaxBlockEntries is skipped and counted, never scored; one at
// the cap is scored.
func TestMatchBook_OversizeBlockSkippedAndCounted(t *testing.T) {
	st, _ := openCatalog(t)
	seedProducts(t, st, "author09", block("Author 09", "AUTH09", MaxBlockEntries+1, 1)...)
	seedProducts(t, st, "author10", block("Author 10", "AUTH10", MaxBlockEntries, 1)...)

	res, err := MatchBook(st, BookQuestion{Titles: []string{"Title 000007"}, Credits: []string{"Author 09", "Author 10"}})
	require.NoError(t, err)
	assert.Equal(t, 1, res.OversizeBlocks)
	require.Len(t, res.Candidates, 1, "the block at the cap is scored")
	assert.Equal(t, 1, res.Origins[0].CreditPosition)
}

// Bulk matching reads exact keys only: a credit that is a substring of a
// stored author fold ("phelps" in "josephphelps") finds nothing, where
// EntryIDsByAuthorName's fallback walk (which scans every author key) would.
// The database package pins that the exact-key reads never report a scan;
// here the observable consequence is pinned.
func TestMatchBook_NeverUsesSubstringWalk(t *testing.T) {
	st, _ := openCatalog(t)
	seedProducts(t, st, "josephphelps", synthProduct("B0PHELPS01", "Title 000301", "Joseph Phelps", "AUTHJP"))

	for _, credit := range []string{"Phelps", "Joseph", "Joseph Phelp", "Joseph Phelpss"} {
		res, err := MatchBook(st, BookQuestion{Titles: []string{"Title 000301"}, Credits: []string{credit}})
		require.NoError(t, err, credit)
		assert.Empty(t, res.Candidates, credit)
	}
	ids, err := st.EntryIDsByAuthorName("phelps", 0)
	require.NoError(t, err)
	require.Len(t, ids, 1, "control: the substring walk does find the entry")

	res, err := MatchBook(st, BookQuestion{Titles: []string{"Title 000301"}, Credits: []string{"Joseph Phelps"}})
	require.NoError(t, err)
	assert.Len(t, res.Candidates, 1, "control: the exact credit does")
}

// An entry no harvest lists any more (stale_since set) is not a candidate,
// neither by block nor by ASIN.
func TestMatchBook_StaleEntriesSkipped(t *testing.T) {
	st, _ := openCatalog(t)
	seedProducts(t, st, "author07", block("Author 07", "AUTH07", 3, 701)...)
	q := BookQuestion{Titles: []string{"Title 000702"}, Credits: []string{"Author 07"},
		ASIN: "B0AUTH0700702", Provider: testProvider, Marketplace: testMarket}
	res, err := MatchBook(st, q)
	require.NoError(t, err)
	require.Equal(t, metadata.CatalogStageIdentifier, res.Stage, "fixture: live, the ASIN hits")

	stale, err := st.MarkUnseen("author07", map[string]bool{})
	require.NoError(t, err)
	require.Equal(t, 3, stale)
	res, err = MatchBook(st, q)
	require.NoError(t, err)
	assert.Empty(t, res.Candidates)
	assert.Empty(t, res.Stage)
}

// B1 is a point read by the book's own ASIN and yields exactly one candidate;
// a miss falls through to B2.
func TestMatchBook_B1ASINPointRead(t *testing.T) {
	st, _ := openCatalog(t)
	seedProducts(t, st, "author07", block("Author 07", "AUTH07", 3, 801)...)

	res, err := MatchBook(st, BookQuestion{ASIN: "B0AUTH0700802", Provider: testProvider, Marketplace: testMarket})
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 000802"}, titlesOf(res), "no title and no credit needed: the ASIN alone")
	assert.Equal(t, metadata.CatalogStageIdentifier, res.Stage)
	require.Len(t, res.Origins, 1)
	assert.Equal(t, metadata.CatalogStageIdentifier, res.Origins[0].Stage)

	// Another marketplace, or an unknown ASIN, misses; B2 then answers.
	for _, q := range []BookQuestion{
		{ASIN: "B0AUTH0700802", Provider: testProvider, Marketplace: "uk"},
		{ASIN: "B0UNKNOWN", Provider: testProvider, Marketplace: testMarket},
	} {
		q.Titles, q.Credits = []string{"Title 000803"}, []string{"Author 07"}
		res, err = MatchBook(st, q)
		require.NoError(t, err)
		assert.Equal(t, metadata.CatalogStageAuthor, res.Stage)
		assert.Equal(t, []string{"Title 000803"}, titlesOf(res))
	}
}

// The floor: a block with no similar title yields nothing, not its five
// nearest; digits are part of a title; an edit within the cap still matches.
func TestMatchBook_TitleFloor(t *testing.T) {
	st, _ := openCatalog(t)
	ps := block("Author 07", "AUTH07", 8, 901)
	ps = append(ps, synthProduct("B0LONG0001", "The Synthetic Chronicle of the Long Winter", "Author 07", "AUTH07"))
	ps[1].Series = []metadata.CatalogProductSeries{{Title: "Series 05", Sequence: "3"}}
	seedProducts(t, st, "author07", ps...)
	match := func(q BookQuestion) MatchResult {
		q.Credits = []string{"Author 07"}
		res, err := MatchBook(st, q)
		require.NoError(t, err)
		return res
	}

	assert.Empty(t, match(BookQuestion{Titles: []string{"Completely Different Words Here"}}).Candidates)
	assert.Empty(t, match(BookQuestion{Titles: []string{"Title 000999"}}).Candidates, "digits differ from every entry")
	assert.Equal(t, []string{"The Synthetic Chronicle of the Long Winter"},
		titlesOf(match(BookQuestion{Titles: []string{"The Synthetic Cronicle of the Long Winter"}})), "one edit on a 36-rune title")
	assert.Empty(t, match(BookQuestion{Titles: []string{"The Synthetic Cronicle of the Lonng Winnter xyzzy"}}).Candidates, "past the edit cap")
	assert.Empty(t, match(BookQuestion{Titles: []string{"It"}}).Candidates, "a title that short must match exactly")

	// The same series at another position is another book, even on an exact title.
	assert.Equal(t, []string{"Title 000902"}, titlesOf(match(BookQuestion{Titles: []string{"Title 000902"}, Series: "Series 05", Position: "3"})))
	assert.Empty(t, match(BookQuestion{Titles: []string{"Title 000902"}, Series: "Series 05", Position: "4"}).Candidates)
}

// The write-through: a real harvest files an alias row for each spelling
// variant of every credited author, pointing at the credit's own block.
func TestAliasWriteThrough_OnHarvest(t *testing.T) {
	st, _ := openCatalog(t)
	harvestSynthetic(t, st, "Jane Q. Author", block("Jane Q. Author", "AUTHJQA", 2, 1001))
	for alias, want := range map[string]string{
		"janeauthor":      "janeqauthor",
		"authorjaneq":     "janeqauthor",
		"authorjane":      "janeqauthor",
		"jqauthor":        "janeqauthor",
		"jauthor":         "janeqauthor",
		"janeqauthor":     "", // its own fold: the block itself, no alias
		"janequinnauthor": "", // never filed
	} {
		got, ok, err := st.ResolveAuthorAlias(alias)
		require.NoError(t, err)
		if want == "" {
			assert.False(t, ok, alias)
			continue
		}
		assert.True(t, ok, alias)
		assert.Equal(t, want, got, alias)
	}
}

func TestAuthorAliasFolds(t *testing.T) {
	assert.ElementsMatch(t, []string{"janeauthor", "authorjaneq", "authorjane", "jqauthor", "jauthor"},
		AuthorAliasFolds("Jane Q. Author"), "the variants of a three-word name, never its own fold")
	assert.Contains(t, AuthorAliasFolds("Author, Jane"), "janeauthor", "Last, First reads as First Last")
	assert.Contains(t, AuthorAliasFolds("Jane Doe Jr."), "janedoe", "a generational suffix is dropped")
	assert.Nil(t, AuthorAliasFolds("Cher"), "one word has nothing to vary")
	assert.Empty(t, AuthorAliasFolds("Al Li"), "variants under five letters are not filed")
}
