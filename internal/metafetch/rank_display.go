// file: internal/metafetch/rank_display.go
// version: 1.0.1
// guid: c4eb70a9-a1b7-4d57-9f02-cebd827eb8e3
// last-edited: 2026-10-10

package metafetch

import "sort"

// ownASINRanked reports whether the search ranked c in the "carries the looked-up
// ASIN and agrees with the book" class (the asin_match boost step, or the direct
// ASIN override), which sorts ahead of every score.
func ownASINRanked(c *MetadataCandidate) bool {
	if c.ScoreBreakdown == nil {
		return false
	}
	for _, st := range c.ScoreBreakdown.Steps {
		if st.ID != "asin_match" {
			continue
		}
		if st.Op == ScoreOpReplace || (st.Op == ScoreOpMultiply && st.Operand > 1) {
			return true
		}
	}
	return false
}

// SortForDisplay returns a copy of cands ordered for the interactive
// Search/Browse dialog response: the looked-up-ASIN class first, then the
// highest RankValue (the score without the missing-author / missing-narrator
// penalties), ties in the order given.
//
// What the person sees is set by the dialogs themselves: both re-sort the list
// they receive by rankScoreOf (rank_score, or score for an older row), so this
// order only breaks ties between equal ranking scores and keeps the raw API
// response in step with the screen. It is for the response a person reads,
// nothing else. The stored candidate order, the review lane and snapshot,
// op-results and every apply gate keep reading Score and the stored order, so
// the row a pinned review approves is the row it applies. A dialog apply posts
// the clicked candidate itself, never an index into this list.
func SortForDisplay(cands []MetadataCandidate) []MetadataCandidate {
	out := make([]MetadataCandidate, len(cands))
	copy(out, cands)
	sort.SliceStable(out, func(i, j int) bool {
		if ai, aj := ownASINRanked(&out[i]), ownASINRanked(&out[j]); ai != aj {
			return ai
		}
		return out[i].RankValue() > out[j].RankValue()
	})
	return out
}
