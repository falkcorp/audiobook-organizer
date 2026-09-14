// file: internal/metafetch/transcription_boost_test.go
// version: 1.2.0
// guid: 3c1f9a52-8d47-4e60-b9a2-6f0e5d213c74
// last-edited: 2026-09-13

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
		cand, pos, transcribed string
		want                   bool
	}{
		{"The Way of Kings", "", "The Way of Kings", true},
		{"the way of kings", "", "The Way of Kings", true}, // normalized
		// The raw substring the old rule accepted is refused now.
		{"The Way of Kings (Stormlight 1)", "", "The Way of Kings", false},
		{"The Final Empire", "", "The Way of Kings", false},
		{"", "", "The Way of Kings", false},
		{"The Way of Kings", "", "", false},
		// The strict matcher's trailer strip, within what the old rule allowed:
		// the heard volume must equal the candidate's series position.
		{"This Gilded Abyss", "1", "This Gilded Abyss, book one of the Gilded Abyss trilogy", true},
		{"This Gilded Abyss", "", "This Gilded Abyss, book one of the Gilded Abyss trilogy", false},
	}
	for _, tc := range tests {
		if got := transcribedTitleAgrees(tc.cand, tc.pos, tc.transcribed); got != tc.want {
			t.Errorf("transcribedTitleAgrees(%q, %q, %q) = %v, want %v", tc.cand, tc.pos, tc.transcribed, got, tc.want)
		}
	}
}

// The legacy substring test accepts "Dune" inside "Dune, book two of the
// Dune Chronicles". Auto-fetch must refuse such a pair unless the candidate
// is that volume: a stripped trailer's number is identity, not noise.
func TestTranscribedTitleAgrees_HeardVolumeMustMatchPosition(t *testing.T) {
	tests := []struct {
		cand, pos, transcribed string
		want                   bool
	}{
		{"Dune", "", "Dune, book two of the Dune Chronicles", false},
		{"Dune", "1", "Dune, book two of the Dune Chronicles", false},
		{"Dune", "2", "Dune, book two of the Dune Chronicles", true},
		{"Harry Potter", "", "Harry Potter book 2", false},
		{"Harry Potter", "1", "Harry Potter book 2", false},
		{"The Expanse", "", "The Expanse, volume 3", false},
		{"The Expanse", "3.0", "The Expanse, volume 3", true},
		{"The Expanse", "2.5", "The Expanse, volume 2", false},
	}
	for _, tc := range tests {
		if !legacyTranscribedTitleAgrees(tc.cand, tc.transcribed) {
			t.Fatalf("fixture %q ~ %q: the legacy rule refuses it, so it tests nothing", tc.cand, tc.transcribed)
		}
		if got := transcribedTitleAgrees(tc.cand, tc.pos, tc.transcribed); got != tc.want {
			t.Errorf("transcribedTitleAgrees(%q, pos %q, %q) = %v, want %v", tc.cand, tc.pos, tc.transcribed, got, tc.want)
		}
	}
}

// Auto-fetch applies with no human in the loop: every reviewer false
// positive must refuse, in both orders.
func TestTranscribedTitleAgrees_ReviewerFalsePositivesRefuse(t *testing.T) {
	for _, p := range [][2]string{
		{"Mistborn: The Hero of Ages", "Mistborn"},
		{"Foundation", "Foundation and Empire"},
		{"The Witches", "The Witcher"},
	} {
		if transcribedTitleAgrees(p[0], "", p[1]) || transcribedTitleAgrees(p[1], "", p[0]) {
			t.Errorf("auto-fetch accepted %q ~ %q", p[0], p[1])
		}
	}
}

// Auto-fetch must never be looser than origin/main on any input: whatever
// the new rule accepts, the old one accepted too.
func TestTranscribedTitleAgrees_NeverLooserThanMain(t *testing.T) {
	titles := []string{
		"Knaves Over Queens", "Naves Over Queens", "Sojourn", "Mistborn",
		"Mistborn: The Hero of Ages", "The Witcher", "The Witches", "Foundation",
		"Foundation and Empire", "This Gilded Abyss", "This Gilded Abyss, book one of the Gilded Abyss trilogy",
		"Witness to a Trial", "Witness to a Trial A short story prequel to The Whistler",
		"Ready Player One", "Ready Player 1", "The Sandersonian Way Home", "The Sandersinian Way Home",
		"Blood of Elves", "Blood of Elves (Unabridged)", "It", "",
	}
	for _, a := range titles {
		for _, b := range titles {
			for _, pos := range []string{"", "1", "2"} {
				if transcribedTitleAgrees(a, pos, b) && !legacyTranscribedTitleAgrees(a, b) {
					t.Errorf("auto-fetch accepts %q (pos %q) ~ %q, which origin/main refused", a, pos, b)
				}
			}
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
