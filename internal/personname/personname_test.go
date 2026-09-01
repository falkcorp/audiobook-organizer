// file: internal/personname/personname_test.go
// version: 1.0.0
// guid: 4b8d1f36-7c05-4e29-a930-2f9c3a7e5d61
// last-edited: 2026-09-01

package personname

import "testing"

// The corpus below is the differential evidence that motivated this package.
// Each `was` column records what the THREE previous copies answered, so a
// reader can see which copy this row was written against.
func TestLooksLikePersonName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		why  string
	}{
		// --- agreed by all three copies; must not change ---
		{"Isaac Asimov", true, "plain two-word name"},
		{"J.R.R. Tolkien", true, "collapsed initials"},
		{"J. K. Rowling", true, "spaced initials"},
		{"Ursula K. Le Guin", true, "four words with a middle initial"},
		{"José Saramago", true, "accent NOT in the first letter -- passed before too"},
		{"Søren Kierkegaard", true, "same"},
		{"A Game of Thrones", false, "interior lowercase function words mark a title"},
		{"Dune", false, "single word"},
		{"The Lord of the Rings", false, "too many words"},
		{"01", false, "purely numeric"},
		{"1984", false, "purely numeric"},
		{"Something (Unabridged)", false, "trailing edition parenthetical"},
		{"Fear and Loathing!", false, "sentence punctuation"},

		// --- scanner and metadata got these WRONG (ASCII byte comparison) ---
		{"Émile Zola", true, "first letter non-ASCII"},
		{"Åsa Larsson", true, "first letter non-ASCII"},
		{"Ítalo Calvino", true, "first letter non-ASCII"},
		{"Øyvind Torseter", true, "first letter non-ASCII"},
		{"Александр Пушкин", true, "Cyrillic"},
		{"村上 春樹", true, "CJK -- caseless, so IsUpper is false and IsLower must be the test"},
		{"Simone de Beauvoir", true, "lowercase name particle"},
		{"Ludwig van Beethoven", true, "lowercase name particle"},

		// --- dedup got these WRONG (no validity guard at all) ---
		{"Book 3", false, "structural marker, not a person"},
		{"Chapter 1", false, "structural marker"},
		{"Volume 2", false, "structural marker"},
		{"Disc 1", false, "structural marker"},

		// --- scanner and metadata got this WRONG (no punctuation guard) ---
		{"Do Androids Dream?", false, "question mark marks a title"},
	}
	for _, c := range cases {
		if got := LooksLikePersonName(c.in); got != c.want {
			t.Errorf("LooksLikePersonName(%q) = %v, want %v (%s)", c.in, got, c.want, c.why)
		}
	}
}

// TestCaselessScriptsAreNotRejected is the single most important row above,
// isolated so its failure message says what actually broke. Writing the
// capitalisation check as "must be uppercase" instead of "must not be
// lowercase" passes every ASCII test in this file and silently drops every
// Chinese, Japanese, Korean, Hebrew, Arabic and Thai author.
func TestCaselessScriptsAreNotRejected(t *testing.T) {
	for _, n := range []string{"村上 春樹", "김 은영", "משה כהן", "محمد علي", "สมชาย ใจดี"} {
		if !LooksLikePersonName(n) {
			t.Errorf("LooksLikePersonName(%q) = false; caseless scripts have no uppercase, "+
				"so the test must be !IsLower, never IsUpper", n)
		}
	}
}

func TestIsValidAuthor(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"", false}, {"Isaac Asimov", true}, {"12345", false},
		{"Book 3", false}, {"Chapter 1", false}, {"Volume 2", false},
		{"Disc 1", false}, {"Part 1", false}, {"Vol 2", false},
		{"Bookbinder Jones", false}, // known prefix-match limitation, pinned deliberately
	} {
		if got := IsValidAuthor(c.in); got != c.want {
			t.Errorf("IsValidAuthor(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
