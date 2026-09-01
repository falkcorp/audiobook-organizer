// file: internal/scanner/extract_info_from_path_policy_test.go
// version: 1.0.0
// guid: 69224863-2011-4701-864c-feaa3a9825e0
// last-edited: 2026-09-01

package scanner

import "testing"

// TestUnderscorePathRefusesOnTie pins the tie POLICY at this call site, not just
// in personname. The two separators carry different evidence: " - " resolves a
// tie as "Title - Author" by convention, while "_" refuses, because the
// pre-refactor code refused there and turning that into a default would mint an
// author where the old code deliberately minted none.
//
// Without this test, swapping RefuseOnTie for PreferRightOnTie here changes
// behaviour and every test still passes -- the policy was load-bearing and
// unobserved.
//
// The parent directory is "audiobooks", which is in the scanner's skip list, so
// the directory fallback cannot supply an author and mask the result.
func TestUnderscorePathRefusesOnTie(t *testing.T) {
	// Both halves are person-shaped, neither has an article or initials: a
	// genuine tie. "Good Omens" and "Neil Gaiman" are the same shape.
	b := &Book{FilePath: "/library/audiobooks/Good Omens_Neil Gaiman.m4b"}
	extractInfoFromPath(b)
	if b.Author != "" {
		t.Errorf("underscore tie produced Author = %q, want no author", b.Author)
	}

	// A tie policy must not suppress a real discriminator: an article still
	// decides, on this path too.
	b = &Book{FilePath: "/library/audiobooks/Stephen King_The Stand.m4b"}
	extractInfoFromPath(b)
	if b.Author != "Stephen King" {
		t.Errorf("underscore article tiebreak gave Author = %q, want %q", b.Author, "Stephen King")
	}

	// And the " - " path must still resolve its tie rather than refuse.
	b = &Book{FilePath: "/library/audiobooks/Good Omens - Neil Gaiman.m4b"}
	extractInfoFromPath(b)
	if b.Author != "Neil Gaiman" {
		t.Errorf("dash tie gave Author = %q, want %q", b.Author, "Neil Gaiman")
	}
}

// TestDecoratedPlaceholderIsStillCleared pins that the placeholder guard sees
// the edition-stripped form.
//
// personname.LooksLikeAuthorCredit accepts "Unknown Author (Unabridged)" BY
// DESIGN -- that is the whole point of the edition-strip clause. So the
// decorated placeholder became reachable as an author, while
// authorname.IsPlaceholder compares against the bare literal and cannot see it.
// It would then close the AI-nomination gate: the exact failure the guard
// exists to prevent, in a shape it was not written for.
func TestDecoratedPlaceholderIsStillCleared(t *testing.T) {
	for _, name := range []string{
		"Mort - Unknown Author",
		"Mort - Unknown Author (Unabridged)",
		"Mort - Unknown Author [Unabridged]",
		"Mort - Unknown Author (Unabridged) (2019)",
	} {
		b := &Book{FilePath: "/library/audiobooks/" + name + ".m4b"}
		extractInfoFromPath(b)
		if b.Author != "" {
			t.Errorf("%q left Author = %q, want cleared", name, b.Author)
		}
	}
}
