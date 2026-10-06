// file: internal/metadata/leading_number_count_test.go
// version: 1.0.0
// guid: a8f3f452-fc28-456e-acb8-bed7a67cc835
// last-edited: 2026-10-06

package metadata

import (
	"strings"
	"testing"
)

func TestLeadingNumberIsCount(t *testing.T) {
	for in, want := range map[string]bool{
		"183 of 301":        true,
		"002 OF 341":        true,
		"01 Title":          false,
		"10 of the Best":    false,
		"Shadow's Edge 183": false,
		"183":               false,
	} {
		if got := LeadingNumberIsCount(strings.Split(in, " ")); got != want {
			t.Errorf("LeadingNumberIsCount(%q) = %v, want %v", in, got, want)
		}
	}
}

// The file-name fallback must not strip the number off "183 of 301" and
// leave "of 301".
func TestExtractFromFilename_CountedPartKeepsItsNumber(t *testing.T) {
	m := extractFromFilename("/lib/Brent Weeks/Shadow's Edge/183 of 301.mp3")
	if m.Title == "of 301" || m.Title == "" {
		t.Fatalf("title = %q", m.Title)
	}
	if m := extractFromFilename("/lib/William Gibson/Zero History/10 Zero History.mp3"); m.Title != "Zero History" {
		t.Fatalf("a track number before a title must still be stripped, got %q", m.Title)
	}
}
