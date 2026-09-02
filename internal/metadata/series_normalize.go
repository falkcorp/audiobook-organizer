// file: internal/metadata/series_normalize.go
// version: 2.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-09-02

package metadata

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reDashPositionTitle = regexp.MustCompile(`^(.+?)\s+-\s+(\d+)\s+-\s+.+$`)
	reTrailingOrdinal   = regexp.MustCompile(`(?i)^(.+?)\s+(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)$`)
)

// reTrailingDigit (`^(.+?)\s+-\s+(\d{1,2})$`) is GONE, not lost. It required a
// SPACED DASH and at most two digits, which is a strict subset of what
// barePositionSuffix now matches via SplitSeriesPosition ("Big Series - 99" →
// "Big Series"/99 either way). Keeping both would mean two rules answering the
// same question, and the narrower one silently deciding which. Pinned by
// TestStripSeriesContamination's "trailing digit with dash-space" case.

var ordinalToDigit = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
	"six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
	"eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14",
	"fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18",
	"nineteen": "19", "twenty": "20",
}

// SeriesFlagReason names WHY a series name needs a human to look at it.
//
// 🔑 It exists because `flagForReview` used to mean exactly one thing —
// "the series name is identical to the book title" — and this change gives it a
// second, unrelated meaning: "there is a number in here but stripping it would
// produce garbage". A single bool carrying two meanings is the defect shape this
// repo keeps re-shipping, so the reason travels WITH the flag rather than being
// inferred downstream from the name.
type SeriesFlagReason string

const (
	// FlagNone is the zero value: not flagged.
	FlagNone SeriesFlagReason = ""
	// FlagNameEqualsTitle: the series field was filled with the book's title.
	FlagNameEqualsTitle SeriesFlagReason = "name-equals-title"
	// FlagUnvouchedPosition: a number sits at the FRONT or MIDDLE of the name
	// with no keyword vouching for it. "86—EIGHTY-SIX" and
	// "08. Battle for the Abyss" are the same string shape with opposite
	// meanings, and stripping the wrong one yields "—EIGHTY-SIX". So the name is
	// left exactly as it is and a human decides.
	FlagUnvouchedPosition SeriesFlagReason = "unvouched-embedded-position"
)

// Rule names, reported so that every automatic rewrite of user-visible data can
// be traced back to the rule that made it. These are logged at each write path.
const (
	RuleDashPositionTitle = "dash-position-title"
	RuleTrailingKeyword   = "trailing-keyword"
	RuleTrailingBare      = "trailing-bare"
	RuleBracketed         = "bracketed"
	RuleEmbeddedKeyword   = "embedded-keyword"
	RuleTrailingOrdinal   = "trailing-ordinal"
)

// SeriesCleanup is the outcome of examining one series name.
type SeriesCleanup struct {
	// Name is the series name to store. It equals the input whenever nothing
	// matched AND whenever Flag is set — a flagged name is never rewritten.
	Name string
	// Position is the book position lifted out of the name, "" when none.
	//
	// 🔑 A caller that drops this is DELETING information, not cleaning it: the
	// number was the only record of where the book sits in its series. Every
	// caller must write it into the book's series_sequence when the book has
	// none yet. See the four write paths.
	Position string
	// Rule names which pattern matched, "" when none did.
	Rule string
	// Flag is true when a human should look at this name. Name is unchanged.
	Flag       bool
	FlagReason SeriesFlagReason
	// CandidateName / CandidatePosition describe what WOULD have been stripped
	// from a FlagUnvouchedPosition name. The owner's instruction is "when we find
	// one I'll manually override", which needs a review surface that shows the
	// choice, not just a name with a question mark on it.
	CandidateName     string
	CandidatePosition string
	// DiscardedPosition is a number that was removed from Name and deliberately
	// NOT written into the book's sequence. Today only ShapeBracketed sets it.
	//
	// 🔑 This is the ONE place the "it is a move, not a delete" contract above is
	// broken on purpose, by the owner's ruling on 2026-09-02. The evidence: of the
	// 198 bracketed rows the 2026-08-06 maintenance.series-denumber run found,
	// roughly 180 were shattered-book debris ("Title [02]" fragments of one book
	// split across rows), not series positions — which is why that plugin declines
	// to apply them. The owner's "zero series have a number in them" rule is about
	// the NAME, so the bracket still goes; but a position ~90% likely to be wrong
	// should not be written. An empty sequence is visible and recoverable, a wrong
	// one is not. The ~10% that really are positions lose the number, and that is
	// accepted. It is logged here so the value is not silently gone.
	DiscardedPosition string
}

// Changed reports whether the series name should be rewritten in the store.
func (c SeriesCleanup) Changed(original string) bool {
	return c.Name != strings.TrimSpace(original)
}

