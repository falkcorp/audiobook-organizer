// file: internal/metadata/extract_author_directory_gates_test.go
// version: 1.1.0
// guid: 5c81b0e6-42fa-4d19-8b73-0a6d92e4f157
// last-edited: 2026-09-01

package metadata

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
// This WAS the near-twin of internal/scanner's copy, tested separately because
// the two had diverged before and a single test over one copy could not observe
// the other drifting. They are now collapsed into
// internal/authorname.ExtractAuthorFromDirectory, so drift is impossible by
// construction and that reason has expired.
//
// The test is kept anyway, and so is scanner's, for a different reason: each
// asserts from ITS OWN consumer's side. A shared unit test in authorname pins
// what the function returns; it cannot see that this package assigns the result
// to metadata.Artist and then clears the placeholder six lines later. Duplicate
// inputs are cheap; the consumer context is not reproducible elsewhere.
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
