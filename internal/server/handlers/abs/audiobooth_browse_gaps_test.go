// file: internal/server/handlers/abs/audiobooth_browse_gaps_test.go
// version: 1.0.1
// guid: 21a537e2-8789-4517-9ef9-576957a6fe05
// last-edited: 2026-09-25

package abs_test

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// Item-6 behaviour findings B1/B2/B3: responses that decoded fine but carried
// the wrong content.

type browseGapsFixture struct {
	h    *harness
	lib  *fakeLibrary
	tok  string
	sync map[string]string // book id -> sync id
	book map[string]string // sync id -> book id
}

func newBrowseGapsFixture(t *testing.T) *browseGapsFixture {
	t.Helper()
	lib := newFakeLibrary()
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	intp := func(i int) *int { return &i }
	i64p := func(i int64) *int64 { return &i }
	now := time.UnixMilli(1785370000000)
	lib.series[7] = &database.Series{ID: 7, Name: "Fiction Saga"}
	lib.series[8] = &database.Series{ID: 8, Name: "Other Saga"}

	add := func(b *database.Book, files ...database.BookFile) {
		b.Title = b.ID
		b.LibraryState = strp("organized")
		b.IsPrimaryVersion = boolp(true)
		b.CreatedAt, b.UpdatedAt = &now, &now
		b.Duration = intp(600)
		lib.addBook(b, files, nil)
	}
	file := func(book string, size int64) database.BookFile {
		// A path that does not exist, so the mapper's os.Stat override does not
		// apply and the displayed size is the stored one.
		return database.BookFile{ID: book + "-f", BookID: book, FilePath: "/nonexistent/" + book + ".m4b",
			FileSize: size, Duration: 600, Format: "m4b"}
	}
	// big: Book.FileSize tiny, files large. small: the reverse. nofiles: fallback.
	add(&database.Book{ID: "big", Genre: strp("Fiction, Adventure"), Language: strp("English"),
		Publisher: strp("Tor"), AudiobookReleaseYear: intp(1999), SeriesID: intp(7), FileSize: i64p(10)},
		file("big", 1000))
	add(&database.Book{ID: "small", Genre: strp("History"), Language: strp("German"),
		PrintYear: intp(2012), SeriesID: intp(8), FileSize: i64p(5000)},
		file("small", 100))
	add(&database.Book{ID: "nofiles", Genre: strp("Fiction"), FileSize: i64p(500)})
	lib.tags = map[string][]string{"favorite": {"small"}}

	provider, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress: lib, Bookmarks: newFakeBookmarks(), Identity: lib, Library: lib, AliasUses: lib,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := &oracleSeed{lib: lib, root: t.TempDir()}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(provider))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	f := &browseGapsFixture{h: h, lib: lib, tok: tok, sync: map[string]string{}, book: map[string]string{}}
	for _, id := range []string{"big", "small", "nofiles"} {
		s, err := lib.MintOrGetSyncID(id)
		if err != nil {
			t.Fatal(err)
		}
		f.sync[id], f.book[s] = s, id
	}
	return f
}

func b64(s string) string { return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(s))) }

// items returns the book ids (in order) and total of an /items request.
func (f *browseGapsFixture) items(t *testing.T, query string) ([]string, int) {
	t.Helper()
	code, body := f.h.doAny(t, request{method: http.MethodGet,
		path: "/api/libraries/" + f.h.libraryID() + "/items?" + query, headers: bearer(f.tok)})
	if code != http.StatusOK {
		t.Fatalf("items?%s = %d", query, code)
	}
	m := obj(t, body)
	raw, _ := m["results"].([]any)
	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		id, _ := obj(t, r)["id"].(string)
		ids = append(ids, f.book[id])
	}
	total, _ := m["total"].(float64)
	return ids, int(total)
}

