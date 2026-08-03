// file: internal/server/handlers/abs/authors_narrators_test.go
// version: 1.0.0
// guid: 4f8b23e7-16c0-4d95-a3e2-58971bd0c4fa
// last-edited: 2026-08-02

package abs_test

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The Authors and Narrators tabs both rendered EMPTY while both endpoints answered
// 200. Neither was a routing problem — both bodies were unparseable by the client, so
// the failure was invisible in the access log.
//
// Both causes are shape mismatches the committed fixtures could not catch:
//
//   - Authors: real ABS switches envelope when the caller paginates, and AudioBooth
//     ALWAYS paginates. The fixture was captured WITHOUT query parameters, so it
//     pinned the wrong one of the two shapes.
//   - Narrators: the fixture body is `{"narrators": []}` — the oracle library has no
//     narrators — so the conformance diff had no element to compare and passed
//     vacuously. An empty golden array cannot pin an element shape.
//
// These tests assert the ELEMENT shapes directly, which is what neither fixture does.

// ── authors ─────────────────────────────────────────────────────────────────

// 🔴 TestAuthors_PaginatedRequestGetsThePageEnvelope is the Authors-tab regression.
// AudioBooth sends `?sort=name&minified=1&limit=100&page=0` and decodes into
// Page<Author>, whose `total` and `page` use `try container.decode` — REQUIRED, not
// decodeIfPresent. Missing either throws and blanks the tab.
func TestAuthors_PaginatedRequestGetsThePageEnvelope(t *testing.T) {
	w := newWriteHarness(t)

	code, body, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/authors?sort=name&minified=1&limit=100&page=0", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}

	// The two fields whose absence threw.
	requireNum(t, body, "total")
	requireNum(t, body, "page")
	results := requireArray(t, body, "results")
	if len(results) == 0 {
		t.Fatal("no authors in the page — the seeded library has two")
	}

	// Page<Author>'s element: id and name are non-optional.
	first, _ := results[0].(map[string]any)
	if first == nil {
		t.Fatalf("results[0] is not an object: %#v", results[0])
	}
	for _, key := range []string{"id", "name"} {
		v, ok := first[key].(string)
		if !ok || v == "" {
			t.Fatalf("author.%s = %#v, want a non-empty string (the client decodes it non-optionally)", key, first[key])
		}
	}
}

// TestAuthors_UnpaginatedRequestKeepsTheBareShape — the oracle's own behaviour, and
// what the committed fixture pins. Both shapes must keep working.
func TestAuthors_UnpaginatedRequestKeepsTheBareShape(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	requireArray(t, body, "authors")
	if _, present := body["results"]; present {
		t.Fatal("unpaginated request got the paginated envelope — real ABS returns the bare shape here")
	}
}

// TestAuthors_TotalIsTheFullCountNotThePageSize — the client uses `total` to decide
// whether more pages exist, so reporting the slice length would hide every author
// past the first page.
func TestAuthors_TotalIsTheFullCountNotThePageSize(t *testing.T) {
	w := newWriteHarness(t)

	_, all, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors", nil)
	fullCount := len(requireArray(t, all, "authors"))
	if fullCount < 2 {
		t.Skipf("need >=2 authors to exercise paging, have %d", fullCount)
	}

	code, body, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/authors?limit=1&page=0", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	if got := len(requireArray(t, body, "results")); got != 1 {
		t.Fatalf("results has %d entries, want 1 (limit=1)", got)
	}
	if got := int(requireNum(t, body, "total")); got != fullCount {
		t.Fatalf("total = %d, want the full count %d — not the page size", got, fullCount)
	}
}

// TestAuthors_PagePastTheEndIsEmptyNotAnError — a client scrolling past the last page
// should see nothing more, not a failure.
func TestAuthors_PagePastTheEndIsEmptyNotAnError(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/authors?limit=10&page=999", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	if got := len(requireArray(t, body, "results")); got != 0 {
		t.Fatalf("results has %d entries past the end, want 0", got)
	}
}

// ── narrators ───────────────────────────────────────────────────────────────

