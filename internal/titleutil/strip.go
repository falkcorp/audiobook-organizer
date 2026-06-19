// file: internal/titleutil/strip.go
// version: 1.1.0
// guid: 7e2a1b3c-4d5e-6f7a-8b9c-0d1e2f3a4b5c

// Package titleutil provides shared helpers for normalising book titles.
package titleutil

import (
	"regexp"
	"strings"
)

// chapterPrefixPatterns matches a leading chapter/track marker on an iTunes
// per-chapter track Name. Order matters — most-specific first. Each pattern
// is anchored at the start of the string; the matched span is stripped.
//
// Examples this catches:
//
//	"(76/85) Tarkin: Star Wars (Unabridged)"   → "Tarkin: Star Wars (Unabridged)"
//	"(76 of 85) Tarkin: Star Wars"             → "Tarkin: Star Wars"
//	"Chapter 03 - The Storm"                   → "The Storm"
//	"Chapter 03: The Storm"                    → "The Storm"
//	"Track 12 - Foo"                           → "Foo"
//	"Part 4 - Bar"                             → "Bar"
//	"03 - Foo"                                 → "Foo"
//
// Does NOT touch titles without a leading marker (e.g. "The Hobbit").
var chapterPrefixPatterns = []*regexp.Regexp{
	// "(76 of 85)" / "(76/85)" / "(76-85)" / "(76_85)" with trailing space
	regexp.MustCompile(`^\(\s*\d{1,4}\s*(?:of|[\s_\-\/])\s*\d{1,4}\s*\)\s+`),
	// "Chapter 03 - " / "Chapter 03: " / "Chapter 03 "
	regexp.MustCompile(`(?i)^chapter[\s_\-]+\d{1,4}\s*[\-:\s]\s*`),
	// "Track 12 - " / "Track 12: "
	regexp.MustCompile(`(?i)^track[\s_\-]+\d{1,4}\s*[\-:\s]\s*`),
	// "Part 4 - " / "Part 4 of 8 - "
	regexp.MustCompile(`(?i)^part[\s_\-]+\d{1,4}(?:\s+of\s+\d{1,4})?\s*[\-:\s]\s*`),
	// Leading bare number with delimiter: "03 - " / "002. " / "1: "
	regexp.MustCompile(`^\d{1,4}\s*[\-:\.]\s+`),
}

// StripChapterPrefix removes a leading chapter/track marker from a book
// title, so that iTunes per-chapter track names can be used as Book.Title
// without the "(76/85)" / "Chapter 03" prefix leaking in. Idempotent; safe
// to call on titles that have no prefix.
func StripChapterPrefix(title string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return s
	}
	for _, re := range chapterPrefixPatterns {
		if loc := re.FindStringIndex(s); loc != nil && loc[0] == 0 {
			s = strings.TrimSpace(s[loc[1]:])
			break
		}
	}
	return s
}

// chapterSuffixPatterns matches a TRAILING part marker on an iTunes per-chapter
// track Name, e.g. the reported "At All Costs – 11/23". Order matters — most
// specific first. Each pattern is anchored at the end of the string.
//
// Every pattern requires an explicit N/M shape (two numbers, or "(N of M)"),
// so a title ending in a lone number ("Catch 22", "1984") is left untouched.
//
//	"At All Costs – 11/23"   → "At All Costs"
//	"At All Costs (11 of 23)"→ "At All Costs"
//	"At All Costs 11/23"     → "At All Costs"
var chapterSuffixPatterns = []*regexp.Regexp{
	// Trailing "(11/23)" / "(11 of 23)" / "(1-2)" / "(1_2)"
	regexp.MustCompile(`\s*\(\s*\d{1,4}\s*(?:of|[\s_\-/])\s*\d{1,4}\s*\)$`),
	// Trailing delimiter then fraction: " – 11/23" / " - 13/23" / " : 1/23"
	regexp.MustCompile(`\s*[-–—:]\s*\d{1,4}\s*/\s*\d{1,4}$`),
	// Trailing "11 of 23"
	regexp.MustCompile(`(?i)\s+\d{1,4}\s+of\s+\d{1,4}$`),
	// Trailing bare fraction "11/23"
	regexp.MustCompile(`\s+\d{1,4}\s*/\s*\d{1,4}$`),
}

// StripChapterSuffix removes a trailing part/chapter marker ("– 11/23",
// "(11 of 23)", "11/23") from a book title. It exists so that iTunes
// per-chapter track names with a TRAILING marker collapse to a single album
// key (StripChapterPrefix only handles leading markers). Idempotent; only
// strips an explicit N-of-M shape, so lone trailing numbers are preserved.
func StripChapterSuffix(title string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return s
	}
	for _, re := range chapterSuffixPatterns {
		if loc := re.FindStringIndex(s); loc != nil && loc[1] == len(s) {
			s = strings.TrimSpace(s[:loc[0]])
			break
		}
	}
	return s
}
