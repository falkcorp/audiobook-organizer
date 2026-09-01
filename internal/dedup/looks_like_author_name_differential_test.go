// file: internal/dedup/looks_like_author_name_differential_test.go
// version: 1.0.0
// guid: 6b4e0d27-51a8-4c93-8f16-e0a7c25b39d4
// last-edited: 2026-09-01

package dedup

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// legacyAuthorName is internal/dedup's looksLikeAuthorName EXACTLY as it stood on
// origin/main, frozen here so the fifth copy of the person-name heuristic gets the
// same differential treatment the other four got in
// internal/personname/legacy_differential_test.go.
//
// It was unified without one. That omission is how the dropped
// "last word must not be lowercase" constraint shipped: composing the new version
// from LooksLikePersonName + a length rule LOOKED like it preserved everything,
// and nothing measured it in either direction. Do not "simplify" this function --
// it is a historical record, and its ASCII byte test is the bug, deliberately kept.
func legacyAuthorName(s string) bool {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) < 2 {
		return false
	}
	first := parts[0]
	if len(first) == 0 {
		return false
	}
	r := rune(first[0])
	if r < 'A' || r > 'Z' {
		return false
	}
	last := parts[len(parts)-1]
	lastTrimmed := strings.TrimRight(last, ".")
	if len(lastTrimmed) < 3 {
		return false
	}
	r = rune(last[0])
	return r >= 'A' && r <= 'Z'
}

// TestLooksLikeAuthorNameDifferential measures the fifth copy in BOTH directions
// and asserts the only intended one.
//
// Newly ADMITTED is the dangerous direction here: looksLikeAuthorName gates
// trySplitConcatenatedAuthors, and admitting one extra candidate does not merely
// add a split -- through scoreAuthorSplit it can make a WRONG split outscore the
// right one. That is exactly what admitting the particle "van" as a surname did:
//
//	"Ludwig van Beethoven Wolfgang Amadeus Mozart"
//	  correct 2-way ["Ludwig van Beethoven" "Wolfgang Amadeus Mozart"]  score 22
//	  garbage 3-way ["Ludwig van" "Beethoven Wolfgang" "Amadeus Mozart"] score 36
//
// So the assertion is: the ONLY strings the unified version may newly admit are
// ones the legacy version rejected for its ASCII byte test alone.
func TestLooksLikeAuthorNameDifferential(t *testing.T) {
	corpus := []string{
		// Particles as trailing words -- the regression this test exists for.
		"Ludwig van", "Vincent van", "Simone de", "Jose del", "Ana della",
		"Carlos dos", "Piet den", "Jan ter", "Omar bin", "Ali ibn", "Ian mac",
		// Real names, ASCII.
		"Ludwig van Beethoven", "Vincent van Gogh", "Simone de Beauvoir",
		"Wolfgang Amadeus Mozart", "R.A. Mejia", "Charles Dean", "Jane Smith",
		// Real names, non-ASCII first letter: legacy rejected ALL of these for the
		// byte test alone, and admitting them is the entire point of the change.
		"Émile Zola", "Åsa Larsson", "Ítalo Calvino", "Øyvind Torseter",
		"Александр Пушкин", "村上 春樹", "Ödipus Rex", "Über Wolken",
		// Initials as trailing words -- must stay refused (the surname rule).
		"Charles D.", "Mejia R.A.", "Jane S", "Bob X.",
		// Structural and title fragments.
		"Book 3", "Chapter 1", "the quick brown", "Do Androids Dream?",
		"Ann Petry (DBY)", "One Two Three Four Five", "So Long and Thanks",
		// Single words -- refused by both.
		"Tolkien", "Discworld", "",
	}

	newlyAdmitted, newlyRefused := 0, 0
	for _, in := range corpus {
		legacy := legacyAuthorName(in)
		unified := looksLikeAuthorName(in)
		switch {
		case !legacy && unified:
			newlyAdmitted++
			// Legitimate only when legacy's ASCII byte test is the sole reason.
			fields := strings.Fields(in)
			asciiOnlyReason := len(fields) >= 2 &&
				!isASCIIUpper([]rune(fields[0])[0]) &&
				personname.LooksLikePersonName(in)
			if !asciiOnlyReason {
				t.Errorf("NEWLY ADMITTED %q for a reason OTHER than the ASCII byte test. "+
					"looksLikeAuthorName gates trySplitConcatenatedAuthors, where an extra "+
					"candidate can make a wrong split OUTSCORE the right one.", in)
			} else {
				t.Logf("newly admitted (intended -- ASCII byte test only) %q", in)
			}
		case legacy && !unified:
			newlyRefused++
			t.Logf("newly refused (safe direction) %q", in)
		}
	}
	if newlyAdmitted == 0 {
		t.Error("no newly-admitted inputs -- the corpus no longer contains a " +
			"non-ASCII name, so it cannot demonstrate why the copy was unified")
	}
	t.Logf("fifth-copy differential: %d newly admitted, %d newly refused",
		newlyAdmitted, newlyRefused)
}

func isASCIIUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

// TestLooksLikeAuthorNameRejectsParticleSurnames is the narrow regression pin.
func TestLooksLikeAuthorNameRejectsParticleSurnames(t *testing.T) {
	for _, in := range []string{
		"Ludwig van", "Vincent van", "Jose del", "Ana della", "Carlos dos",
		"Piet den", "Jan ter", "Omar bin", "Ali ibn", "Ian mac",
	} {
		if looksLikeAuthorName(in) {
			t.Errorf("looksLikeAuthorName(%q) = true; want false. A name particle is "+
				"never a surname, and admitting one unlocks a 3-way split that "+
				"outscores the correct 2-way split.", in)
		}
	}
}
