// file: internal/personname/letterskey.go
// version: 1.0.0
// guid: 3c8e1f72-5a9d-4b06-9e41-d27b6a0f8c35
// last-edited: 2026-10-04

package personname

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// LettersKey is a name's letters and digits, lower-cased and NFC-normalized:
// "J.N. Chaney" and "J N Chaney" share one key.
//
// It lives here, in a leaf package, so packages that authorcredit itself
// depends on (internal/authority's read API, which authorcredit will consult)
// fold names the same way without importing authorcredit.
// authorcredit.LettersKey delegates to it.
func LettersKey(s string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(strings.ToLower(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
