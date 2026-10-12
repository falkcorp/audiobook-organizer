// file: internal/server/list_cache_author_credit_test.go
// version: 1.0.0
// guid: 3f90f31c-6be1-47fc-a7e5-4ba09868ef03
// last-edited: 2026-10-12

package server

import (
	"net/url"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestListCacheSeesNewCoCredit: an author: filter matches every author-role
// credit, so adding a co-author changes which books an author: page lists.
// That page is served from the HTTP list cache, keyed on the library
// generation; a credit-only write must advance it, or the page keeps
// answering without the newly credited book until the TTL runs out.
//
// Both shapes are checked: the filter alone, which the HTTP list cache serves
// and which only the generation bump refreshes, and the filter with free text
// ("foo author:X"). Names and titles are synthetic.
func TestListCacheSeesNewCoCredit(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()

	lead, err := store.CreateAuthor("Synthauthor Lima")
	require.NoError(t, err)
	added, err := store.CreateAuthor("Synthauthor Kilo")
	require.NoError(t, err)
	solo, err := store.CreateBook(&database.Book{Title: "Synthetic Kilo Solo", FilePath: "/synthetic/kilo-solo.m4b", AuthorID: &added.ID})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(solo.ID, []database.BookAuthor{{AuthorID: added.ID, Role: "author", Position: 0}}))
	later, err := store.CreateBook(&database.Book{Title: "Synthetic Lima Later Pair", FilePath: "/synthetic/lima-later.m4b", AuthorID: &lead.ID})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(later.ID, []database.BookAuthor{{AuthorID: lead.ID, Role: "author", Position: 0}}))

	filterOnly := "limit=50&offset=0&filters=" + url.QueryEscape(`[{"field":"author","value":"Synthauthor Kilo"}]`)
	// The same filter with free text ("foo author:X").
	withSearch := filterOnly + "&search=Synthetic"
	queries := []string{filterOnly, withSearch}

	for _, rawQuery := range queries {
		before, beforeBody := listTitles(t, server, rawQuery)
		require.Equal(t, []string{solo.Title}, before, "fixture: only the solo book credits Kilo yet (%s): %s", rawQuery, beforeBody)
		// The identical request must give the same answer (for the
		// filter-only query, a list cache hit), or the assertion after the
		// write would pass with no cache involved at all.
		_, cachedBody := listTitles(t, server, rawQuery)
		require.Equal(t, beforeBody, cachedBody, "expected the second request to be served from cache (%s)", rawQuery)
	}

	require.NoError(t, store.SetBookAuthors(later.ID, []database.BookAuthor{
		{AuthorID: lead.ID, Role: "author", Position: 0},
		{AuthorID: added.ID, Role: "co-author", Position: 1},
	}))

	for _, rawQuery := range queries {
		after, afterBody := listTitles(t, server, rawQuery)
		require.ElementsMatch(t, []string{solo.Title, later.Title}, after,
			"a cached author: page must pick up a newly added co-credit (%s): %s", rawQuery, afterBody)
	}
}
