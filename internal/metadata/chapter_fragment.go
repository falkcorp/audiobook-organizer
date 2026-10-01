// file: internal/metadata/chapter_fragment.go
// version: 1.1.0
// guid: 7d2f1a4c-9b6e-4c0a-8f31-2e5a9c1d3b67
// last-edited: 2026-09-30

package metadata

import (
	"regexp"
	"strings"
)

// Chapter-fragment detection — context.
//
// "Shattered" audiobooks were imported into the library as one book PER CHAPTER:
// each chapter is its own Book row with a title like "06 Chapter 6", a single
// short mp3, and a path like
//
//	.../Metro 2034/Metro 2034 - 06 Chapter 6/06 Chapter 6/06 Chapter 6 - Metro 2034 - read by narrator.mp3
//
// When the bulk metadata matcher searches a catalog (Audible / OpenLibrary /
// etc.) for a title like "06 Chapter 6" it confidently matches a RANDOM catalog
// entry at ~100%+ confidence (e.g. "06 Chapter 6" -> some 2026 public-domain
// "Daniel Boone" book at 101%). Applying that writes garbage onto every chapter.
//
// IsLikelyChapterFragment is a CONSERVATIVE, title-pattern-only guard so callers
// can skip catalog search/matching for these obvious fragments. It must avoid
// false positives on real books, so it deliberately keys only on the title
// (never on duration — legitimate short books exist).

var (
	// "06 Chapter 6", "01 - Track 1", "12. Part 3", "3 disc 2"
	// A leading number, optional separator, then a chapter/track keyword.
	chapterFragNumberThenWord = regexp.MustCompile(`(?i)^\d{1,3}\s*[-.]?\s*(?:chapter|track|part|disc|cd|section)\b`)

	// "Chapter 6", "Track 1", "Part 2", "Disc 2", "CD 3", "Section 4"
	// A chapter/track keyword that MUST be followed by a number. Requiring the
	// trailing number is what keeps real titles like "Part of Your World" and
	// "Discworld" out of the net.
	chapterFragWordThenNumber = regexp.MustCompile(`(?i)^(?:chapter|track|part|disc|cd|section)\s*\d+\b`)

	// Pure zero-padded numeric titles like "06", "01", "012".
	// A leading zero is REQUIRED so bare years/numbers that are legitimate
	// titles ("1984", "2001", "451") are NOT treated as fragments.
	chapterFragZeroPadded = regexp.MustCompile(`^0\d*$`)

	// "Before They Are Hanged 002 of 341", "Cobra 100 of 151", "The Tears of
	// the Sun A Novel of the Change Part 02 of 63": a file's position out of a
	// count, anywhere in the title. Digits on BOTH sides, so "13 of Hearts"
	// and "One of Us" are not matched.
	chapterFragOfCount = regexp.MustCompile(`(?i)\b\d{1,4}\s*of\s*\d{1,4}\b`)

	// "Elantris 084 of": the same position with the count cut off by a
	// filename length limit. Anchored at the end so "4 of a Kind Stories"
	// stays a title.
	chapterFragOfTail = regexp.MustCompile(`(?i)\b\d{1,4}\s+of\s*$`)

	// "Elantris_copy179": a file manager's duplicate-name suffix. Never part
	// of a book's title; "_copyright" is not matched.
	chapterFragCopySuffix = regexp.MustCompile(`(?i)_copy\d*\b`)
)

// IsLikelyChapterFragment reports whether title looks like a single-chapter
// fragment of a shattered audiobook (see file-level doc comment). It is
// intentionally conservative: only obvious chapter/track-style titles match,
// so legitimate books are never suppressed from metadata matching.
//
// TRUE examples:  "06 Chapter 6", "Chapter 6", "01 - Track 1", "Disc 2", "06",
//
//	"Cobra 100 of 151", "Elantris 084 of", "Part 02 of 63", "Elantris_copy179"
//
// FALSE examples: "The Moons of Barsk", "Metro 2034", "1984",
//
//	"Part of Your World", "Discworld", "Catch-22"
func IsLikelyChapterFragment(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	if chapterFragNumberThenWord.MatchString(t) {
		return true
	}
	if chapterFragWordThenNumber.MatchString(t) {
		return true
	}
	if chapterFragZeroPadded.MatchString(t) {
		return true
	}
	return IsCountedPartTitle(t)
}

