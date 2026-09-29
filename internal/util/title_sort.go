// file: internal/util/title_sort.go
// version: 1.3.0
// guid: 2f6c8a14-5d3b-4e97-b0a2-9e4d1c7f6b58
// last-edited: 2026-09-29

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
//
// "mose" is German Bible numbering ("1. Mose" … "5. Mose", the books of
// Moses).
var numberedBooks = []string{
	"samuel", "kings", "chronicles", "esdras", "maccabees",
	"corinthians", "thessalonians", "timothy", "peter", "john", "mose",
}

// ordinalWords maps a spelled or roman ordinal (lower case) to its number.
var ordinalWords = map[string]int{
	"1": 1, "i": 1, "first": 1, "1st": 1,
	"2": 2, "ii": 2, "second": 2, "2nd": 2,
	"3": 3, "iii": 3, "third": 3, "3rd": 3,
	"4": 4, "iv": 4, "fourth": 4, "4th": 4,
	// Five for the German books of Moses ("5. Mose"); matchNumberedBook
	// accepts it for Mose only.
	"5": 5, "v": 5, "fifth": 5, "5th": 5,
}

// numberedBookRe: an ordinal token, a separator (a dot, a hyphen or
// underscore run, or whitespace: "1. John", "2 - Kings", "2-Peter"), a
// numbered book name, and a remainder that is empty or starts with something other
// than a letter or an apostrophe (a chapter number, " - ", "(") or the word
// chapter. The remainder rule keeps "First Kings of England" and "2 Peter's
// Journey" titles of their own.
//
// Whitespace here is ASCII only (RE2's \s), and web/src/utils/titleSortKey.ts
// uses the same explicit class, so the two cannot disagree on a NBSP.
var numberedBookRe = regexp.MustCompile(`(?i)^\s*([0-9a-z]+)(?:\.\s*|\s*[-_]+\s*|\s+)(` + strings.Join(numberedBooks, "|") +
	`)((?:\s*[^\pL\s'’].*)|(?:\s+(?:chapter|ch\.?)\b.*)|\s*)$`)

// mayBeOrdinal is the fast path in front of numberedBookRe: the first token
// (up to a space, dot, hyphen or underscore) must be an ordinal or made of
// the letters l/I/i, or no numbered-book rewrite is possible.
func mayBeOrdinal(title string) bool {
	t := strings.TrimLeft(title, " \t")
	end := strings.IndexAny(t, " \t.-_")
	if end <= 0 {
		return false
	}
	tok := strings.ToLower(t[:end])
	if _, ok := ordinalWords[tok]; ok {
		return true
	}
	return strings.Trim(tok, "li") == ""
}

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
	if !mayBeOrdinal(title) {
		return 0, "", "", false, false
	}
	m := numberedBookRe.FindStringSubmatch(title)
	if m == nil {
		return 0, "", "", false, false
	}
	n, letterL = ordinalOf(m[1])
	// Only the books of Moses go past four: "Fifth Corinthians" is no book.
	if n == 0 || (n > 4 && !strings.EqualFold(m[2], "mose")) {
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
	return joinSortForm(book, n, rest), true
}

// joinSortForm renders "<Book> <n>[ <rest>]", with no space before a rest
// that starts with punctuation: "John 1: Commentary", not "John 1 : …".
func joinSortForm(book string, n int, rest string) string {
	out := book + " " + strconv.Itoa(n)
	switch {
	case rest == "":
	case strings.ContainsRune(":;,.!?)", rune(rest[0])):
		out += rest
	default:
		out += " " + rest
	}
	return out
}

// SeriesSortForm is the sort form of a SERIES name: the numbered-book
// rewrite only when the whole name is a numbered book ("I Corinthians"), and
// ok=false otherwise, so a series keeps its ordinary article handling.
func SeriesSortForm(name string) (string, bool) {
	n, book, rest, _, ok := matchNumberedBook(name)
	if !ok || rest != "" {
		return name, false
	}
	return joinSortForm(book, n, ""), true
}

// SeriesSortKey is TitleSortKey for a series name (SeriesSortForm).
func SeriesSortKey(name string) string {
	if form, ok := SeriesSortForm(name); ok {
		return NormalizeTitle(form)
	}
	return NormalizeTitle(name)
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
	// m[2]:m[3] is the ordinal token; its separator is normalised to one
	// space before the book name (m[4]).
	return strings.TrimLeft(title[:m[2]], " \t") + strconv.Itoa(n) + " " + title[m[4]:], true
}
