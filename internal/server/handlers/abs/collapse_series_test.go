// file: internal/server/handlers/abs/collapse_series_test.go
// version: 1.0.0
// guid: c7e3a915-4b2d-4e08-9f61-2d85b0a7c4e3
// last-edited: 2026-09-26

package abs_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// ── ?collapseseries=1 ────────────────────────────────────────────────────────
//
// AudioBooth sends collapseseries=1 on the root library list when "Collapse
// series" is on, and we ignored it: every book of every series came back as its
// own tile. Real ABS (libraryItemsBookFilters.js getFilteredLibraryItems) lists
// each series ONCE, as its first book in sequence order carrying a
// collapsedSeries object; total and paging apply to the collapsed list; a title
// sort files the entry under the SERIES name; a series filter turns it off.
//
// The library below is built so each rule has a visible failure mode:
//   - Mistborn's books were inserted out of sequence order, so the
//     representative must come from the reading order, not insertion order.
//   - Zeta Cycle's only book is titled "Alpha Start": title-sorted by book it is
//     FIRST, by series name it is LAST. Only the series-name sort puts it last.
//   - Mistborn book 2 was added EARLIEST, so an addedAt sort that let a
//     non-representative stand in (or sorted by the series' minimum) would put
//     Mistborn first; the representative's own addedAt puts it third.

type collapseFixture struct {
	h    *harness
	tok  string
	sync map[string]string // book id -> sync id
}

func newCollapseFixture(t *testing.T) *collapseFixture {
	t.Helper()
	lib := newFakeLibrary()
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	intp := func(i int) *int { return &i }
	base := time.UnixMilli(1785370000000)

	lib.series[1] = &database.Series{ID: 1, Name: "Mistborn"}
	lib.series[2] = &database.Series{ID: 2, Name: "Zeta Cycle"}

	add := func(id, title string, series, seq *int, addedMin int, genre string) {
		at := base.Add(time.Duration(addedMin) * time.Minute)
		b := &database.Book{
			ID: id, Title: title, SeriesID: series, SeriesSequence: seq,
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
			CreatedAt: &at, UpdatedAt: &at, Duration: intp(600),
		}
		if genre != "" {
			b.Genre = strp(genre)
		}
		// Size (a handler-side sort) mirrors addedAt: book 2 is the smallest of
		// all, the representative (book 1) sits third among the entries.
		size := int64(100 * (addedMin + 1))
		b.FileSize = &size
		lib.addBook(b, nil, nil)
	}
	add("m3", "The Hero of Ages", intp(1), intp(3), 5, "Fiction")
	add("carrie", "Carrie", nil, nil, 1, "Horror")
	add("m1", "The Final Empire", intp(1), intp(1), 4, "")
	add("dune", "Dune", nil, nil, 2, "Fiction")
	add("m2", "The Well of Ascension", intp(1), intp(2), 0, "Fiction")
	add("z1", "Alpha Start", intp(2), intp(1), 6, "Horror")

	provider, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress: lib, Bookmarks: newFakeBookmarks(), Identity: lib, Library: lib, AliasUses: lib,
		AliasSeedCutoff: testAliasSeedCutoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "jwt", nil, withLibrary(&oracleSeed{lib: lib, root: t.TempDir()}), withUserData(provider))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	f := &collapseFixture{h: h, tok: tok, sync: map[string]string{}}
	for _, id := range []string{"m1", "m2", "m3", "carrie", "dune", "z1"} {
		s, err := lib.MintOrGetSyncID(id)
		if err != nil {
			t.Fatal(err)
		}
		f.sync[id] = s
	}
	return f
}

// collapseEntry is one /items result: the item's title, and its collapsedSeries.
type collapseEntry struct {
	title     string
	id        string
	collapsed map[string]any
}

// label is the entry's series name when collapsed, else its title.
func (e collapseEntry) label() string {
	if e.collapsed != nil {
		name, _ := e.collapsed["name"].(string)
		return "[" + name + "]"
	}
	return e.title
}