// 🔴 TestNarrators_EveryEntryCarriesAnID is the Narrators-tab regression. AudioBooth's
// `Narrator` declares `public let id: String` non-optionally, so one entry without it
// throws the entire decode.
func TestNarrators_EveryEntryCarriesAnID(t *testing.T) {
	w := newWriteHarness(t)
	w.seed.lib.addNarrators("Victor Bevine", "Homer, transl. Samuel Butler")

	code, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/narrators", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	narrators := requireArray(t, body, "narrators")
	if len(narrators) == 0 {
		// NOT a skip. An empty list is exactly how this bug survived: the committed
		// oracle fixture is `{"narrators": []}`, so the conformance diff had no
		// element to compare and passed vacuously. A test that skips on an empty
		// library reproduces that same blind spot.
		t.Fatal("no narrators in the seeded library — this assertion is about the ELEMENT " +
			"shape, so an empty list makes it vacuous (that is how the missing id shipped)")
	}
	for i, raw := range narrators {
		n, _ := raw.(map[string]any)
		if n == nil {
			t.Fatalf("narrators[%d] is not an object: %#v", i, raw)
		}
		id, ok := n["id"].(string)
		if !ok || id == "" {
			t.Fatalf("narrators[%d].id = %#v — the client decodes it non-optionally and "+
				"throws the WHOLE list without it", i, n["id"])
		}
		if name, ok := n["name"].(string); !ok || name == "" {
			t.Fatalf("narrators[%d].name = %#v, want a non-empty string", i, n["name"])
		}
	}
}

// TestNarrators_IDMatchesTheRealABSFormula. Narrators are not entities in ABS — the
// name IS the identity — so the id must be DERIVED, or it changes on restart and every
// id the client cached rots. Real ABS uses
// encodeURIComponent(Buffer.from(name).toString('base64')).
//
// Asserted through the HTTP RESPONSE rather than by calling the helper: a test that
// recomputed the formula and compared it to itself would pass no matter what the
// endpoint actually emits.
func TestNarrators_IDMatchesTheRealABSFormula(t *testing.T) {
	names := []string{
		"Victor Bevine",
		"Homer, transl. Samuel Butler", // comma + spaces
		"Ellé Jones",                   // non-ASCII
		"a?b&c=d",                      // characters that must survive escaping
	}
	w := newWriteHarness(t)
	w.seed.lib.addNarrators(names...)

	code, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/narrators", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	byName := map[string]string{}
	for _, entry := range requireArray(t, body, "narrators") {
		if n, _ := entry.(map[string]any); n != nil {
			name, _ := n["name"].(string)
			id, _ := n["id"].(string)
			byName[name] = id
		}
	}

	for _, name := range names {
		got, present := byName[name]
		if !present {
			t.Errorf("narrator %q missing from the response", name)
			continue
		}
		want := url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name)))
		if got != want {
			t.Errorf("id for %q = %q, want %q (real ABS formula)", name, got, want)
		}
		// The id travels in a URL PATH segment (Narrator.imageURL builds
		// api/narrators/<id>/image), so a raw '/' would split the path and address a
		// different route. Base64 emits '/', which is why the escape is required.
		if strings.Contains(got, "/") {
			t.Errorf("id for %q = %q contains a raw '/', which breaks the image URL path", name, got)
		}
	}
}

// TestNarrators_NumBooksOmittedRatherThanZero — there is no reverse narrator->book
// index, so a real count would need a library scan on a request path. The field is
// optional in the client; omitting it beats rendering "0 books" beside every name.
func TestNarrators_NumBooksOmittedRatherThanZero(t *testing.T) {
	w := newWriteHarness(t)
	w.seed.lib.addNarrators("Victor Bevine")
	_, body, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/narrators", nil)
	for _, raw := range requireArray(t, body, "narrators") {
		n, _ := raw.(map[string]any)
		if n == nil {
			continue
		}
		if v, present := n["numBooks"]; present {
			if count, ok := v.(float64); ok && count == 0 {
				t.Fatal("numBooks is present and 0 — omit it instead of claiming the narrator has no books")
			}
		}
	}
}

// TestAuthors_PaginatedConformsToOracle diffs our paginated body against a capture
// from real ABS 2.36.0 taken with the CLIENT'S OWN query string. The pre-existing
// authors fixture was captured without query parameters, so it pinned the bare shape
// and could never have caught this.
func TestAuthors_PaginatedConformsToOracle(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/authors?sort=name&minified=1&limit=100&page=0", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	assertConformant(t, "get_api_libraries_id_authors_paginated.json", body)
}
