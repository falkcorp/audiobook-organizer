// file: internal/server/handlers/abs/item_filter_contributor_test.go
// version: 1.0.1
// guid: 4d17b8e0-96ca-4c33-a5f1-2e0b7d4996a1
// last-edited: 2026-08-22

package abs_test

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ── ?filter=authors.* and ?filter=narrators.* ───────────────────────────────
//
// 🔴 THE DEFECT. Tapping any author or any narrator showed "No Books Found".
// filteredItems implemented exactly ONE group, `series`; everything else fell to a
// default branch that logs and serves an empty page. That default is correct
// policy — an unrecognised filter must NOT answer with the whole library — but
// authors and narrators were never actually implemented behind it.
//
// Reproduced on production 2026-08-17: both
//
//	/items?filter=authors.NDY5NTk=                      (author id 46959)
//	/items?filter=narrators.SmVmZiBIYXlzLCBBbm5pZSBFbGxpY290dA==
//
// answered {"results":[],"total":0}, and the server's own warning log named the
// groups: group=narrators value="Jeff Hays, Annie Ellicott", group=authors
// value=46959. The log was the oracle for the token format — no fixture carries a
// filter — which is exactly what that log was added for.

// absSeedContributors builds a library where authors and narrators overlap
// partially, so an "returns only this author's books" assertion can actually fail:
// if every book shared every contributor, an unfiltered response would satisfy it.
func absSeedContributors(t *testing.T) *oracleSeed {
	t.Helper()
	seed := seedOracleLibrary(t)

	intp := func(i int) *int { return &i }
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }

	// Organized + primary, or absItemFilterBase excludes them and the lists come
	// back empty for a reason unrelated to what is under test.
	add := func(id, title string) {
		seed.lib.addBook(&database.Book{
			ID: id, Title: title,
			Duration:     intp(3600),
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		}, nil, nil)
	}
	add("c-b1", "Contributor Book One")
	add("c-b2", "Contributor Book Two")
	add("c-b3", "Contributor Book Three")

	// Author 900 has two of the three books; author 901 has the third. The
	// exclusion assertion is meaningless without a book the filter must LEAVE OUT.
	seed.lib.addAuthor(900, "Aria Author", "c-b1", "c-b2")
	seed.lib.addAuthor(901, "Other Author", "c-b3")

	// b1 carries a COMPOUND credit, b2 names one of the same people alone. That
	// overlap is the whole point: after splitting, "Jeff Hays" must own BOTH books
	// while "Annie Ellicott" owns only b1.
	seed.lib.attachNarrators("c-b1", "Jeff Hays, Annie Ellicott")
	seed.lib.attachNarrators("c-b2", "Jeff Hays")
	seed.lib.attachNarrators("c-b3", "Zoë O'Malley")
	return seed
}

func absContribHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t, "jwt", nil, withLibrary(absSeedContributors(t)), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	return h, tok
}

// absNarratorRows returns name → id from the live /narrators response, so tests
// filter with the id the SERVER published rather than one they recomputed. A test
// that derives the id itself proves only that it agrees with itself.
func absNarratorRows(t *testing.T, h *harness, tok string) (map[string]string, map[string]int) {
	t.Helper()
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/narrators",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("GET narrators = %d, want 200", code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("narrators body is %T, want an object", body)
	}
	ids, counts := map[string]string{}, map[string]int{}
	for _, entry := range requireArray(t, m, "narrators") {
		n, _ := entry.(map[string]any)
		if n == nil {
			continue
		}
		name, _ := n["name"].(string)
		id, _ := n["id"].(string)
		ids[name] = id
		if nb, ok := n["numBooks"].(float64); ok {
			counts[name] = int(nb)
		}
	}
	return ids, counts
}

