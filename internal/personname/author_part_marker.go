// file: internal/personname/author_part_marker.go
// version: 1.2.0
// guid: 3d0f6a52-8b1e-4c77-9a4e-5e61c2f0b9d3
// last-edited: 2026-10-21

package personname

import (
	"regexp"
	"slices"
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
//     Part2, Trk3, Ch00, Book6, Vol3, Chapter3, Bk3, D1T05, S01E02. "S4 E1" counts as one marker;
//   - a marker word (part, pt, track, trk, disc, disk, cd, book, vol,
//     chapter, ch) followed by a number token ("Part 2", "Track 07") is a
//     marker too;
//   - markers are peeled off the END of the name, any number of them, so
//     combos ("P2 S3", "Part 2 Track 07") work. A last token that is purely
//     digits ("Copperfield 07", "Maroon 5") is peeled as well (rule b);
//   - bare numbers peel too, however many ("Charles Dickens 2 3");
//   - HasPartTrackMarker needs a name in front: if peeling consumes everything
//     it is a lone marker phrase ("Part 2", "Book 3", "Part 2 P3"), which
//     IsLoneMarkerPhrase answers instead; and if every token left in front
//     carries a digit ("R2 D2", "H2g2 S4 E1") the string is not read as a name
//     with a marker on it. One word-token in front is enough ("Pip Estella 1
//     Track 04").
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
	partMarkerTokenRe = regexp.MustCompile(`(?i)^(?:(?:p|s|d|pt|cd|ch|bk|vol|volume|chapter|disc|disk|track|trk|part|book)\.?\p{Nd}+(?:-\p{Nd}+)?|d\p{Nd}+t\p{Nd}+|s\p{Nd}+e\p{Nd}+)$`)
	// partSeasonRe / partEpisodeRe: "S4 E1" is two tokens, one marker.
	partSeasonRe  = regexp.MustCompile(`(?i)^s\p{Nd}+$`)
	partEpisodeRe = regexp.MustCompile(`(?i)^e\p{Nd}+$`)
	// partMarkerWords: a word that is a marker when a number follows it.
	partMarkerWords = map[string]bool{
		"part": true, "pt": true, "track": true, "trk": true,
		"disc": true, "disk": true, "cd": true, "book": true,
		"vol": true, "volume": true, "chapter": true, "ch": true, "bk": true,
	}
	digitsOnlyRe = regexp.MustCompile(`^\p{Nd}+$`)
)

// markerTokens splits a name into tokens for the marker rules: the edition
// suffix is stripped and punctuation hanging on a token ("Pt. 4", "P2,",
// "[Track") never changes the verdict.
func markerTokens(name string) []string {
	var toks []string
	for _, t := range strings.Fields(StripEditionSuffix(name)) {
		if t = strings.Trim(t, ".,;:()[]"); t != "" {
			toks = append(toks, t)
		}
	}
	return toks
}

// peelMarkers peels markers off the end of toks and returns how many tokens
// are left in front. Bare numbers peel wherever they sit in the run
// ("Charles Dickens 2 3"), as do marker words with their number ("Track 01").
func peelMarkers(toks []string) int {
	end := len(toks)
	for end > 0 {
		last := toks[end-1]
		switch {
		case end > 1 && digitsOnlyRe.MatchString(last) && partMarkerWords[strings.ToLower(toks[end-2])]:
			end -= 2
		case digitsOnlyRe.MatchString(last), partMarkerTokenRe.MatchString(last):
			end--
		case end > 1 && partEpisodeRe.MatchString(last) && partSeasonRe.MatchString(toks[end-2]):
			end -= 2
		default:
			return end
		}
	}
	return 0
}

// HasPartTrackMarker reports whether name is a person name with a part, track
// or disc marker on its end, or whose surname (last token) is purely numeric.
// See the comment above for exactly what counts and what does not. It is false
// for a lone marker phrase; IsLoneMarkerPhrase answers that.
func HasPartTrackMarker(name string) bool {
	toks := markerTokens(name)
	n := len(toks)
	if n < 2 {
		return false
	}
	end := peelMarkers(toks)
	// Something must have peeled, and at least one token left in front must be
	// a word: "R2 D2", "R2 D2 P3" and "H2g2 S4 E1" are all digits-and-codes and
	// are not read as a name with a marker on it.
	return end > 0 && end < n && slices.ContainsFunc(toks[:end], func(t string) bool { return !tokenHasDigit(t) })
}

// IsLoneMarkerPhrase reports whether name is nothing but a part/track/book
// label: every token is a marker, a marker word with its number, or a bare
// number ("Book 1", "Book 2 (Unabridged)", "[Track 003] 2", "Vol. 01", "10
// Chapter 10", "Part 2 P3"). It is the complement of HasPartTrackMarker, which
// needs a name in front. Such a row is a structural label, not a person.
func IsLoneMarkerPhrase(name string) bool {
	toks := markerTokens(name)
	return len(toks) > 0 && peelMarkers(toks) == 0
}

func tokenHasDigit(t string) bool { return strings.IndexFunc(t, unicode.IsDigit) >= 0 }
