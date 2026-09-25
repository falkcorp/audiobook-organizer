// file: internal/server/handlers/abs/search_book_cache_test.go
// version: 1.1.0
// guid: 5e10f4fe-7e2d-4948-a417-4c262f7ac2f4
// last-edited: 2026-09-25

package abs_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

func withSearchResults(c *searchcache.Cache) harnessOpt {
	return func(o *abshandler.Options) { o.SearchResults = c }
}

// searchBooks returns the book hits as "id:title" in order, and their count.
// The expanded items also carry fixture-minted sync file IDs, which differ
// between two harnesses and say nothing about which books matched.
func searchBooks(t *testing.T, h *harness, tok, q string) (string, int) {
	t.Helper()
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/search?q=" + q,
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("search %q: status %d", q, code)
	}
	hits := body.(map[string]any)["book"].([]any)
	var out []string
	for _, hit := range hits {
		item := hit.(map[string]any)["libraryItem"].(map[string]any)
		md := item["media"].(map[string]any)["metadata"].(map[string]any)
		out = append(out, fmt.Sprint(item["id"], ":", md["title"]))
	}
	return strings.Join(out, "|"), len(hits)
}

// TestSearch_SharedResultCache: ABS book hits come from the shared cache, match
// the direct search byte for byte, and a book edit is visible to the next
// search (the document cache is stamped with the change generation) without
// waiting out the TTL.
func TestSearch_SharedResultCache(t *testing.T) {
	// Uncached reference over the SAME library: the fake breaks ties by its
	// insertion order, which differs between two seeds.
	seed := seedOracleLibrary(t)
	plain := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	plain.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	plainTok := str(t, userObj(t, plain.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	want, n := searchBooks(t, plain, plainTok, "odyssey")
	if n != 2 {
		t.Fatalf("reference search returned %d books, want 2", n)
	}

	changes := searchcache.NewChangeLog(0)
	cache := searchcache.New(changes, searchcache.Config{})
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withSearchResults(cache))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	got, _ := searchBooks(t, h, tok, "odyssey")
	if got != want {
		t.Fatalf("cached book hits differ from the direct search\n got: %s\nwant: %s", got, want)
	}
	scans := seed.lib.searchCalls()
	if again, _ := searchBooks(t, h, tok, "Odyssey"); again != want {
		t.Fatal("case-folded repeat differs")
	}
	if seed.lib.searchCalls() != scans {
		t.Fatalf("a case-folded repeat rescanned (%d -> %d)", scans, seed.lib.searchCalls())
	}

	// Rename one match away from the query and record it, as the store does.
	seed.lib.mu.Lock()
	seed.lib.books[seed.singleID].Title = "Something Else Entirely"
	seed.lib.mu.Unlock()
	changes.Record(seed.singleID)

	if _, n := searchBooks(t, h, tok, "odyssey"); n != 1 {
		t.Fatalf("after the rename the search returned %d books, want 1", n)
	}
	if st := cache.Stats(); st.Patches == 0 {
		t.Fatalf("the rename was not applied by a patch: %+v", st)
	}
	// Review findings 2/25: the patch ranked the changed book by point
	// lookups; neither Match nor the order comparator rescanned the library.
	if seed.lib.searchCalls() != scans {
		t.Fatalf("the patch ran full library scans (%d -> %d)", scans, seed.lib.searchCalls())
	}
}

// Review finding 19: one unreadable book row costs that hit only. The search
// still answers 200 with every other hit, and the partial answer is not pinned
// into the document cache.
func TestSearch_UnreadableRowIsSkipped(t *testing.T) {
	seed := seedOracleLibrary(t)
	changes := searchcache.NewChangeLog(0)
	cache := searchcache.New(changes, searchcache.Config{})
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withSearchResults(cache))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	seed.lib.mu.Lock()
	seed.lib.badRows = map[string]bool{seed.singleID: true}
	seed.lib.mu.Unlock()
	if _, n := searchBooks(t, h, tok, "odyssey"); n != 1 {
		t.Fatalf("with one unreadable match the search returned %d books, want 1", n)
	}
	// The row becomes readable again: the next search must see both hits, so
	// the partial list above was not cached as a complete document.
	seed.lib.mu.Lock()
	seed.lib.badRows = nil
	seed.lib.mu.Unlock()
	if _, n := searchBooks(t, h, tok, "odyssey"); n != 2 {
		t.Fatalf("after the row recovered the search returned %d books, want 2", n)
	}
}

// Review finding 19 (storage error): a failed hydration read degrades to an
// empty book section, never a 500.
func TestSearch_HydrationErrorIsNot500(t *testing.T) {
	seed := seedOracleLibrary(t)
	cache := searchcache.New(searchcache.NewChangeLog(0), searchcache.Config{})
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withSearchResults(cache))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	seed.lib.mu.Lock()
	seed.lib.hydrateErr = fmt.Errorf("disk read failed")
	seed.lib.mu.Unlock()
	searchBooks(t, h, tok, "odyssey") // fails the test on any status but 200
}
