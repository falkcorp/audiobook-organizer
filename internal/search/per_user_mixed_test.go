// file: internal/search/per_user_mixed_test.go
// version: 1.0.0
// guid: 8f2a6d1c-5e47-4b93-b0a8-1c7d3e9f4a56
// last-edited: 2026-10-30

package search

import (
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mixedLibrary indexes a small Batman library and returns it with one viewer's rows.
//
//	b1 "Alpha Year One"      Bruce Wayne     no row
//	b2 "Alpha Hush"          Dick Grayson    finished
//	b3 "Knightfall"          Bruce Wayne     finished
//	b4 "The Long Halloween"  Harvey Dent     in_progress, 30%
//	b5 "Hush"                Bruce Wayne     no row
//
// plus a row for a book that is not in the index (it was deleted), which must never
// surface.
func mixedLibrary(t *testing.T) (*BleveIndex, map[string]*database.UserBookState) {
	t.Helper()
	idx := openTestIndex(t)
	for _, d := range []BookDocument{
		{BookID: "b1", Title: "Alpha Year One", Author: "Bruce Wayne"},
		{BookID: "b2", Title: "Alpha Hush", Author: "Dick Grayson"},
		{BookID: "b3", Title: "Knightfall", Author: "Bruce Wayne"},
		{BookID: "b4", Title: "The Long Halloween", Author: "Harvey Dent"},
		{BookID: "b5", Title: "Hush", Author: "Bruce Wayne"},
	} {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}
	rows := map[string]*database.UserBookState{
		"b2":   {BookID: "b2", Status: database.UserBookStatusFinished, ProgressPct: 100},
		"b3":   {BookID: "b3", Status: database.UserBookStatusFinished, ProgressPct: 100},
		"b4":   {BookID: "b4", Status: database.UserBookStatusInProgress, ProgressPct: 30},
		"gone": {BookID: "gone", Status: database.UserBookStatusFinished, ProgressPct: 100},
	}
	return idx, rows
}

// runMixed translates q the way every caller does, resolves it against rows and
// returns the matching ids, sorted.
func runMixed(t *testing.T, idx *BleveIndex, rows map[string]*database.UserBookState, q string) []string {
	t.Helper()
	now := time.Now()
	ast, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	if err := CheckQueryTerms(ast, now); err != nil {
		t.Fatalf("check %q: %v", q, err)
	}
	tr, err := TranslateQuery(ast, now)
	if err != nil {
		t.Fatalf("translate %q: %v", q, err)
	}
	if tr.Mixed == nil {
		t.Fatalf("%q did not take the tree route", q)
	}
	bq, err := tr.Mixed.Query(rows)
	if err != nil {
		t.Fatalf("resolve %q: %v", q, err)
	}
	hits, total, err := idx.SearchNative(bq, 0, 100)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	if uint64(len(hits)) != total {
		t.Fatalf("%q: %d hits but total %d", q, len(hits), total)
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.BookID)
	}
	slices.Sort(ids)
	return ids
}

