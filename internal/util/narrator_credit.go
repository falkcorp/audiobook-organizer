// file: internal/util/narrator_credit.go
// version: 1.0.0
// guid: 103b5e2d-c894-4e2f-b95d-6b796e77762e
// last-edited: 2026-09-23

package util

import (
	"regexp"
	"strings"
)

// NarratorCreditVerdict says what CleanNarratorCredit made of a credit.
type NarratorCreditVerdict int

const (
	// NarratorCreditPeople: the returned names are the narrators.
	NarratorCreditPeople NarratorCreditVerdict = iota
	// NarratorCreditEmpty: nothing usable was left (blank, or only role pieces).
	NarratorCreditEmpty
	// NarratorCreditJunk: the credit is not a list of people (a URL). Leave it
	// alone and surface it for review.
	NarratorCreditJunk
	// NarratorCreditAllAuthors: every person in the credit is one of the
	// book's authors. That is either a real self-read or an author mis-tagged
	// as narrator, and the text cannot tell which, so leave it alone and
	// surface it for review.
	NarratorCreditAllAuthors
)

// narratorRoleSuffix matches a piece that names someone in a role other than
// narrator: "Andrew Schmitt - translator", "Stephen Fry - introductions",
// "Zachary Lorang -translated by", "Bryan Schmidt (editor)".
var narratorRoleSuffix = regexp.MustCompile(`(?i)(\s-\s*|\s*-\s+|\s*\()\s*(translator|translated by|translation|editor|edited by|introductions?|introduced by|foreword|afterword|illustrator|adapted by|adaptation)\b`)

// narratorRolePrefix matches "Translated by X", "Introduction by X".
var narratorRolePrefix = regexp.MustCompile(`(?i)^(translated|edited|introduction|introduced|foreword|afterword|adapted)\s+by\b`)

// narratorByPrefix is a leading "By:" / "By " / "Read by " a tag left on
// the first name.
var narratorByPrefix = regexp.MustCompile(`(?i)^(read\s+by|narrated\s+by|by)\s*:?\s+`)

// CleanNarratorCredit turns a narrator credit into the people who narrate.
//
// Providers and tags put more than narrators in this field. On 2026-09-23 the
// joined narrator entries in production included author+narrator pairs
// ("Adrian Tchaikovsky, Ben Allen"), authors only ("Craig Martelle, Michael
// Anderle"), translators and editors with a role suffix, a leading "By:", and
// a URL. Rules, in order (owner decision 2026-09-23):
//
//   - A URL-like credit is junk: returned as NarratorCreditJunk, unsplit.
//   - The credit is split with SplitCreditNames ("Surname, Given" stays whole).
//   - A leading "By:" / "Read by" is stripped from a piece.
//   - A piece naming a non-narrator role ("- translator", "(editor)",
//     "Introduction by ...") is dropped.
//   - A piece matching one of bookAuthors (NormalizeAuthor equality) is
//     dropped, but only when at least one non-author piece remains. If every
//     piece is an author the verdict is NarratorCreditAllAuthors and nothing
//     is returned.
func CleanNarratorCredit(credit string, bookAuthors []string) ([]string, NarratorCreditVerdict) {
	credit = strings.TrimSpace(credit)
	if credit == "" {
		return nil, NarratorCreditEmpty
	}
	lower := strings.ToLower(credit)
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "www.") || strings.Contains(lower, " www.") {
		return nil, NarratorCreditJunk
	}
	authors := make(map[string]bool, len(bookAuthors))
	for _, a := range bookAuthors {
		if n := NormalizeAuthor(a); n != "" {
			authors[n] = true
		}
	}
	var people, nonAuthors []string
	seen := make(map[string]bool)
	for _, piece := range SplitCreditNames(credit) {
		piece = strings.TrimSpace(narratorByPrefix.ReplaceAllString(strings.TrimSpace(piece), ""))
		if piece == "" || narratorRoleSuffix.MatchString(piece) || narratorRolePrefix.MatchString(piece) {
			continue
		}
		norm := NormalizeAuthor(piece)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		people = append(people, piece)
		if !authors[norm] {
			nonAuthors = append(nonAuthors, piece)
		}
	}
	if len(people) == 0 {
		return nil, NarratorCreditEmpty
	}
	if len(nonAuthors) == 0 && len(authors) > 0 {
		return nil, NarratorCreditAllAuthors
	}
	return nonAuthors, NarratorCreditPeople
}
