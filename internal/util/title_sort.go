// file: internal/util/title_sort.go
// version: 1.0.0
// guid: 2f6c8a14-5d3b-4e97-b0a2-9e4d1c7f6b58
// last-edited: 2026-09-28

package util

import (
	"regexp"
	"strconv"
	"strings"
)

// numberedBooks are the works whose titles carry a leading ordinal: the
// numbered books of the Bible (and apocrypha). "I Corinthians",
// "1 Corinthians" and "First Corinthians" are one book; without this they
// sort under "I", "1" and "F".
//
// The rewrite is limited to these names on purpose. A general "leading roman
// numeral or ordinal" rule would file "V for Vendetta" under "for" and
// "I, Robot" under "Robot".
var numberedBooks = []string{
	"samuel", "kings", "chronicles", "esdras", "maccabees",
	"corinthians", "thessalonians", "timothy", "peter", "john",
}

// ordinalWords maps a spelled or roman ordinal (lower case) to its number.
var ordinalWords = map[string]int{
	"1": 1, "i": 1, "first": 1, "1st": 1,
	"2": 2, "ii": 2, "second": 2, "2nd": 2,
	"3": 3, "iii": 3, "third": 3, "3rd": 3,
	"4": 4, "iv": 4, "fourth": 4, "4th": 4,
}

// numberedBookRe: an ordinal token, an optional dot, whitespace, a numbered
// book name, and a remainder that is empty or starts with something other
// than a letter (a chapter number, " - ", "(") or the word chapter. The
// remainder rule keeps "First Kings of England" a title of its own.
var numberedBookRe = regexp.MustCompile(`(?i)^\s*([0-9a-z]+)\.?\s+(` + strings.Join(numberedBooks, "|") +
	`)((?:\s*[^\pL\s].*)|(?:\s+(?:chapter|ch\.?)\b.*)|\s*)$`)

// ordinalOf resolves a leading token to its number. letterL is true when the
// token used the letter l for the digit 1 / roman I ("l", "ll", "lI"): a
// glyph confusion in source folder names, resolved only here, in front of a
// numbered book name, so "l'Étranger" is never touched.
func ordinalOf(tok string) (n int, letterL bool) {
	lower := strings.ToLower(tok)
	if n, ok := ordinalWords[lower]; ok {
		return n, false
	}
	if strings.ContainsRune(tok, 'l') && strings.Trim(tok, "lIi") == "" {
		if n, ok := ordinalWords[strings.ReplaceAll(lower, "l", "i")]; ok {
			return n, true
		}
	}
	return 0, false
}

// matchNumberedBook parses a numbered-book title into its parts.
func matchNumberedBook(title string) (n int, book, rest string, letterL, ok bool) {
	m := numberedBookRe.FindStringSubmatch(title)
	if m == nil {
		return 0, "", "", false, false
	}
	n, letterL = ordinalOf(m[1])
	if n == 0 {
		return 0, "", "", false, false
	}
	return n, m[2], strings.TrimSpace(m[3]), letterL, true
}

// NumberedBookSortForm returns the sort form of a numbered-book title,
// "<Book> <n>[ <rest>]": "I Corinthians", "1 Corinthians", "First
// Corinthians" and "l Corinthians" all give "Corinthians 1", and
// "II Kings 3" gives "Kings 2 3". ok is false (and title is returned
// unchanged) for every other title.
func NumberedBookSortForm(title string) (string, bool) {
	n, book, rest, _, ok := matchNumberedBook(title)
	if !ok {
		return title, false
	}
	out := book + " " + strconv.Itoa(n)
	if rest != "" {
		out += " " + rest
	}
	return out, true
}

// TitleSortKey is the library's title sort key: the normalised title
// (NormalizeTitle), with a numbered-book title first rewritten to its sort
// form so its three spellings sort together under the book's name. The
// memdb title index, the materialised title sort and the ABS sort all use it,
// so they cannot drift.
func TitleSortKey(title string) string {
	if form, ok := NumberedBookSortForm(title); ok {
		return NormalizeTitle(form)
	}
	return NormalizeTitle(title)
}

// NormalizeLetterLOrdinal rewrites a numbered-book title whose ordinal was
// spelled with the letter l ("l Corinthians", "ll Kings") to arabic digits
// ("1 Corinthians", "2 Kings"), keeping the rest of the title as it was. ok is
// false for every title that does not use the letter-l ordinal, including
// "I Corinthians" (a correct roman spelling) and "l'Étranger".
func NormalizeLetterLOrdinal(title string) (string, bool) {
	n, _, _, letterL, ok := matchNumberedBook(title)
	if !ok || !letterL {
		return title, false
	}
	m := numberedBookRe.FindStringSubmatchIndex(title)
	// m[2]:m[3] is the ordinal token; an optional dot after it goes too.
	after := title[m[3]:]
	after = strings.TrimPrefix(after, ".")
	return strings.TrimLeft(title[:m[2]], " \t") + strconv.Itoa(n) + after, true
}
