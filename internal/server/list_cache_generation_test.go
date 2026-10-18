// file: internal/server/list_cache_generation_test.go
// version: 1.1.0
// guid: 51004630-5b56-463f-851b-8225e2367353
// last-edited: 2026-10-18

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listTitles issues a real GET against the production router and returns the
// titles in the response, plus the raw body.
//
// It goes through server.router rather than calling the handler directly so
// the cache read/write, the store decorator chain, and the generation
// resolution are all exercised exactly as they are in production. A handler
// constructed by hand in the test would resolve the generation from a bare
// store and could pass while the wrapped production path stayed broken.
func listTitles(t *testing.T, server *Server, rawQuery string) ([]string, string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?"+rawQuery, nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "list request failed: %s", w.Body.String())

	var wrapper struct {
		Data struct {
			Items []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))

	titles := make([]string, 0, len(wrapper.Data.Items))
	for _, item := range wrapper.Data.Items {
		titles = append(titles, item.Title)
	}
	return titles, w.Body.String()
}

// TestListCacheDropsDeletedBookOnNextRequest is the regression test for the
// production defect: the owner merges books, the merge hard-deletes the
// losers, and the library page keeps listing them for up to 24 hours.
//
// Measured on production before the fix, on the UI's own default list key:
// the cached response returned 40,957 books where the identical cache-busted
// query returned 40,839 — a drift of 118 rows. Three of the merged-away books
// were still listed while returning HTTP 404 on fetch:
// 01KQAQEJ7HGX9YJC94WMYZG954, 01KQAQEEWWQWXHGCB9KRR013H5,
// 01KQAQEHG337RJ2HEHN9PAA61W.
//
// The delete here goes through the same store call the merge path uses
// (DeleteBook), and both list requests go through the production router, so
// the two response caches involved (the HTTP list cache and the audiobook
// service's own list cache underneath it) are both exercised.
//
// One book is deleted, not all of them: a test that empties the library would
// pass even if the cache were keyed on nothing at all, because "no books" is
// indistinguishable from a correct empty page.
func TestListCacheDropsDeletedBookOnNextRequest(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()

	keep1 := createTestBook(t, "Kept Book One")
	victim := createTestBook(t, "Merged Away Book")
	keep2 := createTestBook(t, "Kept Book Two")

	const rawQuery = "limit=50&offset=0"

	// First request populates both caches and must actually contain the
	// victim — otherwise the assertion after the delete proves nothing.
	before, beforeBody := listTitles(t, server, rawQuery)
	require.Contains(t, before, victim.Title,
		"victim must be listed before deletion, else the test is vacuous: %s", beforeBody)
	require.Contains(t, before, keep1.Title)
	require.Contains(t, before, keep2.Title)

	// Second identical request: confirm the cache is genuinely serving this
	// query. If it were not, the post-delete assertion below would pass even
	// with the fix reverted, and the test would be worthless as a regression
	// guard.
	_, cachedBody := listTitles(t, server, rawQuery)
	require.Equal(t, beforeBody, cachedBody, "expected the second request to be served from cache")

	require.NoError(t, store.DeleteBook(victim.ID))

	after, afterBody := listTitles(t, server, rawQuery)
	assert.NotContains(t, after, victim.Title,
		"deleted book is still being served from the list cache: %s", afterBody)
	assert.Contains(t, after, keep1.Title, "unrelated book vanished: %s", afterBody)
	assert.Contains(t, after, keep2.Title, "unrelated book vanished: %s", afterBody)
}

