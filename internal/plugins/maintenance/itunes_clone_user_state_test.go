// file: internal/plugins/maintenance/itunes_clone_user_state_test.go
// version: 1.0.0
// guid: 25096c43-be96-4bd4-8926-6b0af7861373
// last-edited: 2026-10-05

package maintenance

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// icFailingStateStore fails every user-state write onto one book, so a
// follow onto it cannot complete (its pending-repair record still lands).
type icFailingStateStore struct {
	*database.PebbleStore
	failOn string
}

func (s *icFailingStateStore) SetUserBookState(st *database.UserBookState) error {
	if st.BookID == s.failOn {
		return fmt.Errorf("injected SetUserBookState failure for %s", st.BookID)
	}
	return s.PebbleStore.SetUserBookState(st)
}

// icStateDeps is icTestDeps with the merge user-state store overridden.
type icStateDeps struct {
	icTestDeps
	userState merge.UserStateRepairStore
}

func (d icStateDeps) MergeUserStateStore() merge.UserStateRepairStore { return d.userState }

// applyCloneWithProgress clones book T into the library, then gives a user
// progress, a position and a status on the clone (the crowned primary ABS
// lists), as listening to it, or a merge following state onto it, would.
func applyCloneWithProgress(t *testing.T, f *icFixture) (cloneID string, user *database.User) {
	t.Helper()
	src := f.itunes("Book T.m4b")
	f.book(t, "T", "vg-t", "Book T", []string{src})
	g := icGroup(t, f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Error)
	cloneID = g.CloneBookID
	clone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.True(t, *clone.IsPrimaryVersion, "fixture: the clone is the crowned primary")
	user, err = f.s.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{UserID: user.ID, BookID: cloneID,
		Status: database.UserBookStatusInProgress, ProgressPct: 55, LastActivityAt: time.Now()}))
	require.NoError(t, f.s.SetUserPosition(user.ID, cloneID, "seg", 1234))
	return cloneID, user
}

// A rollback deletes the clone book. Its users' listening state goes back to
// the source first, so nothing a user did on the copy ABS showed is lost.
func TestITunesClone_RollbackCarriesStateToSource(t *testing.T) {
	f := newICFixture(t)
	cloneID, user := applyCloneWithProgress(t, f)

	g := icGroup(t, f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeRolled, g.Outcome, g.Error)
	gone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.True(t, gone == nil || gone.IsSoftDeleted(), "clone book removed")
	st, err := f.s.GetUserBookState(user.ID, "T")
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 55, st.ProgressPct, "the progress is on the source")
	require.Equal(t, database.UserBookStatusInProgress, st.Status)
	pos, err := f.s.ListUserPositionsForBook(user.ID, "T")
	require.NoError(t, err)
	require.NotEmpty(t, pos, "the position is on the source")
}

// When the state cannot be fully moved, the rollback is refused before it
// changes anything: the clone, its rows, its file and the user's state all
// stay, and the record is kept so a retry can finish.
func TestITunesClone_RollbackRefusedWhenStateCarryFails(t *testing.T) {
	f := newICFixture(t)
	cloneID, user := applyCloneWithProgress(t, f)
	sf := &icFailingStateStore{PebbleStore: f.s, failOn: "T"}

	p := &Plugin{deps: icStateDeps{icTestDeps: f.deps, userState: sf}}
	rep, err := p.itunesCloneIntoLibrary(t.Context(), icParams{Rollback: true, GroupIDs: []string{"vg-t"}}, f.root, &opIDReporter{id: "op-ic-test"})
	require.NoError(t, err)
	g := icGroup(t, rep, "vg-t")
	require.Equal(t, icOutcomeFailed, g.Outcome)
	require.Contains(t, g.Error, "refusing to delete clone")

	clone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.NotNil(t, clone, "the clone is not deleted")
	require.False(t, clone.IsSoftDeleted())
	files, err := f.s.GetBookFiles(cloneID)
	require.NoError(t, err)
	require.NotEmpty(t, files, "the clone keeps its rows")
	require.FileExists(t, filepath.Join(f.root, "Author", "Book T", "Book T.m4b"))
	st, err := f.s.GetUserBookState(user.ID, cloneID)
	require.NoError(t, err)
	require.Equal(t, 55, st.ProgressPct, "the user's state is still on the clone")
	rec, err := (&icRunner{store: icStore{OpsStore: f.s}}).loadRecord("vg-t")
	require.NoError(t, err)
	require.NotNil(t, rec, "the record is kept for a retry")

	// With the store healthy again, the retry finishes it.
	g = icGroup(t, f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeRolled, g.Outcome, g.Error)
	st, err = f.s.GetUserBookState(user.ID, "T")
	require.NoError(t, err)
	require.Equal(t, 55, st.ProgressPct)
}
