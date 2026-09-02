// file: internal/plugins/maintenance/series_denumber.go
// version: 3.0.0
// guid: dee834d3-1f7e-453e-9303-85d37479e79d
// last-edited: 2026-09-02

package maintenance

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// ─── THE MATCHER MOVED TO internal/metadata ─────────────────────────────────
//
// The regex set, SeriesSplit, SplitSeriesPosition and IsJunkSeriesBase used to
// live in this file, reachable only through `maintenance.series-denumber` —
// which is sdk.LivenessManual and dry-run by default. Meanwhile every WRITE path
// (metadata apply, scanner, iTunes import, series normalize) called
// metadata.StripSeriesContamination, which matched three shapes and wrote the
// rest through verbatim. The op could only ever clean up behind a leak it had no
// way to plug.
//
// They are now one matcher in internal/metadata — the package the write paths
// already import — and this file re-exports the vocabulary it still speaks in.
// These are TYPE ALIASES, not new types: maintenance.SeriesSplit and
// metadata.SeriesSplit are the same type, so nothing has to convert and there is
// exactly one copy of the regexes. Do not "restore" a local copy.
//
// 🔑 The matcher is shared; the POLICY is not. This package keeps its
// corroboration requirement (padded, or a sibling series shares the base) and
// still refuses the low tier outright, because a merge creates and deletes
// series rows library-wide in one unattended pass. The write path strips every
// trailing shape on sight, by the owner's explicit instruction, because the cost
// of a false positive there is one row the owner retypes. See the policy note at
// the top of internal/metadata/series_position.go before changing either.

type (
	SeriesShape      = metadata.SeriesShape
	SeriesConfidence = metadata.SeriesConfidence
	SeriesSplit      = metadata.SeriesSplit
)

const (
	ShapeTrailingKeyword = metadata.ShapeTrailingKeyword
	ShapeTrailingBare    = metadata.ShapeTrailingBare
	ShapeEmbeddedKeyword = metadata.ShapeEmbeddedKeyword
	ShapeBracketed       = metadata.ShapeBracketed
	ShapeMidColon        = metadata.ShapeMidColon
	ShapeLeadingBare     = metadata.ShapeLeadingBare

	ConfidenceHigh   = metadata.SeriesConfidenceHigh
	ConfidenceMedium = metadata.SeriesConfidenceMedium
	ConfidenceLow    = metadata.SeriesConfidenceLow
)

// SplitSeriesPosition separates a book position from a series name.
// See metadata.SplitSeriesPosition — this is the same function.
func SplitSeriesPosition(name string) (SeriesSplit, bool) {
	return metadata.SplitSeriesPosition(name)
}

// IsJunkSeriesBase reports whether a stripped base name is a tag artefact rather
// than a series. See metadata.IsJunkSeriesBase — this is the same function.
func IsJunkSeriesBase(base string) bool {
	return metadata.IsJunkSeriesBase(base)
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
