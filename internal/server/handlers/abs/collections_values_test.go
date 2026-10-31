// file: internal/server/handlers/abs/collections_values_test.go
// version: 1.1.0
// guid: 5e2b9d84-1c73-4a06-8f95-d0b3a7e61c42
// last-edited: 2026-10-30

package abs_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// S-B: a smart playlist's cached list is what this surface serves, and it is the list
// the previous evaluation left. When the stored query is one the evaluator now
// refuses, that list is the answer of an evaluation that read the query wrongly
// (read_status:completed matched nothing, -read_status:completed matched every
// book), so it is not served, whether or not the native GET has run since.
func TestLibraryPlaylists_ASmartPlaylistWithARefusedQueryServesNoBooks(t *testing.T) {
	store := &absplFakeStore{}
	h, seed, tok := absplHarness(t, store)
	cached := []string{seed.singleID}
	store.lists = []database.UserPlaylist{
		{ID: "01REFUSED00000000000000000", Name: "No Man's Land", Type: database.UserPlaylistTypeSmart,
			Query: "-read_status:completed", MaterializedBookIDs: cached, CreatedByUserID: "u1"},
		{ID: "01REFUSEDOR000000000000000", Name: "Knightfall", Type: database.UserPlaylistTypeSmart,
			Query: "read_status:completed OR title:Alpha", MaterializedBookIDs: cached, CreatedByUserID: "u1"},
		{ID: "01GOOD00000000000000000000", Name: "Hush", Type: database.UserPlaylistTypeSmart,
			Query: "read_status:finished", MaterializedBookIDs: cached, CreatedByUserID: "u1"},
	}

	results := absplGet(t, h, tok)["results"].([]any)
	items := map[string]int{}
	for _, r := range results {
		row := r.(map[string]any)
		items[row["name"].(string)] = len(row["items"].([]any))
	}
	if items["No Man's Land"] != 0 {
		t.Errorf("a refused value served %d items from the stale cache, want 0", items["No Man's Land"])
	}
	if items["Knightfall"] != 0 {
		t.Errorf("a refused value under an OR served %d items from the stale cache, want 0", items["Knightfall"])
	}
	if items["Hush"] != 1 {
		t.Errorf("a query that is fine served %d items, want its cached 1", items["Hush"])
	}

	code, body := absplDetail(t, h, tok, "01REFUSED00000000000000000")
	if code != http.StatusOK {
		t.Fatalf("detail = %d, want 200", code)
	}
	if n := len(body.(map[string]any)["items"].([]any)); n != 0 {
		t.Errorf("the detail route served %d items for a refused query, want 0", n)
	}
}

// N-f: the client went away while the page was being worked out. Every collection
// route answers 499 (the code the native routes use) rather than a 200 whose
// collections are all empty, which a client that is still listening would take as
// the truth.
func TestCollections_ABSAGoneClientIsAnswered499NotAnEmptyPage(t *testing.T) {
	f := newViewerFixture(t)
	f.store.cols = append(f.store.cols, database.Collection{
		ID: "c1", Name: "The Long Halloween", Type: database.CollectionTypeDynamic, Query: "read_status:finished",
		MaterializedBookIDs: []string{f.seed.singleID},
	})
	gone := func(o *abshandler.Options) {
		o.CollectionViewerMembers = func(context.Context, []database.Collection, string) (abshandler.ViewerMembers, error) {
			return abshandler.ViewerMembers{}, context.Canceled
		}
	}
	h := newHarness(t, "jwt", nil, withLibrary(f.seed), withUserData(fixtureUserData()), withCollections(f.store), gone)
	h.seedUser(t, "alice", "alice", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "alice", "pw-pw-pw-pw")), "accessToken")

	for _, path := range []string{"/api/libraries/" + h.libraryID() + "/collections", "/api/collections/c1"} {
		w, _ := h.do(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
		if w.Code != 499 {
			t.Errorf("GET %s = %d, want 499 (body %s)", path, w.Code, w.Body.String())
		}
	}
}

// N-m: a per-viewer collection whose members could not be worked out has no flag to
// carry on this surface, so a list shows it with no books (one row cannot fail a
// page). A read of that one collection is told to try again instead of being served
// an empty collection as the truth.
func TestCollections_ABSDetailOfAnUnavailableCollectionAnswers503(t *testing.T) {
	f := newViewerFixture(t)
	f.store.cols = append(f.store.cols,
		database.Collection{ID: "down", Name: "The Long Halloween", Type: database.CollectionTypeDynamic, Query: "read_status:finished"},
		database.Collection{ID: "up", Name: "Year One", Type: database.CollectionTypeDynamic, Query: "read_status:unstarted"},
	)
	partial := func(o *abshandler.Options) {
		o.CollectionViewerMembers = func(context.Context, []database.Collection, string) (abshandler.ViewerMembers, error) {
			return abshandler.ViewerMembers{
				Sets:        map[string][]string{"down": {}, "up": {f.seed.singleID}},
				Unavailable: map[string]struct{}{"down": {}},
			}, nil
		}
	}
	h := newHarness(t, "jwt", nil, withLibrary(f.seed), withUserData(fixtureUserData()), withCollections(f.store), partial)
	h.seedUser(t, "alice", "alice", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "alice", "pw-pw-pw-pw")), "accessToken")

	w, _ := h.do(t, request{method: http.MethodGet, path: "/api/collections/down", headers: bearer(tok)})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("detail of an unavailable collection = %d, want 503 (body %s)", w.Code, w.Body.String())
	}
	w, _ = h.do(t, request{method: http.MethodGet, path: "/api/collections/up", headers: bearer(tok)})
	if w.Code != http.StatusOK {
		t.Errorf("detail of a collection that evaluated = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	rows := abscolResults(t, h, tok)
	if got := bookIDsOf(rowNamed(t, rows, "The Long Halloween")); len(got) != 0 {
		t.Errorf("the list shows the unavailable collection with %d books, want none", len(got))
	}
	if got := bookIDsOf(rowNamed(t, rows, "Year One")); len(got) != 1 {
		t.Errorf("the list shows the evaluated collection with %d books, want 1", len(got))
	}
}