// IsCountedPartTitle reports whether title carries a file's position out of
// a count ("002 of 341", "Part 02 of 63", "084 of" cut short) or a file
// manager's copy suffix ("_copy179"). Either names ONE file of a set, so the
// title is a chapter fragment whatever words surround it. A caller holding
// the book's folder (metabatch.ResolveCandidateSearchQuery) also refuses any
// stand-in title for such a row when it is one of several file rows in its
// folder: the stand-in names the whole work, not this file.
func IsCountedPartTitle(title string) bool {
	t := strings.TrimSpace(title)
	return chapterFragOfCount.MatchString(t) || chapterFragOfTail.MatchString(t) ||
		chapterFragCopySuffix.MatchString(t)
}

// trailingPartTokenRe is a title ending in a bare part token after a space
// or underscore: a 1-4 digit number ("The Sunrise Lands 1") or one capital
// letter ("Sealed to the Flame E"). The stem must hold a letter. A hyphen is
// not a separator, so "Catch-22" never splits.
var trailingPartTokenRe = regexp.MustCompile(`^(.*\pL.*?)[\s_]+(\d{1,4}|[A-Z])$`)

// partTokenLabelRe: the stem ends in a section label, so the token is a
// labelled series position ("Mistborn Book 1", "The Way of Kings, Part 1",
// "Halls of Power: Ancient Dreams, Book 3"), which stripChapterFromTitle
// already cleans for a search, not a bare file-part token.
var partTokenLabelRe = regexp.MustCompile(`(?i)\b` + sectionLabels + `\.?$`)

// SiblingPartStem splits a title that ends in a bare part token into its
// stem and token: "The Sunrise Lands 1" -> ("The Sunrise Lands", "1"),
// "Sealed to the Flame E" -> ("Sealed to the Flame", "E").
//
// The shape alone is NOT evidence: "Apollo 13", "Plan B", "Vitamin C" and
// "Malcolm X" are whole books. A caller treats the title as a chapter part
// only when other book rows in the same folder share the stem with a
// different token (metabatch: titleJudge.stemSiblings). Year-like numbers
// ("Metro 2034", "Blade Runner 2049") and a labelled position ("Book 1",
// "Part 1") never split.
func SiblingPartStem(title string) (stem, token string, ok bool) {
	t := strings.TrimSpace(title)
	m := trailingPartTokenRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", false
	}
	stem = strings.TrimRight(strings.TrimSpace(m[1]), " ,-_:")
	token = m[2]
	if stem == "" || yearLikeTitleRe.MatchString(token) || partTokenLabelRe.MatchString(stem) {
		return "", "", false
	}
	return stem, token, true
}

// ripJunkGroupRe is a bracketed or parenthesised group holding a rip's
// encoding details: a bitrate ("64k", "128kbps"), a size ("577MB", "1.2 GB")
// or a running time written with semicolons or colons ("20;57;42"), as in
// "2002 - Neil Gaiman - American Gods [64k 20;57;42 577MB]".
var ripJunkGroupRe = regexp.MustCompile(`(?i)\s*[\[(][^\])]*?\b(?:\d{2,3}\s*k(?:bps)?|\d+(?:\.\d+)?\s*[mg]i?b|\d{1,2}[;:]\d{2}[;:]\d{2})\b[^\])]*[\])]`)

// StripRipJunk removes the rip-detail groups ripJunkGroupRe matches and
// reports whether there were any. The remainder is "" when nothing but junk
// was there.
func StripRipJunk(title string) (cleaned string, had bool) {
	if !ripJunkGroupRe.MatchString(title) {
		return strings.TrimSpace(title), false
	}
	cleaned = ripJunkGroupRe.ReplaceAllString(title, "")
	return strings.Trim(strings.TrimSpace(cleaned), " -_"), true
}