func TestMixedQuery_OrOverAPerUserTerm(t *testing.T) {
	idx, rows := mixedLibrary(t)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		// The Alpha books plus the viewer's finished books. As an AND it was b2 alone.
		{"title:Alpha OR read_status:finished", []string{"b1", "b2", "b3"}},
		{"read_status:finished OR title:Alpha", []string{"b1", "b2", "b3"}},
		{"title:Alpha || read_status:finished", []string{"b1", "b2", "b3"}},
		// narrowed by a sibling conjunct
		{"(title:Alpha OR read_status:finished) AND author:Wayne", []string{"b1", "b3"}},
		{"author:Wayne (title:Alpha OR read_status:finished)", []string{"b1", "b3"}},
		// both sides per-user
		{"read_status:finished OR read_status:in_progress", []string{"b2", "b3", "b4"}},
		{"read_status:(finished|in_progress) OR title:Hush", []string{"b2", "b3", "b4", "b5"}},
		// a term a book with NO row satisfies (unstarted): b1 and b5 have no row
		{"title:Knightfall OR read_status:unstarted", []string{"b1", "b3", "b5"}},
		{"title:Halloween OR progress_pct:<50", []string{"b1", "b4", "b5"}},
		// a negated per-user term under an OR
		{"title:Halloween OR -read_status:finished", []string{"b1", "b4", "b5"}},
		// an OR nested in an OR / AND with per-user terms at several depths
		{"title:Halloween OR (author:Wayne AND read_status:finished)", []string{"b3", "b4"}},
		{"(title:Hush AND read_status:finished) OR (title:Knightfall AND read_status:finished)", []string{"b2", "b3"}},
		// no row can satisfy it: only the indexed side contributes
		{"title:Hush OR progress_pct:>100", []string{"b2", "b5"}},
		{"last_played:* OR title:Hush", []string{"b2", "b5"}},
	} {
		if got := runMixed(t, idx, rows, tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%q = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestMixedQuery_NotOverAGroupWithAPerUserTerm(t *testing.T) {
	idx, rows := mixedLibrary(t)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		// "not both": every book except the viewer's finished Wayne books
		{"-(read_status:finished author:Wayne)", []string{"b1", "b2", "b4", "b5"}},
		// "neither": the per-term negation the flat path applies is a different query
		{"NOT (read_status:finished OR title:Hush)", []string{"b1", "b4"}},
		// an odd number of NOTs is one NOT (an even number is the flat path's, below)
		{"NOT NOT NOT (read_status:finished author:Wayne)", []string{"b1", "b2", "b4", "b5"}},
		{"author:Wayne -(title:Alpha OR read_status:finished)", []string{"b5"}},
	} {
		if got := runMixed(t, idx, rows, tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%q = %v, want %v", tc.q, got, tc.want)
		}
	}
}

// A viewer with no rows (no user, or one who never opened a book) is judged as the
// zero state: nothing is finished, everything is unstarted.
func TestMixedQuery_NoRows(t *testing.T) {
	idx, _ := mixedLibrary(t)
	for _, rows := range []map[string]*database.UserBookState{nil, {}} {
		if got, want := runMixed(t, idx, rows, "title:Alpha OR read_status:finished"), []string{"b1", "b2"}; !slices.Equal(got, want) {
			t.Errorf("finished with no rows = %v, want %v", got, want)
		}
		if got, want := runMixed(t, idx, rows, "title:Knightfall OR read_status:unstarted"), []string{"b1", "b2", "b3", "b4", "b5"}; !slices.Equal(got, want) {
			t.Errorf("unstarted with no rows = %v, want %v", got, want)
		}
	}
}

// For every query the flat path answers, the tree route must give the same books:
// that is what makes the rewrite of a per-user term into an index query exact, and
// it holds the two routes together if either is changed.
func TestMixedQuery_AgreesWithTheFlatPathWhereBothApply(t *testing.T) {
	idx, rows := mixedLibrary(t)
	now := time.Now()
	for _, q := range []string{
		"read_status:finished",
		"-read_status:finished",
		"read_status:unstarted",
		"read_status:(finished|in_progress)",
		"-read_status:(finished|in_progress)",
		"read_status:finished author:Wayne",
		"title:Hush -read_status:finished",
		"progress_pct:>10 progress_pct:<=100",
		"progress_pct:<50",
		"last_played:*",
		"(title:Alpha OR title:Hush) read_status:finished",
		"NOT NOT (read_status:finished author:Wayne)",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		flatQ, filters, err := TranslateAt(ast, now)
		if err != nil {
			t.Fatalf("flat translate %q: %v", q, err)
		}
		hits, _, err := idx.SearchNative(flatQ, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, h := range hits {
			if MatchPerUserFilters(rows[h.BookID], filters, now) {
				want = append(want, h.BookID)
			}
		}
		slices.Sort(want)

		m, err := newMixedQuery(ast, now)
		if err != nil {
			t.Fatalf("mixed %q: %v", q, err)
		}
		bq, err := m.Query(rows)
		if err != nil {
			t.Fatal(err)
		}
		hits, _, err = idx.SearchNative(bq, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, h := range hits {
			got = append(got, h.BookID)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%q: tree route %v, flat route %v", q, got, want)
		}
	}
}
