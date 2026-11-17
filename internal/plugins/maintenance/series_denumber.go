// file: internal/plugins/maintenance/series_denumber.go
// version: 5.0.1
// guid: dee834d3-1f7e-453e-9303-85d37479e79d
// last-edited: 2026-11-17

package maintenance

import (
	"fmt"
	"sort"
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
// 🔑 The matcher is shared; the POLICY is not. This package scores a NAME (the
// tiers below), but a score is not permission: an apply needs the positive
// grammar (internal/seriesdenumber.ParseName) AND a run of consecutive series of
// the same author and base, each one book with its own title (CheckRun) AND a
// target the decision can see. It still refuses the low tier outright, because a
// merge creates and deletes series rows library-wide in one unattended pass. The
// write path strips every trailing shape on sight, by the owner's explicit
// instruction, because the cost of a false positive there is one row the owner
// retypes. See the policy note at the top of internal/metadata/series_position.go
// before changing either.

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
// to low, so the confidence alone carries both). High is necessary and not
// sufficient: the books of an eligible plan still move only when a run of
// single-book series with distinct titles corroborates the position
// (seriesdenumber.Decide), and a plan whose run fails carries no books.
//
// 🔒 Medium and low are unconditional refusals: there is no parameter that lets
// them through. Low, because "52—Gotham Central" and "08. Court of Owls" are
// the same string shape and a merge creates and deletes series rows. Medium (a
// bracketed number, "Court of Owls [04]"), because the owner ruled on 2026-09-02
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
	// SiblingIDs are the other series of the same author whose names pass the
	// grammar with the same base and that hold at least one book, ascending by id:
	// the candidates for a run with this series. The planner reads only names, so
	// whether they form a run of distinct books is decided where their books are
	// read (seriesdenumber.CheckRun).
	SiblingIDs []int
}

// SeriesDenumber plans the merges that collapse "<Series> <N>" rows into one
// series per base name, per author. It reads NAMES only and scores each one:
//
//   - an explicit marker ("Book 3") is the high tier;
//   - a zero-padded number ("Court of Owls 05") is the high tier;
//   - a bare number is the high tier when another series shares the base
//     ("Dark Knight 3" alongside "Dark Knight 6");
//   - a lone, unpadded bare number ("Robin 3") is reported as low, held as
//     position_uncorroborated, and never moved: the cost of wrongly merging a real
//     name is a corrupted series, while the cost of skipping one is that it stays
//     as it is;
//   - the embedded, bracketed and leading shapes are medium or low and never
//     applied.
//
// The high tier is the planner's reading of the NAME, not corroboration. The
// corroboration is a run of consecutive series, each one book with its own title,
// which the caller checks with the books in hand: each plan carries the sibling
// candidates (SiblingIDs) for that.
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
		// Runs of space inside the base are folded: the store keys a series by its
		// name with whitespace folded, so a base created with a doubled space would
		// be a row nothing else resolves to.
		sp.Base = strings.Join(strings.Fields(sp.Base), " ")
		splits[s.ID] = sp
		baseCount[key{strings.ToLower(sp.Base), s.AuthorID}]++
	}

	// Pass 2 — an existing series already named exactly the base is the target.
	canonical := map[key]int{}
	for _, s := range in {
		if _, numbered := splits[s.ID]; numbered {
			continue
		}
		canonical[key{strings.ToLower(strings.Join(strings.Fields(s.Name), " ")), s.AuthorID}] = s.ID
	}

	// Pass 3 -- the siblings that can take part in a run: series of the same
	// author, with the same base, whose names pass the positive grammar and that
	// hold at least one book. An empty row, a held shape ("Part 1") and a series
	// of another author are not candidates.
	siblings := map[key][]int{}
	for _, s := range in {
		if s.AuthorID == 0 || s.Books < 1 {
			continue
		}
		if parsed, _, ok := sdn.ParseName(s.Name); ok {
			k := key{strings.ToLower(parsed.Base), s.AuthorID}
			siblings[k] = append(siblings[k], s.ID)
		}
	}
	siblingIDs := func(s SeriesInput) []int {
		parsed, _, ok := sdn.ParseName(s.Name)
		if !ok || s.AuthorID == 0 {
			return nil
		}
		var ids []int
		for _, id := range siblings[key{strings.ToLower(parsed.Base), s.AuthorID}] {
			if id != s.ID {
				ids = append(ids, id)
			}
		}
		sort.Ints(ids)
		return ids
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
		demotedBy, demotedFrom := "", SeriesConfidence("")
		switch {
		// ── Trailing shapes. ──
		case sp.Shape == ShapeTrailingKeyword:
			reason, conf = "position keyword in the name", ConfidenceHigh
		case sp.Shape == ShapeTrailingBare && sp.Padded:
			reason, conf = "zero-padded number in the name", ConfidenceHigh
		case sp.Shape == ShapeTrailingBare && baseCount[k] > 1:
			reason, conf = "number in the name, another series shares this base", ConfidenceHigh
		case sp.Shape == ShapeTrailingBare:
			// A lone unpadded trailing number is not evidence. It is reported as
			// low and held, never dropped silently: a count nobody can see is a
			// shape nobody can review. Low is never applied.
			reason, conf = "a lone unpadded number: nothing but the name says it is a position", ConfidenceLow
			demotedBy, demotedFrom = string(sdn.HoldUncorroborated), sp.Confidence

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
			Shape: sp.Shape, Confidence: conf, DemotedBy: demotedBy, DemotedFrom: demotedFrom,
			SiblingIDs: siblingIDs(s),
		})
	}
	return plans
}

// DemotePlans moves every plan the positive grammar does not accept out of the
// apply tier, to low, with the rule that held it in DemotedBy and the evidence in
// Reason. The planner scores a NAME with a loose matcher; the grammar
// (seriesdenumber.ParseName) re-reads the name strictly and the checks beside it
// read what a name cannot show -- an author row that is a placeholder, a base
// that is an author's name in any word order or a part of the series' own
// author's name, a planner that read a different base or number than the name
// carries. authorNames maps EVERY author id to its name; the base is compared
// with every author's name, not only the series' own. An author id that is not in
// the map names an author row that does not exist, and is held as no_author like a
// series with none. The input is not modified.
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
			AuthorMissing:     !known,
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
