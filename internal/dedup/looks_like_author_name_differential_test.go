// file: internal/dedup/looks_like_author_name_differential_test.go
// version: 1.1.0
// guid: 6b4e0d27-51a8-4c93-8f16-e0a7c25b39d4
// last-edited: 2026-09-01

package dedup

import (
	"strings"
	"testing"
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
		// TWO-RUNE LATIN last words. The surname threshold moved between 3 and 2,
		// and the corpus tested only 1 and >=3 -- a boundary change tested only
		// AWAY from the boundary. These are the shapes that exposed it: St, Zu, Ph
		// are real particles/abbreviations absent from the closed particle list,
		// and at a flat >=2 they qualified as surnames.
		"Jane St", "Klaus Zu", "Jane Ph", "Louis IX", "Mies Der", "Ana Op",
		// Two-rune NON-Latin last words, which must still be ACCEPTED -- this is
		// the whole reason the threshold is script-conditional rather than 3.
		"村上 春樹", "김 민준",
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
			// Legitimate ONLY when legacy's ASCII byte tests are the sole reason.
			//
			// Legacy had THREE rejection reasons -- first-word ASCII, last-word
			// ASCII, and a byte-length test -- and an earlier version of this
			// guard modelled only the first, waving through anything newly
			// admitted for the other two so long as the FIRST word was non-ASCII
			// ("Ödipus IX" passed as intended). All three are modelled now.
			//
			// It also carried `&& personname.LooksLikePersonName(in)`, which is
			// dead inside this branch: `unified` is true here, and that is the
			// first thing looksLikeAuthorName checks. A conjunct that can never
			// change the result reads like a check and is not one.
			fields := strings.Fields(in)
			lastTrimmed := strings.TrimRight(fields[len(fields)-1], ".")
			asciiOnlyReason := len(fields) >= 2 &&
				(!isASCIIUpper([]rune(fields[0])[0]) ||
					!isASCIIUpper([]rune(fields[len(fields)-1])[0])) &&
				len(lastTrimmed) >= 3
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
	// Both casings. The lowercase forms alone cannot distinguish the guard from
	// its former `unicode.IsLower(...) ||` half -- which was dead code, since
	// LooksLikePersonName already rejects a non-particle lowercase word before
	// this point. The CAPITALIZED forms are what actually pin IsNameParticle.
	for _, in := range []string{
		"Ludwig van", "Vincent van", "Jose del", "Ana della", "Carlos dos",
		"Piet den", "Jan ter", "Omar bin", "Ali ibn", "Ian mac",
		"Volker Le", "Ursula La", "Jean De", "Klaus Von", "Pieter Van",
	} {
		if looksLikeAuthorName(in) {
			t.Errorf("looksLikeAuthorName(%q) = true; want false. A name particle is "+
				"never a surname, and admitting one unlocks a 3-way split that "+
				"outscores the correct 2-way split.", in)
		}
	}
}