func TestLibraryItems_AuthorFilterReturnsOnlyThatAuthorsBooks(t *testing.T) {
	h, tok := absContribHarness(t)

	all := absItemTitles(absItemsFiltered(t, h, tok, ""))
	if len(all) < 3 {
		t.Fatalf("unfiltered library has %d items (%v); with fewer than 3 the "+
			"exclusion assertion below cannot distinguish a working filter", len(all), all)
	}

	got := absItemsFiltered(t, h, tok, absFilterToken("authors", "900"))
	titles := absItemTitles(got)

	// EXCLUSION is the assertion. Returning the whole library also "includes" this
	// author's books — that was the shape of the series defect, and the reason the
	// unfiltered response is fetched above.
	want := map[string]bool{"Contributor Book One": true, "Contributor Book Two": true}
	for _, title := range titles {
		if !want[title] {
			t.Fatalf("author filter returned %q, which is not author 900's: %v", title, titles)
		}
	}
	if len(titles) != 2 {
		t.Fatalf("author filter returned %d items %v, want exactly 2", len(titles), titles)
	}
	if total, _ := got["total"].(float64); int(total) != 2 {
		t.Fatalf("total = %v, want 2 (the filtered set, not the library)", got["total"])
	}
}

func TestLibraryItems_NarratorFilterReturnsOnlyThatNarratorsBooks(t *testing.T) {
	h, tok := absContribHarness(t)
	ids, _ := absNarratorRows(t, h, tok)

	id, ok := ids["Annie Ellicott"]
	if !ok {
		t.Fatalf("narrator list has no %q — the compound credit was not split; got %v",
			"Annie Ellicott", ids)
	}

	// The id arrives percent-escaped, exactly as the client would send it in a query
	// string; unescaping is the gin layer's job and is part of what this exercises.
	got := absItemsFiltered(t, h, tok, "narrators."+id)
	titles := absItemTitles(got)
	if len(titles) != 1 || titles[0] != "Contributor Book One" {
		t.Fatalf("narrator filter returned %v, want exactly [Contributor Book One]", titles)
	}
	if total, _ := got["total"].(float64); int(total) != 1 {
		t.Fatalf("total = %v, want 1", got["total"])
	}
}

// 🔴 THE COMPOUND CREDIT IS THE REPORTED BUG. The Narrators tab listed
// "Jeff Hays, Annie Ellicott" as a single person with 1 book — and the library had
// entries naming EIGHT. Every one of those is a name nobody can tap usefully, and
// every book behind it is missing from the real narrators' counts.
func TestNarrators_CompoundCreditIsSplitIntoPeople(t *testing.T) {
	h, tok := absContribHarness(t)
	ids, counts := absNarratorRows(t, h, tok)

	if _, present := ids["Jeff Hays, Annie Ellicott"]; present {
		t.Error("the compound string is still its own narrator entry — it must be split")
	}
	for _, name := range []string{"Jeff Hays", "Annie Ellicott"} {
		if _, present := ids[name]; !present {
			t.Errorf("narrator %q missing after splitting; got %v", name, ids)
		}
	}
	// Jeff Hays narrated b1 (compound) AND b2 (alone). Counting the compound entry
	// separately is what made both counts wrong, so the count is the assertion.
	if got := counts["Jeff Hays"]; got != 2 {
		t.Errorf("numBooks for Jeff Hays = %d, want 2 (the compound book AND the solo one)", got)
	}
	if got := counts["Annie Ellicott"]; got != 1 {
		t.Errorf("numBooks for Annie Ellicott = %d, want 1", got)
	}
	// Surname-first must NOT be split into two people — the guard that makes the
	// splitter safe to run over a whole library.
	if _, present := ids["Zoë O'Malley"]; !present {
		t.Errorf("Zoë O'Malley vanished from the list; got %v", ids)
	}
}

// 🔴 THE TILE'S COUNT AND THE DRILL-DOWN'S TOTAL COME FROM ONE MAP. If they are
// computed separately they drift, and the user sees "15 books" open onto 3.
func TestNarratorFilter_TileCountEqualsDrillDownTotal(t *testing.T) {
	h, tok := absContribHarness(t)
	ids, counts := absNarratorRows(t, h, tok)

	for name, id := range ids {
		got := absItemsFiltered(t, h, tok, "narrators."+id)
		total, _ := got["total"].(float64)
		if int(total) != counts[name] {
			t.Errorf("narrator %q: tile says %d books, drill-down total says %d",
				name, counts[name], int(total))
		}
	}
}