func sortedCopy(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

// ── B1: filter groups beyond series/authors/narrators ───────────────────────

func TestItemsFilter_AttributeGroupsMatchRenderedFields(t *testing.T) {
	f := newBrowseGapsFixture(t)
	for _, tc := range []struct {
		filter string
		want   []string
	}{
		// a part of a comma-separated genre (what the item renders)...
		{"genres." + b64("Fiction"), []string{"big", "nofiles"}},
		// ...and the whole raw value (what /filterdata offers)
		{"genres." + b64("Fiction, Adventure"), []string{"big"}},
		{"genres." + b64("Cookery"), []string{}},
		{"languages." + b64("english"), []string{"big"}},
		{"publishers." + b64("Tor"), []string{"big"}},
		// release year wins over print year, as in yearString
		{"publishedDecades." + b64("1990"), []string{"big"}},
		{"publishedDecades." + b64("2010"), []string{"small"}},
		{"tags." + b64("favorite"), []string{"small"}},
	} {
		got, total := f.items(t, "filter="+tc.filter+"&limit=50")
		if !slices.Equal(sortedCopy(got), sortedCopy(tc.want)) || total != len(tc.want) {
			t.Errorf("filter %s = %v (total %d), want %v", tc.filter, got, total, tc.want)
		}
	}
}

func TestItemsFilter_ProgressGroupIsPerUser(t *testing.T) {
	f := newBrowseGapsFixture(t)
	patch := func(syncID string, body map[string]any) {
		t.Helper()
		if rec, _ := f.h.do(t, request{method: http.MethodPatch, path: "/api/me/progress/" + syncID,
			headers: bearer(f.tok), body: body}); rec.Code != http.StatusOK {
			t.Fatalf("PATCH progress = %d %s", rec.Code, rec.Body.String())
		}
	}
	patch(f.sync["big"], map[string]any{"isFinished": true, "currentTime": 600.0})
	patch(f.sync["small"], map[string]any{"currentTime": 120.0})

	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"finished", []string{"big"}},
		{"in-progress", []string{"small"}},
		{"not-started", []string{"nofiles"}},
		{"not-finished", []string{"small", "nofiles"}},
	} {
		got, total := f.items(t, "filter=progress."+b64(tc.state)+"&limit=50")
		if !slices.Equal(sortedCopy(got), sortedCopy(tc.want)) || total != len(tc.want) {
			t.Errorf("progress.%s = %v (total %d), want %v", tc.state, got, total, tc.want)
		}
	}
	// An unknown state is an empty page, never the whole library.
	if got, total := f.items(t, "filter=progress."+b64("bogus")); len(got) != 0 || total != 0 {
		t.Errorf("progress.bogus = %v (total %d), want empty", got, total)
	}
}

// ── B2: the series tab honours ?filter= ─────────────────────────────────────

func TestSeriesFilter_KeepsOnlySeriesWithAMatchingBook(t *testing.T) {
	f := newBrowseGapsFixture(t)
	names := func(query string) ([]string, int) {
		t.Helper()
		code, body := f.h.doAny(t, request{method: http.MethodGet,
			path: "/api/libraries/" + f.h.libraryID() + "/series?limit=50&" + query, headers: bearer(f.tok)})
		if code != http.StatusOK {
			t.Fatalf("series?%s = %d", query, code)
		}
		m := obj(t, body)
		raw, _ := m["results"].([]any)
		out := []string{}
		for _, r := range raw {
			n, _ := obj(t, r)["name"].(string)
			out = append(out, n)
		}
		total, _ := m["total"].(float64)
		return out, int(total)
	}
	if all, _ := names(""); len(all) != 2 {
		t.Fatalf("unfiltered series = %v, want both", all)
	}
	got, total := names("filter=genres." + b64("Adventure"))
	if !slices.Equal(got, []string{"Fiction Saga"}) || total != 1 {
		t.Fatalf("series filtered by genre = %v (total %d), want [Fiction Saga]", got, total)
	}
	got, total = names("filter=languages." + b64("German"))
	if !slices.Equal(got, []string{"Other Saga"}) || total != 1 {
		t.Fatalf("series filtered by language = %v (total %d), want [Other Saga]", got, total)
	}
	got, total = names("filter=nosuchgroup." + b64("x"))
	if len(got) != 0 || total != 0 {
		t.Fatalf("unknown filter group on series = %v (total %d), want an empty page", got, total)
	}
}

// ── B3: sort=size orders by the size the items display ──────────────────────

