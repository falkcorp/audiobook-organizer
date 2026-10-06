// file: internal/personname/names_credit.go
// version: 1.0.0
// guid: a05f7b3e-c778-4b91-9b39-cb9e58f8361b
// last-edited: 2026-10-06

package personname

import "strings"

// NamesCredit reports whether name is the author credit, or one person of a
// composite credit ("A, B and C"; SplitCompositeAuthorName), compared by
// LettersKey. The series paths use it to refuse a series named after the
// book's own author: "Brandon Sanderson - Elantris" splits into a "series"
// of "Brandon Sanderson", and a row of that name is author junk, not a
// series (prod, 2026-10-06: 3,188 series rows carry the name of their own
// author row).
func NamesCredit(name, credit string) bool {
	k := LettersKey(strings.TrimSpace(name))
	if k == "" || strings.TrimSpace(credit) == "" {
		return false
	}
	if k == LettersKey(credit) {
		return true
	}
	for _, p := range SplitCompositeAuthorName(credit) {
		if k == LettersKey(p) {
			return true
		}
	}
	return false
}
