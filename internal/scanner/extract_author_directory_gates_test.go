// file: internal/scanner/extract_author_directory_gates_test.go
// version: 1.0.0
// guid: 2a97f31c-6d40-4b8e-9c05-7f31e8ab2064
// last-edited: 2026-09-01

package scanner

import "testing"

// TestExtractAuthorFromDirectoryGatesEveryBranch pins that ALL THREE branches of
// extractAuthorFromDirectory shape-check their candidate.
//
// The "- translator -" branch had no predicate at all -- not IsValidAuthor, not
// LooksLikePersonName -- and it is the FIRST branch tried, so it decided the
// author before either gated branch could run. It was missed the same way
// internal/dedup's slash branch was: the branches were gated one at a time by
// READING the function, and the corpus that measured the result contained no
// "- translator -" strings, so nothing reached it.
//
// The result of this function is assigned to book.Author, and per the
// Pratchett-036 finding recorded at scanner.go's title-fallback guard, a WRONG
// author is strictly worse than an absent one here: it closes the AI nomination
// gate and nothing downstream can recognise it as junk.
func TestExtractAuthorFromDirectoryGatesEveryBranch(t *testing.T) {
	cases := []struct {
		dir  string
		want string
		why  string
	}{
		// --- the translator/narrator branch (tried FIRST) ---
		{"Discworld - translator - Mort", "", "series name, not a person"},
		{"the quick brown - translator - Mort", "", "lowercase title fragment"},
		{"Unabridged - narrated by - Stephen Fry", "", "edition marker"},
		{"Book 3 - translator - Mort", "", "structural label"},
		{"Chapterhouse - narrated by - Stephen Fry", "", "series name"},
		{"Jane Smith - translator - The Book", "Jane Smith", "a real name must survive"},
		{"Ursula Le Guin - narrated by - Someone", "Ursula Le Guin", "particle name must survive"},
		{"村上 春樹 - translator - The Book", "村上 春樹", "non-ASCII name must survive"},

		// --- the "Author - Title" branch ---
		{"Jane Smith - The Book", "Jane Smith", "ordinary case"},
		{"Discworld - Mort", "", "series name reaches this branch too"},

		// --- the bare-directory branch ---
		{"Jane Smith", "Jane Smith", "ordinary case"},
		{"Discworld", "", "series name as a bare directory"},
		{"Book 3", "", "structural label"},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			got := extractAuthorFromDirectory("/lib/" + tc.dir + "/book.m4b")
			if got != tc.want {
				t.Errorf("extractAuthorFromDirectory(%q) = %q; want %q (%s)",
					tc.dir, got, tc.want, tc.why)
			}
		})
	}
}
