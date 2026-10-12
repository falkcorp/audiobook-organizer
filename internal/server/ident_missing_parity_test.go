// file: internal/server/ident_missing_parity_test.go
// version: 1.0.0
// guid: 2e59aba3-1d79-4ad8-b33e-68e7f9cd1482
// last-edited: 2026-10-11

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// identParityFixtureRelPath is the fixture the Library's vitest
// (web/src/pages/Library.identMissing.test.tsx) reads: the list count and
// the review summary's ident_counts.missing this test measures through the
// real handlers. Regenerate deliberately with
//
//	UPDATE_FIXTURE=1 go test ./internal/server/ -run TestIdentMissing_LibraryCountEqualsReviewSummary
const identParityFixtureRelPath = "../../web/src/pages/__fixtures__/ident_missing_parity.json"

// identParityFixture is the fixture's shape.
type identParityFixture struct {
	Search             string `json:"search"`
	ListCount          int    `json:"list_count"`
	IdentCountsMissing int    `json:"ident_counts_missing"`
}

// TestIdentMissing_LibraryCountEqualsReviewSummary is the acceptance check
// of every count clicking through: the count the Library shows for
// `ident:missing` (GET /audiobooks with the filters JSON the Library sends:
// the "Select all N items" banner's N) equals the review summary's
// ident_counts.missing, in both the index and the page view, over a fixture
// whose default view differs from "every book": non-primary, trashed and
// quarantined books in the missing states, a nil primary flag (which counts
// as primary), and books in every other state.
func TestIdentMissing_LibraryCountEqualsReviewSummary(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok)

	n := 0
	mk := func(edit func(*database.Book)) string {
		n++
		title := fmt.Sprintf("Title %06d", n)
		b := &database.Book{Title: title, FilePath: "/lib/parity/" + title + ".m4b", Format: "m4b"}
		if edit != nil {
			edit(b)
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		return created.ID
	}
	putCache := func(id string, candidates int) {
		cands := make([]json.RawMessage, candidates)
		for i := range cands {
			cands[i] = json.RawMessage(fmt.Sprintf(`{"title":"Title %06d"}`, i))
		}
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: id, FetchedAt: time.Now(), Candidates: cands}))
	}
	modify := func(id string, fn func(*database.Book)) {
		_, err := store.ModifyBook(id, func(b *database.Book) error { fn(b); return nil })
		require.NoError(t, err)
	}
	f, tr := false, true

	wantMissing := 0
	for range 7 { // unasked, nil primary flag: counted
		mk(nil)
		wantMissing++
	}
	for range 3 { // asked_empty: counted
		putCache(mk(nil), 0)
		wantMissing++
	}
	putCache(mk(func(b *database.Book) { b.IsPrimaryVersion = &tr }), 0) // explicit primary, asked_empty
	wantMissing++
	for range 2 { // has_candidates: not missing
		putCache(mk(nil), 2)
	}
	applied := mk(nil)
	modify(applied, func(b *database.Book) { s := "matched"; b.MetadataReviewStatus = &s })
	held := mk(nil)
	modify(held, func(b *database.Book) { s := "no_match"; b.MetadataReviewStatus = &s })
	for range 2 { // non-primary, unasked: outside the default view
		mk(func(b *database.Book) { b.IsPrimaryVersion = &f })
	}
	trashed := mk(nil) // unasked, in the trash
	modify(trashed, func(b *database.Book) { b.MarkedForDeletion = &tr })
	quarantined := mk(nil) // unasked, quarantined
	modify(quarantined, func(b *database.Book) { now := time.Now(); b.QuarantinedAt = &now })

	get := func(path string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		if d, ok := body["data"].(map[string]any); ok {
			return d
		}
		return body
	}
	missingOf := func(m map[string]any, where string) int {
		t.Helper()
		ic, ok := m["ident_counts"].(map[string]any)
		require.Truef(t, ok, "%s has no ident_counts: %v", where, m)
		require.Equal(t, true, ic["ready"], where)
		return int(ic["missing"].(float64))
	}

	// The Library's request for `ident:missing` typed in the search bar:
	// the parsed field filter in the filters JSON, the default view.
	filters, err := json.Marshal([]map[string]any{{"field": "ident", "value": "missing", "negated": false}})
	require.NoError(t, err)
	q := url.Values{"limit": {"5"}, "offset": {"0"}, "is_primary_version": {"true"}, "filters": {string(filters)}}
	list := get("/api/v1/audiobooks?" + q.Encode())
	listCount := int(list["count"].(float64))
	items, _ := list["items"].([]any)
	require.Len(t, items, 5, "a page smaller than the total, as the banner needs")

	index := missingOf(get("/api/v1/audiobooks/metadata/cache/review?limit=1"), "index summary")
	page := get("/api/v1/audiobooks/metadata/cache/review?view=page&limit=1")
	facets, ok := page["facets"].(map[string]any)
	require.True(t, ok, "page view has facets: %v", page)
	pageMissing := missingOf(facets, "page facets")

	t.Logf("ident:missing: Library list count N=%d, review summary ident_counts.missing=%d (index) / %d (page), fixture expects %d",
		listCount, index, pageMissing, wantMissing)
	require.Equal(t, wantMissing, listCount)
	require.Equal(t, listCount, index)
	require.Equal(t, listCount, pageMissing)

	// With show_quarantined the request is no longer the default view, so
	// the count must not come from the default-view counter: the walk counts
	// the quarantined unasked book too.
	q.Set("show_quarantined", "true")
	withQuarantined := int(get("/api/v1/audiobooks?" + q.Encode())["count"].(float64))
	require.Equal(t, wantMissing+1, withQuarantined, "show_quarantined adds the quarantined unasked book")

	got := identParityFixture{Search: "ident:missing", ListCount: listCount, IdentCountsMissing: index}
	path := filepath.FromSlash(identParityFixtureRelPath)
	if os.Getenv("UPDATE_FIXTURE") != "" {
		data, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
	}
	raw, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read %s (regenerate with UPDATE_FIXTURE=1)", path)
	var want identParityFixture
	require.NoError(t, json.Unmarshal(raw, &want))
	require.Equal(t, want, got, "the web fixture is stale: regenerate with UPDATE_FIXTURE=1")
}
