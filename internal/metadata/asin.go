// file: internal/metadata/asin.go
// version: 1.1.0
// guid: 7d3b1c52-8e64-4f0a-9b27-5a1e6c4d9f38
// last-edited: 2026-11-16

package metadata

import (
	"strings"
)

// The ASIN grammar a provider will answer for. Audnexus validates every
// /books/{asin} request against it and answers anything else with HTTP 400
// "Bad ASIN" (probed 2026-11-16 with synthetic strings): `B` followed by nine
// upper-case letters or digits, or nine digits and a digit or X (an ISBN-10
// shape, still looked up as an ASIN). Case matters: a lower-case ASIN is a 400.
//
// A ten-letter ENGLISH WORD is not an ASIN, and neither is any other ten
// characters: before this file the search sent every ten-character word of a
// book's title to Audible and Audnexus as an ASIN (800 of 814 failed books in
// the 2026-11-16 candidate-fetch run).

// isCanonicalASIN reports whether s, exactly as written, is in the ASIN grammar.
func isCanonicalASIN(s string) bool {
	if len(s) != 10 {
		return false
	}
	if s[0] == 'B' {
		for i := 1; i < 10; i++ {
			if !isUpperAlnum(s[i]) {
				return false
			}
		}
		return true
	}
	for i := 0; i < 9; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return (s[9] >= '0' && s[9] <= '9') || s[9] == 'X'
}

func isUpperAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')
}

// CanonicalASIN returns the form of s a provider accepts and whether s is an
// ASIN at all. It is for a value that IS an identifier -- a book's stored ASIN
// field, or a query the user typed as one -- so surrounding space is trimmed
// and case is normalised ("b0gotham99" is the ASIN "B0GOTHAM99").
//
// A value of letters only is normalised only when it is already upper case as
// written: upper-casing the word "Batmobiles" gives the grammatical
// "BATMOBILES", which would be sent and answered 404 in every region. A real
// ASIN holds a digit ("B0" is one), so a lower-case value that does is a real
// ASIN typed in the wrong case, and one that does not is a word.
//
// It must not be used on a word picked out of free text: that is ASINInText.
func CanonicalASIN(s string) (string, bool) {
	t := strings.TrimSpace(s)
	c := strings.ToUpper(t)
	if !isCanonicalASIN(c) {
		return "", false
	}
	if c != t && !strings.ContainsAny(t, "0123456789") {
		return "", false
	}
	return c, true
}

// ASINQuery returns the ASIN a whole search query IS, or "". A user who pastes
// an identifier into "Search again" means it, in either case and in the
// ten-digit form, so the query is normalised like a stored ASIN (CanonicalASIN);
// but a one-word title is also a whole query, so a value of letters alone is
// not taken: the modern shape (`B` then a digit) or the digit form is required,
// which no English word has. "Batmobiles" and "BATMOBILES" are titles.
func ASINQuery(s string) string {
	c, ok := CanonicalASIN(s)
	if !ok || !(c[0] >= '0' && c[0] <= '9' || c[1] >= '0' && c[1] <= '9') {
		return ""
	}
	return c
}

// ASINInText returns the first token of free text (a title, a folder name)
// that is an ASIN as written, or "". A token is only an ASIN when it is
// upper case as written and is Amazon's current shape, `B` then a digit then
// eight upper-case letters or digits: that rejects every English word in any
// case ("Batmobiles", "BATMOBILES", "batmobiles"), which the bare grammar
// would admit for the all-capitals B-words. A bare ten-digit token is left
// alone too: in a title it is far more likely an ISBN-10, a date or a phone
// number than an ASIN, and an ISBN-10 is not an ASIN (masterlist.ClassifyASIN).
func ASINInText(s string) string {
	for word := range strings.FieldsSeq(s) {
		word = strings.Trim(word, ",.;:!?()[]{}\"'")
		if len(word) == 10 && word[0] == 'B' && word[1] >= '0' && word[1] <= '9' && isCanonicalASIN(word) {
			return word
		}
	}
	return ""
}