func TestItemsSort_SizeUsesTheDisplayedSummedSize(t *testing.T) {
	f := newBrowseGapsFixture(t)
	// Displayed sizes: big 1000 (files), nofiles 500 (Book.FileSize fallback),
	// small 100 (files) — Book.FileSize would have said small 5000 > big 10.
	want := []string{"big", "nofiles", "small"}
	if got, _ := f.items(t, "sort=size&desc=1&limit=50"); !slices.Equal(got, want) {
		t.Fatalf("sort=size desc = %v, want %v", got, want)
	}
	slices.Reverse(want)
	if got, _ := f.items(t, "sort=size&limit=50"); !slices.Equal(got, want) {
		t.Fatalf("sort=size asc = %v, want %v", got, want)
	}
	// Same order inside a filtered drill-down.
	if got, _ := f.items(t, "filter=genres."+b64("Fiction")+"&sort=size&desc=1"); !slices.Equal(got, []string{"big", "nofiles"}) {
		t.Fatalf("filtered sort=size desc = %v, want [big nofiles]", got)
	}
}

// ── B5: author name sort is case-insensitive ────────────────────────────────

func TestAuthorsSort_NameIsCaseInsensitive(t *testing.T) {
	w := newWriteHarness(t)
	w.seed.lib.addAuthor(501, "Zed Upper", w.seed.singleID)
	w.seed.lib.addAuthor(502, "alpha lower", w.seed.singleID)
	w.seed.lib.addAuthor(503, "Mid Upper", w.seed.multiID)
	names := func(query string) []string {
		t.Helper()
		code, body, raw := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors?limit=100"+query, nil)
		if code != http.StatusOK {
			t.Fatalf("authors = %d %s", code, raw)
		}
		out := []string{}
		for _, n := range authorNames(t, body, "results") {
			if n == "Zed Upper" || n == "alpha lower" || n == "Mid Upper" {
				out = append(out, n)
			}
		}
		return out
	}
	want := []string{"alpha lower", "Mid Upper", "Zed Upper"}
	for _, q := range []string{"", "&sort=name"} {
		if got := names(q); !slices.Equal(got, want) {
			t.Errorf("authors%s order = %v, want %v (a lowercase initial must not sort after Z)", q, got, want)
		}
	}
	slices.Reverse(want)
	if got := names("&sort=name&desc=1"); !slices.Equal(got, want) {
		t.Errorf("authors name desc = %v, want %v", got, want)
	}
}

// Review W6: filter requests — every group, every page — are served from the
// cached attribute index; the whole-library BookCore walk happens once.
func TestItemsFilter_AttributeIndexIsCachedAcrossRequestsAndPages(t *testing.T) {
	f := newBrowseGapsFixture(t)
	f.items(t, "filter=genres."+b64("Fiction")+"&limit=1&page=0")
	first := f.lib.coreWalkCalls()
	if first == 0 {
		t.Fatal("the first filter request did not build the index; the counter is not observing it")
	}
	f.items(t, "filter=genres."+b64("Fiction")+"&limit=1&page=1")
	f.items(t, "filter=languages."+b64("German"))
	f.items(t, "filter=publishedDecades."+b64("1990"))
	if got := f.lib.coreWalkCalls(); got != first {
		t.Fatalf("GetAllBooksCore ran %d times across 4 filter requests, want %d (the first build only)", got, first)
	}
}

// Re-review LOW #4: a book write (which bumps the library generation) must
// invalidate the cached filter index immediately, not after its TTL.
func TestItemsFilter_BookWriteInvalidatesTheAttributeIndex(t *testing.T) {
	f := newBrowseGapsFixture(t)
	if got, _ := f.items(t, "filter=genres."+b64("Horror")); len(got) != 0 {
		t.Fatalf("precondition: no Horror books, got %v", got)
	}
	horror := "Horror"
	f.lib.mu.Lock()
	f.lib.books["small"].Genre = &horror
	f.lib.mu.Unlock()
	f.lib.gen.Bump() // what UpdateBook does on the real store
	if got, _ := f.items(t, "filter=genres."+b64("Horror")); !slices.Equal(got, []string{"small"}) {
		t.Fatalf("after a book write the Horror filter returned %v, want [small]: the index was served stale", got)
	}
}
