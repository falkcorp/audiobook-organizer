// file: internal/util/transcript_initials_test.go
// version: 1.0.0
// guid: 8d2f4a61-3c7e-4b95-a0d8-6e1b9f5c2a47
// last-edited: 2026-09-14

package util

import "testing"

func TestInitialsFolded(t *testing.T) {
	for in, want := range map[string]string{
		"R. A. Salvatore":   "ra salvatore",
		"R.A. Salvator":     "ra salvator",
		"J.R.R. Tolkien":    "jrr tolkien",
		"J. R. R. Tolkien":  "jrr tolkien",
		"Brandon Sanderson": "brandon sanderson",
	} {
		if got := initialsFolded(in); got != want {
			t.Errorf("initialsFolded(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMainTranscriptionConfirms_InitialsSpacing(t *testing.T) {
	// Prod 2026-09-14: Sojourn was blocked as transcription_mismatch although
	// Whisper heard "Sojourn, written by R.A. Salvator".
	if !MainTranscriptionConfirms("Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Salvator") {
		t.Fatal("initials spacing still blocks a matching transcript")
	}
	// A different author is still refused.
	if MainTranscriptionConfirms("Sojourn", "Someone Else", "Sojourn", "R.A. Salvator") {
		t.Fatal("different author accepted")
	}
	// A different title is still refused.
	if MainTranscriptionConfirms("Homeland", "R. A. Salvatore", "Sojourn", "R.A. Salvator") {
		t.Fatal("different title accepted")
	}
}
