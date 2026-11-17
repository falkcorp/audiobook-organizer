// file: internal/plugins/maintenance/series_denumber.go
// version: 4.0.0
// guid: dee834d3-1f7e-453e-9303-85d37479e79d
// last-edited: 2026-11-17

package maintenance

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	sdn "github.com/falkcorp/audiobook-organizer/internal/seriesdenumber"
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

// ApplyEligible reports whether a plan may be executed unattended: only the
// high tier, and only when no shape check demoted it (a demotion moves the plan
// to low, so the confidence alone carries both).
//
// 🔒 Medium and low are unconditional refusals: there is no parameter that lets
// them through. Low, because "86—EIGHTY-SIX" and "08. Battle for the Abyss" are
// the same string shape and a merge creates and deletes series rows. Medium (a
// bracketed number, "Dragon Born [04]"), because the owner ruled on 2026-09-02
// that a bracketed number is usually a fragment of one shattered book and not a
// position, and that a position about 90% likely to be wrong must not be written
// (metadata.SeriesCleanup.DiscardedPosition). Both tiers are reported, never
// applied.
func ApplyEligible(p SeriesMergePlan) bool {
	return p.Confidence == ConfidenceHigh
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
	// AuthorID is the numbered series' author, 0 when it has none. The base is
	// resolved and created under it, so a base name shared by two authors is two
	// bases.
	AuthorID int
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
	// DemotedBy names the check (seriesdenumber.Code) that moved this plan to low
	// after the planner scored it, "" when none did. DemotedFrom is the tier the
	// planner gave it. See DemotePlans.
	DemotedBy   string
	DemotedFrom SeriesConfidence
	// SiblingRun is true when at least two other series of the same author, with
	// the same base, that pass the grammar and hold at least one book form a run
	// of consecutive numbers with this one (seriesdenumber.RunCorroborated). It
	// is the library's evidence for a position the book does not carry itself.
	SiblingRun bool
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

	// Pass 3 -- the siblings that can corroborate a position: series of the same
	// author, with the same base, whose names pass the positive grammar and that
	// hold at least one book. An empty row, a held shape ("Part 1") and a series
	// of another author are not evidence.
	type sibling struct{ id, n int }
	siblings := map[key][]sibling{}
	for _, s := range in {
		if s.AuthorID == 0 || s.Books < 1 {
			continue
		}
		if parsed, _, ok := sdn.ParseName(s.Name); ok {
			k := key{strings.ToLower(parsed.Base), s.AuthorID}
			siblings[k] = append(siblings[k], sibling{s.ID, parsed.N})
		}
	}
	siblingRun := func(s SeriesInput) bool {
		parsed, _, ok := sdn.ParseName(s.Name)
		if !ok || s.AuthorID == 0 {
			return false
		}
		var nums []int
		for _, sib := range siblings[key{strings.ToLower(parsed.Base), s.AuthorID}] {
			if sib.id != s.ID {
				nums = append(nums, sib.n)
			}
		}
		return sdn.RunCorroborated(parsed.N, nums)
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
			AuthorID: s.AuthorID,
			IntoID:   canonical[k], Position: sp.Position, Reason: reason, Books: s.Books,
			Shape: sp.Shape, Confidence: conf, SiblingRun: siblingRun(s),
		})
	}
	return plans
}

// DemotePlans moves every plan the positive grammar does not accept out of the
// apply tier, to low, with the rule that held it in DemotedBy and the evidence in
// Reason. The planner scores a NAME with a loose matcher; the grammar
// (seriesdenumber.ParseName) re-reads the name strictly and the checks beside it
// read what a name cannot show -- an author row that is a placeholder, a base
// that is an author's name in any word order, a planner that read a different
// base or number than the name carries. authorNames maps an author id to its
// name; the base is compared with every author's name, not only the series' own,
// and an empty map skips the author checks only. The input is not modified.
func DemotePlans(plans []SeriesMergePlan, authorNames map[int]string) []SeriesMergePlan {
	out := make([]SeriesMergePlan, len(plans))
	names := make([]string, 0, len(authorNames))
	for _, n := range authorNames {
		names = append(names, n)
	}
	authors := sdn.NewAuthorSet(names)
	for i, pl := range plans {
		out[i] = pl
		if pl.Confidence != ConfidenceHigh {
			continue
		}
		name, known := authorNames[pl.AuthorID]
		v, held := sdn.Demote(sdn.Candidate{
			FromName: pl.FromName, Base: pl.IntoName, Position: pl.Position,
			AuthorID: pl.AuthorID, AuthorName: name, Authors: authors,
			AuthorPlaceholder: known && isPlaceholderSeriesAuthor(name),
		})
		if !held {
			continue
		}
		out[i].DemotedBy = string(v.Code)
		out[i].DemotedFrom = pl.Confidence
		out[i].Confidence = ConfidenceLow
		out[i].Reason = fmt.Sprintf("held (%s): %s; was %s: %s", v.Code, v.Reason, pl.Confidence, pl.Reason)
	}
	return out
}

// isPlaceholderSeriesAuthor reports whether an author row stands for "no known
// author": the system's own placeholder, a placeholder title that leaked into
// the author field, or one of the stand-ins ("Various", "VA", "N/A") the author
// repair ops treat the same way.
func isPlaceholderSeriesAuthor(name string) bool {
	return authorname.IsPlaceholder(name) || authorname.IsPlaceholderAuthor(name) || isPlaceholderAuthorName(name)
}
