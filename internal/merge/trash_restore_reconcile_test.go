// file: internal/merge/trash_restore_reconcile_test.go
// version: 1.1.0
// guid: 3b547034-881d-4b1b-b911-df20c1e6ce56
// last-edited: 2026-10-01

package merge

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// trashedLoser makes an ungrouped book with a file under the root, primary
// flag primary ("true", "false" or "nil"), a sync redirect to a live winner,
// a pending user-state repair onto that winner, and puts it in the trash.
func trashedLoser(t *testing.T, f *vptest.Fixture, primary string) (loser, winner, pendingKey string) {
	t.Helper()
	winner = f.Book(t, vptest.Spec{ID: "winner", Primary: "nil"})
	loser = f.Book(t, vptest.Spec{ID: "loser", Primary: primary})
	_, err := f.S.MintOrGetSyncID(loser)
	require.NoError(t, err)
	require.NoError(t, f.S.RecordSyncMerge(loser, winner))
	rec, err := json.Marshal(PendingUserStateRepair{LoserBookID: loser, WinnerBookID: winner, RecordedAt: time.Now()})
	require.NoError(t, err)
	pendingKey = PendingUserStateRepairPrefix + loser + ":" + winner
	require.NoError(t, f.S.SetRaw(pendingKey, rec))
	f.SoftDelete(t, loser)
	return loser, winner, pendingKey
}

// resolvesTo reports the book id a book's sync id resolves to.
func resolvesTo(t *testing.T, s *database.PebbleStore, id string) string {
	t.Helper()
	syncID, err := s.MintOrGetSyncID(id)
	require.NoError(t, err)
	item, err := s.ResolveSyncItem(syncID)
	require.NoError(t, err)
	require.NotNil(t, item)
	return item.CurrentBookID
}

// setFlag is a hand-off stand-in that writes the restored row's primary flag.
func setFlag(t *testing.T, s *database.PebbleStore, v bool) func(before, after *database.Book) {
	return func(_, after *database.Book) {
		_, err := s.ModifyBook(after.ID, func(b *database.Book) error {
			b.IsPrimaryVersion = &v
			return nil
		})
		require.NoError(t, err)
	}
}

// The preview says listable, so the redirect is removed before the write;
// the hand-off then demotes the row. The post-write decision puts the
// redirect back, reports no RedirectFrom, and keeps the pending repair.
func TestRestoreFromTrash_ReconcilePutsRedirectBackWhenHandOffDemotes(t *testing.T) {
	f := combineUndoFixture(t)
	loser, winner, pendingKey := trashedLoser(t, f, "nil")

	res, err := RestoreFromTrash(f.S, loser, nil, setFlag(t, f.S, false))
	require.NoError(t, err)

	require.True(t, res.Restored)
	require.Empty(t, res.RedirectFrom, "the redirect was put back, so the restore removed none")
	require.Equal(t, winner, resolvesTo(t, f.S, loser), "the redirect is back: the old id reaches the winner")
	got, err := f.S.GetRaw(pendingKey)
	require.NoError(t, err)
	require.NotNil(t, got, "the pending move is still owed")
}

// The preview says not listable (explicit false), so the redirect is kept
// through the write; the hand-off then crowns the row. The post-write
// decision removes the redirect late and drops the pending repair.
func TestRestoreFromTrash_ReconcileClearsRedirectLateWhenHandOffCrowns(t *testing.T) {
	f := combineUndoFixture(t)
	loser, winner, pendingKey := trashedLoser(t, f, "false")

	res, err := RestoreFromTrash(f.S, loser, nil, setFlag(t, f.S, true))
	require.NoError(t, err)

	require.True(t, res.Restored)
	require.Equal(t, winner, res.RedirectFrom)
	require.Equal(t, loser, resolvesTo(t, f.S, loser), "the crowned row is its own ABS item")
	got, err := f.S.GetRaw(pendingKey)
	require.NoError(t, err)
	require.Nil(t, got, "the pending move onto the old winner is dropped")
}

// untrashBeforeWrite is a store whose next ModifyBook first takes the row out
// of the trash, as a restore that got there between RestoreFromTrash's read
// and its write would.
type untrashBeforeWrite struct {
	*database.PebbleStore
	t *testing.T
}

func (u untrashBeforeWrite) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	_, err := u.PebbleStore.ModifyBook(id, func(b *database.Book) error {
		notMarked := false
		b.MarkedForDeletion = &notMarked
		b.MarkedForDeletionAt = nil
		organized := "organized"
		b.LibraryState = &organized
		return nil
	})
	require.NoError(u.t, err)
	return u.PebbleStore.ModifyBook(id, fn)
}

// The preview says listable and removes the redirect, but the row has left
// the trash by the time of the write, so this call restores nothing. The
// redirect it removed goes back.
func TestRestoreFromTrash_RowLeftTrashBeforeWritePutsRedirectBack(t *testing.T) {
	f := combineUndoFixture(t)
	loser, winner, _ := trashedLoser(t, f, "nil")

	res, err := RestoreFromTrash(untrashBeforeWrite{f.S, t}, loser, nil, func(_, _ *database.Book) {})
	require.NoError(t, err)

	require.False(t, res.Restored, "the row was already out of the trash at the write")
	require.Empty(t, res.RedirectFrom)
	require.Equal(t, winner, resolvesTo(t, f.S, loser), "the redirect the preview removed is back")
}
