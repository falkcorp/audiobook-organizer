// file: internal/metadata/series_junk_words_test.go
// version: 1.0.0
// guid: 2f90c4b7-d518-4a3e-9c62-e07b1a584d93
// last-edited: 2026-11-17

package metadata

import "testing"

// PR #4698 round 2 (B2): a base that ENDS in a part, format or abbreviated volume
// word is a tag artefact, on the write paths as well as in the denumber op. The
// op's grammar (internal/seriesdenumber) and this list move together: a word one
// holds and the other does not is a way past the gate.
func TestIsJunkSeriesBase_PartFormatAndAbbreviatedVolumeWords(t *testing.T) {
	for _, word := range []string{
		// the first list
		"chapter", "disc", "cd", "track", "part", "pt", "book", "vol", "volume", "side",
		// added with PR #4698 round 2
		"ch", "ep", "episode", "episodes", "discs", "cds", "sides", "reel", "hour", "hours",
		"v", "t", "bd", "nr", "vols", "volumes",
	} {
		base := "Gotham Knights " + word
		if !IsJunkSeriesBase(base) {
			t.Errorf("IsJunkSeriesBase(%q) = false, want true", base)
		}
		if !IsJunkSeriesBase("Gotham Knights - " + word) {
			t.Errorf("IsJunkSeriesBase(%q) = false, want true", "Gotham Knights - "+word)
		}
	}
}

// The words are whole words after a separator: a base that merely ends in the
// same letters is a real name.
func TestIsJunkSeriesBase_DoesNotReachWordsThatEndTheSameWay(t *testing.T) {
	for _, base := range []string{
		"Gotham Knights Tout", "Dark Knight Chronicles", "Batman Hush Hourglass", "Court of Owls Vendetta",
		"Gotham Knights Nrg", "Batman Beyond", "Arkham Reeling", "Gotham Knights Chaps",
	} {
		if IsJunkSeriesBase(base) {
			t.Errorf("IsJunkSeriesBase(%q) = true, want false", base)
		}
	}
}
