// file: internal/audiobooks/service_search_cache_key_test.go
// version: 1.0.0
// guid: 3f6a9d21-7c4e-4b58-a1e2-8d0f5c3b7e96
// last-edited: 2026-09-25

package audiobooks

import (
	"testing"
)

// Review findings 15/16: a shared key never merges two queries the engine
// evaluates differently.
func TestNormalizeSearchKey_NeverMergesDifferentQueries(t *testing.T) {
	type pair struct{ a, b string }
	bleveDiffer := []pair{
		{"primal hunter", "primal hunter"}, // NBSP is not a parser separator
		{"primal\nhunter", "primal hunter"},
		{"alpha_bravo", "alpha bravo"},
		{"a AND b", "a and b"},
	}
	for _, p := range bleveDiffer {
		if NormalizeSearchKey(p.a, false) == NormalizeSearchKey(p.b, false) {
			t.Errorf("bleve: %q and %q share a key", p.a, p.b)
		}
	}
	bleveSame := []pair{{"Alpha  Bravo", "alpha bravo"}, {"  alpha\tbravo ", "alpha bravo"}}
	for _, p := range bleveSame {
		if NormalizeSearchKey(p.a, false) != NormalizeSearchKey(p.b, false) {
			t.Errorf("bleve: %q and %q should share a key", p.a, p.b)
		}
	}
	substringDiffer := []pair{{"rock ", "rock"}, {" rock", "rock"}}
	for _, p := range substringDiffer {
		if NormalizeSearchKey(p.a, true) == NormalizeSearchKey(p.b, true) {
			t.Errorf("substring: %q and %q share a key", p.a, p.b)
		}
	}
	if NormalizeSearchKey("Foo_Bar", true) != NormalizeSearchKey("foo bar", true) {
		t.Error("substring: the '_' fold the store applies should share a key")
	}
}
