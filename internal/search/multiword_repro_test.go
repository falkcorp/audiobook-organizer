// file: internal/search/multiword_repro_test.go
// version: 1.0.0
// guid: 6f2b9d41-8c05-4e37-a1d9-3b7e0c5a824f
// last-edited: 2026-08-11

package search

import "testing"

// Reported 2026-08-11: "when there are spaces in the word search doesn't find
// them. In the app or in the web interface."
//
// Exercises the exact path the API uses for a library search box query:
// ParseQuery -> Translate -> SearchNative. The parser documents whitespace as
// AND, so a two-word query should require both terms and still match a book
// whose title contains both.
func TestMultiWordFreeTextFindsTheBook(t *testing.T) {
	idx := openTestIndex(t)

	docs := []BookDocument{
		{BookID: "ascend", Title: "Ascend Online", Author: "Luke Chmilenko"},
		{BookID: "other", Title: "Completely Different", Author: "Nobody"},
		{BookID: "shards", Title: "Shards of Oblivion", Author: "Some Author"},
	}
	for _, d := range docs {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}

	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"single word", "Ascend", "ascend"},
		{"two words", "Ascend Online", "ascend"},
		{"two words lowercase", "ascend online", "ascend"},
		{"title field two words", "title:Ascend Online", "ascend"},
		{"quoted phrase", `"Ascend Online"`, "ascend"},
		{"author two words", "Luke Chmilenko", "ascend"},
		// Owner's actual failing example, 2026-08-11. "of" is an English
		// stopword: if the index analyzer drops it but the parser still ANDs a
		// match query for it, the conjunction can never be satisfied and the
		// whole search returns nothing.
		{"stopword in the middle", "shards of oblivion", "shards"},
		{"stopword capitalized", "Shards of Oblivion", "shards"},
		{"same query without the stopword", "shards oblivion", "shards"},
		{"trailing space", "shards of oblivion ", "shards"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.query, err)
			}
			bq, _, err := Translate(ast)
			if err != nil {
				t.Fatalf("translate %q: %v", tc.query, err)
			}
			hits, total, err := idx.SearchNative(bq, 0, 10)
			if err != nil {
				t.Fatalf("search %q: %v", tc.query, err)
			}
			if total == 0 {
				t.Fatalf("query %q returned NO results; want to find %q", tc.query, tc.want)
			}
			found := false
			for _, h := range hits {
				if h.BookID == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("query %q did not return %q; got %v (total=%d)",
					tc.query, tc.want, hitIDs(hits), total)
			}
		})
	}
}
