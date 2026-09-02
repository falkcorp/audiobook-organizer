// file: internal/metadata/series_position.go
// version: 1.0.0
// guid: 5c1d0a4e-9b73-4f26-8a51-6d2e4c9f7b30
// last-edited: 2026-09-02

package metadata

import (
	"regexp"
	"strconv"
	"strings"
)

// A series name should name the SERIES. These carry the book's position instead,
// which splits one series into as many one-book series as it has volumes:
// production holds 37 separate "Discworld NN" series, plus Safehold 01/02,
// Mistborn 3/6, Frontiers Saga 07 and 2,251 others.
//
// ─── WHY THIS LIVES IN internal/metadata AND NOT IN THE MAINTENANCE PLUGIN ───
//
// It used to live in internal/plugins/maintenance/series_denumber.go, reachable
// only through the manual, dry-run-by-default `maintenance.series-denumber` op.
// Every WRITE path — metadata apply, scanner, iTunes import, the series
// normalize pass — went through StripSeriesContamination instead, which matched
// three shapes and let the rest through verbatim. So contamination kept arriving
// faster than the manual op could clear it: 955 keyword-suffixed names and 6,859
// bare-numbered ones out of 42,495 series rows in production.
//
// There is now ONE matcher and both callers share it. Do not reintroduce a
// second copy of these regexes in the plugin — the plugin re-exports these
// symbols as type aliases (see series_denumber.go) precisely so that it can
// speak in them without owning them.
//
// ─── TWO CALLERS, TWO DIFFERENT POLICIES (this is deliberate) ───────────────
//
// This file answers "what shape is this name, and how much does the name itself
// vouch for the number being a position?". It does NOT decide whether to act.
// The two callers decide differently, on purpose:
//
//   - The WRITE path (StripSeriesContamination) strips every trailing shape
//     automatically, including a lone unpadded bare number. That is the library
//     owner's explicit instruction: "There is absolutely zero series that have a
//     number in them. And when we find one I'll manually override." The cost of
//     a false positive is one series name the owner retypes, and every strip is
//     logged so it can be found.
//
//   - The MERGE path (maintenance.SeriesDenumber / ApplyEligible) keeps its
//     corroboration requirement — zero-padding or a sibling sharing the base —
//     and never applies the low tier. It is not doing the same thing: a merge
//     CREATES AND DELETES SERIES ROWS across the whole library in one unattended
//     pass, so a wrong verdict there is expensive and hard to reverse, whereas a
//     wrong verdict on a single write is one row.
//
// If you are tempted to "unify" those two policies: don't. Making the write path
// as timid as the merge re-opens the bleed this file was moved to stop, and
// making the merge as eager as the write path hands an unattended job the right
// to delete series rows on a lone unpadded number.

// explicitPositionSuffix matches an UNAMBIGUOUS position marker: a keyword that
// only ever introduces a number. "Schooled in Magic: Book 11", "Reclaiming Honor
// bk 6", "The Long Winter Trilogy Book 3", "Nameless Sovereign #5".
//
// 🔑 `#` is in its own alternative rather than inside the `\b`-prefixed keyword
// group, and that is load-bearing. `\b` before the group asserts a word
// boundary, and `#` is a non-word character: with a space in front of it
// ("Sovereign #5") the assertion sits between two non-word characters and FAILS.
// The owner's own headline example was therefore matched by nothing at all
// while `#` sat inside the group looking handled. Verified with a probe before
// this was changed; `TestSplitSeriesPosition_HashSuffix` pins it.
var explicitPositionSuffix = regexp.MustCompile(
	`(?i)(?:[\s:,\-_]*\b(?:book|bk|vol|volume|part|pt|no|num)|[\s:,\-_]*#)\s*[.:#]?\s*(\d{1,3})\s*$`)

// barePositionSuffix matches a trailing number with no keyword: "Discworld 05".
// Riskier in the abstract — "Fahrenheit 451" and "Blake's 7" are real names — so
// the two callers treat it differently; see the policy note above.
var barePositionSuffix = regexp.MustCompile(`^(.*?)[\s\-_.]+(\d{1,3})$`)

