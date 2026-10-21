// file: internal/personname/author_part_marker_test.go
// version: 1.0.0
// guid: 8e2b4d91-6c3a-4f08-b7d5-1a9e0c4f2e76
// last-edited: 2026-10-21

package personname

import "testing"

func TestHasPartTrackMarker(t *testing.T) {
	flagged := []string{
		"David Copperfield P2 S3",
		"David Copperfield P2",
		"David Copperfield S3",
		"David Copperfield p2 s3",
		"Copperfield 07",
		"David Copperfield 2",
		"Charles Dickens Pt 4",
		"Charles Dickens Pt. 4",
		"Charles Dickens Pt4",
		"Charles Dickens Part 2",
		"Charles Dickens part 12",
		"Charles Dickens Track 07",
		"Charles Dickens Disc 1",
		"Charles Dickens CD1",
		"Charles Dickens CD 2",
		"Charles Dickens D1T05",
		"Charles Dickens d2t10 ",
		"Charles Dickens Part 2 Track 07",
		"Charles Dickens P2 07",
		"Charles Dickens Disc 1 P3",
		"Maroon 5", // accepted cost: a bare-number surname is flagged by design
	}
	for _, n := range flagged {
		if !HasPartTrackMarker(n) {
			t.Errorf("HasPartTrackMarker(%q) = false, want true", n)
		}
	}
	clean := []string{
		"",
		"Charles Dickens",
		"Malcolm X",
		"Pope John Paul II",
		"Louis XIV",
		"50 Cent",
		"Thurston Howell 3rd",
		"Henry VIII",
		"Agent K9",
		"Pip",
		"P2",         // lone token: not judged here
		"07",         // lone token: pure-number gate's business
		"Part 2",     // structural label: its own gate rule
		"Disc 1",     // likewise
		"Part Time",  // marker word with no number
		"Track Star", // likewise
		"Charles Dickens Part",
		"Charles Dickens Part Two",
		"Charles P2 Dickens", // marker mid-name is not an ending
		"Dee Sparks",
		"Sam D Jones",
		"J. R. R. Tolkien",
		"S. A. Corey",
		"Ursula K. Le Guin",
	}
	for _, n := range clean {
		if HasPartTrackMarker(n) {
			t.Errorf("HasPartTrackMarker(%q) = true, want false", n)
		}
	}
}
