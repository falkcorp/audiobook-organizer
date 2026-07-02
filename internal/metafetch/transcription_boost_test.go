// file: internal/metafetch/transcription_boost_test.go
// version: 1.0.0
// guid: 3c1f9a52-8d47-4e60-b9a2-6f0e5d213c74
// last-edited: 2026-07-02

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// TestTranscriptionBoost_AuthorRequiresTitle locks the fix for the
// "matches the author but not the actual book" bug: the audio-derived
// author/narrator boosts must NOT multiply the score unless the transcribed
// TITLE also agrees. Author agreement without a title match is a tiebreaker,
// not a score driver.
func TestTranscriptionBoost_AuthorRequiresTitle(t *testing.T) {
	const base = 1.0

	tests := []struct {
		name      string
		cand      metadata.BookMetadata
		hints     transcriptionHints
		wantScore float64
		wantBoost bool
	}{
		{
			name:      "author matches but title does NOT -> no boost",
			cand:      metadata.BookMetadata{Title: "The Wrong Book", Author: "Brandon Sanderson", Narrator: "Michael Kramer"},
			hints:     transcriptionHints{title: "The Way of Kings", author: "Brandon Sanderson", narrator: "Michael Kramer"},
			wantScore: base, // unchanged: author/narrator must not carry a wrong title
			wantBoost: false,
		},
		{
			name:      "exact title + author -> title x2 then author x1.6",
			cand:      metadata.BookMetadata{Title: "The Way of Kings", Author: "Brandon Sanderson"},
			hints:     transcriptionHints{title: "The Way of Kings", author: "Brandon Sanderson"},
			wantScore: base * 2.0 * 1.6,
			wantBoost: true,
		},
		{
			name:      "exact title only (no author hint) -> x2",
			cand:      metadata.BookMetadata{Title: "The Way of Kings", Author: "Brandon Sanderson"},
			hints:     transcriptionHints{title: "The Way of Kings"},
			wantScore: base * 2.0,
			wantBoost: true,
		},
		{
			name:      "substring title + author + narrator -> x1.4 x1.6 x1.4",
			cand:      metadata.BookMetadata{Title: "The Way of Kings (Stormlight 1)", Author: "Brandon Sanderson", Narrator: "Michael Kramer"},
			hints:     transcriptionHints{title: "The Way of Kings", author: "Brandon Sanderson", narrator: "Michael Kramer"},
			wantScore: base * 1.4 * 1.6 * 1.4,
			wantBoost: true,
		},
		{
			// No transcribed title at all → author is a legitimate tiebreaker
			// (there is no title to contradict). This is NOT the bug case.
			name:      "no transcribed title, author only -> author tiebreaker applies",
			cand:      metadata.BookMetadata{Title: "Some Book", Author: "Brandon Sanderson"},
			hints:     transcriptionHints{author: "Brandon Sanderson"},
			wantScore: base * 1.6,
			wantBoost: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, boosted := transcriptionBoost(base, tc.cand, tc.hints)
			if boosted != tc.wantBoost {
				t.Errorf("boosted = %v, want %v", boosted, tc.wantBoost)
			}
			if !floatNear(got, tc.wantScore) {
				t.Errorf("score = %v, want %v", got, tc.wantScore)
			}
		})
	}
}

func TestTranscribedTitleAgrees(t *testing.T) {
	tests := []struct {
		cand, transcribed string
		want              bool
	}{
		{"The Way of Kings", "The Way of Kings", true},
		{"the way of kings", "The Way of Kings", true},         // normalized
		{"The Way of Kings (Stormlight 1)", "The Way of Kings", true}, // substring
		{"The Final Empire", "The Way of Kings", false},
		{"", "The Way of Kings", false},
		{"The Way of Kings", "", false},
	}
	for _, tc := range tests {
		if got := transcribedTitleAgrees(tc.cand, tc.transcribed); got != tc.want {
			t.Errorf("transcribedTitleAgrees(%q, %q) = %v, want %v", tc.cand, tc.transcribed, got, tc.want)
		}
	}
}

func floatNear(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
