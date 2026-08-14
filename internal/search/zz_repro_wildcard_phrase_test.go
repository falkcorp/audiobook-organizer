// file: internal/search/zz_repro_wildcard_phrase_test.go
// version: 1.1.0
// guid: 8c4d1f06-2b93-4a57-91e8-73f5a0c6d284
// last-edited: 2026-08-13

package search

import (
	"strings"
	"testing"
)

// Repro for two defects reported by the owner on 2026-08-13, both reproduced
// against production first:
//
//  1. A trailing '*' returns ZERO results whenever the term is capitalised.
//     Prod: "Hyperion*" -> 0 but "hyperion*" -> 21; "Dragon*" -> 0 but
//     "dragon*" -> 1757. Prefix/wildcard queries bypass the field analyser, so
//     they are compared against already-lowercased index terms without being
//     lowercased themselves. The web UI appends '*', so this hits ordinary
//     typing.
//
//  2. A quoted phrase is not a phrase. Prod: `"All Jobs"` returned 300 rows,
//     byte-identical to the unquoted query, topped by "Side Jobs" and "The
//     Icarus Job" — neither of which contains the phrase.
//
// Both assert PRECISION (decoys absent), not merely presence: a MatchAll
// satisfies a presence-only assertion, which is exactly how these survived.

func wildcardPhraseCorpus(t *testing.T) *BleveIndex {
	t.Helper()
	idx := openTestIndex(t)
	for _, d := range []BookDocument{
		{BookID: "hyperion", Title: "Hyperion", Author: "Dan Simmons"},
		{BookID: "alljobs", Title: "All Jobs and Classes", Author: "Comedian0 L"},
		{BookID: "sidejobs", Title: "Side Jobs", Author: "Jim Butcher"},
		{BookID: "icarus", Title: "The Icarus Job", Author: "Timothy Zahn"},
		{BookID: "dragon", Title: "Dragon Conjurer", Author: "Eric Vall"},

		// Word-order decoys. These carry every word of the phrases under
		// test but never adjacently and in order, so they are matched by a
		// conjunction and rejected by a true phrase query. WITHOUT them the
		// phrase assertions pass even when the parser fix is reverted —
		// verified by mutation — because AND-matching alone happens to
		// return the same single book. They are what makes this test
		// measure phrase behaviour rather than mere co-occurrence.
		{BookID: "decoy_side", Title: "Jobs on the Side", Author: "Nobody"},
		{BookID: "decoy_dragon", Title: "Conjurer of the Dragon", Author: "Nobody"},

		// Stopword decoys, for the mapping change that stopped the analyser
		// deleting stopwords. Each is chosen to be indistinguishable from
		// the target UNLESS the stopword itself is indexed and positioned:
		//
		//   decoy_jobsforall — carries "Jobs" and "All" but not adjacent
		//                      and in that order, so it separates a real
		//                      `"All Jobs"` phrase from a bare `jobs` term.
		//                      Under the old mapping "All Jobs" analysed to
		//                      the single token [jobs@2] and matched this.
		//   decoy_oddjobs    — carries "Jobs" with NO "All" at all: the
		//                      cheapest possible witness that the phrase
		//                      degraded to a term query.
		//   lotr / decoy_lotr — differ only in the INTERIOR stopword. The
		//                      old mapping produced [lord@1, ring@4], a
		//                      four-slot phrase with slots 2-3 left nil,
		//                      which bleve treats as wildcards — so "Lord
		//                      ANY ANY Rings" matched both. This pair is
		//                      the only assertion here that can detect the
		//                      interior-wildcard behaviour.
		{BookID: "decoy_jobsforall", Title: "Jobs for All", Author: "Nobody"},
		{BookID: "decoy_oddjobs", Title: "Odd Jobs", Author: "Nobody"},
		{BookID: "lotr", Title: "Lord of the Rings", Author: "J R R Tolkien"},
		{BookID: "decoy_lotr", Title: "Lord of All Rings", Author: "Nobody"},

		// Recall guard for the 2026-08-11 "shards of oblivion" bug. Keeping
		// stopwords in the index must NOT make unquoted queries strict.
		{BookID: "shards", Title: "Shards of Oblivion", Author: "Nobody"},
	} {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}
	return idx
}

func runQuery(t *testing.T, idx *BleveIndex, q string) []string {
	t.Helper()
	ast, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	bq, _, err := Translate(ast)
	if err != nil {
		t.Fatalf("translate %q: %v", q, err)
	}
	if bq == nil {
		t.Fatalf("Translate returned nil for %q — caller turns this into MatchAll", q)
	}
	hits, _, err := idx.SearchNative(bq, 0, 50)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	return hitIDs(hits)
}

// TestWildcardIsCaseInsensitive pins defect 1. A trailing '*' must find the
// same books regardless of how the user capitalised the term.
func TestWildcardIsCaseInsensitive(t *testing.T) {
	idx := wildcardPhraseCorpus(t)

	for _, q := range []string{"hyperion*", "Hyperion*", "HYPERION*", "Hyperio*"} {
		t.Run(q, func(t *testing.T) {
			got := runQuery(t, idx, q)
			if len(got) == 0 {
				t.Fatalf("%q returned NOTHING; want the hyperion book. "+
					"A trailing * must not silently empty the result set.", q)
			}
			if !contains(got, "hyperion") {
				t.Errorf("%q did not find hyperion: got %v", q, got)
			}
			for _, bad := range []string{"sidejobs", "icarus", "dragon"} {
				if contains(got, bad) {
					t.Errorf("IMPRECISE: %q matched unrelated %q: got %v", q, bad, got)
				}
			}
		})
	}
}

