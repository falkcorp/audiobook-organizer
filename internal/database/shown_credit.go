// file: internal/database/shown_credit.go
// version: 1.0.0
// guid: 803f15f9-b5e8-45a0-be13-5238775486c0
// last-edited: 2026-10-03

package database

import "strings"

// ShownCreditName is the author or narrator credit a book's GET response
// shows: primary when it is non-empty (the resolved primary author's name,
// or the Narrator column), else the linked names (book_authors /
// book_narrators, in order) joined with " & ".
//
// It is the one definition of that rule. The GET's enrichment
// (server.enrichBookForResponse) builds its response with it, and the book
// edit endpoint compares what a client sends back against it, so a client
// re-sending what it was shown is never read as an edit. The two used to
// drift: the edit side knew only the primary name, so a title-only save of a
// book whose author lives only in the join relinked it to a new "A & B"
// author and locked the field.
func ShownCreditName(primary string, linked []string) string {
	if primary != "" {
		return primary
	}
	return strings.Join(linked, " & ")
}