// ─── Embedded positions ──────────────────────────────────────────────────────
//
// The two regexes above are `$`-anchored. That is NOT an oversight — trailing is
// the SAFE half. A number at the front or in the middle of a series name is far
// more often part of the real title, and the shapes are indistinguishable:
//
//	86—EIGHTY-SIX             ← "86" IS the series name (17 books in production)
//	08. Battle for the Abyss  ← "08" IS a Horus Heresy position
//
// Same shape, opposite meaning. So each embedded shape carries a confidence and
// only the ones a keyword vouches for are eligible to apply unattended.
// Design: docs/specs/2026-08-06-series-embedded-positions-design.md

// embeddedKeywordPosition matches a keyword-introduced position with the TITLE
// still trailing behind it: "Evil Genius: Book 4: Becoming the Apex Supervillain",
// "Vampire Hunter D: Vol 09: The Rose Princess", "Frontiers Saga Part 2: Rogue
// Castes". The keyword is the evidence, exactly as it is for the trailing shape.
var embeddedKeywordPosition = regexp.MustCompile(
	`(?i)^(.{2,}?)[\s,:\-]+(?:book|bk|vol|volume|part|pt)\s*\.?\s*(\d{1,3})\s*[:\-–—]\s*(\S.*)$`)

// bracketedPosition matches a single parenthesised/bracketed number at the end:
// "Dragon Born [04]", "The Hollows (7)". Brackets around a bare number are a
// deliberate mark — no real title wears them — but the number is unvouched, so
// this is medium rather than high.
var bracketedPosition = regexp.MustCompile(
	`(?i)^(.{2,}?)\s*[\(\[]\s*(?:book|bk|vol|volume|part|pt|#)?\s*(\d{1,3})\s*[\)\]]\s*$`)

// midColonPosition matches a bare number sitting between the series and the
// title: "Station 64: The Doll Dungeon". The largest new bucket (303 names) and
// the least trustworthy — "Station 64" is a perfectly plausible series name — so
// it is LOW and never applies unattended.
var midColonPosition = regexp.MustCompile(`^(.{2,}?)\s+(\d{1,3})\s*:\s*(\S.*)$`)

// leadingBarePosition matches a number at the very front: "08. Battle for the
// Abyss", "11. Fallen Angels".
//
// 🔑 The separator class is the load-bearing part. A genuine position prefix is
// punctuated like a list item — "08. ", "1 - " — while a number fused to the
// next word by an unspaced dash is part of the NAME: "86—EIGHTY-SIX",
// "5-Minute Sherlock". Requiring either a period or a SPACED dash is what keeps
// those two out of the candidate set entirely rather than relying on the low
// tier to hold them.
var leadingBarePosition = regexp.MustCompile(`^\s*(\d{1,3})\s*(?:[.)\]]|\s[-–—:]|\s)\s*(\S.*)$`)

// anyNumber counts position candidates for the multi-number refusal (spec D5).
var anyNumber = regexp.MustCompile(`\d+`)

// junkSeriesBases are base names that are not series at all. They come from disc
// and chapter tags being written into the series field, and production holds
// hundreds: "Chapter 12", "Disc 3". Merging these would create one giant bogus
// "Chapter" series, which is worse than leaving them split.
var junkSeriesBases = map[string]struct{}{
	"chapter": {}, "chapters": {}, "disc": {}, "disk": {}, "cd": {},
	"track": {}, "part": {}, "pt": {}, "book": {}, "vol": {}, "volume": {},
	"side": {}, "tape": {}, "file": {}, "section": {},
	// Bundle/packaging words. "Renegade Star: Publisher's Pack 7" numbers the
	// PACK, not the series — merging on it would gather unrelated bundles.
	"pack": {}, "publisher's pack": {}, "publishers pack": {}, "publisher": {},
	"box set": {}, "boxset": {}, "omnibus": {}, "collection": {}, "set": {},
}

// trailingJunkWord catches a base that ENDS in a tag keyword rather than being
// one outright: "The Tower of Nero - Chapter", "The Darkling Child: ... , Chapter".
// Stripping "Chapter 12" leaves the keyword stranded on the end, and merging on
// that base manufactures a series named after the word "chapter".
var trailingJunkWord = regexp.MustCompile(
	`(?i)[\s:,\-–—_]+(chapter|chapters|disc|disk|cd|track|part|pt|book|vol|volume|side|tape|section|pack|omnibus|box set|boxset)\s*$`)

// leadingSeparator catches the mirror-image artefact the embedded shapes create.
// The existing guard only checks SUFFIXES because it was built for a parser that
// only stripped suffixes; stripping a LEADING number strands the separator at the
// front instead — ". Battle for the Abyss", "- Fallen Angels".
var leadingSeparator = regexp.MustCompile(`^[\s:,\-–—_.)\]]+`)

