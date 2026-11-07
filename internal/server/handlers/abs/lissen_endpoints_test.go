// file: internal/server/handlers/abs/lissen_endpoints_test.go
// version: 1.0.0
// guid: 5d0e8a73-b216-4f9c-a3e7-1c64b9d2f085
// last-edited: 2026-11-07

package abs_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lissenSeed adds three Batman-titled organized books to the oracle seed so the
// genre counts and the batch lookups have data that is not the oracle's own.
func lissenSeed(t *testing.T) *oracleSeed {
	t.Helper()
	seed := seedOracleLibrary(t)
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	intp := func(i int) *int { return &i }
	add := func(id, title, genre string, dur int) {
		seed.lib.addBook(&database.Book{
			ID: id, Title: title, Genre: strp(genre), Duration: intp(dur),
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		}, nil, nil)
	}
	add("lissen-year-one", "Batman: Year One", "Gotham Noir", 3600)
	add("lissen-long-halloween", "Batman: The Long Halloween", "Gotham Noir", 7200)
	add("lissen-killing-joke", "Batman: The Killing Joke", "Gotham Tragedy", 1800)
	return seed
}

func lissenLogin(t *testing.T) (*harness, string) {
	t.Helper()
	return decodeGapsLogin(t, lissenSeed(t), fixtureUserData())
}

// lissenItemIDs maps title -> libraryItemId from the library listing.
func lissenItemIDs(t *testing.T, h *harness, tok string) map[string]string {
	t.Helper()
	var body struct {
		Results []struct {
			ID    string `json:"id"`
			Media struct {
				Metadata struct {
					Title string `json:"title"`
				} `json:"metadata"`
			} `json:"media"`
		} `json:"results"`
	}
	raw := decodeGapsRaw(t, h, tok, "/api/libraries/"+h.libraryID()+"/items?limit=100")
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode items: %v\n%s", err, raw)
	}
	out := map[string]string{}
	for _, r := range body.Results {
		out[r.Media.Metadata.Title] = r.ID
	}
	return out
}

func TestLibraryStats_GenresWithCountMatchesLissenDTO(t *testing.T) {
	h, tok := lissenLogin(t)
	raw := decodeGapsRaw(t, h, tok, "/api/libraries/"+h.libraryID()+"/stats")

	// Lissen's LibraryStatsResponse: genresWithCount is a non-null list of
	// {genre: String, count: Int}; Moshi fails on a missing/null key.
	var got struct {
		GenresWithCount *[]struct {
			Genre *string `json:"genre"`
			Count *int    `json:"count"`
		} `json:"genresWithCount"`
		TotalGenres      int `json:"totalGenres"`
		TotalAuthors     int `json:"totalAuthors"`
		AuthorsWithCount []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"authorsWithCount"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if got.GenresWithCount == nil {
		t.Fatalf("genresWithCount missing: %s", raw)
	}
	counts := map[string]int{}
	for i, g := range *got.GenresWithCount {
		if g.Genre == nil || g.Count == nil {
			t.Fatalf("genresWithCount[%d] missing genre/count: %s", i, raw)
		}
		counts[*g.Genre] = *g.Count
	}
	if counts["Gotham Noir"] != 2 || counts["Gotham Tragedy"] != 1 {
		t.Fatalf("genre counts = %v, want Gotham Noir=2 Gotham Tragedy=1", counts)
	}
	if got.TotalGenres != len(*got.GenresWithCount) {
		t.Fatalf("totalGenres = %d, want %d", got.TotalGenres, len(*got.GenresWithCount))
	}
	if got.AuthorsWithCount == nil {
		t.Fatalf("authorsWithCount must be an array, not null: %s", raw)
	}
}

func TestLibraryStats_UnknownLibraryIs404(t *testing.T) {
	h, tok := lissenLogin(t)
	w, _ := h.do(t, request{method: http.MethodGet, path: "/api/libraries/lib_nobody/stats", headers: bearer(tok)})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown library stats = %d, want 404", w.Code)
	}
}

func TestLibraryStats_RequiresAuth(t *testing.T) {
	h, _ := lissenLogin(t)
	w, _ := h.do(t, request{method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/stats"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stats = %d, want 401", w.Code)
	}
}

func lissenBatch(t *testing.T, h *harness, tok string, body any) (int, map[string]any) {
	t.Helper()
	w, m := h.do(t, request{method: http.MethodPost, path: "/api/items/batch/get", headers: bearer(tok), body: body})
	return w.Code, m
}

