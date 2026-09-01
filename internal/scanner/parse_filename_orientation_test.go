// file: internal/scanner/parse_filename_orientation_test.go
// version: 1.0.0
// guid: 1fc4b119-f608-404a-9c62-e580bb5d51c0
// last-edited: 2026-09-01

package scanner

import "testing"

// TestParseFilenameDoesNotFileTheTitleAsTheAuthor pins the inversion found in
// review round 4.
//
// parseFilenameForAuthor uses "does this side look like a name?" to choose an
// ORIENTATION. That makes a FALSE dangerous in a way it is not anywhere else in
// this change: it does not cause a miss, it flips the assignment. When the
// shared predicate replaced the old per-package one it became strict about
// decoration -- LooksLikePersonName("Neil Gaiman (Unabridged)") is false
// because of the trailing paren, not because Neil Gaiman is not a person -- and
// every "<title> - <decorated or compound author>" filename fell into the
// "Author - Title" branch and stored the TITLE as the author.
//
// Nothing downstream can catch that. The minted author is person-shaped by
// construction (it passed LooksLikePersonName on the left), so it is not the
// placeholder, and re-running the predicate on it returns true.
//
// "(Unabridged)" is native to this library -- internal/titleutil exists to
// strip it -- so this is a common shape, not an exotic one.
func TestParseFilenameDoesNotFileTheTitleAsTheAuthor(t *testing.T) {
	cases := []struct {
		filename   string
		wantAuthor string
	}{
		// The regression: right side fails the bare-name predicate for
		// DECORATION, and must still be recognised as the author.
		{"Good Omens - Neil Gaiman (Unabridged)", "Neil Gaiman (Unabridged)"},
		{"The Hobbit - Neil Gaiman [Unabridged]", "Neil Gaiman [Unabridged]"},
		{"Dune - Frank Herbert (Dramatized Adaptation)", "Frank Herbert (Dramatized Adaptation)"},
		// Right side fails for being a CREDIT LIST rather than one name.
		{"Good Omens - Neil Gaiman and Terry Pratchett", "Neil Gaiman and Terry Pratchett"},
		{"Good Omens - Neil Gaiman & Terry Pratchett", "Neil Gaiman & Terry Pratchett"},
		{"Good Omens - Neil Gaiman, Terry Pratchett", "Neil Gaiman, Terry Pratchett"},
		// Non-ASCII author, decorated -- the two bugs together.
		{"Good Omens - Émile Zola (Unabridged)", "Émile Zola (Unabridged)"},
		{"Good Omens - 村上 春樹 (Unabridged)", "村上 春樹 (Unabridged)"},
		// The "Author - Title" orientation must still work when the right side
		// is genuinely NOT a credit. This is the half of the behaviour that a
		// naive "make the branch refuse" fix would have destroyed.
		{"Neil Gaiman - The Sandman Omnibus Volume One (Unabridged)", "Neil Gaiman"},
		{"Neil Gaiman - A Game of Thrones (Unabridged)", "Neil Gaiman"},
	}
	for _, tc := range cases {
		_, gotAuthor := parseFilenameForAuthor(tc.filename)
		if gotAuthor != tc.wantAuthor {
			t.Errorf("parseFilenameForAuthor(%q) author = %q, want %q",
				tc.filename, gotAuthor, tc.wantAuthor)
		}
	}
}

// TestParseFilenameNeverMintsAKnownTitleAsAuthor states the invariant directly
// rather than by example: for a corpus where the author side is known, the
// author must never come back as the title side.
func TestParseFilenameNeverMintsAKnownTitleAsAuthor(t *testing.T) {
	titles := []string{"Good Omens", "Anansi Boys", "The Stand", "Dune"}
	authors := []string{"Neil Gaiman", "Émile Zola", "Ursula Le Guin"}
	decos := []string{"", " (Unabridged)", " [Unabridged]"}
	coauthors := []string{"", " and Terry Pratchett", " & Terry Pratchett"}

	for _, ti := range titles {
		for _, a := range authors {
			for _, d := range decos {
				for _, c := range coauthors {
					name := ti + " - " + a + c + d
					_, gotAuthor := parseFilenameForAuthor(name)
					if gotAuthor == ti {
						t.Errorf("parseFilenameForAuthor(%q) filed the TITLE %q as the author",
							name, ti)
					}
				}
			}
		}
	}
}
