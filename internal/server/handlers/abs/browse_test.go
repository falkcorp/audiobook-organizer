// file: internal/server/handlers/abs/browse_test.go
// version: 1.0.0
// guid: 8b3e10c4-6d97-4a52-bf08-2e4c95d7130a
// last-edited: 2026-07-30

package abs_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newBrowseHarness wires the browse surface over the oracle seed and returns a
// logged-in bearer token alongside it.
func newBrowseHarness(t *testing.T) (*harness, *oracleSeed, string) {
	t.Helper()
	seed := seedOracleLibrary(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	body := h.login(t, "oracle", "pw-pw-pw-pw")
	return h, seed, str(t, userObj(t, body), "accessToken")
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// libraryID is the library the whole ABS surface exposes. It comes from config
// (ABS_DEFAULT_LIBRARY_ID) and must be the SAME value /login reports as
// userDefaultLibraryId, or AudioBooth selects a library that does not exist.
func (h *harness) libraryID() string { return h.cfg.DefaultLibraryID }

// ── GET /api/libraries ──────────────────────────────────────────────────────

func TestLibraries_ConformsToOracle(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{method: http.MethodGet, path: "/api/libraries", headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries.json", body)
}

// TestLibraries_IDMatchesUserDefaultLibraryID pins the §1.8.2 login blocker from
// the other end: /login promises a userDefaultLibraryId and this endpoint must
// actually contain a library with that id, or Absorb selects nothing and the app
// is dead (library_provider.dart:301-303).
func TestLibraries_IDMatchesUserDefaultLibraryID(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	_, body := h.do(t, request{method: http.MethodGet, path: "/api/libraries", headers: bearer(tok)})
	libs, ok := body["libraries"].([]any)
	if !ok || len(libs) == 0 {
		t.Fatalf("no libraries in %#v", body)
	}
	first, _ := libs[0].(map[string]any)
	if got := first["id"]; got != h.libraryID() {
		t.Fatalf("library id %v does not match userDefaultLibraryId %q", got, h.libraryID())
	}
	if mt := first["mediaType"]; mt != "book" {
		t.Fatalf(`mediaType must be exactly "book" (§1.8.5 item 9), got %#v`, mt)
	}
}

// ── GET /api/libraries/:id/items ────────────────────────────────────────────

func TestLibraryItems_ConformsToOracle(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/items?limit=10&page=0",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries_id_items.json", body)
}

// TestLibraryItems_MinifiedDurationIsNonZero pins verified requirement 13: if
// media.duration <= 0 and audioFiles/tracks/numAudioFiles are empty, Absorb
// classifies the item ebook-only and THE PLAY BUTTON DISAPPEARS
// (player_settings.dart:895-909).
func TestLibraryItems_MinifiedDurationIsNonZero(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	_, body := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/items",
		headers: bearer(tok),
	})
	for i, raw := range body["results"].([]any) {
		media := raw.(map[string]any)["media"].(map[string]any)
		d, ok := media["duration"].(float64)
		if !ok || d <= 0 {
			t.Fatalf("results[%d].media.duration must be a positive number, got %#v", i, media["duration"])
		}
		if n, ok := media["numAudioFiles"].(float64); !ok || n <= 0 {
			t.Fatalf("results[%d].media.numAudioFiles must be a positive number, got %#v", i, media["numAudioFiles"])
		}
	}
}

// TestLibraryItems_PublishedYearIsStringNeverNumber pins verified requirement 1.
// In Dart `as String?` on a number THROWS rather than yielding null, and the cast
// sits inside a widget build() (book_detail_sheet.dart:494) — a numeric
// publishedYear red-screens the book detail sheet. Swift throws too.
func TestLibraryItems_PublishedYearIsStringNeverNumber(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	_, body := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/items",
		headers: bearer(tok),
	})
	sawString := false
	for i, raw := range body["results"].([]any) {
		md := raw.(map[string]any)["media"].(map[string]any)["metadata"].(map[string]any)
		for _, key := range []string{"publishedYear", "publishedDate"} {
			switch v := md[key].(type) {
			case nil:
			case string:
				if key == "publishedYear" {
					sawString = true
				}
			default:
				t.Fatalf("results[%d].media.metadata.%s must be a string or null, got %T (%#v)", i, key, v, v)
			}
		}
		for _, s := range md["series"].([]any) {
			if seq := s.(map[string]any)["sequence"]; seq != nil {
				if _, ok := seq.(string); !ok {
					t.Fatalf("series[].sequence must be a string or null, got %#v", seq)
				}
			}
		}
	}
	if !sawString {
		t.Fatal("seed has a book with a known year; at least one publishedYear should be a non-null string")
	}
}

