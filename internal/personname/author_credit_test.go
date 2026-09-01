// file: internal/personname/author_credit_test.go
// version: 1.2.0
// guid: 3c1d3f97-1705-4519-a344-cc8eb9f0d038
// last-edited: 2026-09-01

package personname

import "testing"

// TestLooksLikeAuthorCreditSeparators pins the separator set. Each of these
// survived a mutation that deleted it from creditSeparatorRe, which means
// nothing in the tree observed the difference.
//
// "/" matters most: internal/dedup treats it as an author separator AND tries
// it FIRST, so omitting it here gives one repo two answers to the same
// question. It cannot be reached through a filename (a path separator cannot
// appear in one), which is exactly why it needs a direct test -- the
// consumer-level differentials structurally cannot see it.
func TestLooksLikeAuthorCreditSeparators(t *testing.T) {
	credits := []string{
		"Neil Gaiman / Terry Pratchett",
		"Neil Gaiman + Terry Pratchett",
		"Neil Gaiman & Terry Pratchett",
		"Neil Gaiman and Terry Pratchett",
		"Neil Gaiman with Terry Pratchett",
		"Neil Gaiman, Terry Pratchett",
		"Neil Gaiman; Terry Pratchett",
		"Émile Zola / 村上 春樹",
	}
	for _, c := range credits {
		if !LooksLikeAuthorCredit(c) {
			t.Errorf("LooksLikeAuthorCredit(%q) = false, want true", c)
		}
	}

	// A title clause anywhere poisons the whole credit -- every clause must be
	// a name, not merely one of them.
	notCredits := []string{
		"The Old Man and the Sea",
		"Pride and Prejudice",
		"Crime and Punishment",
		"Fear and Loathing in Las Vegas",
		"Coffee with Milk",
		"Neil Gaiman / the quick brown",
		"Neil Gaiman and Volume Two",
	}
	for _, c := range notCredits {
		if LooksLikeAuthorCredit(c) {
			t.Errorf("LooksLikeAuthorCredit(%q) = true, want false", c)
		}
	}
}

// TestLooksLikeAuthorCreditStripsRepeatedEditionMarkers pins that the strip is
// a repeat, not a single group. With a single group "X (Unabridged) (2019)"
// is not a credit, and the caller then concludes the OTHER side is the author.
func TestLooksLikeAuthorCreditStripsRepeatedEditionMarkers(t *testing.T) {
	for _, s := range []string{
		"Neil Gaiman (Unabridged)",
		"Neil Gaiman [Unabridged]",
		"Neil Gaiman (Unabridged) (2019)",
		"Neil Gaiman [Unabridged] (Dramatized Adaptation)",
		"Neil Gaiman (Unabridged) (2019) [Retail]",
	} {
		if !LooksLikeAuthorCredit(s) {
			t.Errorf("LooksLikeAuthorCredit(%q) = false, want true", s)
		}
	}
	if got := StripEditionSuffix("Neil Gaiman (Unabridged) (2019)"); got != "Neil Gaiman" {
		t.Errorf("StripEditionSuffix = %q, want %q", got, "Neil Gaiman")
	}
	// The placeholder wearing an edition marker is still the placeholder. The
	// guard in internal/scanner compares against this stripped form; without
	// the strip it walks past a check written to catch it.
	if got := StripEditionSuffix("Unknown Author (Unabridged)"); got != "Unknown Author" {
		t.Errorf("StripEditionSuffix = %q, want %q", got, "Unknown Author")
	}
}

