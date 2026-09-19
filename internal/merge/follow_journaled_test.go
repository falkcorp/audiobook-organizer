// file: internal/merge/follow_journaled_test.go
// version: 1.0.0
// guid: 9aa51106-7c96-4240-b74e-f49c0d7d7213
// last-edited: 2026-09-19

package merge

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func fjStore(t *testing.T) (*database.PebbleStore, string, string, string) {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	u, err := s.CreateUser("reader", "reader@example.invalid", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	surv, err := s.CreateBook(&database.Book{Title: "01 - Tale", FilePath: "/lib/T/01 - Tale.mp3"})
	require.NoError(t, err)
	abs, err := s.CreateBook(&database.Book{Title: "02 - Tale", FilePath: "/lib/T/02 - Tale.mp3"})
	require.NoError(t, err)
	return s, u.ID, surv.ID, abs.ID
}

// Chapter 2 finished (it is a SLICE of the merged book) must not mark the
// merged book Finished; its position is mapped into the merged timeline
// (offset of chapter 2 + position) and carried only when further.
func TestFollowAbsorbedJournaled_SliceModeNeverCarriesFinished(t *testing.T) {
	s, u, surv, abs := fjStore(t)
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u, BookID: surv, Status: database.UserBookStatusInProgress, ProgressPct: 30}))
	require.NoError(t, s.SetUserPosition(u, surv, "abs", 100))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u, BookID: abs, Status: database.UserBookStatusFinished, ProgressPct: 100}))
	require.NoError(t, s.SetUserPosition(u, abs, "abs", 280))

	prog, _, err := FollowAbsorbedJournaled(s, surv, abs, &SliceMapping{OffsetSeconds: 300, Mappable: true}, nil)
	require.NoError(t, err)
	require.Len(t, prog, 1)

	st, err := s.GetUserBookState(u, surv)
	require.NoError(t, err)
	require.Equal(t, database.UserBookStatusInProgress, st.Status, "a finished chapter must not finish the merged book")
	pos, err := s.GetUserPosition(u, surv)
	require.NoError(t, err)
	require.InDelta(t, 580, pos.PositionSeconds, 0.001, "position must be mapped into the merged timeline")
}

// A mapped position that is not further than the survivor's is not carried,
// and a Finished survivor stays Finished.
func TestFollowAbsorbedJournaled_SliceModeKeepsSurvivorWhenFurther(t *testing.T) {
	s, u, surv, abs := fjStore(t)
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u, BookID: surv, Status: database.UserBookStatusFinished, ProgressPct: 100}))
	require.NoError(t, s.SetUserPosition(u, surv, "abs", 5000))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u, BookID: abs, Status: database.UserBookStatusInProgress, ProgressPct: 10}))
	require.NoError(t, s.SetUserPosition(u, abs, "abs", 20))

	_, _, err := FollowAbsorbedJournaled(s, surv, abs, &SliceMapping{OffsetSeconds: 300, Mappable: true}, nil)
	require.NoError(t, err)
	st, err := s.GetUserBookState(u, surv)
	require.NoError(t, err)
	require.Equal(t, database.UserBookStatusFinished, st.Status)
	pos, err := s.GetUserPosition(u, surv)
	require.NoError(t, err)
	require.InDelta(t, 5000, pos.PositionSeconds, 0.001)
}

// Whole-book mode (nil slice) keeps the dedup semantics: the further book's
// state, Finished included, is carried.
func TestFollowAbsorbedJournaled_WholeBookModeKeepsOldSemantics(t *testing.T) {
	s, u, surv, abs := fjStore(t)
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u, BookID: surv, Status: database.UserBookStatusInProgress, ProgressPct: 30}))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u, BookID: abs, Status: database.UserBookStatusFinished, ProgressPct: 100}))
	_, _, err := FollowAbsorbedJournaled(s, surv, abs, nil, nil)
	require.NoError(t, err)
	st, err := s.GetUserBookState(u, surv)
	require.NoError(t, err)
	require.Equal(t, database.UserBookStatusFinished, st.Status)
}