// TestLibraryItems_CountsAreIntegers pins verified requirement 6: Dart throws on
// `42.0 as int?` and numBooks is cast during widget build.
func TestLibraryItems_CountsAreIntegers(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	w, _ := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/items",
		headers: bearer(tok),
	})
	raw := w.Body.String()
	for _, key := range []string{"total", "page", "limit", "numAudioFiles", "numChapters", "numTracks"} {
		assertNoFractionalNumber(t, raw, key)
	}
}

// assertNoFractionalNumber fails when a JSON key is serialized with a decimal
// point, which is how a Go float64 count leaks into an int-typed client field.
func assertNoFractionalNumber(t *testing.T, body, key string) {
	t.Helper()
	needle := `"` + key + `":`
	for idx := 0; ; {
		at := strings.Index(body[idx:], needle)
		if at < 0 {
			return
		}
		idx += at + len(needle)
		end := idx
		for end < len(body) && !strings.ContainsRune(",}]", rune(body[end])) {
			end++
		}
		if strings.Contains(body[idx:end], ".") {
			t.Fatalf("%s must be an integer, got %s", key, strings.TrimSpace(body[idx:end]))
		}
	}
}

// TestLibraryItems_PaginationAndTotal pins Page<T>: total and page are both
// required (§1.8.5 item 5) and total is the WHOLE-library count, not the page size.
func TestLibraryItems_PaginationAndTotal(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	_, body := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/items?limit=1&page=1",
		headers: bearer(tok),
	})
	if got := body["total"]; got != float64(2) {
		t.Fatalf("total must be the library count (2), got %#v", got)
	}
	if got := body["page"]; got != float64(1) {
		t.Fatalf("page must echo the request, got %#v", got)
	}
	if n := len(body["results"].([]any)); n != 1 {
		t.Fatalf("limit=1 must yield 1 result, got %d", n)
	}
}

// TestLibraryItems_PodcastMediaTypeIsEmptyNotError pins the podcast stub: a
// probing client must get a valid empty page, never a 4xx/5xx.
func TestLibraryItems_PodcastMediaTypeIsEmptyNotError(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/items?mediaType=podcast",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("podcast probe must be 200, got %d", code)
	}
	obj := body.(map[string]any)
	if n := len(obj["results"].([]any)); n != 0 {
		t.Fatalf("podcast results must be empty, got %d", n)
	}
	if obj["total"] != float64(0) {
		t.Fatalf("podcast total must be 0, got %#v", obj["total"])
	}
}

// TestLibraryRecentEpisodes_WrapperKey pins §1.8.6: the wrapper key is required.
func TestLibraryRecentEpisodes_WrapperKey(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/recent-episodes",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	eps, ok := body.(map[string]any)["episodes"]
	if !ok {
		t.Fatalf(`recent-episodes must return {"episodes":[]}, got %#v`, body)
	}
	if _, ok := eps.([]any); !ok {
		t.Fatalf("episodes must be an array, got %#v", eps)
	}
}

// ── /personalized, /series, /authors, /narrators, /filterdata ───────────────

// TestPersonalized_ConformsToOracle also pins §1.8.6: the body is a BARE ARRAY.
// An object there throws in AudioBooth's decoder.
func TestPersonalized_ConformsToOracle(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	// One in-progress book so continue-listening has exactly the oracle's single
	// entity and discover has the remaining one.
	if err := seed.lib.SetUserPosition("u1", seed.multiID, "abs", 42); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/personalized",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	if _, isObj := body.(map[string]any); isObj {
		t.Fatal("/personalized must be a bare array, not an object (§1.8.6)")
	}
	assertConformant(t, "get_api_libraries_id_personalized.json", body)
}