// TestQuotedPhraseIsAPhrase pins defect 2: a quoted phrase must match only
// books containing that phrase. "Side Jobs" must not drag in "All Jobs and
// Classes", and vice versa.
func TestQuotedPhraseIsAPhrase(t *testing.T) {
	idx := wildcardPhraseCorpus(t)

	for _, tc := range []struct {
		query string
		want  string
	}{
		{`"Side Jobs"`, "sidejobs"},
		{`"Icarus Job"`, "icarus"},
		{`"Dragon Conjurer"`, "dragon"},
		{`"Jobs and Classes"`, "alljobs"},
		{`"Jobs on the Side"`, "decoy_side"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got := runQuery(t, idx, tc.query)
			if len(got) != 1 || !contains(got, tc.want) {
				t.Errorf("%s should match exactly [%s], got %v", tc.query, tc.want, got)
			}
		})
	}
}

// TestQuotedPhraseWithStopword covers the owner's original example, which the
// phrase fix alone did NOT solve: `"All Jobs"` returned 300 rows on prod
// because "all" was a stopword the analyser deleted before matching.
//
// This was a characterization test pinning that limitation until 2026-08-13,
// when the index mapping moved to a stopword-preserving analyzer
// (bookTextAnalyzerName). It now asserts the intended behaviour.
//
// The two cases are different failure modes of one cause, and each needs its
// own decoy to be detectable at all — see the corpus comments:
//
//	leading  — "All Jobs" collapsed to a 1-token phrase == a bare term query
//	interior — "Lord of the Rings" became "Lord ANY ANY Rings"
func TestQuotedPhraseWithStopword(t *testing.T) {
	idx := wildcardPhraseCorpus(t)

	t.Run("leading stopword", func(t *testing.T) {
		got := runQuery(t, idx, `"All Jobs"`)
		if !contains(got, "alljobs") {
			t.Errorf(`"All Jobs" must find the book containing it: got %v`, got)
		}
		// The whole point: a phrase whose first word is a stopword must
		// still constrain word order.
		if contains(got, "decoy_jobsforall") {
			t.Errorf(`"All Jobs" matched "Jobs for All" — the stopword is not `+
				`constraining order, so the phrase is behaving as a `+
				`conjunction: got %v`, got)
		}
		if contains(got, "decoy_oddjobs") || contains(got, "sidejobs") {
			t.Errorf(`"All Jobs" matched a book with no "all" at all — the `+
				`phrase degraded to a bare "jobs" term query: got %v`, got)
		}
		if len(got) != 1 {
			t.Errorf(`"All Jobs" should match exactly [alljobs], got %v`, got)
		}
	})

	t.Run("interior stopwords are not wildcards", func(t *testing.T) {
		got := runQuery(t, idx, `"Lord of the Rings"`)
		if !contains(got, "lotr") {
			t.Errorf(`"Lord of the Rings" must find lotr: got %v`, got)
		}
		if contains(got, "decoy_lotr") {
			t.Errorf(`"Lord of the Rings" matched "Lord of All Rings" — the `+
				`dropped stopwords left wildcard slots in the phrase `+
				`instead of exact terms: got %v`, got)
		}
		if len(got) != 1 {
			t.Errorf(`"Lord of the Rings" should match exactly [lotr], got %v`, got)
		}
	})
}

// TestUnquotedStopwordRecallUnchanged is the counterweight to the mapping
// change: indexing stopwords must not make ORDINARY unquoted search strict.
//
// Regression guard for the 2026-08-11 "shards of oblivion returns nothing"
// bug. Whitespace parses as AND, so an unquoted three-word query becomes a
// conjunction; if "of" survives as a required conjunct that the target does
// not satisfy in every field, recall collapses again. dropStopwordOnlyConjuncts
// still removes it because it detects stopwords with the STOCK English
// analyzer, deliberately decoupled from the field mapping.
func TestUnquotedStopwordRecallUnchanged(t *testing.T) {
	idx := wildcardPhraseCorpus(t)

	for _, q := range []string{
		"shards of oblivion",
		"lord of the rings",
		"all jobs and classes",
	} {
		t.Run(q, func(t *testing.T) {
			got := runQuery(t, idx, q)
			if len(got) == 0 {
				t.Fatalf("unquoted %q returned NOTHING — a stopword conjunct "+
					"is taking the whole query down", q)
			}
		})
	}

	if got := runQuery(t, idx, "shards of oblivion"); !contains(got, "shards") {
		t.Errorf("unquoted 'shards of oblivion' lost its book: got %v", got)
	}
}

// TestUnquotedStillBroad guards the other direction: making quotes strict must
// NOT make the ordinary unquoted query strict too.
func TestUnquotedStillBroad(t *testing.T) {
	idx := wildcardPhraseCorpus(t)

	got := runQuery(t, idx, "Jobs")
	if !contains(got, "alljobs") || !contains(got, "sidejobs") {
		t.Errorf("unquoted 'Jobs' should still match both job books broadly, got %v", got)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
