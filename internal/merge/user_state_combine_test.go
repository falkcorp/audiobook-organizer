// file: internal/merge/user_state_combine_test.go
// version: 1.0.0
// guid: 0c8e4a71-6b2d-4f93-a5e0-3d7b19f2c864
// last-edited: 2026-10-22

package merge

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestCombineUserStates(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	pos := func(seg string, sec float64, at time.Time) database.UserPosition {
		return database.UserPosition{SegmentID: seg, PositionSeconds: sec, UpdatedAt: at}
	}
	state := func(user, status string, pct int, listened float64, last time.Time) *database.UserBookState {
		return &database.UserBookState{UserID: user, BookID: "book1", Status: status, ProgressPct: pct, TotalListenedSeconds: listened, LastActivityAt: last}
	}

	t.Run("finished sticks even when the absorbed side is newer and unfinished", func(t *testing.T) {
		fin := t0.Add(-time.Hour)
		kept := state("keep", database.UserBookStatusFinished, 100, 5000, t0.Add(-2*time.Hour))
		kept.FinishedAt = &fin
		got := CombineUserStates("keep", "book1",
			UserStateSide{State: state("gone", database.UserBookStatusInProgress, 40, 200, t0), Positions: []database.UserPosition{pos("s1", 90, t0)}},
			UserStateSide{State: kept, Positions: []database.UserPosition{pos("s9", 3000, t0.Add(-2*time.Hour))}})
		if !got.Carry || got.State == nil {
			t.Fatalf("combination = %+v, want a carried state", got)
		}
		if got.State.Status != database.UserBookStatusFinished || got.State.UserID != "keep" || got.State.BookID != "book1" {
			t.Errorf("state = %+v, want finished under keep/book1", got.State)
		}
		if got.State.TotalListenedSeconds != 5000 {
			t.Errorf("listened = %v, want the larger counter 5000", got.State.TotalListenedSeconds)
		}
		if !got.PositionsFromAbsorbed {
			t.Errorf("the newer absorbed side holds the position, got PositionsFromAbsorbed=false")
		}
		if !got.State.LastActivityAt.Equal(t0) {
			t.Errorf("last played = %v, want the later %v", got.State.LastActivityAt, t0)
		}
	})

	t.Run("a tie goes to the kept side", func(t *testing.T) {
		got := CombineUserStates("keep", "book1",
			UserStateSide{State: state("gone", database.UserBookStatusInProgress, 10, 100, t0), Positions: []database.UserPosition{pos("s1", 10, t0)}},
			UserStateSide{State: state("keep", database.UserBookStatusInProgress, 60, 100, t0), Positions: []database.UserPosition{pos("s2", 60, t0)}})
		if !got.Carry || got.State == nil {
			t.Fatalf("combination = %+v", got)
		}
		if got.PositionsFromAbsorbed {
			t.Errorf("on a tie the kept side's positions stay")
		}
		if got.State.ProgressPct != 60 {
			t.Errorf("progress = %d, want the kept side's 60", got.State.ProgressPct)
		}
	})

	t.Run("hide is OR'd and reset tombstones are unioned", func(t *testing.T) {
		reset := t0.Add(time.Minute)
		absorbed := state("gone", database.UserBookStatusInProgress, 10, 10, t0)
		absorbed.HideFromContinueListening = true
		absorbed.ProgressResetAt = &reset
		absorbed.ProgressResetPositions = []float64{12}
		kept := state("keep", database.UserBookStatusInProgress, 10, 10, t0.Add(time.Hour))
		kept.ProgressResetPositions = []float64{99}
		got := CombineUserStates("keep", "book1",
			UserStateSide{State: absorbed, Positions: []database.UserPosition{pos("s1", 5, t0)}},
			UserStateSide{State: kept, Positions: []database.UserPosition{pos("s1", 6, t0.Add(time.Hour))}})
		if got.State == nil || !got.State.HideFromContinueListening {
			t.Fatalf("hide flag lost: %+v", got.State)
		}
		if got.State.ProgressResetAt == nil || !got.State.ProgressResetAt.Equal(reset) {
			t.Errorf("reset tombstone = %v, want %v", got.State.ProgressResetAt, reset)
		}
		if len(got.State.ProgressResetPositions) != 2 {
			t.Errorf("reset positions = %v, want both sides' kept", got.State.ProgressResetPositions)
		}
	})

	t.Run("an absorbed side with nothing to carry changes nothing", func(t *testing.T) {
		drained := &database.UserBookState{UserID: "gone", BookID: "book1", LastActivityAt: t0.Add(24 * time.Hour)}
		got := CombineUserStates("keep", "book1",
			UserStateSide{State: drained},
			UserStateSide{State: state("keep", database.UserBookStatusFinished, 100, 1, t0)})
		if got.Carry || got.State != nil || got.PositionsFromAbsorbed {
			t.Errorf("combination = %+v, want the zero value (a drained row must not overwrite the kept one)", got)
		}
	})

	t.Run("the kept side has nothing: the absorbed state moves whole", func(t *testing.T) {
		got := CombineUserStates("keep", "book1",
			UserStateSide{State: state("gone", database.UserBookStatusInProgress, 33, 77, t0), Positions: []database.UserPosition{pos("s1", 33, t0)}},
			UserStateSide{})
		if !got.Carry || got.State == nil || got.State.UserID != "keep" || got.State.ProgressPct != 33 || !got.PositionsFromAbsorbed {
			t.Errorf("combination = %+v, want the absorbed row re-owned by keep", got)
		}
	})
}