func TestItemsBatchGet_ReturnsExpandedItemsInRequestOrder(t *testing.T) {
	h, tok := lissenLogin(t)
	ids := lissenItemIDs(t, h, tok)
	a, b := ids["Batman: The Killing Joke"], ids["Batman: Year One"]
	if a == "" || b == "" {
		t.Fatalf("seed ids not found in listing: %v", ids)
	}

	code, body := lissenBatch(t, h, tok, map[string]any{
		// Unknown id and a duplicate: the first is skipped, the second answered once.
		"libraryItemIds": []string{a, "li_does_not_exist", b, a},
	})
	if code != http.StatusOK {
		t.Fatalf("batch = %d: %v", code, body)
	}
	items, ok := body["libraryItems"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("libraryItems = %#v, want exactly 2 items", body["libraryItems"])
	}
	for i, want := range []struct{ id, title string }{{a, "Batman: The Killing Joke"}, {b, "Batman: Year One"}} {
		it := items[i].(map[string]any)
		if it["id"] != want.id {
			t.Fatalf("items[%d].id = %v, want %s (request order)", i, it["id"], want.id)
		}
		media, _ := it["media"].(map[string]any)
		meta, _ := media["metadata"].(map[string]any)
		if meta["title"] != want.title {
			t.Fatalf("items[%d] title = %v, want %q", i, meta["title"], want.title)
		}
		// Same expanded shape as GET /api/items/:id.
		for _, k := range []string{"libraryId", "mediaType", "media", "path"} {
			if _, present := it[k]; !present {
				t.Fatalf("items[%d] missing %q: %v", i, k, it)
			}
		}
	}
}

func TestItemsBatchGet_MatchesSingleItemEndpoint(t *testing.T) {
	h, tok := lissenLogin(t)
	id := lissenItemIDs(t, h, tok)["Batman: The Long Halloween"]
	single := decodeGapsRaw(t, h, tok, "/api/items/"+id)
	_, body := lissenBatch(t, h, tok, map[string]any{"libraryItemIds": []string{id}})
	items, _ := body["libraryItems"].([]any)
	if len(items) != 1 {
		t.Fatalf("libraryItems = %v, want 1", body)
	}
	batched, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	if err := json.Unmarshal(single, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(batched, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("batch item has %d keys, single item has %d\nbatch:  %s\nsingle: %s", len(b), len(a), batched, single)
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			t.Fatalf("batch item lacks key %q that GET /api/items/:id has", k)
		}
	}
}

func TestItemsBatchGet_AllUnknownAndEmptyAreEmptyLists(t *testing.T) {
	h, tok := lissenLogin(t)
	for name, ids := range map[string][]string{"unknown": {"li_zzz", "li_yyy"}, "empty": {}} {
		code, body := lissenBatch(t, h, tok, map[string]any{"libraryItemIds": ids})
		items, ok := body["libraryItems"].([]any)
		if code != http.StatusOK || !ok || len(items) != 0 {
			t.Fatalf("%s: = %d %v, want 200 with libraryItems: []", name, code, body)
		}
	}
}

func TestItemsBatchGet_BadBodyAndOversizeAre400(t *testing.T) {
	h, tok := lissenLogin(t)
	for name, body := range map[string]any{
		"no key":      map[string]any{},
		"wrong type":  map[string]any{"libraryItemIds": "nope"},
		"null":        map[string]any{"libraryItemIds": nil},
		"not objects": []string{"a"},
	} {
		if code, _ := lissenBatch(t, h, tok, body); code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", name, code)
		}
	}
	w, _ := h.do(t, request{method: http.MethodPost, path: "/api/items/batch/get", headers: bearer(tok)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty body = %d, want 400", w.Code)
	}
	big := make([]string, 201)
	for i := range big {
		big[i] = "li_" + strconv.Itoa(i)
	}
	if code, _ := lissenBatch(t, h, tok, map[string]any{"libraryItemIds": big}); code != http.StatusBadRequest {
		t.Fatalf("201 ids = %d, want 400", code)
	}
}

func TestItemsBatchGet_RequiresAuth(t *testing.T) {
	h, _ := lissenLogin(t)
	w, _ := h.do(t, request{method: http.MethodPost, path: "/api/items/batch/get", body: map[string]any{"libraryItemIds": []string{}}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated batch = %d, want 401", w.Code)
	}
}

func TestAuthorImage_IsAPlain404(t *testing.T) {
	h, tok := lissenLogin(t)
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		w, _ := h.do(t, request{method: m, path: "/api/authors/1/image?width=400", headers: bearer(tok)})
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s author image = %d, want 404", m, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "" {
			t.Fatalf("%s author image redirected to %q", m, loc)
		}
	}
}