func (f *collapseFixture) items(t *testing.T, query string) ([]collapseEntry, int) {
	t.Helper()
	code, body := f.h.doAny(t, request{method: http.MethodGet,
		path: "/api/libraries/" + f.h.libraryID() + "/items?" + query, headers: bearer(f.tok)})
	if code != http.StatusOK {
		t.Fatalf("items?%s = %d", query, code)
	}
	m := obj(t, body)
	raw, _ := m["results"].([]any)
	out := make([]collapseEntry, 0, len(raw))
	for _, r := range raw {
		item, _ := r.(map[string]any)
		media, _ := item["media"].(map[string]any)
		meta, _ := media["metadata"].(map[string]any)
		e := collapseEntry{}
		e.title, _ = meta["title"].(string)
		e.id, _ = item["id"].(string)
		if cs, ok := item["collapsedSeries"]; ok {
			e.collapsed, _ = cs.(map[string]any)
			if e.collapsed == nil {
				t.Fatalf("collapsedSeries is %T, want an object (null crashes nothing but carries no card)", cs)
			}
		}
		out = append(out, e)
	}
	if want := strings.Contains(query, "collapseseries=1"); m["collapseseries"] != want {
		t.Fatalf("collapseseries echoed as %v, want %v", m["collapseseries"], want)
	}
	total, _ := m["total"].(float64)
	return out, int(total)
}

func labels(es []collapseEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.label()
	}
	return out
}

func TestCollapseSeries_TitleSortListsEachSeriesOnceUnderItsName(t *testing.T) {
	f := newCollapseFixture(t)

	plain, plainTotal := f.items(t, "limit=100&page=0&sort=media.metadata.title")
	if plainTotal != 6 || len(plain) != 6 {
		t.Fatalf("without collapseseries: %d items (total %d), want all 6: %v", len(plain), plainTotal, labels(plain))
	}
	for _, e := range plain {
		if e.collapsed != nil {
			t.Fatalf("collapsedSeries emitted without collapseseries=1: %v", labels(plain))
		}
	}

	got, total := f.items(t, "limit=100&page=0&sort=media.metadata.title&collapseseries=1")
	want := []string{"Carrie", "Dune", "[Mistborn]", "[Zeta Cycle]"}
	if !reflect.DeepEqual(labels(got), want) {
		t.Fatalf("collapsed title order = %v, want %v (a series files under its NAME, so "+
			"Zeta Cycle's 'Alpha Start' goes last, not first)", labels(got), want)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4: total counts the COLLAPSED list, or the client pages past the end", total)
	}

	mist := got[2]
	if mist.id != f.sync["m1"] || mist.title != "The Final Empire" {
		t.Fatalf("Mistborn is represented by %q (%s), want book 1 in reading order (%s)", mist.title, mist.id, f.sync["m1"])
	}
	cs := mist.collapsed
	if cs["id"] != "1" || cs["name"] != "Mistborn" || cs["nameIgnorePrefix"] != "Mistborn" || cs["sequence"] != "1" {
		t.Fatalf("collapsedSeries = %v, want id 1, name Mistborn, sequence 1", cs)
	}
	if n, _ := cs["numBooks"].(float64); n != 3 {
		t.Fatalf("numBooks = %v, want 3", cs["numBooks"])
	}
	ids, _ := cs["libraryItemIds"].([]any)
	wantIDs := []any{f.sync["m1"], f.sync["m2"], f.sync["m3"]}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("libraryItemIds = %v, want the series' items in reading order %v", ids, wantIDs)
	}
	if zeta := got[3].collapsed; zeta["numBooks"].(float64) != 1 {
		t.Fatalf("a one-book series still collapses with numBooks 1, got %v", zeta)
	}

	desc, _ := f.items(t, "limit=100&page=0&sort=media.metadata.title&desc=1&collapseseries=1")
	wantDesc := []string{"[Zeta Cycle]", "[Mistborn]", "Dune", "Carrie"}
	if !reflect.DeepEqual(labels(desc), wantDesc) {
		t.Fatalf("collapsed title desc = %v, want %v", labels(desc), wantDesc)
	}
}