func TestSeries_ConformsToOracle(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/series",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries_id_series.json", body)
}

// TestSeries_PageEnvelopeAlwaysPresent pins §1.8.6: Page<T> needs total AND page
// even when results is empty.
func TestSeries_PageEnvelopeAlwaysPresent(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	_, body := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/series",
		headers: bearer(tok),
	})
	for _, key := range []string{"results", "total", "page"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("series response is missing required key %q: %#v", key, body)
		}
	}
}

func TestAuthors_ConformsToOracle(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/authors",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries_id_authors.json", body)
}

// TestAuthors_NumBooksIsInteger pins verified requirement 6 at its most dangerous
// site: numBooks is cast to int during widget build (library_grid_tiles.dart:441).
func TestAuthors_NumBooksIsInteger(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	w, _ := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/authors",
		headers: bearer(tok),
	})
	assertNoFractionalNumber(t, w.Body.String(), "numBooks")
}

func TestNarrators_ConformsToOracle(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/narrators",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries_id_narrators.json", body)
}

// TestFilterData_AllEightKeys pins §1.8.6: every one of the eight filterdata keys
// is decoded non-optionally, so a missing key throws.
func TestFilterData_AllEightKeys(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/filterdata",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries_id_filterdata.json", body)

	obj := body.(map[string]any)
	for _, key := range []string{"authors", "genres", "tags", "series", "narrators", "languages", "publishers", "publishedDecades"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("filterdata is missing required key %q", key)
		}
	}
	// §1.7.3 item 8: filterdata.authors is objects, filterdata.narrators is PLAIN
	// NAME STRINGS. Getting these two backwards is a silent decode failure.
	for _, a := range obj["authors"].([]any) {
		if _, ok := a.(map[string]any); !ok {
			t.Fatalf("filterdata.authors entries must be objects, got %#v", a)
		}
	}
	for _, n := range obj["narrators"].([]any) {
		if _, ok := n.(string); !ok {
			t.Fatalf("filterdata.narrators entries must be plain strings, got %#v", n)
		}
	}
}

// ── GET /api/libraries/:id/search ───────────────────────────────────────────

func TestSearch_ConformsToOracle(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/search?q=odyssey",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	assertConformant(t, "get_api_libraries_id_search.json", body)
}

// TestSearch_EmptyQueryIsEmptyResultNotError keeps a probing client from seeing a
// 4xx it would misread as "unsupported" (§1.7.3 item 10).
func TestSearch_EmptyQueryIsEmptyResultNotError(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/search",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200", code)
	}
	if n := len(body.(map[string]any)["book"].([]any)); n != 0 {
		t.Fatalf("empty query must match nothing, got %d", n)
	}
}

// ── GET /api/items/:id ──────────────────────────────────────────────────────

func TestItem_ConformsToOracle(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	if err := seed.lib.SetUserPosition("u1", seed.multiID, "abs", 42); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	syncID := mustSyncID(t, seed, seed.multiID)
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/items/" + syncID + "?expanded=1&include=progress",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("got %d want 200: %#v", code, body)
	}
	assertConformant(t, "get_api_items_id.json", body)
}

// TestItem_LibraryItemIDIs36CharUUID pins §1.7.1, the single BREAKING id
// requirement: Absorb splits compound keys by FIXED OFFSET substring(0,36), so a
// 26-char ULID mis-truncates into the wrong /api/me/progress path.
func TestItem_LibraryItemIDIs36CharUUID(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	if len(syncID) != 36 {
		t.Fatalf("minted libraryItemId must be 36 chars, got %d (%q)", len(syncID), syncID)
	}
	if syncID == seed.multiID {
		t.Fatal("libraryItemId must never be the raw Book ULID")
	}
	_, body := h.do(t, request{
		method: http.MethodGet, path: "/api/items/" + syncID, headers: bearer(tok),
	})
	if got := body["id"]; got != syncID {
		t.Fatalf("item id %#v != requested syncID %q", got, syncID)
	}
}

