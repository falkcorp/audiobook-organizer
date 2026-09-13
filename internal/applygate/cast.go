// file: internal/applygate/cast.go
// version: 1.2.0
// guid: 7c3e1a95-2b6d-4f08-9e47-a1d5c8f20b63
// last-edited: 2026-09-13

package applygate

import (
	"strconv"
	"strings"
)

// ReasonCastInAuthor: the candidate credits as AUTHORS people the files name
// as narrator or cast. On the 2026-09-13 prod preview 13 Big Finish full-cast
// dramas passed the gate with the cast list applied as the author.
const ReasonCastInAuthor = "cast_in_author"

// checkCastInAuthor blocks one shape, only when the candidate lists two or
// more distinct people and names no narrator of its own (a provider that
// separates author and narrator has already told us who is who; the stored
// narrator is then only a tag guess, so this check steps aside):
//
//	the book's narrator credit names the candidate's FIRST author, and at
//	least one later candidate author is named nowhere in that credit.
//
// That is the Big Finish shape: the writer is tagged as narrator in the files
// ("James Swallow") and the provider's author list is the writer followed by
// the cast ("James Swallow, Nicholas Courtney, Toby Longworth"). All 13 such
// rows on the 2026-09-13 preview match it; no other passing row does.
//
// Deliberately NOT a block:
//   - the narrator is a LATER candidate author ("Terry Pratchett, Neil Gaiman"
//     read by Neil Gaiman). An author reading their own co-written book looks
//     the same as a provider appending the narrator to the author list, and
//     the files cannot tell the two apart, so neither is blocked here;
//   - a narrator tag copied from the author credit, or one that names every
//     candidate author;
//   - a stored author who appears as a non-first name in a long credit list
//     ("Kevin J. Anderson" after "Frank Herbert, Brian Herbert"): position in
//     a list is not evidence of performing. This drops "The War Master"
//     (Derek Jacobi folded into a four-name writing credit), which needs a
//     cast credit the files do not carry.
//
// People are compared by full normalized name when both sides give at least
// two name words ("Owen King" is not "Stephen King"), by surname only when one
// side is a single word.
//
// It returns only block or neutral: an agreement from here would loosen the
// gate's MinAgreements count.
func checkCastInAuthor(book *nameSource, candAuthor, candNarrator string) CheckResult {
	r := CheckResult{Name: "cast_in_author", Outcome: OutcomeNeutral}
	if strings.TrimSpace(candNarrator) != "" {
		return r
	}
	authors := distinctPeople(people(candAuthor))
	if len(authors) < 2 {
		return r
	}
	narr := strings.TrimSpace(parenRe.ReplaceAllString(book.narrator, " "))
	// No early return when the narrator equals the stored author: the apply
	// writes the candidate's whole author string over the stored author
	// unless it equals the narrator (metafetch applyMetadataToBook), so
	// "Steve Lyons" stored as both would become "Steve Lyons, Anneke Wills,
	// John Sackville". A narrator tag copied from an author credit that
	// names every candidate author still passes: nothing is left over.
	if narr == "" {
		return r
	}
	narrators := people(narr)
	if !anyPerson(narrators, authors[0]) {
		return r
	}
	var cast []string
	for _, a := range authors[1:] {
		if !anyPerson(narrators, a) {
			cast = append(cast, strings.Join(a, " "))
		}
	}
	if len(cast) == 0 {
		return r
	}
	r.Outcome, r.Reason = OutcomeBlock, ReasonCastInAuthor
	r.Detail = "the files credit " + strconv.Quote(narr) + " (the candidate's first author) as narrator; the candidate appends " +
		strings.Join(cast, ", ") + " to the author list (" + strconv.Quote(candAuthor) + "): a cast list credited as authors"
	return r
}

// people splits a credit into persons, each as its name words. A single
// "Last, First" credit is one person, as in surnames.
func people(credit string) [][]string {
	if last, rest, ok := strings.Cut(credit, ","); ok && !strings.ContainsAny(credit, "&;") &&
		strings.Count(credit, ",") == 1 && !andWord.MatchString(credit) {
		if w, r := nameWords(last), nameWords(rest); len(w) == 1 && len(r) > 0 && len(r) <= 3 {
			return [][]string{append(r, w...)}
		}
	}
	var out [][]string
	for _, part := range creditSplit.Split(credit, -1) {
		if w := nameWords(part); len(w) > 0 {
			out = append(out, w)
		}
	}
	return out
}

// distinctPeople drops repeats ("Terrance Dicks, Terrance Dicks").
func distinctPeople(ps [][]string) [][]string {
	var out [][]string
	for _, p := range ps {
		if !anyPerson(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// samePerson compares full names when both have two or more words, surnames
// when either is a single word.
func samePerson(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) >= 2 && len(b) >= 2 {
		return strings.Join(a, " ") == strings.Join(b, " ")
	}
	return a[len(a)-1] == b[len(b)-1]
}

func anyPerson(ps [][]string, p []string) bool {
	for _, q := range ps {
		if samePerson(q, p) {
			return true
		}
	}
	return false
}

// nameSource is the book's stored person credits.
type nameSource struct {
	author, narrator string
}