// StripSeriesContamination removes the book's position (and, for one shape, the
// book's title) from a series name.
//
// ─── WHY THIS STRIPS BARE TRAILING NUMBERS NOW ──────────────────────────────
//
// It used to match three shapes and let everything else through verbatim, which
// is why production accumulated 955 keyword-suffixed series names (", Book 1",
// "#5", "Vol 09") and 6,859 more ending in a bare number, out of 42,495 rows.
// "Nameless Sovereign #5" matched nothing and was written through as a series
// NAME on every metadata apply.
//
// The library owner's instruction is explicit and overrides the hedging that the
// matcher's own comments carry about legitimate numeric names:
//
//	"We should also stop the bleeding and strip that automatically from
//	 metadata. There is absolutely zero series that have a number in them.
//	 And when we find one I'll manually override."
//
// So "Fahrenheit 451" and "Blake's 7" now strip on the write path. That is a
// deliberate inversion of assertions this function's own test used to make, and
// it is safe only because (a) the owner says the false-positive rate is
// effectively zero in THIS library, and (b) every strip is logged at the call
// site with the book id, so a false positive can be found and overridden.
//
// Do not copy this policy into maintenance.SeriesDenumber. That path MERGES
// series rows library-wide unattended and keeps its corroboration requirement;
// see the policy note in series_position.go.
//
// ─── WHAT IS NOT STRIPPED ────────────────────────────────────────────────────
//
// A number at the FRONT or in the MIDDLE with no keyword vouching for it is left
// exactly as it is, with Flag set and FlagReason=FlagUnvouchedPosition:
//
//	86—EIGHTY-SIX             ← "86" IS the name; stripping yields "—EIGHTY-SIX"
//	08. Battle for the Abyss  ← "08" IS a position
//
// Identical shape, opposite meaning, and no string-only rule tells them apart.
// Silently damaging the first to clean the second is worse than either.
//
// Rules, in order, stopping at the first match:
//
//  1. Dash-embedded position+title: "Series - 1 - Title" → "Series", "1"
//  2. Trailing keyword position:    "Nameless Sovereign #5" → "Nameless Sovereign", "5"
//  3. Trailing bare number:         "Discworld 05" → "Discworld", "5"
//  4. Bracketed trailing number:    "Dragon Born [04]" → "Dragon Born", NO position
//     (the number is removed from the name and DELIBERATELY not written into the
//     book's sequence — see SeriesCleanup.DiscardedPosition for the measurement
//     and the ruling behind that)
//  5. Keyword-vouched embedded:     "Evil Genius: Book 4: Becoming…" → "Evil Genius", "4"
//  6. Un-vouched embedded/leading:  unchanged, Flag=true
//  7. Trailing ordinal word:        "Series One" → "Series", "1"
//  8. Series equals title:          unchanged, Flag=true
func StripSeriesContamination(name, title string) SeriesCleanup {
	name = strings.TrimSpace(name)
	if name == "" {
		return SeriesCleanup{}
	}

	// Rule 1 is FIRST and cannot be folded into SplitSeriesPosition: it is the
	// only shape that also discards a trailing book TITLE.
	if m := reDashPositionTitle.FindStringSubmatch(name); m != nil {
		return SeriesCleanup{Name: strings.TrimSpace(m[1]), Position: m[2], Rule: RuleDashPositionTitle}
	}

	// Rules 2-6 come from the shared matcher.
	if sp, ok := SplitSeriesPosition(name); ok {
		switch sp.Shape {
		case ShapeTrailingKeyword, ShapeTrailingBare, ShapeBracketed, ShapeEmbeddedKeyword:
			// 🔑 The junk-base guard is NOT redundant with the one inside
			// SplitSeriesPosition, which only covers the embedded shapes. The two
			// TRAILING shapes reach here unguarded, and "Chapter 12" / "Disc 3"
			// are trailing-bare matches: stripping them on a write would rewrite
			// hundreds of rows into one giant bogus series named "Chapter".
			// Leaving a disc tag in the series field is the lesser damage.
			if IsJunkSeriesBase(sp.Base) {
				return SeriesCleanup{Name: name}
			}
			if sp.Shape == ShapeBracketed {
				// Strip the NAME, do not write the sequence. See DiscardedPosition
				// for the measured reason and whose call it was. Position stays ""
				// so that every write path's `Position != ""` gate declines on its
				// own — do NOT "fix" this by populating Position.
				return SeriesCleanup{
					Name:              sp.Base,
					Rule:              RuleBracketed,
					DiscardedPosition: strconv.Itoa(sp.Position),
				}
			}
			return SeriesCleanup{
				Name:     sp.Base,
				Position: strconv.Itoa(sp.Position),
				Rule:     ruleForShape(sp.Shape),
			}
		default:
			// ShapeMidColon / ShapeLeadingBare: un-vouched. Report, never apply.
			return SeriesCleanup{
				Name:              name,
				Rule:              string(sp.Shape),
				Flag:              true,
				FlagReason:        FlagUnvouchedPosition,
				CandidateName:     sp.Base,
				CandidatePosition: strconv.Itoa(sp.Position),
			}
		}
	}

	if m := reTrailingOrdinal.FindStringSubmatch(name); m != nil {
		return SeriesCleanup{
			Name:     strings.TrimSpace(m[1]),
			Position: ordinalToDigit[strings.ToLower(m[2])],
			Rule:     RuleTrailingOrdinal,
		}
	}

	if title != "" && strings.EqualFold(name, strings.TrimSpace(title)) {
		return SeriesCleanup{Name: name, Flag: true, FlagReason: FlagNameEqualsTitle}
	}

	return SeriesCleanup{Name: name}
}

func ruleForShape(s SeriesShape) string {
	switch s {
	case ShapeTrailingKeyword:
		return RuleTrailingKeyword
	case ShapeTrailingBare:
		return RuleTrailingBare
	case ShapeBracketed:
		return RuleBracketed
	case ShapeEmbeddedKeyword:
		return RuleEmbeddedKeyword
	default:
		return string(s)
	}
}