// TestItem_TitleNeverNull pins §1.8.5 item 4: Book.swift:196 decodes title
// non-optionally, so ONE null title blanks the entire page.
func TestItem_TitleNeverNull(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	seed.lib.mu.Lock()
	seed.lib.books[seed.multiID].Title = ""
	seed.lib.mu.Unlock()

	syncID := mustSyncID(t, seed, seed.multiID)
	_, body := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID, headers: bearer(tok)})
	md := body["media"].(map[string]any)["metadata"].(map[string]any)
	title, ok := md["title"].(string)
	if !ok || title == "" {
		t.Fatalf("title must fall back to the filename, got %#v", md["title"])
	}
}

// TestItem_BothRelationFormsPresent pins verified requirement 9: items are read
// via the FLAT strings, while the object arrays matter in filterdata. ABS emits
// both, so we emit both.
func TestItem_BothRelationFormsPresent(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	_, body := h.do(t, request{
		method: http.MethodGet, path: "/api/items/" + syncID + "?expanded=1", headers: bearer(tok),
	})
	md := body["media"].(map[string]any)["metadata"].(map[string]any)
	for _, key := range []string{
		"authors", "authorName", "authorNameLF",
		"narrators", "narratorName",
		"series", "seriesName",
		"title", "titleIgnorePrefix",
		"description", "descriptionPlain",
		"subtitle",
	} {
		if _, ok := md[key]; !ok {
			t.Fatalf("media.metadata is missing %q — every relation must be emitted in BOTH forms", key)
		}
	}
	if _, ok := md["authorName"].(string); !ok {
		t.Fatalf("authorName must be a flat string, got %#v", md["authorName"])
	}
	// authorNameLF is "Last, First"; the oracle turns "transl. Samuel Butler
	// Homer" into "Homer, transl. Samuel Butler".
	if got := md["authorNameLF"]; got != "Homer, transl. Samuel Butler" {
		t.Fatalf("authorNameLF = %#v, want %q", got, "Homer, transl. Samuel Butler")
	}
}

// TestItem_ChaptersHaveAllFourFields pins §1.8.5 item 7. Chapter id is an INT
// while every other id in the protocol is a String.
func TestItem_ChaptersHaveAllFourFields(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	for _, bookID := range []string{seed.singleID, seed.multiID} {
		syncID := mustSyncID(t, seed, bookID)
		_, body := h.do(t, request{
			method: http.MethodGet, path: "/api/items/" + syncID + "?expanded=1", headers: bearer(tok),
		})
		chapters := body["media"].(map[string]any)["chapters"].([]any)
		if len(chapters) != 6 {
			t.Fatalf("%s: want 6 chapters, got %d", bookID, len(chapters))
		}
		var prevEnd float64
		for i, raw := range chapters {
			ch := raw.(map[string]any)
			for _, key := range []string{"id", "start", "end", "title"} {
				if _, ok := ch[key]; !ok {
					t.Fatalf("chapters[%d] missing %q", i, key)
				}
			}
			if id, ok := ch["id"].(float64); !ok || int(id) != i {
				t.Fatalf("chapters[%d].id must be the integer index, got %#v", i, ch["id"])
			}
			if start := ch["start"].(float64); start != prevEnd {
				t.Fatalf("chapters[%d].start = %v, want %v (cumulative timeline)", i, start, prevEnd)
			}
			prevEnd = ch["end"].(float64)
		}
	}
}

// TestItem_UnknownIDIs404 keeps 404 meaning "no such item" — §1.7.3 item 10 makes
// 404 the correct answer for what we do not have, and never a 200 with HTML.
func TestItem_UnknownIDIs404(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	w, _ := h.do(t, request{
		method: http.MethodGet, path: "/api/items/00000000-0000-4000-8000-000000000000",
		headers: bearer(tok),
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("404 body must be JSON, got Content-Type %q", ct)
	}
}

func TestBrowse_RequiresAuth(t *testing.T) {
	h, seed, _ := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	for _, path := range []string{
		"/api/libraries",
		"/api/libraries/" + h.libraryID() + "/items",
		"/api/libraries/" + h.libraryID() + "/personalized",
		"/api/libraries/" + h.libraryID() + "/search?q=x",
		"/api/items/" + syncID,
	} {
		w, _ := h.do(t, request{method: http.MethodGet, path: path})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d want 401", path, w.Code)
		}
	}
}