// hasLetter reports whether s contains any letter at all. A base of "01" or "—"
// is not a name.
func hasLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127 {
			return true
		}
	}
	return false
}

// IsJunkSeriesBase reports whether a stripped base name is a tag artefact rather
// than a series.
//
// The dry run against production is what taught this its shape: an
// equality-only check let through 76 merges into
// "The Darkling Child: ... (Unabridged), Chapter", 39 into
// "The Tower of Nero - Chapter", and 11 each into bases of "01".."04".
func IsJunkSeriesBase(base string) bool {
	b := strings.TrimSpace(base)
	// A single character is never a series name, and the embedded shapes can
	// strip a name down to one ("2: A" → base "A"). Landing this WITH the parser
	// rather than after it is deliberate: a mistake in a new shape fails closed.
	if len([]rune(b)) < 2 {
		return true
	}
	if _, ok := junkSeriesBases[strings.ToLower(b)]; ok {
		return true
	}
	if trailingJunkWord.MatchString(b) {
		return true
	}
	// The leading-number shapes strand punctuation at the FRONT, where the
	// suffix checks below cannot see it.
	if leadingSeparator.MatchString(b) {
		return true
	}
	// Purely numeric or punctuation-only: "01", "—", "3.".
	if !hasLetter(b) {
		return true
	}
	// A base left dangling on a separator ("Drew Hayes – Bones of the Past –")
	// means the split landed mid-name rather than at a real boundary.
	if strings.HasSuffix(b, "-") || strings.HasSuffix(b, "–") || strings.HasSuffix(b, "—") ||
		strings.HasSuffix(b, ":") || strings.HasSuffix(b, ",") {
		return true
	}
	return false
}

// SeriesShape names which textual pattern carried the position. It exists so
// callers can gate and report per shape: the trailing shapes are safe to apply
// on a write, the un-vouched embedded ones are not (see StripSeriesContamination).
type SeriesShape string

const (
	ShapeTrailingKeyword SeriesShape = "trailing-keyword" // "Schooled in Magic: Book 11"
	ShapeTrailingBare    SeriesShape = "trailing-bare"    // "Discworld 05"
	ShapeEmbeddedKeyword SeriesShape = "embedded-keyword" // "Evil Genius: Book 4: Becoming…"
	ShapeBracketed       SeriesShape = "bracketed"        // "Dragon Born [04]"
	ShapeMidColon        SeriesShape = "mid-colon"        // "Station 64: The Doll Dungeon"
	ShapeLeadingBare     SeriesShape = "leading-bare"     // "08. Battle for the Abyss"
)

// SeriesConfidence is how much the name alone vouches for the number being a
// position. Only high is eligible to apply unattended in the MERGE path; low
// NEVER applies there, at any setting, because no string-only rule separates
// "86—EIGHTY-SIX" from "08. Battle for the Abyss" (spec D3).
type SeriesConfidence string

const (
	SeriesConfidenceHigh   SeriesConfidence = "high"
	SeriesConfidenceMedium SeriesConfidence = "medium"
	SeriesConfidenceLow    SeriesConfidence = "low"
)

// SeriesSplit is the outcome of parsing one series name.
type SeriesSplit struct {
	Base     string // the series name with the position removed
	Position int    // the parsed position, 0 when none
	Explicit bool   // true when a keyword marked the position ("Book 3")
	Padded   bool   // true when the number was zero-padded ("05")
	Shape    SeriesShape
	// Confidence is the SHAPE's inherent trust. For the trailing-bare shape it is
	// only a floor — maintenance.SeriesDenumber raises it when the library
	// corroborates the number (padding, or a sibling series sharing the base),
	// which is evidence this function cannot see.
	Confidence SeriesConfidence
}

// countPositionCandidates counts digit runs in a name. Two or more means the name
// offers two candidate positions and picking one is a guess.
func countPositionCandidates(name string) int {
	return len(anyNumber.FindAllString(name, -1))
}

