// file: internal/personname/author_part_marker.go
// version: 1.0.0
// guid: 3d0f6a52-8b1e-4c77-9a4e-5e61c2f0b9d3
// last-edited: 2026-10-21

package personname

import (
	"regexp"
	"strings"
)

// Part/track/disc markers glued onto a name by a filename or an artist tag
// ("David Copperfield P2 S3", "Copperfield 07", "Great Expectations Pt 4")
// are not part of any person's name, and neither is a surname that is only a
// number. Owner-approved rule (2026-10-21): such a string is NOT a person.
//
// The rule is a shape test, so it deliberately stays narrow:
//
//   - a marker token is one of P<n>, S<n>, Pt<n>, CD<n>, D<n>T<n> glued
//     ("P2", "s3", "CD1", "D1T05"), or a marker word (part, pt, track, trk,
//     disc, disk, cd) followed by a number token ("Part 2", "Pt. 4",
//     "Track 07", "Disc 1");
//   - markers are peeled off the END of the name, any number of them, so
//     combos ("P2 S3", "Part 2 Track 07") work;
//   - rule (b): the LAST token is purely digits ("Copperfield 07",
//     "Maroon 5").
//
// Both need a name left in front of the marker: a lone token ("P2", "07") is
// not judged here (pure numbers and structural labels already have their own
// gate rules). Roman numerals ("Pope John Paul II", "Louis XIV"), ordinal
// suffixes ("Thurston Howell 3rd"), a leading number ("50 Cent"), a letter
// X ("Malcolm X") and a letter plus digits that is not one of the markers
// above ("Agent K9") are not markers. Accepted cost: a band or stage name
// that really ends in a bare number ("Maroon 5", "Blink 182") is flagged;
// callers treat the flag as relink-only / skip, never as a delete.
var (
	// partMarkerTokenRe: one glued marker token.
	partMarkerTokenRe = regexp.MustCompile(`(?i)^(?:(?:p|s|pt|cd)\d+|d\d+t\d+)$`)
	// partMarkerWords: a word that is a marker when a number follows it.
	partMarkerWords = map[string]bool{
		"part": true, "pt": true, "track": true, "trk": true,
		"disc": true, "disk": true, "cd": true,
	}
	digitsOnlyRe = regexp.MustCompile(`^\d+$`)
)

// HasPartTrackMarker reports whether name is a person name with a part, track
// or disc marker on its end, or whose surname (last token) is purely numeric.
// See the package comment above for exactly what counts and what does not.
func HasPartTrackMarker(name string) bool {
	toks := strings.Fields(name)
	// Trailing "." / "," on a token ("Pt. 4", "P2,") never changes the verdict.
	for i, t := range toks {
		toks[i] = strings.Trim(t, ".,;:")
	}
	n := len(toks)
	// A name must stay in front of the marker.
	if n < 2 || toks[n-1] == "" {
		return false
	}
	// A lone marker word and its number ("Part 2", "Disc 1") is a structural
	// label, which the creation gate already refuses; not judged here.
	if n == 2 && partMarkerWords[strings.ToLower(toks[0])] {
		return false
	}
	// Rule (b): purely numeric surname.
	if digitsOnlyRe.MatchString(toks[n-1]) {
		return true
	}
	// Rule (a): peel markers off the end.
	end, peeled := n, false
	for end > 1 {
		switch {
		case partMarkerTokenRe.MatchString(toks[end-1]):
			end--
		case end > 2 && digitsOnlyRe.MatchString(toks[end-1]) && partMarkerWords[strings.ToLower(toks[end-2])]:
			end -= 2
		default:
			return peeled
		}
		peeled = true
	}
	return peeled
}
