// file: internal/search/tag_field_test.go
// version: 1.0.0
// guid: 5c8e2a14-7d93-4b60-a1f5-3e9b0d7c4a28
// last-edited: 2026-10-26

package search

import (
	"sort"
	"testing"
)

// tagSearch runs q against idx and returns the sorted ids it matches.
func tagSearch(t *testing.T, idx *BleveIndex, q string) []string {
	t.Helper()
	ast, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	bq, _, err := Translate(ast)
	if err != nil {
		t.Fatalf("translate %q: %v", q, err)
	}
	hits, _, err := idx.SearchNative(bq, 0, 100)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	ids := hitIDs(hits)
	sort.Strings(ids)
	return ids
}

// TestTranslate_TagIsTheIndexedTagsField pins the "tag" spelling of the indexed
// "tags" field. The library search box writes tag:noir into the query (the chip,
// the typed term); the index field is "tags", and with no alias the term named a
// field the index does not hold and matched no book, so a whole-sent query that
// carried a tag term showed an empty list with no message.
//
// Case is folded the way GetBooksByTag folds it (tag:Noir finds the books
// tags[]=Noir finds). The index analyses a tag into words, so the in-query match
// is by word where tags[] is exact; the "by word" case below pins that.
func TestTranslate_TagIsTheIndexedTagsField(t *testing.T) {
	idx := openTestIndex(t)
	for _, d := range []BookDocument{
		{BookID: "b1", Title: "Hush", Tags: []string{"noir", "gotham"}},
		{BookID: "b2", Title: "Year One", Tags: []string{"noir"}},
		{BookID: "b3", Title: "The Long Halloween", Tags: []string{"gotham", "dark knight"}},
		{BookID: "b4", Title: "Knightfall"},
	} {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}

	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{"tag spelling", "tag:noir", []string{"b1", "b2"}},
		{"tags spelling", "tags:noir", []string{"b1", "b2"}},
		{"upper case is folded like GetBooksByTag", "tag:Noir", []string{"b1", "b2"}},
		{"tags upper case is folded too", "tags:NOIR", []string{"b1", "b2"}},
		{"quoted tag with a space", `tag:"dark knight"`, []string{"b3"}},
		{"quoted, folded", `tag:"Dark Knight"`, []string{"b3"}},
		{"alternation", "tag:(noir|gotham)", []string{"b1", "b2", "b3"}},
		{"alternation is folded", "tag:(NOIR|camelot)", []string{"b1", "b2"}},
		{"matches by word, where tags[] is exact", "tag:dark", []string{"b3"}},
		{"two tags both required", "tag:noir tag:gotham", []string{"b1"}},
		{"negated", "-tag:noir", []string{"b3", "b4"}},
		{"with another field", "tag:gotham title:Hush", []string{"b1"}},
		{"a tag nobody has", "tag:camelot", []string{}},
		{"prefix", "tag:nol*", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tagSearch(t, idx, tc.query)
			if !sameStrings(got, tc.want) {
				t.Fatalf("%q matched %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestIsIndexedField_AcceptsTag keeps the alias visible to the handler's
// "is this field one the index answers" check, so a filters= entry is not
// refused for a name the translator now routes.
func TestIsIndexedField_AcceptsTag(t *testing.T) {
	if !IsIndexedField("tag") || !IsIndexedField("tags") {
		t.Fatalf("tag and tags must both be answerable by the index")
	}
}
