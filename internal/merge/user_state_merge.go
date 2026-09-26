// file: internal/merge/user_state_merge.go
// version: 1.0.0
// guid: 9b1f6c2e-4d7a-4e83-a5c9-2f8e0d3b7a61
// last-edited: 2026-09-26

package merge

import (
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The per-user conflict rule for a merge (owner requirement, 2026-09-26).
//
// When a book is merged away, every piece of a user's state on it ends up on
// the surviving book and none is lost. When BOTH books carry state for one
// user, the two are combined field by field:
//
//   - finished is sticky: if either side is Finished, the result is Finished
//     (status, FinishedAt and ProgressPct come from the finished side; from
//     the more recent side when both are finished, the survivor on a tie);
//   - otherwise the side with the newest lastUpdate wins the position: its
//     upos rows, LastSegmentID, TotalListenedSeconds, ProgressPct and status.
//     lastUpdate is what the ABS surface reports (item.go: the latest upos
//     UpdatedAt), falling back to the state's LastActivityAt for a side with
//     no position rows. A tie goes to the survivor;
//   - last played (LastActivityAt) is the later of the two;
//   - HideFromContinueListening is kept if either side has it;
//   - the reset tombstone (ProgressResetAt) is the later of the two and the
//     discarded positions (ProgressResetPositions) are the union, newest kept,
//     so offline replay cannot resurrect a position the user reset on either
//     copy.
//
// The previous rule was "furthest ProgressPct wins", which let an old,
// abandoned listen on one copy overwrite where the user actually is on the
// other.
//
// Bookmarks are not part of this rule: they are keyed by time, so the two
// sides' sets are unioned (bookmark_copy.go).
//
// Chapter / split-part merges (followSlice) deliberately do NOT apply the
// sticky-finished half: finishing chapter 2 is not finishing the book.

// hasCarryableState reports whether a (state, positions) pair holds anything a
// merge must carry. A state drained by an earlier follow (status cleared, no
// progress, not hidden) is NOT carryable: it is the residue of a move that
// already happened, and treating it as live state would let a replayed follow
// overwrite the survivor with an empty row whose LastActivityAt is newer.
func hasCarryableState(st *database.UserBookState, positions []database.UserPosition) bool {
	if len(positions) > 0 {
		return true
	}
	if st == nil {
		return false
	}
	return st.Status != "" || st.ProgressPct != 0 || st.TotalListenedSeconds != 0 ||
		st.LastSegmentID != "" || st.HideFromContinueListening
}

// lastUpdate is a side's recency for the "newest wins" half of the rule.
func lastUpdate(st *database.UserBookState, positions []database.UserPosition) time.Time {
	var t time.Time
	if st != nil {
		t = st.LastActivityAt
	}
	for i := range positions {
		if positions[i].UpdatedAt.After(t) {
			t = positions[i].UpdatedAt
		}
	}
	return t
}

// userStateSide is one book's state for one user.
type userStateSide struct {
	state     *database.UserBookState
	positions []database.UserPosition
}

// userStateMergePlan is what applying the rule to one user produced.
type userStateMergePlan struct {
	// state is the survivor's new state row, or nil when there is no state row
	// on either side (positions only).
	state *database.UserBookState
	// loserPositionsWin: the loser's upos rows are written onto the survivor.
	loserPositionsWin bool
	// loserFinished: the loser side was finished, so its iTunes play-count
	// mark is carried (carryPlayCountMark only ever moves a later mark).
	loserFinished bool
}

func isFinished(st *database.UserBookState) bool {
	return st != nil && st.Status == database.UserBookStatusFinished
}

// planUserStateMerge applies the rule. loser must be carryable (the caller
// checked); winner may be empty.
func planUserStateMerge(userID, winnerBookID string, loser, winner userStateSide) userStateMergePlan {
	plan := userStateMergePlan{loserFinished: isFinished(loser.state)}
	if !hasCarryableState(winner.state, winner.positions) {
		// Nothing on the survivor worth keeping: the loser's state moves over
		// whole, preserving its FinishedAt (SetUserBookState keeps a caller's
		// stamp).
		plan.loserPositionsWin = true
		if loser.state != nil {
			carried := *loser.state
			carried.UserID = userID
			carried.BookID = winnerBookID
			plan.state = &carried
		}
		return plan
	}

	loserNewer := lastUpdate(loser.state, loser.positions).After(lastUpdate(winner.state, winner.positions))
	plan.loserPositionsWin = loserNewer && len(loser.positions) > 0

	newer, older := winner.state, loser.state
	if loserNewer {
		newer, older = loser.state, winner.state
	}
	if newer == nil && older == nil {
		return plan
	}

	// Base: the survivor's own row when it has one (so any field this rule
	// does not name stays the survivor's), else the loser's.
	var merged database.UserBookState
	if winner.state != nil {
		merged = *winner.state
	} else {
		merged = *loser.state
	}
	merged.UserID = userID
	merged.BookID = winnerBookID

	// Position-linked fields and status from the newer side that has a row.
	posSrc := newer
	if posSrc == nil {
		posSrc = older
	}
	merged.Status = posSrc.Status
	merged.StatusManual = posSrc.StatusManual
	merged.ProgressPct = posSrc.ProgressPct
	merged.LastSegmentID = posSrc.LastSegmentID
	merged.TotalListenedSeconds = posSrc.TotalListenedSeconds
	merged.FinishedAt = posSrc.FinishedAt

	// Finished is sticky.
	var fin *database.UserBookState
	switch {
	case isFinished(newer):
		fin = newer
	case isFinished(older):
		fin = older
	}
	if fin != nil {
		merged.Status = database.UserBookStatusFinished
		merged.StatusManual = fin.StatusManual
		merged.FinishedAt = fin.FinishedAt
		merged.ProgressPct = fin.ProgressPct
	}

	// Last played = max; hide = OR; reset tombstone = max, union of positions.
	for _, s := range []*database.UserBookState{loser.state, winner.state} {
		if s == nil {
			continue
		}
		if s.LastActivityAt.After(merged.LastActivityAt) {
			merged.LastActivityAt = s.LastActivityAt
		}
		if s.HideFromContinueListening {
			merged.HideFromContinueListening = true
		}
		if s.ProgressResetAt != nil && (merged.ProgressResetAt == nil || s.ProgressResetAt.After(*merged.ProgressResetAt)) {
			t := *s.ProgressResetAt
			merged.ProgressResetAt = &t
		}
	}
	merged.ProgressResetPositions = unionResetPositions(older, newer)
	plan.state = &merged
	return plan
}

// unionResetPositions unions both sides' discarded positions, older side's
// first so the newest resets sort last, and keeps the newest
// database.MaxProgressResetPositions (the store's own bound).
func unionResetPositions(older, newer *database.UserBookState) []float64 {
	var out []float64
	for _, s := range []*database.UserBookState{older, newer} {
		if s == nil {
			continue
		}
		for _, p := range s.ProgressResetPositions {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if len(out) > database.MaxProgressResetPositions {
		out = out[len(out)-database.MaxProgressResetPositions:]
	}
	return out
}

// drainedUserState neutralizes a loser row in place after its state moved.
// There is no DeleteUserBookState; an empty Status also removes the row from
// the ubs status index (PebbleStore.SetUserBookState), which keeps a
// merged-away book out of "in progress" listings. The hide flag is cleared
// too: it has been OR'd onto the survivor, and leaving it would make the row
// look carryable to the next follow (hasCarryableState).
func drainedUserState(st database.UserBookState) *database.UserBookState {
	st.Status = ""
	st.StatusManual = false
	st.ProgressPct = 0
	st.TotalListenedSeconds = 0
	st.LastSegmentID = ""
	st.HideFromContinueListening = false
	return &st
}

// sortPositionsOldestFirst orders positions so that writing them in order
// leaves the side's latest position as the survivor's latest:
// SetUserPosition stamps UpdatedAt=now, and LatestPosition picks the newest.
func sortPositionsOldestFirst(positions []database.UserPosition) []database.UserPosition {
	out := slices.Clone(positions)
	slices.SortStableFunc(out, func(a, b database.UserPosition) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	return out
}