// SplitSeriesPosition separates a book position from a series name.
//
// ok=false means the name carries no position and must be left exactly as it is.
//
// 🔑 Explicit and Padded are reported rather than acted on, because a bare
// trailing number is genuinely ambiguous in isolation: "Discworld 5" is a
// position and "Fahrenheit 451" is not. The caller decides — see the two-policy
// note at the top of this file.
func SplitSeriesPosition(name string) (SeriesSplit, bool) {
	n := strings.TrimSpace(name)
	if n == "" {
		return SeriesSplit{}, false
	}

	// ── Trailing shapes, tried FIRST and left exactly as they were. Anything the
	// op already handles in production must keep resolving identically, so the
	// new shapes below can only ever claim names these two decline.
	if m := explicitPositionSuffix.FindStringSubmatch(n); m != nil {
		base := strings.TrimSpace(strings.TrimRight(n[:len(n)-len(m[0])], " :,-_"))
		pos, _ := strconv.Atoi(m[1])
		if base == "" || pos <= 0 {
			return SeriesSplit{}, false
		}
		return SeriesSplit{Base: base, Position: pos, Explicit: true,
			Padded: strings.HasPrefix(m[1], "0"),
			Shape:  ShapeTrailingKeyword, Confidence: SeriesConfidenceHigh}, true
	}

	if m := barePositionSuffix.FindStringSubmatch(n); m != nil {
		base := strings.TrimSpace(m[1])
		pos, _ := strconv.Atoi(m[2])
		if base == "" || pos <= 0 {
			return SeriesSplit{}, false
		}
		// Low is the FLOOR, not the verdict: maintenance.SeriesDenumber promotes
		// this to high on padding or a sibling base, and drops it entirely
		// without either. The write path applies it regardless, by instruction.
		return SeriesSplit{Base: base, Position: pos, Explicit: false,
			Padded: strings.HasPrefix(m[2], "0"),
			Shape:  ShapeTrailingBare, Confidence: SeriesConfidenceLow}, true
	}

	// ── Spec D5: refuse a name offering two candidate positions.
	//
	// "The Demon Wars Saga [07] Immortalis [02]" — 07 is the series position, 02
	// is not, and nothing in the string says which. Guessing writes a wrong
	// position AND a wrong base, so refuse outright.
	//
	// This gate sits BELOW the trailing shapes on purpose. Applying it to them
	// would newly refuse names like "The 100 Book 3" that production already
	// resolves correctly — a coverage regression dressed up as caution. Neither
	// two-bracket example reaches here by accident: both end in "]", which
	// barePositionSuffix cannot match.
	if countPositionCandidates(n) > 1 {
		return SeriesSplit{}, false
	}

	// Explicit stays FALSE for every embedded shape even where a keyword vouches
	// for it. maintenance.SeriesDenumber's evidence switch has a `case
	// sp.Explicit` arm that applies unattended; inheriting it here would ship 61
	// books' worth of merges on the first apply with no gate of their own. Shape
	// is the wiring now.
	if m := embeddedKeywordPosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[1], m[2], m[3], ShapeEmbeddedKeyword, SeriesConfidenceHigh); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	if m := bracketedPosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[1], m[2], "", ShapeBracketed, SeriesConfidenceMedium); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	if m := midColonPosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[1], m[2], m[3], ShapeMidColon, SeriesConfidenceLow); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	// rest is "" here, not m[2]: for a leading number the remainder IS the base,
	// so passing it as rest would trip the base==title check on every name.
	if m := leadingBarePosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[2], m[1], "", ShapeLeadingBare, SeriesConfidenceLow); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	return SeriesSplit{}, false
}

// embeddedSplit validates one embedded-shape match into a SeriesSplit.
//
// rest is the title text trailing the number, empty when the shape has none. A
// base identical to that title means the split found nothing real —
// "Rebirth Online 2: Rebirth Online" is one name repeated, not a series and its
// volume (spec D6).
func embeddedSplit(rawBase, rawPos, rest string, shape SeriesShape, conf SeriesConfidence) (SeriesSplit, bool) {
	base := strings.TrimSpace(rawBase)
	pos, err := strconv.Atoi(rawPos)
	if err != nil || pos <= 0 {
		return SeriesSplit{}, false
	}
	if IsJunkSeriesBase(base) {
		return SeriesSplit{}, false
	}
	if rest != "" && strings.EqualFold(base, strings.TrimSpace(rest)) {
		return SeriesSplit{}, false
	}
	return SeriesSplit{
		Base: base, Position: pos, Padded: strings.HasPrefix(rawPos, "0"),
		Shape: shape, Confidence: conf,
	}, true
}
