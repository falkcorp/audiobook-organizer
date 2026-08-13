// file: internal/search/zz_repro_alljobs_test.go
// version: 1.0.0
// guid: 4a1c7e93-0d62-4b58-9f31-6ad2c8b7e015
// last-edited: 2026-08-13

package search

import (
	"encoding/json"
	"testing"
)

// Throwaway repro for the 2026-08-13 report: searching "All Jobs and Classes"
// in the web UI returns unrelated books ("All in Charisma", "Dragon Conjurer").
// Asserts PRECISION, not just presence — the existing
// TestMultiWordFreeTextFindsTheBook only checks the wanted book is somewhere in
// the hits, which a MatchAll would satisfy.
func TestReproAllJobsAndClasses(t *testing.T) {
	idx := openTestIndex(t)

	docs := []BookDocument{
		{BookID: "target", Title: "All Jobs and Classes! I Just Wanted One Skill, But I Got Them All", Author: "Comedian0 L"},
		{BookID: "charisma", Title: "All in Charisma", Author: "Kyle West"},
		{BookID: "dragon", Title: "Dragon Conjurer", Author: "Eric Vall"},
		{BookID: "parallax", Title: "Parallax Rising", Author: "Christopher Hopper"},
		{BookID: "solo", Title: "Solo Leveling, Vol. 2", Author: "Chugong"},
	}
	for _, d := range docs {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}

	for _, q := range []string{
		"All Jobs and Classes",
		"all jobs",
		"all jobs and",
		`"All Jobs and Classes"`,
		"All Jobs",
	} {
		t.Run(q, func(t *testing.T) {
			ast, err := ParseQuery(q)
			if err != nil {
				t.Logf("PARSE ERROR (falls back to substring SearchBooks): %v", err)
				return
			}
			bq, _, err := Translate(ast)
			if err != nil {
				t.Logf("TRANSLATE ERROR: %v", err)
				return
			}
			if bq == nil {
				t.Errorf("Translate returned NIL query -> caller turns this into MatchAll -> whole library")
				return
			}
			j, _ := json.Marshal(bq)
			hits, total, err := idx.SearchNative(bq, 0, 10)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			t.Logf("query=%s", j)
			t.Logf("total=%d hits=%v", total, hitIDs(hits))
			if total > 1 {
				t.Errorf("IMPRECISE: %d hits for %q; only \"target\" should match. got=%v", total, q, hitIDs(hits))
			}
			if len(hits) == 0 || hits[0].BookID != "target" {
				t.Errorf("WRONG TOP HIT for %q: got %v, want target first", q, hitIDs(hits))
			}
		})
	}
}
