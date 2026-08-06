// file: internal/plugins/maintenance/series_denumber.go
// version: 2.0.0
// guid: dee834d3-1f7e-453e-9303-85d37479e79d
// last-edited: 2026-08-06

package maintenance

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
// maintenance.series-normalize cannot see this — metadata.StripSeriesContamination
// returns every one of these unchanged with pos="" — so the merge machinery
// downstream is never told they belong together.

// explicitPositionSuffix matches an UNAMBIGUOUS position marker: a keyword that
// only ever introduces a number. "Schooled in Magic: Book 11", "Reclaiming Honor
// bk 6", "The Long Winter Trilogy Book 3".
var explicitPositionSuffix = regexp.MustCompile(
	`(?i)[\s:,\-_]*\b(?:book|bk|vol|volume|part|pt|no|num|#)\s*[.:#]?\s*(\d{1,3})\s*$`)

// barePositionSuffix matches a trailing number with no keyword: "Discworld 05".
// Far riskier — "Fahrenheit 451" and "Blake's 7" are real names — so the caller
// decides whether to trust it (see SeriesDenumber).
var barePositionSuffix = regexp.MustCompile(`^(.*?)[\s\-_.]+(\d{1,3})$`)

// ─── Embedded positions (owner item 4) ───────────────────────────────────────
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

// SeriesShape names which textual pattern carried the position. It exists so the
// op can gate and report per shape: the two trailing shapes are long-established
// production behaviour, the embedded ones are new and land behind their own gate.
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
// position. Only high is eligible to apply unattended; low NEVER applies, at any
// setting, because no string-only rule separates "86—EIGHTY-SIX" from
// "08. Battle for the Abyss" (spec D3).
type SeriesConfidence string

const (
	ConfidenceHigh   SeriesConfidence = "high"
	ConfidenceMedium SeriesConfidence = "medium"
	ConfidenceLow    SeriesConfidence = "low"
)

// SeriesSplit is the outcome of parsing one series name.
type SeriesSplit struct {
	Base     string // the series name with the position removed
	Position int    // the parsed position, 0 when none
	Explicit bool   // true when a keyword marked the position ("Book 3")
	Padded   bool   // true when the number was zero-padded ("05")
	Shape    SeriesShape
	// Confidence is the SHAPE's inherent trust. For the trailing-bare shape it is
	// only a floor — SeriesDenumber raises it when the library corroborates the
	// number (padding, or a sibling series sharing the base), which is evidence
	// this function cannot see.
	Confidence SeriesConfidence
}

// countPositionCandidates counts digit runs in a name. Two or more means the name
// offers two candidate positions and picking one is a guess.
func countPositionCandidates(name string) int {
	return len(anyNumber.FindAllString(name, -1))
}

