// file: internal/database/search_rank_test.go
// version: 1.1.0
// guid: 3f7c1e0a-9b2d-4e65-8a14-6d0c2b9e7f53
// last-edited: 2026-09-25

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubstringSearchRank_Tiers(t *testing.T) {
	authorID := 7
	authors := map[int]string{7: "roll writer"}
	narr := "Read by Roll"
	cases := []struct {
		name     string
		title    string
		narrator *string
		authorID *int
		wantTier int
		wantOK   bool
	}{
		{"exact", "Roll", nil, nil, SearchTierExactTitle, true},
		{"exact case-insensitive", "ROLL", nil, nil, SearchTierExactTitle, true},
		{"prefix", "Rolling Thunder", nil, nil, SearchTierTitlePrefix, true},
		{"whole word", "Rock and Roll", nil, nil, SearchTierTitleWord, true},
		{"whole word before punctuation", "Roll, Jordan, Roll", nil, nil, SearchTierTitlePrefix, true},
		{"word mid-title with punctuation", "Let's Roll!", nil, nil, SearchTierTitleWord, true},
		{"substring", "The Silas Kane Scrolls", nil, nil, SearchTierTitleSubstr, true},
		{"substring troll", "The Apocalypse Troll", nil, nil, SearchTierTitleSubstr, true},
		{"author only", "Something Else", nil, &authorID, SearchTierAuthor, true},
		{"narrator only", "Something Else", &narr, nil, SearchTierNarrator, true},
		{"no match", "Something Else", nil, nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := SubstringSearchRank("id", tc.title, tc.narrator, tc.authorID, authors, "roll")
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, SubstringSearchMatches(tc.title, tc.narrator, tc.authorID, authors, "roll"), ok,
				"rank and match predicates must agree on WHETHER a book matches")
			if ok {
				require.Equal(t, tc.wantTier, r.Tier)
			}
		})
	}
}

// TestSearchBooks_ExactTitleBeatsOlderSubstringMatches is the prod bug: q="Roll"
// returned the first 12 substring matches in ULID order ("...Scrolls",
// "...Troll") and never the book titled exactly "Roll", which was created
// after them. Every older decoy here matches, there are more of them than the
// limit, and the exact match is created LAST — the position an unranked
// early-exit scan can never reach. Both backends and both entry points
// (SearchBooks, SearchBooksFiltered) must rank it first and agree on the order.
func TestSearchBooks_ExactTitleBeatsOlderSubstringMatches(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p, ok := store.(*PebbleStore)
	require.True(t, ok)

	yes := true
	organized := "organized"
	mk := func(title string) string {
		t.Helper()
		b, err := store.CreateBook(&Book{Title: title, IsPrimaryVersion: &yes, LibraryState: &organized})
		require.NoError(t, err)
		return b.ID
	}
	const limit = 12
	for i := range limit + 8 {
		if i%2 == 0 {
			mk(fmt.Sprintf("The Silas Kane Scrolls %02d", i))
		} else {
			mk(fmt.Sprintf("The Apocalypse Troll %02d", i))
		}
	}
	wordHit := mk("Rock and Roll")
	prefixHit := mk("Rolling Thunder")
	exact := mk("Roll") // created LAST

	f := BookSummaryFilter{IsPrimaryVersion: &yes, LibraryState: organized, ExcludeQuarantined: true}
	results := map[string][]string{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		if useMemDB {
			require.NotNil(t, p.mem(), "memdb must be published, or both runs hit the Pebble path")
		}
		plain, err := p.SearchBooks("Roll", limit, 0)
		require.NoError(t, err)
		filtered, err := p.SearchBooksFiltered("Roll", limit, 0, f)
		require.NoError(t, err)

		for name, got := range map[string][]Book{"SearchBooks": plain, "SearchBooksFiltered": filtered} {
			ids := searchIDs(got)
			require.Len(t, ids, limit, "%s memdb=%v", name, useMemDB)
			require.Equal(t, []string{exact, prefixHit, wordHit}, ids[:3],
				"%s memdb=%v: exact, then prefix, then whole-word must lead", name, useMemDB)
			results[fmt.Sprintf("%s/%v", name, useMemDB)] = ids
		}

		// Offset paging over the ranked order is stable: page 2 continues page 1.
		all, err := p.SearchBooks("Roll", 0, 0)
		require.NoError(t, err)
		page2, err := p.SearchBooks("Roll", limit, limit)
		require.NoError(t, err)
		require.Equal(t, searchIDs(all)[limit:], searchIDs(page2), "memdb=%v page 2", useMemDB)
	}
	p.UseMemDB = true

	require.Equal(t, results["SearchBooks/false"], results["SearchBooks/true"],
		"memdb and pebble returned a different order for SearchBooks")
	require.Equal(t, results["SearchBooksFiltered/false"], results["SearchBooksFiltered/true"],
		"memdb and pebble returned a different order for SearchBooksFiltered")
}

// BenchmarkMemSearchBookIDs_OneChar measures the worst case ranking adds: a
// one-character query that matches nearly every book, so there is no early
// exit and every match is offered to the ranker.
func BenchmarkMemSearchBookIDs_OneChar(b *testing.B) {
	store, cleanup := setupPebbleTestDB(&testing.T{})
	defer cleanup()
	p := store.(*PebbleStore)
	const n = 20000
	for i := range n {
		if _, err := store.CreateBook(&Book{Title: fmt.Sprintf("Book Title Number %06d", i)}); err != nil {
			b.Fatal(err)
		}
	}
	m := p.mem()
	if m == nil {
		b.Skip("memdb not published")
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := m.SearchBookIDs("e", 12, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// TestSubstringSearch_UnderscoreReadsAsSpace: a title that came from a file
// name with underscores for spaces must match a query typed with spaces, in
// both the match predicate and the ranker, and a query typed with underscores
// must still match a spaced title.
func TestSubstringSearch_UnderscoreReadsAsSpace(t *testing.T) {
	title := "Arcane_Chef_2__A_LitRPG_Adventure"
	r, ok := SubstringSearchRank("b2", title, nil, nil, nil, "arcane chef")
	require.True(t, ok, "spaced query must match an underscored title")
	require.Equal(t, SearchTierTitlePrefix, r.Tier)
	require.True(t, SubstringSearchMatches(title, nil, nil, nil, "arcane chef"))

	require.True(t, SubstringSearchMatches("Arcane Chef 2", nil, nil, nil, "arcane_chef"))
	_, ok = SubstringSearchRank("b1", "Arcane Chef 2", nil, nil, nil, "arcane_chef")
	require.True(t, ok)

	narr := "Some_Reader"
	require.True(t, SubstringSearchMatches("x", &narr, nil, nil, "some reader"))
	_, ok = SubstringSearchRank("b3", "x", &narr, nil, nil, "some reader")
	require.True(t, ok)
}
