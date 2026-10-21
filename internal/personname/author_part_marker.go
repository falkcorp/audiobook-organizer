// file: internal/personname/author_part_marker.go
// version: 1.1.0
// guid: 3d0f6a52-8b1e-4c77-9a4e-5e61c2f0b9d3
// last-edited: 2026-10-21

package personname

import (
	"regexp"
	"strings"
	"unicode"
)

// Part/track/disc markers glued onto a name by a filename or an artist tag
// ("David Copperfield P2 S3", "Copperfield 07", "Great Expectations Pt 4")
// are not part of any person's name, and neither is a surname that is only a
// number. Owner-approved rule (2026-10-21): such a string is NOT a person.
//
// The rule is a shape test, so it deliberately stays narrow:
//
//   - a glued marker token is a marker word or letter code directly followed
//     by a number: P2, S3, D1, Pt4, Pt.4, P.2, CD1, CD1-2, Disc1, Track07,
//     Part2, Trk3, Ch00, Book6, D1T05, S01E02. "S4 E1" counts as one marker;
//   - a marker word (part, pt, track, trk, disc, disk, cd, book, vol,
//     chapter, ch) followed by a number token ("Part 2", "Track 07") is a
//     marker too;
//   - markers are peeled off the END of the name, any number of them, so
//     combos ("P2 S3", "Part 2 Track 07") work. A last token that is purely
//     digits ("Copperfield 07", "Maroon 5") is peeled as well (rule b);
//   - the name left in front must be a real name token: if peeling consumes
//     everything it is a lone marker phrase ("Part 2", "Book 3", "Part 2 P3"),
//     which is a structural label the creation gate already refuses, not
//     judged here; and if the token in front itself carries a digit ("R2 D2")
//     the string is not read as a name with a marker on it.
//
// A trailing edition suffix ("(Unabridged)") is stripped first, so a decorated
// row is judged the same as the bare one. Roman numerals ("Pope John Paul
// II", "Louis XIV"), ordinal suffixes ("Thurston Howell 3rd"), a leading
// number ("50 Cent"), "Malcolm X", "Agent K9" and "Tech N9ne" are not markers.
// Accepted cost: a band or stage name that really ends in a bare number
// ("Maroon 5", "Blink 182") is flagged; callers treat the flag as relink-only
// / skip, never as a delete.
var (
	// partMarkerTokenRe: one glued marker token (digits are any Unicode Nd).
	partMarkerTokenRe = regexp.MustCompile(`(?i)^(?:(?:p|s|d|pt|cd|ch|disc|disk|track|trk|part|book)\.?\p{Nd}+(?:-\p{Nd}+)?|d\p{Nd}+t\p{Nd}+|s\p{Nd}+e\p{Nd}+)$`)
	// partSeasonRe / partEpisodeRe: "S4 E1" is two tokens, one marker.
	partSeasonRe  = regexp.MustCompile(`(?i)^s\p{Nd}+$`)
	partEpisodeRe = regexp.MustCompile(`(?i)^e\p{Nd}+$`)
	// partMarkerWords: a word that is a marker when a number follows it.
	partMarkerWords = map[string]bool{
		"part": true, "pt": true, "track": true, "trk": true,
		"disc": true, "disk": true, "cd": true, "book": true,
		"vol": true, "chapter": true, "ch": true,
	}
	digitsOnlyRe = regexp.MustCompile(`^\p{Nd}+$`)
)

// HasPartTrackMarker reports whether name is a person name with a part, track
// or disc marker on its end, or whose surname (last token) is purely numeric.
// See the comment above for exactly what counts and what does not.
func HasPartTrackMarker(name string) bool {
	var toks []string
	for _, t := range strings.Fields(StripEditionSuffix(name)) {
		// Punctuation hanging on a token ("Pt. 4", "P2,", "(P2)") never
		// changes the verdict.
		if t = strings.Trim(t, ".,;:()[]"); t != "" {
			toks = append(toks, t)
		}
	}
	n := len(toks)
	if n < 2 {
		return false
	}
	end := n
	for end > 0 {
		last := toks[end-1]
		switch {
		case end > 1 && digitsOnlyRe.MatchString(last) && partMarkerWords[strings.ToLower(toks[end-2])]:
			end -= 2
		case end == n && digitsOnlyRe.MatchString(last): // rule (b)
			end--
		case partMarkerTokenRe.MatchString(last):
			end--
		case end > 1 && partEpisodeRe.MatchString(last) && partSeasonRe.MatchString(toks[end-2]):
			end -= 2
		default:
			return end < n && !tokenHasDigit(toks[end-1])
		}
	}
	// Everything was marker: a lone marker phrase, not judged here.
	return false
}

func tokenHasDigit(t string) bool { return strings.IndexFunc(t, unicode.IsDigit) >= 0 }