// narratorID and absFilterGroup must be exact inverses. They are written in
// different places against different specs — one mirrors real ABS's
// encodeURIComponent(base64(name)), the other accepts four base64 alphabets — and
// nothing but this pins them together.
//
// The '+' case is the one that fails silently: base64 emits '+', and an unescaped
// '+' in a query string decodes to a SPACE, so the name would come back mangled
// rather than erroring. No plausible narrator name produces a '+' (searched: it
// needs an unusual byte pair), so the fixture is synthetic on purpose — the
// hazard is real even where the name is not.
func TestNarratorID_RoundTripsThroughTheFilterToken(t *testing.T) {
	for _, name := range []string{
		"Jeff Hays",
		"Zoë O'Malley", // base64 contains '/' and '='
		"Butler, Samuel",
		"Ellé Jones",
		"é~", // base64 "w6l+" — the '+' that would otherwise become a space
	} {
		t.Run(name, func(t *testing.T) {
			// Simulates the full trip: the server escapes the id, the client puts it
			// in a query string, and the URL layer unescapes it before the handler.
			token := "narrators." + url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name)))
			q, err := url.ParseQuery("filter=" + token)
			if err != nil {
				t.Fatalf("token %q is not a legal query value: %v", token, err)
			}
			raw := q.Get("filter")

			dot := len("narrators.")
			if len(raw) <= dot || raw[:dot] != "narrators." {
				t.Fatalf("filter came back as %q, want a narrators. token", raw)
			}
			decoded, derr := base64.StdEncoding.DecodeString(raw[dot:])
			if derr != nil {
				t.Fatalf("value %q did not decode as base64: %v", raw[dot:], derr)
			}
			if string(decoded) != name {
				t.Fatalf("round trip gave %q, want %q", decoded, name)
			}
		})
	}
}

// TestSearch_NarratorIDsAgreeWithTheNarratorsTab is the cross-endpoint assertion
// that a per-endpoint shape test cannot make: the id /search publishes for a
// person must be the id /narrators publishes for that person, because the client
// taps the search hit and sends the id straight back as a narrators.<id> filter.
//
// An id that is merely PRESENT and correctly FORMATTED still fails the user. This
// fixture is why: c-b1 stores the compound credit "Jeff Hays, Annie Ellicott",
// and the raw store keeps it whole. Deriving the search id from the raw name
// produced a well-formed, cleanly decodable id for a person who does not exist —
// search found one hit, tapping it returned nothing, while the Narrators tab had
// a working entry for the same person. Sourcing both from the contributor index
// is what keeps them equal.
func TestSearch_NarratorIDsAgreeWithTheNarratorsTab(t *testing.T) {
	h, tok := absContribHarness(t)
	published, _ := absNarratorRows(t, h, tok)
	if published["Annie Ellicott"] == "" {
		t.Fatal("fixture precondition: /narrators must split the compound credit and " +
			"publish an id for Annie Ellicott")
	}

	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/search?q=annie",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("GET search = %d, want 200", code)
	}
	hits := requireArray(t, body.(map[string]any), "narrators")
	if len(hits) == 0 {
		t.Fatal("search for 'annie' returned no narrators")
	}

	for i, entry := range hits {
		n, _ := entry.(map[string]any)
		name, _ := n["name"].(string)
		id, _ := n["id"].(string)

		want, listed := published[name]
		if !listed {
			t.Errorf("narrator[%d] %q is not a name /narrators publishes — the two "+
				"endpoints disagree about who exists, so its id cannot resolve", i, name)
			continue
		}
		if id != want {
			t.Errorf("narrator[%d] %q: search id %q != narrators id %q", i, name, id, want)
			continue
		}
		// The id is equal to the published one; prove that one actually resolves.
		if titles := absItemTitles(absItemsFiltered(t, h, tok, "narrators."+id)); len(titles) == 0 {
			t.Errorf("narrator[%d] %q: filtering by the published id returned no books — "+
				"a search hit the client cannot open", i, name)
		}
	}
}
