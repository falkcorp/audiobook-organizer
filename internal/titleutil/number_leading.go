// file: internal/titleutil/number_leading.go
// version: 1.0.0
// guid: c221f6dd-df76-430b-be42-1a55d8faec16
// last-edited: 2026-10-03

package titleutil

import (
	"strings"
	"unicode"
)

// LegitNumberTitles are real book titles that start with a digit. They are the
// allowlist IsNumberLeadingTitle consults, so a census of "titles that are
// probably a stray track number" does not count them and a fixer never
// rewrites them. Matched case-insensitively against the trimmed title; add a
// title here, not a special case in a caller.
var LegitNumberTitles = []string{
	"1984",
	"11/22/63",
	"2001: A Space Odyssey",
	"2010: Odyssey Two",
	"1Q84",
	"1491",
	"1493",
	"1776",
	"12 Rules for Life",
	"13 Reasons Why",
	"20,000 Leagues Under the Sea",
	"1632",
	"1635",
	"2312",
	"1066",
	"84K",
	"11.22.63",
}

// legitNumberTitleSet is LegitNumberTitles folded for the lookup.
var legitNumberTitleSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(LegitNumberTitles))
	for _, t := range LegitNumberTitles {
		m[foldTitle(t)] = struct{}{}
	}
	return m
}()

func foldTitle(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

// IsNumberLeadingTitle reports whether title, after trimming surrounding
// whitespace, starts with a decimal digit and is NOT one of
// LegitNumberTitles (compared case-insensitively, exact match). It is the
// predicate behind the audiobook_organizer_number_leading_titles gauge and the
// number-leading-titles fixer, so both see the same set of books.
func IsNumberLeadingTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	r := []rune(t)[0]
	if !unicode.IsDigit(r) {
		return false
	}
	_, legit := legitNumberTitleSet[foldTitle(t)]
	return !legit
}