func TestCollapseSeries_PagesTheCollapsedList(t *testing.T) {
	f := newCollapseFixture(t)
	var pages []string
	for page := 0; page < 4; page++ {
		got, total := f.items(t, "limit=1&sort=media.metadata.title&collapseseries=1&page="+string(rune('0'+page)))
		if total != 4 {
			t.Fatalf("page %d: total = %d, want 4 on every page", page, total)
		}
		if len(got) != 1 {
			t.Fatalf("page %d: %d items, want 1", page, len(got))
		}
		pages = append(pages, got[0].label())
	}
	want := []string{"Carrie", "Dune", "[Mistborn]", "[Zeta Cycle]"}
	if !reflect.DeepEqual(pages, want) {
		t.Fatalf("page by page = %v, want %v: each page must be cut from the collapsed list", pages, want)
	}
	past, total := f.items(t, "limit=1&page=4&sort=media.metadata.title&collapseseries=1")
	if len(past) != 0 || total != 4 {
		t.Fatalf("past the end: %d items, total %d; want 0 items, total 4", len(past), total)
	}
}

func TestCollapseSeries_OtherSortsUseTheRepresentativesOwnValue(t *testing.T) {
	f := newCollapseFixture(t)
	got, total := f.items(t, "limit=100&page=0&sort=addedAt&collapseseries=1")
	// Mistborn book 2 is the earliest-added book of all; book 1 (the
	// representative) is fourth. The entry sorts by book 1's value.
	want := []string{"Carrie", "Dune", "[Mistborn]", "[Zeta Cycle]"}
	if !reflect.DeepEqual(labels(got), want) || total != 4 {
		t.Fatalf("addedAt collapsed = %v (total %d), want %v", labels(got), total, want)
	}

	// size is ordered by the handler, not the store (absHandlerSort), on both
	// the unfiltered path and the ?filter= path.
	got, total = f.items(t, "limit=100&page=0&sort=size&collapseseries=1")
	if !reflect.DeepEqual(labels(got), want) || total != 4 {
		t.Fatalf("size collapsed = %v (total %d), want %v", labels(got), total, want)
	}
	got, total = f.items(t, "limit=100&page=0&sort=size&desc=1&collapseseries=1&filter=genres."+b64("Fiction"))
	// Fiction: m2 (100), dune (300), m3 (600). Book 2 represents Mistborn and
	// is the smallest, so desc puts Dune first.
	if wantF := []string{"Dune", "[Mistborn]"}; !reflect.DeepEqual(labels(got), wantF) || total != 2 {
		t.Fatalf("filtered size desc collapsed = %v (total %d), want %v", labels(got), total, wantF)
	}
}

func TestCollapseSeries_NotAppliedToASeriesFilter(t *testing.T) {
	f := newCollapseFixture(t)
	got, total := f.items(t, "limit=100&page=0&collapseseries=1&filter=series."+b64("1"))
	want := []string{"The Final Empire", "The Well of Ascension", "The Hero of Ages"}
	if !reflect.DeepEqual(labels(got), want) || total != 3 {
		t.Fatalf("series drill-down with collapseseries=1 = %v (total %d), want its books %v: "+
			"real ABS turns the collapse off for a series filter", labels(got), total, want)
	}
}

func TestCollapseSeries_AppliesWithinOtherFilters(t *testing.T) {
	f := newCollapseFixture(t)
	got, total := f.items(t, "limit=100&page=0&sort=media.metadata.title&collapseseries=1&filter=genres."+b64("Fiction"))
	want := []string{"Dune", "[Mistborn]"}
	if !reflect.DeepEqual(labels(got), want) || total != 2 {
		t.Fatalf("genre Fiction collapsed = %v (total %d), want %v", labels(got), total, want)
	}
	// Book 1 has no genre, so it is outside the filter: book 2 is the first
	// MATCHING book in reading order and stands in; libraryItemIds lists only the
	// matching books, numBooks the whole series.
	mist := got[1]
	if mist.id != f.sync["m2"] {
		t.Fatalf("representative = %s, want book 2 (%s), the first matching book", mist.id, f.sync["m2"])
	}
	ids, _ := mist.collapsed["libraryItemIds"].([]any)
	if !reflect.DeepEqual(ids, []any{f.sync["m2"], f.sync["m3"]}) {
		t.Fatalf("libraryItemIds = %v, want only the filtered books [m2 m3]", ids)
	}
	if n, _ := mist.collapsed["numBooks"].(float64); n != 3 {
		t.Fatalf("numBooks = %v, want 3 (the series, not the filtered subset)", mist.collapsed["numBooks"])
	}
	if mist.collapsed["sequence"] != "2" {
		t.Fatalf("sequence = %v, want the representative's own (2)", mist.collapsed["sequence"])
	}
}
