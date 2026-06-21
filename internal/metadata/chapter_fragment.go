// file: internal/metadata/chapter_fragment.go
// version: 1.0.0
// guid: 7d2f1a4c-9b6e-4c0a-8f31-2e5a9c1d3b67
// last-edited: 2026-06-21

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
)

// IsLikelyChapterFragment reports whether title looks like a single-chapter
// fragment of a shattered audiobook (see file-level doc comment). It is
// intentionally conservative: only obvious chapter/track-style titles match,
// so legitimate books are never suppressed from metadata matching.
//
// TRUE examples:  "06 Chapter 6", "Chapter 6", "01 - Track 1", "Disc 2", "06"
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
	return false
}