// ── GET /api/items/:id/cover ────────────────────────────────────────────────

// TestCover_ServesWithoutCredentials pins §1.8.8 item 7 / §1.9.5: AudioBooth's
// widget extension sends NO headers at all, so the cover endpoint must not
// require the app bearer. The Cloudflare edge still gates it in Modes B/C.
func TestCover_ServesWithoutCredentials(t *testing.T) {
	h, seed, _ := newBrowseHarness(t)
	writeCover(t, seed, seed.multiID)
	syncID := mustSyncID(t, seed, seed.multiID)

	w, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID + "/cover"})
	if w.Code != http.StatusOK {
		t.Fatalf("cover without credentials: got %d want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Fatalf("cover Content-Type = %q, want an image/* type", ct)
	}
	if w.Header().Get("ETag") == "" {
		t.Fatal("cover must carry an ETag so clients can cache it")
	}
	if w.Header().Get("Last-Modified") == "" {
		t.Fatal("cover must carry Last-Modified so clients can cache it")
	}
}

// TestCover_AcceptsTokenQueryParamAndImageParams pins §1.7.2 (Absorb/CarPlay use
// ?token=) and §1.8.8 item 7 (honour width/raw/format rather than erroring).
func TestCover_AcceptsTokenQueryParamAndImageParams(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	writeCover(t, seed, seed.multiID)
	syncID := mustSyncID(t, seed, seed.multiID)

	for _, q := range []string{
		"?token=" + tok,
		"?width=400",
		"?raw=1",
		"?format=jpg",
		"?token=" + tok + "&width=200&format=jpeg",
	} {
		w, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID + "/cover" + q})
		if w.Code != http.StatusOK {
			t.Fatalf("cover%s: got %d want 200", q, w.Code)
		}
	}
}

// TestCover_NotModifiedOnIfNoneMatch keeps the cache round-trip honest.
func TestCover_NotModifiedOnIfNoneMatch(t *testing.T) {
	h, seed, _ := newBrowseHarness(t)
	writeCover(t, seed, seed.multiID)
	syncID := mustSyncID(t, seed, seed.multiID)

	first, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID + "/cover"})
	etag := first.Header().Get("ETag")
	second, _ := h.do(t, request{
		method: http.MethodGet, path: "/api/items/" + syncID + "/cover",
		headers: map[string]string{"If-None-Match": etag},
	})
	if second.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match must yield 304, got %d", second.Code)
	}
}

// TestCover_MissingCoverIs404 — a 404 here is correct and harmless: both clients
// fall back to a placeholder.
func TestCover_MissingCoverIs404(t *testing.T) {
	h, seed, _ := newBrowseHarness(t)
	syncID := mustSyncID(t, seed, seed.multiID)
	w, _ := h.do(t, request{method: http.MethodGet, path: "/api/items/" + syncID + "/cover"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func mustSyncID(t *testing.T, seed *oracleSeed, bookID string) string {
	t.Helper()
	id, err := seed.lib.MintOrGetSyncID(bookID)
	if err != nil {
		t.Fatalf("MintOrGetSyncID(%s): %v", bookID, err)
	}
	return id
}

// writeCover drops a minimal PNG where metadata.CoverPathForBook looks for it:
// {root}/covers/{bookID}.png. Book.CoverURL is an API path, not a disk path.
func writeCover(t *testing.T, seed *oracleSeed, bookID string) {
	t.Helper()
	dir := filepath.Join(seed.root, "covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir covers: %v", err)
	}
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
		0x1f, 0x15, 0xc4, 0x89,
	}
	if err := os.WriteFile(filepath.Join(dir, bookID+".png"), png, 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
}
