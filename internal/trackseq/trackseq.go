// file: internal/trackseq/trackseq.go
// version: 1.0.0
// guid: 8b52d1a0-47f3-4e69-9c81-2a0d6f35e714
// last-edited: 2026-08-24

// Package trackseq answers one question: what sequence number does this
// audiobook filename carry?
//
// It exists because that question was being answered THREE times, differently,
// and nothing compared the answers:
//
//   - scanner.extractSeqNumber   (import: is this folder one book?)
//   - itunesservice.trackNum     (repair: what order do these tracks play in?)
//   - itunesservice.GroupShatteredBooks
//
// The divergence had a measured cost. Until 2026-08-24 the scanner's vocabulary
// had no TRAILING-number pattern, so a production folder of 80 files named
// "Pratchett 001".."Pratchett 080" extracted no number from any of them, failed
// the multi-file detector's pattern quorum, and was imported as 80 separate book
// rows -- each titled with its file stem, each taking the folder name as its
// author. The repair-side classifier had `trailNumRe` the entire time and would
// have read those files correctly. The scanner was simply the weaker copy of a
// judgement this repo had already made correctly elsewhere.
//
// SCOPE -- this package unifies the VOCABULARY, not the POLICY, and that
// boundary is deliberate. Callers keep their own gating because they ask
// opposite questions:
//
//   - The importer asks "is this folder ONE book?" and must bias toward NOT
//     welding unrelated books together, so it gates on tag quorum, numeric
//     density and a minimum file count.
//   - The repair classifier asks "are these EXISTING rows an already-shattered
//     book, and what shape?" and reasons about disc dirs, chapter subdirs,
//     edition markers and anthology-vs-trilogy markers.
//
// One function serving both biases would serve neither. Only the shared half --
// reading a number off a filename -- lives here.
package trackseq

import (
	"regexp"
	"strconv"
)

// patterns are applied in PRIORITY ORDER and the first match wins. Each captures
// the sequence number in group 1 and optionally a total in group 2.
//
// Order is the correctness property here, not a style choice. The trailing-number
// pattern is the loosest form in the list, so it must be reached only after every
// keyword-anchored and leading-number form has declined. Promote it and
// "Part 1 of 8" starts extracting 8 -- the TOTAL -- and every file in a folder
// sorts identically.
var patterns = []*regexp.Regexp{
	// "Chapter 01", "chapter_05"
	regexp.MustCompile(`(?i)\bchapter[\s_\-]+(\d{1,4})\b`),
	// "Part 1 of 8" / "Part 1"
	regexp.MustCompile(`(?i)\bpart[\s_\-]+(\d{1,4})(?:[\s_\-]+of[\s_\-]+(\d{1,4}))?\b`),
	// "Track 01"
	regexp.MustCompile(`(?i)\btrack[\s_\-]+(\d{1,4})\b`),
	// "Disc 01" / "CD 01"
	regexp.MustCompile(`(?i)\b(?:disc|cd)[\s_\-]+(\d{1,4})\b`),
	// "(76 of 85)"
	regexp.MustCompile(`\((\d{1,4})\s*of\s*(\d{1,4})\)`),
	// "(76/85)" or "(76_85)" or "(76-85)"
	regexp.MustCompile(`\((\d{1,4})[\s_\-\/](\d{1,4})\)`),
	// trailing " - 1_85" / " - 1/85" / "_1_85" near end of stem
	regexp.MustCompile(`[\s_\-](\d{1,4})[\s_\-\/](\d{1,4})$`),
	// "01 of 85"
	regexp.MustCompile(`(?i)\b(\d{1,4})\s+of\s+(\d{1,4})\b`),
	// leading "01 - ", "002. ", "1_"
	regexp.MustCompile(`^(\d{1,4})[\s_\-\.\:]`),
	// bare "01"
	regexp.MustCompile(`^(\d{1,4})$`),
	// TRAILING number: "Pratchett 001", "Carpe Jugulum 03", "Foo_12".
	// Last on purpose -- see the ordering note above.
	regexp.MustCompile(`[\s_\-](\d{1,4})$`),
}

// Extract returns the sequence number and, when the filename states it, the
// total, read from a filename STEM (no directory, no extension).
//
// ok is false when no pattern matched or the matched number was not positive.
// A zero or negative number is treated as no match: "00" carries no ordering
// information and callers use a positive number as their "this file is part of a
// sequence" signal.
func Extract(stem string) (num int, total int, ok bool) {
	for _, re := range patterns {
		m := re.FindStringSubmatch(stem)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			// Keep looking: a later, looser pattern may still find a usable
			// number in the same stem.
			continue
		}
		t := 0
		if len(m) > 2 && m[2] != "" {
			if parsed, perr := strconv.Atoi(m[2]); perr == nil && parsed > 0 {
				t = parsed
			}
		}
		return n, t, true
	}
	return 0, 0, false
}
