// file: internal/applygate/cast.go
// version: 1.0.0
// guid: 7c3e1a95-2b6d-4f08-9e47-a1d5c8f20b63
// last-edited: 2026-09-13

package applygate

import (
	"strconv"
	"strings"
)

// ReasonCastInAuthor: the candidate credits as AUTHORS people the files name
// as narrator or cast. On the 2026-09-13 prod preview 13 Big Finish full-cast
// dramas passed the gate with the cast list applied as the author, and "The
// War Master" (read by Derek Jacobi) would have become a four-author book.
const ReasonCastInAuthor = "cast_in_author"

// checkCastInAuthor blocks two shapes, both only when the candidate names no
// narrator of its own (a provider that separates author and narrator has
// already told us who is who; the stored narrator is then only a tag guess):
//
//  1. the candidate lists two or more authors, and the book's narrator names
//     some but not all of them: part of the "author" list is the cast;
//  2. the candidate lists three or more authors and the book's CURRENT author
//     is one of them, but not the first: the stored credit is a performer the
//     provider has folded into a writing credit ("Derek Jacobi" ->
//     "James Goss, Guy Adams, Derek Jacobi, Rob Harvey").
//
// Deliberately NOT a block: a narrator tag copied from the author (the whole
// credit matches, so nothing is "cast"), and a narrator who narrates every
// author in the list. Real co-authors (Pratchett and Gaiman) never appear in
// the narrator tag, so they pass.
//
// It returns only block or neutral: an agreement from here would loosen the
// gate's MinAgreements count.
func checkCastInAuthor(book *nameSource, candAuthor, candNarrator string) CheckResult {
	r := CheckResult{Name: "cast_in_author", Outcome: OutcomeNeutral}
	authors := surnames(candAuthor)
	if len(set(authors)) < 2 || strings.TrimSpace(candNarrator) != "" {
		return r
	}

	narr := strings.TrimSpace(parenRe.ReplaceAllString(book.narrator, " "))
	if narr != "" && normText(narr) != normText(book.author) {
		credited := set(surnames(narr))
		var hit []string
		missing := false
		for _, a := range authors {
			if credited[a] {
				hit = append(hit, a)
			} else {
				missing = true
			}
		}
		if len(hit) > 0 && missing {
			r.Outcome, r.Reason = OutcomeBlock, ReasonCastInAuthor
			r.Detail = "the files credit " + strings.Join(hit, ", ") + " as narrator/cast; the candidate lists them as author (" + strconv.Quote(candAuthor) + ")"
			return r
		}
	}

	if len(authors) >= 3 {
		if cur := surnames(book.author); len(cur) == 1 {
			for _, a := range authors[1:] {
				if a == cur[0] {
					r.Outcome, r.Reason = OutcomeBlock, ReasonCastInAuthor
					r.Detail = "current author " + strconv.Quote(book.author) + " is a non-first name in a " + strconv.Itoa(len(authors)) + "-name credit (" + strconv.Quote(candAuthor) + "): a performer folded into the author list"
					return r
				}
			}
		}
	}
	return r
}

// nameSource is the book's stored person credits.
type nameSource struct {
	author, narrator string
}
