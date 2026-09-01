// file: internal/personname/author_credit_test.go
// version: 1.0.0
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

// TestChooseAuthorSideDecoratedSideStillWins is the round-4 inversion stated at
// the level of the shared function rather than through a caller.
func TestChooseAuthorSideDecoratedSideStillWins(t *testing.T) {
	for _, p := range []TiePolicy{PreferRightOnTie, RefuseOnTie} {
		_, author, ok := ChooseAuthorSide("Good Omens", "Neil Gaiman and Terry Pratchett", p)
		if !ok || author != "Neil Gaiman and Terry Pratchett" {
			t.Errorf("policy %v: author = %q, ok=%v; want the credit list", p, author, ok)
		}
	}
}