// TestChooseAuthorSideTiePolicy pins the one place the four call sites
// legitimately differ. A tie is "both sides plausible, neither discriminator
// firing" -- "Good Omens" and "Neil Gaiman" are the same shape.
func TestChooseAuthorSideTiePolicy(t *testing.T) {
	// PreferRightOnTie: the " - " convention.
	if title, author, ok := ChooseAuthorSide("Good Omens", "Neil Gaiman", PreferRightOnTie); !ok ||
		author != "Neil Gaiman" || title != "Good Omens" {
		t.Errorf("PreferRightOnTie = (%q, %q, %v), want (Good Omens, Neil Gaiman, true)", title, author, ok)
	}
	// RefuseOnTie: the "_" path, which must NOT invent an orientation. The old
	// scanner code refused here, and defaulting would mint an author where it
	// deliberately minted none.
	if _, author, ok := ChooseAuthorSide("Good Omens", "Neil Gaiman", RefuseOnTie); ok {
		t.Errorf("RefuseOnTie returned ok=true with author %q, want refusal", author)
	}
	// A tie policy must not override a real discriminator: an article still
	// decides, under BOTH policies.
	for _, p := range []TiePolicy{PreferRightOnTie, RefuseOnTie} {
		if _, author, ok := ChooseAuthorSide("Stephen King", "The Stand", p); !ok || author != "Stephen King" {
			t.Errorf("policy %v: article tiebreak gave author %q, ok=%v; want Stephen King", p, author, ok)
		}
		if _, author, ok := ChooseAuthorSide("J.K. Rowling", "Harry Potter", p); !ok || author != "J.K. Rowling" {
			t.Errorf("policy %v: initials tiebreak gave author %q, ok=%v; want J.K. Rowling", p, author, ok)
		}
	}
	// Neither side plausible: refuse under both policies.
	for _, p := range []TiePolicy{PreferRightOnTie, RefuseOnTie} {
		if _, _, ok := ChooseAuthorSide("Disc 1", "Track 2", p); ok {
			t.Errorf("policy %v: accepted two structural markers", p)
		}
	}
}

// TestChooseAuthorSideDoesNotFileAnOmnibusTitleAsTheAuthor pins the round-6
// regression. A "multi-name credit beats a single name" discriminator was added
// here and removed again: it tested whether a string splits into two or more
// person-shaped clauses, which is a multi-CLAUSE test, and titles have clauses.
// A two-work omnibus title is structurally identical to a two-author credit.
func TestChooseAuthorSideDoesNotFileAnOmnibusTitleAsTheAuthor(t *testing.T) {
	cases := []struct{ left, right, wantAuthor string }{
		{"Norse Mythology and Anansi Boys", "Neil Gaiman", "Neil Gaiman"},
		{"Red Rising, Golden Son", "Pierce Brown", "Pierce Brown"},
		{"Black Sun, Red Moon", "J.K. Rowling", "J.K. Rowling"},
		// The credit-list cases the deleted discriminator was added for still
		// resolve correctly on the tie policy alone when the credit is on the
		// right, which is the dominant convention in this library (measured:
		// 57 "Title - AUTHOR" against 9 "AUTHOR - Title" in a real sample).
		{"Good Omens", "Neil Gaiman and Terry Pratchett", "Neil Gaiman and Terry Pratchett"},
		{"Good Omens", "Neil Gaiman & Terry Pratchett", "Neil Gaiman & Terry Pratchett"},
		{"Good Omens", "Neil Gaiman, Terry Pratchett", "Neil Gaiman, Terry Pratchett"},
	}
	for _, tc := range cases {
		_, author, ok := ChooseAuthorSide(tc.left, tc.right, PreferRightOnTie)
		if !ok || author != tc.wantAuthor {
			t.Errorf("ChooseAuthorSide(%q, %q) author = %q, ok = %v; want %q",
				tc.left, tc.right, author, ok, tc.wantAuthor)
		}
	}
}

