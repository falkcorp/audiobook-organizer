// file: internal/personname/author_part_marker_test.go
// version: 1.5.0
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
		"Charles Dickens Disc1",
		"Charles Dickens Track07",
		"Charles Dickens Part2",
		"Charles Dickens Trk3",
		"Charles Dickens Ch00",
		"Charles Dickens Pt.4",
		"Charles Dickens P.2",
		"Charles Dickens Book6",
		"Charles Dickens Book 6",
		"Charles Dickens Vol 3",
		"Charles Dickens S01E02",
		"Charles Dickens S4 E1",
		"Charles Dickens D1",
		"Charles Dickens CD1-2",
		"Pip Estella 2 (Unabridged)",
		"Charles Dickens P2 (Unabridged)",
		"Charles Dickens ٣", // Arabic-Indic digit surname
		"Maroon 5",          // accepted cost: a bare-number surname is flagged by design
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
		"P2", // lone token: not judged here
		"07", // lone token: pure-number gate's business
		"R2 D2",
		"R2 D2 P3",
		"H2g2 S4 E1",
		"Tech N9ne",
		"Book 3",
		"Vol 3",
		"Chapter 3",
		"Part 2 P3", // only markers: a lone marker phrase
		"Part 2 Track 07",
		"Part 2 (Unabridged)",
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

func TestIsLoneMarkerPhrase(t *testing.T) {
	lone := []string{
		"Book 1",
		"Book 2 (Unabridged)",
		"[Track 003] 2",
		"Vol. 01",
		"10 Chapter 10",
		"Part 2",
		"Disc 1",
		"Part 2 P3",
		"Part 2 Track 07",
		"P2 S3",
		"S4 E1",
		"Vol3",
		"Bk 3",
		// Number words, roman numerals, section-style words and a lone
		// edition word (added 2026-10-21 for the label-only relink rule).
		"Track01", "Chapter Twelve", "Volume IV", "Part One", "Side 2", "Section 3",
		"Episode 4", "Unabridged", "Abridged", "Book 2 Unabridged",
		// Ranges, decimals, separators, letter labels, front/back matter.
		"Books 1-3", "Books 4-6 (Unabridged)", "Book 2.5", "Disk 2 -Track 01", "Track A", "Disc B",
		"- Introduction", "Prologue Chapter 1", "Prologue + Chapter 1", "Chapter 5   Epilogue",
		"Prologue", "Epilogue", "Intro", "Part II", "Volume XXXIX",
		// Letterless disc/track codes are labels under the owner rule.
		"1-02", "10-01", "3", "2001", "1.5",
		// "of" between numbers, "#" before a number, "/" in a range.
		"Part 1 of 3", "Book #2", "Track 1/12", "Disc 2 of 10 (Unabridged)", "#3",
	}
	for _, n := range lone {
		if !IsLoneMarkerPhrase(n) {
			t.Errorf("IsLoneMarkerPhrase(%q) = false, want true", n)
		}
		if HasPartTrackMarker(n) {
			t.Errorf("HasPartTrackMarker(%q) = true; a lone phrase has no name in front", n)
		}
	}
	notLone := []string{
		"",
		"Charles Dickens",
		"Charles Dickens Part 2",
		"Pip Estella 1 Track 04",
		"Copperfield 07",
		"Maroon 5",
		"Malcolm X",
		"Pope John Paul II",
		"50 Cent",
		"Agent K9",
		"Part Time",
		"Book Club",
		"R2 D2",
		"Tech N9ne",
		"Unabridged Lies", "Side Effects", "Episode Guide", "Chapter And Verse", "Volume Control",
		// Not valid roman numerals (or too large), so not numbered labels.
		"Part Li", "Book Vic", "Book Civil", "Part ill", "Volume XL",
		// A letter is a label only right after a label word, and a name in
		// front is never a lone phrase.
		"A", "Track A Smith", "Prologue Smith", "Introduction To Physics", "Vitamin C", "- ", "+",
		"Charles Dickens Episode 4", "Charles Dickens Prologue",
		// "of" joins numbers only; entity shrapnel is not a number.
		"Part of 3", "Part 1 of", "Book 1 of Smith", "Part 1 of Three", "&#169", "&#169;", "&#xA9;",
	}
	for _, n := range notLone {
		if IsLoneMarkerPhrase(n) {
			t.Errorf("IsLoneMarkerPhrase(%q) = true, want false", n)
		}
	}
}
