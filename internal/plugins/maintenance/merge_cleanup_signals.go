// file: internal/plugins/maintenance/merge_cleanup_signals.go
// version: 1.0.0
// guid: 67839bc7-cc2f-4877-afbc-e2646b549e49
// last-edited: 2026-11-18

package maintenance

import (
	"fmt"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// The signals of the merge-cleanup fixer, and the one pure function that turns
// them into a verdict.
//
// Everything in this file is a pure function of two mcSide values, so each
// threshold edge and each verdict is a table test with no store behind it.
// The fixer (merge_cleanup_fixer.go) fills the sides from live rows at Plan and
// again at Replan; nothing here reads a store.
//
// Every threshold is a constant, deliberately not a plan param: a param would
// make two trials of the same library non-reproducible (owner question 11, the
// recommended answer taken). Durations compare as integer permille so the edges
// (0.95, 0.80, 0.49, 0.51) are exact rather than floating-point accidents.
const (
	// TITLE_MATCH: normalized title similarity at or above this.
	mcTitleMatchMin = 0.9
	// TITLE_DIFF: similarity strictly below this AND word overlap strictly
	// below mcTitleOverlapMax.
	mcTitleDiffMax    = 0.5
	mcTitleOverlapMax = 0.34

	// DUR_EXACT: the two known runtimes are within this many seconds.
	mcDurExactSec = 2
	// SIZE_CLOSE: the byte totals are within this many permille (0.1 percent)
	// of the larger.
	mcSizeClosePermille = 1

	// Runtime ratio, shorter over longer, in permille.
	mcDurClosePermille = 950 // DUR_CLOSE: at or above
	mcDurNearPermille  = 800 // DUR_NEAR: from here up to DUR_CLOSE; below is DUR_FAR (a fragment)
	mcDur2xLoPermille  = 490 // DUR_2X: the ratio of a book to its half
	mcDur2xHiPermille  = 510

	// A runtime under this many seconds is a probe (a sample or an intro), not
	// a book: a ratio against it decides nothing.
	mcProbeMaxSec = 30 * 60

	// HASH_EQ counts only when no more than this many books carry the hash:
	// a hash a dozen books share is a blank or boilerplate file.
	mcHashShareMax = 4
)

// Row kinds (Row.Class) and skip kinds (Row.Skipped). A kind is what the row
// would do; a skip kind is why it does not.
const (
	mcKindUnmerge = "unmerge"
	mcKindSwap    = "swap_keeper"
	mcKindRepoint = "repoint_chain"
	// mcClassNoVerdict is the class of a row the signals could not settle: it
	// is listed with its signals so the owner can see it, and proposes nothing.
	mcClassNoVerdict = "no_verdict"
	// mcClassError is the class of a row whose reads failed.
	mcClassError = "error"

	// From the signals (mcDecide).
	mcSkipRuntimeIncomplete = "runtime_incomplete"
	mcSkipRuntimeAmbiguous  = "runtime_ambiguous"
	mcSkipSuspicious        = "suspicious"
	mcSkipCannotTell        = "cannot_tell"
	mcSkipMergeLooksCorrect = "merge_looks_correct"
	mcSkipNoLongerMerged    = "no_longer_merged"
	mcSkipGone              = "gone"
	mcSkipError             = "error"

	// From the chain.
	mcSkipChainOverCap     = "chain_over_cap"
	mcSkipCycleNotHead     = "cycle_not_head"
	mcSkipRepointAmbiguous = "repoint_ambiguous"

	// From the gates (merge_cleanup_fixer.go).
	mcSkipHandledByVG          = "handled_by_version_group_repair"
	mcSkipRetiredShell         = "retired_shell"
	mcSkipCrossGroup           = "cross_group"
	mcSkipSwapTargetNotPrimary = "swap_target_not_primary"
	mcSkipSwapNoCrown          = "swap_would_not_crown_keeper"
	mcSkipGroupNeedsHandoff    = "group_needs_handoff"
	mcSkipSyncRedirect         = "sync_redirect_present"
	mcSkipOwnerSaysDistinct    = "owner_says_distinct"
	mcSkipVetoUnresolvable     = "owner_veto_unresolvable"
)

// mcSide is everything the signals read of one book.
type mcSide struct {
	ID    string
	Title string
	// Authors are the credited names, normalized, in credit order.
	Authors []string
	// SeriesID and SeriesSeq are the book's series and its position in it.
	SeriesID  *int
	SeriesSeq *int
	ASIN      string
	// GroupID is the version group ("" none); Primary is the stored primary
	// flag as "true", "false" or "nil".
	GroupID string
	Primary string
	// Files counts the book's present (not missing) book_file rows.
	Files int
	// Runtime is database.ComputeBookRuntime over the book and its file rows.
	// Never Book.Duration: that is a partial sum for a multi-file book.
	Runtime database.BookRuntime
	// TotalBytes is the byte total of the counted rows; BytesComplete says it
	// is a total (every counted row has a size).
	TotalBytes    int64
	BytesComplete bool
	// HashFile is the hash of the book's only present file ("" for a book with
	// several files or none); HashShare is how many books carry that hash.
	HashFile  string
	HashShare int
}

// mcSignals are the facts the verdict is made from, named as in the owner's
// audit of 2026-11-17.
type mcSignals struct {
	TitleSim     float64
	TitleOverlap float64

	TitleMatch    bool
	TitleDiff     bool
	AuthDiff      bool
	SeriesPosDiff bool
	HashEq        bool
	// ASINDiff is a supporting signal only: two different ASINs may be two
	// editions of one work (the parked asin_conflict plan), so it never
	// decides or holds a row; it is reported.
	ASINDiff bool

	// RuntimeKnown: both runtimes are complete. Every duration signal below is
	// false without it.
	RuntimeKnown bool
	// RatioPermille is shorter over longer in permille (0 when not known).
	RatioPermille int
	// TargetShorter is true when the target's runtime is the smaller one.
	TargetShorter bool
	DurExact      bool
	SizeClose     bool
	DurClose      bool
	DurNear       bool
	DurFar        bool
	Dur2x         bool
}

// Names lists the signals that fired, in a fixed order, for the row's evidence.
func (s mcSignals) Names() []string {
	var out []string
	for _, n := range []struct {
		on   bool
		name string
	}{
		{s.TitleMatch, "TITLE_MATCH"}, {s.TitleDiff, "TITLE_DIFF"}, {s.AuthDiff, "AUTH_DIFF"},
		{s.SeriesPosDiff, "SERIES_POS_DIFF"}, {s.HashEq, "HASH_EQ"}, {s.ASINDiff, "ASIN_DIFF"},
		{s.DurClose, "DUR_CLOSE"}, {s.DurNear, "DUR_NEAR"}, {s.DurFar, "DUR_FAR"}, {s.Dur2x, "DUR_2X"},
		{s.DurExact, "DUR_EXACT"}, {s.SizeClose, "SIZE_CLOSE"},
	} {
		if n.on {
			out = append(out, n.name)
		}
	}
	return out
}

// mcTitleSimilarity is 1 - editdistance/length over the titles folded the way
// the dedup engine folds them (case, quotes, punctuation, a leading article),
// on runes. Two empty titles are 0, not 1: no title is no evidence.
func mcTitleSimilarity(a, b string) float64 {
	x, y := []rune(dedup.NormalizeTitle(a)), []rune(dedup.NormalizeTitle(b))
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	longest := max(len(x), len(y))
	return 1 - float64(mcEditDistance(x, y))/float64(longest)
}

func mcEditDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// mcTitleOverlap is the Jaccard overlap of the two titles' words (after the
// same folding). No words on either side is 0.
func mcTitleOverlap(a, b string) float64 {
	wa := strings.Fields(dedup.NormalizeTitle(a))
	wb := strings.Fields(dedup.NormalizeTitle(b))
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	inter, union := 0, len(set)
	seen := map[string]bool{}
	for _, w := range wb {
		if seen[w] {
			continue
		}
		seen[w] = true
		if set[w] {
			inter++
		} else {
			union++
		}
	}
	return float64(inter) / float64(union)
}

// mcAuthorsDiffer is true when both credit lists are known and share no name.
// A side with no credits is missing evidence, never a difference.
func mcAuthorsDiffer(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, n := range a {
		have[n] = true
	}
	for _, n := range b {
		if have[n] {
			return false
		}
	}
	return true
}

// mcComputeSignals reads two sides (l the loser, t the target) into signals.
func mcComputeSignals(l, t mcSide) mcSignals {
	var s mcSignals
	s.TitleSim = mcTitleSimilarity(l.Title, t.Title)
	s.TitleOverlap = mcTitleOverlap(l.Title, t.Title)
	s.TitleMatch = s.TitleSim >= mcTitleMatchMin
	s.TitleDiff = s.TitleSim < mcTitleDiffMax && s.TitleOverlap < mcTitleOverlapMax && l.Title != "" && t.Title != ""
	s.AuthDiff = mcAuthorsDiffer(l.Authors, t.Authors)
	// A position differs only inside one series: two series say nothing about
	// which volume a book is.
	if l.SeriesID != nil && t.SeriesID != nil && *l.SeriesID == *t.SeriesID &&
		l.SeriesSeq != nil && t.SeriesSeq != nil && *l.SeriesSeq != *t.SeriesSeq {
		s.SeriesPosDiff = true
	}
	s.ASINDiff = l.ASIN != "" && t.ASIN != "" && !strings.EqualFold(l.ASIN, t.ASIN)
	s.HashEq = l.Files == 1 && t.Files == 1 && l.HashFile != "" && l.HashFile == t.HashFile &&
		l.HashShare <= mcHashShareMax && t.HashShare <= mcHashShareMax

	ls, lok := l.Runtime.KnownSeconds()
	ts, tok := t.Runtime.KnownSeconds()
	s.RuntimeKnown = lok && tok
	if !s.RuntimeKnown {
		return s
	}
	short, long := min(ls, ts), max(ls, ts)
	s.TargetShorter = ts < ls
	s.RatioPermille = short * 1000 / long
	// The edges compare by cross-multiplication, not the truncated permille:
	// 799.9 is not 800.
	atLeast := func(p int) bool { return short*1000 >= long*p }
	s.DurClose = atLeast(mcDurClosePermille)
	s.DurNear = atLeast(mcDurNearPermille) && !s.DurClose
	s.DurFar = !atLeast(mcDurNearPermille)
	s.Dur2x = atLeast(mcDur2xLoPermille) && short*1000 <= long*mcDur2xHiPermille
	s.DurExact = long-short <= mcDurExactSec
	if l.BytesComplete && t.BytesComplete && l.TotalBytes > 0 && t.TotalBytes > 0 {
		bigger, smaller := max(l.TotalBytes, t.TotalBytes), min(l.TotalBytes, t.TotalBytes)
		s.SizeClose = (bigger-smaller)*1000 <= bigger*mcSizeClosePermille
	}
	return s
}

// mcVerdict is the outcome of mcDecide. Kind is set when the signals support a
// row kind (mcKindUnmerge, mcKindSwap); Skip is set when the row proposes
// nothing, and may be set with a Kind (the kind the row would be, held by Skip).
type mcVerdict struct {
	Kind    string
	Skip    string
	Why     string
	Signals mcSignals
}

// mcDecide turns the signals of a loser l and its live final target t into a
// verdict.
//
//   - UNMERGE (wrong work): at least two of TITLE_DIFF, AUTH_DIFF,
//     SERIES_POS_DIFF, and none of TITLE_MATCH, HASH_EQ, DUR_EXACT+SIZE_CLOSE.
//     The last needs both runtimes complete, so an incomplete runtime holds the
//     row (runtime_incomplete) rather than guess.
//   - SWAP KEEPER: the two are the same work (TITLE_MATCH, HASH_EQ, or a close
//     runtime with the same author), no wrong-work signal contradicts it, and
//     the target is shorter than 0.80 of the loser, both runtimes complete.
//     DUR_2X and a shorter side under thirty minutes are known false signals:
//     they hold the row (runtime_ambiguous), never decide it.
//   - Anything else is listed and skipped: merge_looks_correct (the same work
//     and not a fragment), suspicious (a wrong-work signal that is not enough,
//     or signals that contradict each other), cannot_tell (no signal).
func mcDecide(l, t mcSide) mcVerdict {
	s := mcComputeSignals(l, t)
	v := mcVerdict{Signals: s}
	diffs := 0
	for _, on := range []bool{s.TitleDiff, s.AuthDiff, s.SeriesPosDiff} {
		if on {
			diffs++
		}
	}
	skip := func(kind, why string, a ...any) mcVerdict {
		v.Skip, v.Why = kind, fmt.Sprintf(why, a...)
		return v
	}

	if diffs >= 2 {
		if s.TitleMatch || s.HashEq {
			return skip(mcSkipSuspicious, "%d wrong-work signals (%s) but %s says the two are one work", diffs,
				strings.Join(s.Names(), ", "), mcWhichSameSignal(s))
		}
		if !s.RuntimeKnown {
			v.Kind = mcKindUnmerge
			return skip(mcSkipRuntimeIncomplete, "%d wrong-work signals, but a runtime is incomplete, so the DUR_EXACT+SIZE_CLOSE exclusion cannot be checked", diffs)
		}
		if s.DurExact && s.SizeClose {
			return skip(mcSkipSuspicious, "%d wrong-work signals but the runtimes and sizes match to the second and the byte", diffs)
		}
		v.Kind = mcKindUnmerge
		return v
	}

	authorsKnown := len(l.Authors) > 0 && len(t.Authors) > 0
	sameWork := s.TitleMatch || s.HashEq || ((s.DurClose || s.DurNear) && authorsKnown && !s.AuthDiff)
	if sameWork {
		if diffs >= 1 && !s.HashEq {
			return skip(mcSkipSuspicious, "the same work by %s, but %s says otherwise", mcWhichSameSignal(s), strings.Join(s.Names(), ", "))
		}
		if !s.RuntimeKnown {
			v.Kind = mcKindSwap
			return skip(mcSkipRuntimeIncomplete, "the same work, but a runtime is incomplete, so which copy is the fragment cannot be told")
		}
		if !s.TargetShorter || !s.DurFar {
			return skip(mcSkipMergeLooksCorrect, "the same work and the target is not a fragment of the loser (ratio %d permille)", s.RatioPermille)
		}
		v.Kind = mcKindSwap
		ts, _ := t.Runtime.KnownSeconds()
		switch {
		case s.Dur2x:
			return skip(mcSkipRuntimeAmbiguous, "the target is about half the loser's runtime (DUR_2X), which is as likely two parts of one work as a fragment")
		case ts < mcProbeMaxSec:
			return skip(mcSkipRuntimeAmbiguous, "the target runs %d s, under thirty minutes: a probe, not a book", ts)
		}
		return v
	}
	if diffs == 1 {
		return skip(mcSkipSuspicious, "one wrong-work signal (%s) and no signal that the two are one work", strings.Join(s.Names(), ", "))
	}
	return skip(mcSkipCannotTell, "no signal says wrong work and none says the same work")
}

// mcWhichSameSignal names the signal that makes two books the same work.
func mcWhichSameSignal(s mcSignals) string {
	switch {
	case s.HashEq:
		return "HASH_EQ"
	case s.TitleMatch:
		return "TITLE_MATCH"
	}
	return "a close runtime with the same author"
}
