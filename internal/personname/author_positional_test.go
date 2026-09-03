// file: internal/personname/author_positional_test.go
// version: 1.0.0
// guid: bc1746c3-d397-429e-b281-08413635332a
// last-edited: 2026-09-03

package personname

import "testing"

// TestPositionalClassIsNotCoveredByTheOlderGate pins the reason this file
// exists. IsDirtyAuthorName (C413) targets publisher and copyright shrapnel;
// it does not recognise track/chapter numbering. If someone later widens it to
// cover this class, this test should be revisited deliberately rather than the
// two predicates silently overlapping.
func TestPositionalClassIsNotCoveredByTheOlderGate(t *testing.T) {
	for _, n := range []string{
		"Track 01", "001_Celestia", "000m_00s__056m_16s_43h", "00 Prologue", "0.5",
	} {
		if IsDirtyAuthorName(n) {
			t.Errorf("IsDirtyAuthorName(%q) = true; the older gate was not expected to cover the positional class", n)
		}
		if _, ok := CleanAuthorNameForCreation(n); ok {
			t.Errorf("CleanAuthorNameForCreation(%q) accepted a positional artifact", n)
		}
	}
}

func TestCleanAuthorNameForCreation_SalvagesRealPeople(t *testing.T) {
	cases := map[string]string{
		"001-147 Kevin J Anderson": "Kevin J Anderson",
		"002-299 Kevin J Anderson": "Kevin J Anderson",
		"01 Brandon Sanderson":     "Brandon Sanderson",
	}
	for in, want := range cases {
		got, ok := CleanAuthorNameForCreation(in)
		if !ok {
			t.Errorf("CleanAuthorNameForCreation(%q) rejected a name carrying a real person", in)
			continue
		}
		if got != want {
			t.Errorf("CleanAuthorNameForCreation(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCleanAuthorNameForCreation_LeavesRealNamesAlone is the false-positive
// guard. A creation gate that rejects a real artist tag is the same data loss
// this file prevents, pointed the other way. The zero-padding requirement in
// positionalPrefixShapes is what protects these.
func TestCleanAuthorNameForCreation_LeavesRealNamesAlone(t *testing.T) {
	for _, n := range []string{
		"50 Cent", "2Pac", "24 Hours of Le Mans", "10 Minute Reads",
		"Kevin J Anderson", "Brandon Sanderson", "Homer",
	} {
		got, ok := CleanAuthorNameForCreation(n)
		if !ok {
			t.Errorf("CleanAuthorNameForCreation(%q) rejected a real name", n)
			continue
		}
		if got != NormalizeAuthorName(n) {
			t.Errorf("CleanAuthorNameForCreation(%q) rewrote it to %q", n, got)
		}
	}
}

func TestCleanAuthorNameForCreation_RejectsArtifacts(t *testing.T) {
	for _, n := range []string{
		"001 of 301", "002-32k", "01.Intro", "01-Preface", "019", "0.1", "00 3",
		"001DeathBlackHoleOtherCosmicQuandaries", "0BJLX1~W 3",
		"000m_00s__061m_15s_7h", "Prologue", "Unknown Author", "Anthology",
		"Chapter 3", "Track 07", "",
	} {
		if got, ok := CleanAuthorNameForCreation(n); ok {
			t.Errorf("CleanAuthorNameForCreation(%q) = %q, true; want rejected", n, got)
		}
	}
}

// A single-word residue after stripping numbering is a chapter or book title,
// not a person -- accepting it would trade one junk row for another.
func TestCleanAuthorNameForCreation_RejectsSingleWordResidue(t *testing.T) {
	for _, n := range []string{"001_Celestia", "001-119 Treason", "0 ABY", "01.Magician"} {
		if got, ok := CleanAuthorNameForCreation(n); ok {
			t.Errorf("CleanAuthorNameForCreation(%q) = %q, true; want rejected", n, got)
		}
	}
}

func TestStripPositionalPrefix(t *testing.T) {
	cases := map[string]string{
		"001_Head of the Dragon":   "Head of the Dragon",
		"001-147 Kevin J Anderson": "Kevin J Anderson",
		"07) Monster":              "Monster",
		"01.Magician":              "Magician",
		"001 of 301":               "",
		"50 Cent":                  "50 Cent",
		"Kevin J Anderson":         "Kevin J Anderson",
	}
	for in, want := range cases {
		if got := StripPositionalPrefix(in); got != want {
			t.Errorf("StripPositionalPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
