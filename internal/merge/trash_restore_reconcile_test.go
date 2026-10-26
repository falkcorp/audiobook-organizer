// file: internal/merge/trash_restore_reconcile_test.go
// version: 1.5.0
// guid: 3b547034-881d-4b1b-b911-df20c1e6ce56
// last-edited: 2026-10-26

package merge

import (
	"encoding/json"
	"errors"
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

	res, err := restoreFromTrash(f.S, loser, nil, nil, setFlag(t, f.S, false))
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

	res, err := restoreFromTrash(f.S, loser, nil, nil, setFlag(t, f.S, true))
	require.NoError(t, err)

	require.True(t, res.Restored)
	require.Equal(t, winner, res.RedirectFrom)
	require.Equal(t, loser, resolvesTo(t, f.S, loser), "the crowned row is its own ABS item")
	got, err := f.S.GetRaw(pendingKey)
	require.NoError(t, err)
	require.Nil(t, got, "the pending move onto the old winner is dropped")
}

// untrashBeforeWrite is a store whose next ModifyBook first takes the row out
// of the trash, as a restore that got there between RestoreFromTrashLocked's read
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

	res, err := restoreFromTrash(untrashBeforeWrite{f.S, t}, loser, nil, nil, func(_, _ *database.Book) {})
	require.NoError(t, err)

	require.False(t, res.Restored, "the row was already out of the trash at the write")
	require.Empty(t, res.RedirectFrom)
	require.Equal(t, winner, resolvesTo(t, f.S, loser), "the redirect the preview removed is back")
}

// The judge runs inside the write with the row as locked and as it would be
// written. This one passes on the preview (the first call, which runs before
// the redirect is touched) and refuses inside the write (the second), the case
// of a row that changed between the read and the lock. The refusal writes
// nothing, is returned wrapped, and puts back the redirect the preview removed
// (restoreRedirect after the failed ModifyBook); a judge that accepts both
// calls lets the restore through. Dropping the in-lock judge call or the
// put-back fails this.
func TestRestoreFromTrash_JudgeRefusalWritesNothingAndKeepsTheRedirect(t *testing.T) {
	f := combineUndoFixture(t)
	loser, winner, pendingKey := trashedLoser(t, f, "nil")
	refused := errors.New("not allowed to restore")
	calls := 0
	var sawBeforeInTrash, sawAfterLive bool
	var redirectAtSecondCall string

	res, err := restoreFromTrash(f.S, loser, nil, func(before, after *database.Book) error {
		calls++
		if calls == 1 {
			return nil
		}
		sawBeforeInTrash = database.IsInTrash(before)
		sawAfterLive = !database.IsInTrash(after)
		redirectAtSecondCall = resolvesTo(t, f.S, loser)
		return refused
	}, nil)

	require.ErrorIs(t, err, refused)
	require.Equal(t, 2, calls, "the preview passed and the write was judged too")
	require.True(t, sawBeforeInTrash, "the judge sees the row as locked")
	require.True(t, sawAfterLive, "the judge sees the row as it would be written")
	require.Equal(t, loser, redirectAtSecondCall, "fixture: the preview had removed the redirect before the write")
	require.False(t, res.Restored)
	require.Empty(t, res.RedirectFrom)
	b, err := f.S.GetBookByID(loser)
	require.NoError(t, err)
	require.True(t, database.IsInTrash(b), "nothing was written")
	require.Equal(t, winner, resolvesTo(t, f.S, loser), "the redirect the preview removed is back")
	got, err := f.S.GetRaw(pendingKey)
	require.NoError(t, err)
	require.NotNil(t, got)

	res, err = restoreFromTrash(f.S, loser, nil, func(_, _ *database.Book) error { return nil }, nil)
	require.NoError(t, err)
	require.True(t, res.Restored)
}

// failingModify is a store whose ModifyBook always fails, as a membership
// change or a store error does after the preview cleared the redirect.
type failingModify struct {
	*database.PebbleStore
	err error
}

func (f failingModify) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, f.err
}

// A write that fails for any reason after the preview cleared the redirect
// puts the redirect back, with no judge involved.
func TestRestoreFromTrash_FailedWritePutsTheRedirectBack(t *testing.T) {
	f := combineUndoFixture(t)
	loser, winner, pendingKey := trashedLoser(t, f, "nil")
	boom := errors.New("synthetic store failure")

	res, err := restoreFromTrash(failingModify{f.S, boom}, loser, nil, nil, nil)

	require.ErrorIs(t, err, boom)
	require.False(t, res.Restored)
	require.Empty(t, res.RedirectFrom)
	b, err := f.S.GetBookByID(loser)
	require.NoError(t, err)
	require.True(t, database.IsInTrash(b), "nothing was written")
	require.Equal(t, winner, resolvesTo(t, f.S, loser), "the redirect the preview removed is back")
	got, err := f.S.GetRaw(pendingKey)
	require.NoError(t, err)
	require.NotNil(t, got)
}

// A judge that refuses is asked on the preview, before the merge redirect is
// removed, so the refusal never depends on putting the redirect back. The first
// call must still see the redirect in place, and no second call (inside the
// write) is made once the preview refused. apply runs on the preview, so the
// judge sees the payload's effect there too.
func TestRestoreFromTrash_JudgeRefusesOnThePreviewBeforeTheRedirectIsTouched(t *testing.T) {
	f := combineUndoFixture(t)
	loser, winner, _ := trashedLoser(t, f, "nil")
	refused := errors.New("not allowed to restore")
	calls := 0
	var redirectAtFirstCall string
	var afterTitle string

	res, err := restoreFromTrash(f.S, loser,
		func(b *database.Book) { b.Title = "Hush" },
		func(before, after *database.Book) error {
			calls++
			if calls == 1 {
				redirectAtFirstCall = resolvesTo(t, f.S, loser)
				afterTitle = after.Title
			}
			return refused
		}, nil)

	require.ErrorIs(t, err, refused)
	require.Equal(t, 1, calls, "the preview refused, so the write was never attempted")
	require.Equal(t, winner, redirectAtFirstCall, "the redirect was still in place when the judge refused")
	require.Equal(t, "Hush", afterTitle, "the preview carries the payload")
	require.False(t, res.Restored)
	require.Empty(t, res.RedirectFrom)
	require.Equal(t, winner, resolvesTo(t, f.S, loser))
	b, err := f.S.GetBookByID(loser)
	require.NoError(t, err)
	require.True(t, database.IsInTrash(b), "nothing was written")
	require.NotEqual(t, "Hush", b.Title)
}
