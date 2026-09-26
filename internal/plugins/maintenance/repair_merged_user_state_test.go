// file: internal/plugins/maintenance/repair_merged_user_state_test.go
// version: 1.0.0
// guid: 4b8e1d63-7a2f-4c95-8e0d-6f3a9c1b2e57
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// strandedFixture leaves a user's progress on a soft-deleted loser whose sync
// id redirects to a live winner: what a pre-fix best-effort merge left.
func strandedFixture(t *testing.T) (store database.Store, userID, winnerID, loserID string) {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	u, err := s.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	winnerID, loserID = ulid.Make().String(), ulid.Make().String()
	_, err = s.CreateBook(&database.Book{ID: winnerID, Title: "W", Format: "m4b", FilePath: "/tmp/w.m4b"})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{ID: loserID, Title: "L", Format: "mp3", FilePath: "/tmp/l.mp3"})
	require.NoError(t, err)
	ids := database.AsSyncIdentityStore(s)
	_, err = ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)
	require.NoError(t, ids.RecordSyncMerge(loserID, winnerID))
	require.NoError(t, merge.SoftDeleteBook(s, loserID))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: loserID, Status: database.UserBookStatusInProgress, ProgressPct: 61, LastActivityAt: time.Now(),
	}))
	require.NoError(t, s.SetUserPosition(u.ID, loserID, "seg", 610))
	return s, u.ID, winnerID, loserID
}

func TestRepairMergedUserStateOp_EmptyParamsWritesNothing(t *testing.T) {
	store, userID, winnerID, loserID := strandedFixture(t)
	p := &Plugin{deps: &fakeDeps{store: store}}
	require.NoError(t, p.runRepairMergedUserState(context.Background(), json.RawMessage(`{}`), &fakeReporter{}))

	w, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.Nil(t, w, "{} is a preview: the survivor is not written")
	l, err := store.GetUserBookState(userID, loserID)
	require.NoError(t, err)
	require.Equal(t, 61, l.ProgressPct, "{} is a preview: the loser is not drained")
	pos, err := store.ListUserPositionsForBook(userID, loserID)
	require.NoError(t, err)
	require.Len(t, pos, 1)
}

func TestRepairMergedUserStateOp_ApplyMovesState(t *testing.T) {
	store, userID, winnerID, loserID := strandedFixture(t)
	p := &Plugin{deps: &fakeDeps{store: store}}
	require.NoError(t, p.runRepairMergedUserState(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}))

	w, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.NotNil(t, w)
	require.Equal(t, 61, w.ProgressPct)
	pos, err := store.ListUserPositionsForBook(userID, winnerID)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	lpos, err := store.ListUserPositionsForBook(userID, loserID)
	require.NoError(t, err)
	require.Empty(t, lpos)
}
