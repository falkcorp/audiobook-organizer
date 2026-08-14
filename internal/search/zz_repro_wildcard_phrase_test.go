// file: internal/search/zz_repro_wildcard_phrase_test.go
// version: 1.0.0
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

// TestQuotedPhraseWithLeadingStopword is a CHARACTERIZATION test: it asserts
// what the code does today, which is NOT what the owner asked for.
//
// `"All Jobs"` still returns three books. The phrase machinery above is
// correct — the cause is that "all" is an English stopword, so the analyser
// reduces the phrase to the single term "jobs" before it is ever matched, and
// a one-term phrase is just a term query. Every phrase whose distinguishing
// word is a stopword degrades the same way.
//
// Fixing it means indexing these fields with an analyser that keeps stopwords,
// which changes the index mapping and requires a full re-index of the library
// — deliberately out of scope for this change and filed separately.
//
// This test exists so the limitation cannot be silently forgotten: when the
// stopword work lands, this test WILL fail, and that failure is the signal to
// delete it and fold the case into TestQuotedPhraseIsAPhrase above.
func TestQuotedPhraseWithLeadingStopword(t *testing.T) {
	idx := wildcardPhraseCorpus(t)

	got := runQuery(t, idx, `"All Jobs"`)
	if !contains(got, "alljobs") {
		t.Errorf(`"All Jobs" must at least find the book containing it: got %v`, got)
	}
	if len(got) == 1 {
		t.Errorf("STOPWORD LIMITATION APPEARS FIXED: %q now matches exactly %v. "+
			"Delete this characterization test and move the case into "+
			"TestQuotedPhraseIsAPhrase.", `"All Jobs"`, got)
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