// TestChooseAuthorSideDecoratedSideStillWins is the round-4 inversion stated at
// the level of the shared function rather than through a caller.
func TestChooseAuthorSideDecoratedSideStillWins(t *testing.T) {
	// Under the " - " convention the credit list on the right wins.
	_, author, ok := ChooseAuthorSide("Good Omens", "Neil Gaiman and Terry Pratchett", PreferRightOnTie)
	if !ok || author != "Neil Gaiman and Terry Pratchett" {
		t.Errorf("PreferRightOnTie: author = %q, ok=%v; want the credit list", author, ok)
	}

	// Under RefuseOnTie it REFUSES, and that is correct rather than a shortfall.
	// An earlier version of this test asserted the credit list under BOTH
	// policies, and satisfying it is what motivated a "multi-name credit beats a
	// single name" discriminator -- which turned out to be a multi-CLAUSE test
	// that filed omnibus titles as authors. "Good Omens" and
	// "Neil Gaiman and Terry Pratchett" are not structurally separable, so on a
	// separator carrying no orientation convention the honest answer is no
	// answer. A refusal leaves the field for AI nomination; a guess does not.
	if _, author, ok := ChooseAuthorSide("Good Omens", "Neil Gaiman and Terry Pratchett", RefuseOnTie); ok {
		t.Errorf("RefuseOnTie: returned author %q, want refusal", author)
	}
}

// TestChooseAuthorSideAmpersandCredit covers the narrow replacement for the
// deleted multi-clause discriminator. Deleting it outright was measured on
// 68,793 real library paths and cost 4 rows in the AUTHOR-first orientation,
// all joined by "&"; this restores them without restoring the omnibus-title
// inversion, because "&" is not ordinary title punctuation the way "and" and a
// comma are.
func TestChooseAuthorSideAmpersandCredit(t *testing.T) {
	cases := []struct{ left, right, wantAuthor, why string }{
		{"Elora Bishop & Bridget Essex", "Under Her Spell", "Elora Bishop & Bridget Essex",
			"an ampersand-joined co-author credit on the LEFT still wins"},
		{"David Weber & John Ringo", "March Upcountry", "David Weber & John Ringo",
			"same, second real example"},
		{"Under Her Spell", "Elora Bishop & Bridget Essex", "Elora Bishop & Bridget Essex",
			"and the dominant right-hand orientation is unaffected"},

		// Ordering is load-bearing. The ampersand test is the WEAKEST of the
		// three and must run after the article and initials tests. With it
		// first, both of these filed the title as the author -- the exact defect
		// this change exists to remove. Both titles are real library rows.
		{"The City & The City", "China Mieville", "China Mieville",
			"a leading article beats an ampersand"},
		{"The Savage & The Crown", "Neil Gaiman", "Neil Gaiman",
			"same, second real title"},
	}
	for _, tc := range cases {
		_, author, ok := ChooseAuthorSide(tc.left, tc.right, PreferRightOnTie)
		if !ok || author != tc.wantAuthor {
			t.Errorf("ChooseAuthorSide(%q, %q) author = %q, ok = %v; want %q (%s)",
				tc.left, tc.right, author, ok, tc.wantAuthor, tc.why)
		}
	}
}

// TestAmpersandCreditIsWeakEvidenceNotALaw records a KNOWN limitation rather
// than a desired behaviour, so that a future reader does not mistake the
// predicate for a general "is this a co-author credit" test. Of 323 distinct
// "&"/"+" segments in the real library, looksLikeAmpersandCredit accepts 33 and
// roughly half of those are titles. This one is accepted as wrong: it survives
// only because it needs no article and no initials on either side, and because
// the real library row is HTML-escaped ("&amp;") and so never reaches here.
// origin/main is equally wrong on it -- its multi-clause rule fires too -- so
// this is a documented limit, not a regression.
func TestAmpersandCreditIsWeakEvidenceNotALaw(t *testing.T) {
	_, author, _ := ChooseAuthorSide("Magic Tides & Magic Claims", "Ilona Andrews", PreferRightOnTie)
	if author != "Magic Tides & Magic Claims" {
		t.Fatalf("author = %q; this test PINS a known-wrong answer -- if it now "+
			"returns \"Ilona Andrews\" the predicate got better and this test "+
			"and its comment should be updated, not deleted", author)
	}
}