// TestListCacheDropsDemotedPrimaryOnNextRequest covers the other half of the
// production symptom. Not every phantom row is a deleted book: when a merge
// elects a different winner the loser is demoted to is_primary_version=false
// rather than removed, and the default library view (is_primary_version=true)
// kept showing it from cache.
//
// This is the case that would still be broken if the generation counter had
// been hooked to MarkAllQuickQueriesDirty, because UpdateBook does not call
// it — it marks three targeted quick queries instead.
func TestListCacheDropsDemotedPrimaryOnNextRequest(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()

	winner := createTestBook(t, "Version Winner")
	loser := createTestBook(t, "Version Loser")

	const rawQuery = "limit=50&offset=0&is_primary_version=true"

	before, beforeBody := listTitles(t, server, rawQuery)
	require.Contains(t, before, loser.Title,
		"loser must be listed as primary before demotion: %s", beforeBody)
	require.Contains(t, before, winner.Title)

	// Demote exactly as a merge does. UpdateBook is a full-column replacement,
	// so the whole book is written back with only the flag changed.
	demoted := *loser
	isPrimary := false
	demoted.IsPrimaryVersion = &isPrimary
	_, err := store.UpdateBook(loser.ID, &demoted)
	require.NoError(t, err)

	after, afterBody := listTitles(t, server, rawQuery)
	assert.NotContains(t, after, loser.Title,
		"demoted version loser is still served from the list cache: %s", afterBody)
	assert.Contains(t, after, winner.Title, "winner vanished: %s", afterBody)
}

// TestListWarmerKeyMatchesRequestKeyAndRetiresOnMutation covers the warmer
// half of the bug.
//
// The trickle warmer skips any query it finds already cached. With an
// un-scoped key that made it structurally incapable of refreshing a stale
// entry: it would find the phantom-row response, conclude the query was warm,
// and skip it on every pass forever.
//
// Two properties are asserted:
//
//  1. The key the warmer computes for a query is the SAME key a real request
//     computes for it. If these ever drift the warmer writes entries no
//     request can read — a silent cache-miss regression that no amount of
//     "the tests pass" would surface.
//  2. After a book mutation the warmer's key no longer resolves, so its
//     skip-if-cached branch misses and the query gets rebuilt.
//
// This exercises the key computation the skip branch uses. It does NOT run
// the trickle goroutine itself, so it does not prove the surrounding loop
// behaves; it proves the branch's input can no longer be the stale entry.
func TestListWarmerKeyMatchesRequestKeyAndRetiresOnMutation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()

	createTestBook(t, "Warmer Book One")
	victim := createTestBook(t, "Warmer Victim")

	// The warmer builds its raw query with buildListCacheRawQuery; drive the
	// HTTP request from that same string so any divergence in key layout
	// shows up as a cache miss below.
	raw := buildListCacheRawQuery(50, 0, audiobookspkg.ListFilters{})
	listTitles(t, server, raw)

	warmerKey := server.libraryGeneration().Key("list:", raw)
	_, hit := server.listCache.Get(warmerKey)
	require.True(t, hit,
		"warmer key %q does not resolve to the entry the request path wrote — "+
			"the warmer and the handler disagree on the cache key", warmerKey)

	require.NoError(t, store.DeleteBook(victim.ID))

	staleKey := warmerKey
	freshKey := server.libraryGeneration().Key("list:", raw)
	assert.NotEqual(t, staleKey, freshKey,
		"generation did not advance on delete, so the warmer would keep skipping")

	_, hitAfter := server.listCache.Get(freshKey)
	assert.False(t, hitAfter,
		"warmer would still skip this query as already-cached after a delete")
}

// TestGenerationKeyChangesWithBump is the unit-level guard on the key
// mechanism itself: the same inputs must produce a different key after a
// bump, and a stable one without.
func TestGenerationKeyChangesWithBump(t *testing.T) {
	var gen cache.Generation

	first := gen.Key("list:", "limit=50")
	assert.Equal(t, first, gen.Key("list:", "limit=50"), "key must be stable without a bump")

	gen.Bump()
	assert.NotEqual(t, first, gen.Key("list:", "limit=50"), "key must change after a bump")

	// A nil Generation must still produce a usable, stable key rather than
	// panicking, because that is the fallback path for stores with no counter.
	var nilGen *cache.Generation
	assert.Equal(t, nilGen.Key("list:", "x"), nilGen.Key("list:", "x"))
}