// SplitSeriesPosition separates a trailing book position from a series name.
//
// ok=false means the name carries no position and must be left exactly as it is.
//
// 🔑 Explicit and Padded are reported rather than acted on, because a bare
// trailing number is genuinely ambiguous: "Discworld 5" is a position and
// "Fahrenheit 451" is not. The caller decides, using evidence this function
// cannot see (see SeriesDenumber).
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
			Shape:  ShapeTrailingKeyword, Confidence: ConfidenceHigh}, true
	}

	if m := barePositionSuffix.FindStringSubmatch(n); m != nil {
		base := strings.TrimSpace(m[1])
		pos, _ := strconv.Atoi(m[2])
		if base == "" || pos <= 0 {
			return SeriesSplit{}, false
		}
		// Low is the FLOOR, not the verdict: SeriesDenumber promotes this to high
		// on padding or a sibling base, and drops it entirely without either.
		return SeriesSplit{Base: base, Position: pos, Explicit: false,
			Padded: strings.HasPrefix(m[2], "0"),
			Shape:  ShapeTrailingBare, Confidence: ConfidenceLow}, true
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
	// for it. SeriesDenumber's evidence switch has a `case sp.Explicit` arm that
	// applies unattended; inheriting it here would ship 61 books' worth of merges
	// on the first apply with no gate of their own. Shape is the wiring now.
	if m := embeddedKeywordPosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[1], m[2], m[3], ShapeEmbeddedKeyword, ConfidenceHigh); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	if m := bracketedPosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[1], m[2], "", ShapeBracketed, ConfidenceMedium); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	if m := midColonPosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[1], m[2], m[3], ShapeMidColon, ConfidenceLow); ok {
			return sp, true
		}
		return SeriesSplit{}, false
	}

	// rest is "" here, not m[2]: for a leading number the remainder IS the base,
	// so passing it as rest would trip the base==title check on every name.
	if m := leadingBarePosition.FindStringSubmatch(n); m != nil {
		if sp, ok := embeddedSplit(m[2], m[1], "", ShapeLeadingBare, ConfidenceLow); ok {
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

// ApplyEligible reports whether a plan may be executed unattended.
//
// 🔒 Low is unconditional: there is no parameter that lets it through. The whole
// reason the tier exists is that "86—EIGHTY-SIX" and "08. Battle for the Abyss"
// are the same string shape, and a merge creates and deletes series rows.
// allowMedium exists so the first production apply can be scoped to keyword-
// vouched names only, with the dry run reporting what medium WOULD have done.
func ApplyEligible(p SeriesMergePlan, allowMedium bool) bool {
	switch p.Confidence {
	case ConfidenceHigh:
		return true
	case ConfidenceMedium:
		return allowMedium
	default:
		return false
	}
}

// SeriesInput is one series row, reduced to what the planner needs.
type SeriesInput struct {
	ID       int
	Name     string
	AuthorID int // 0 when unset
	Books    int
}

// SeriesMergePlan is one series that should fold into another.
type SeriesMergePlan struct {
	FromID   int
	FromName string
	IntoName string // canonical base name
	IntoID   int    // existing series with the base name, 0 when one must be created
	Position int
	Reason   string
	Books    int
	Shape    SeriesShape
	// Confidence decides eligibility, not Reason. It lives on the PLAN and not
	// only on the split because the op consumes plans, and a merge that creates
	// and deletes series rows is the destructive step that needs the gate.
	Confidence SeriesConfidence
}

// SeriesDenumber plans the merges that collapse "<Series> <N>" rows into one
// series per base name, per author.
//
// A bare trailing number is only trusted when the library itself corroborates it:
//
//   - the number is zero-padded ("Discworld 05") — nobody titles a work "Blake's 07"; or
//   - another series shares the same base ("Mistborn 3" alongside "Mistborn 6").
//
// A lone, unpadded "Blake's 7" therefore stays untouched, which is the whole
// point: the cost of wrongly merging a real name is a corrupted series, while the
// cost of skipping one is that it stays as it is.
//
// Explicit markers ("Book 3") need no corroboration — the keyword is the evidence.
func SeriesDenumber(in []SeriesInput) []SeriesMergePlan {
	type key struct {
		base   string
		author int
	}

	// Pass 1 — parse every name and count how many series share each base.
	splits := make(map[int]SeriesSplit, len(in))
	baseCount := map[key]int{}
	for _, s := range in {
		sp, ok := SplitSeriesPosition(s.Name)
		if !ok || IsJunkSeriesBase(sp.Base) {
			continue
		}
		splits[s.ID] = sp
		baseCount[key{strings.ToLower(sp.Base), s.AuthorID}]++
	}

	// Pass 2 — an existing series already named exactly the base is the target.
	canonical := map[key]int{}
	for _, s := range in {
		if _, numbered := splits[s.ID]; numbered {
			continue
		}
		canonical[key{strings.ToLower(strings.TrimSpace(s.Name)), s.AuthorID}] = s.ID
	}

	var plans []SeriesMergePlan
	for _, s := range in {
		sp, ok := splits[s.ID]
		if !ok {
			continue
		}
		k := key{strings.ToLower(sp.Base), s.AuthorID}

		reason := ""
		conf := sp.Confidence
		switch {
		// ── Trailing shapes: verdicts unchanged from the shipped behaviour. ──
		case sp.Shape == ShapeTrailingKeyword:
			reason, conf = "explicit position keyword", ConfidenceHigh
		case sp.Shape == ShapeTrailingBare && sp.Padded:
			reason, conf = "zero-padded position", ConfidenceHigh
		case sp.Shape == ShapeTrailingBare && baseCount[k] > 1:
			reason, conf = "another series shares this base", ConfidenceHigh
		case sp.Shape == ShapeTrailingBare:
			// A lone unpadded trailing number is not evidence. Dropped outright
			// rather than reported as low, so "Fahrenheit 451" never appears in a
			// report an operator might act on.
			continue

		// ── Embedded shapes (owner item 4). ──
		case sp.Shape == ShapeEmbeddedKeyword:
			reason = "embedded position keyword"
		case sp.Shape == ShapeBracketed:
			reason = "bracketed position"
		case sp.Shape == ShapeMidColon:
			reason = "bare number before the title"
		case sp.Shape == ShapeLeadingBare:
			reason = "bare leading number"
		default:
			continue
		}

		plans = append(plans, SeriesMergePlan{
			FromID: s.ID, FromName: s.Name, IntoName: sp.Base,
			IntoID: canonical[k], Position: sp.Position, Reason: reason, Books: s.Books,
			Shape: sp.Shape, Confidence: conf,
		})
	}
	return plans
}
