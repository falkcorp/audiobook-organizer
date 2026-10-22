// file: internal/audiobooks/undo_hold_test.go
// version: 1.1.0
// guid: 3c9e5a17-8d42-4b60-a1f3-7e0b6d2c9a48
// last-edited: 2026-10-22

package audiobooks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// journalSoftDelete writes a book_soft_delete ledger row, as a maintenance op
// does for a shell it retires.
func journalSoftDelete(t *testing.T, store *database.PebbleStore, opID, bookID, newValue string) *database.OperationChange {
	t.Helper()
	c := &database.OperationChange{
		OperationID: opID, BookID: bookID, ChangeType: undo.ChangeTypeBookSoftDelete,
		FieldName: "marked_for_deletion", NewValue: newValue,
	}
	require.NoError(t, store.CreateOperationChange(c))
	require.NotEmpty(t, c.ID)
	return c
}

var autoMergeSeq atomic.Int64

// journalAutoMerge writes an auto-merge journal entry whose loser is loserID.
func journalAutoMerge(t *testing.T, store *database.PebbleStore, loserID string, provisional bool, undoneAt int64) string {
	t.Helper()
	emb := database.NewEmbeddingStoreFromStore(store)
	require.NotNil(t, emb)
	key, err := emb.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
		WinnerID: "winner", LoserID: loserID, Provisional: provisional, UndoneAt: undoneAt,
		MergedAt: time.Now().UnixNano() + autoMergeSeq.Add(1),
	})
	require.NoError(t, err)
	return key
}

// restoreAndRetrash takes a trashed book out of the trash and deletes it again
// at once, so it carries a new marked_for_deletion_at stamp.
func restoreAndRetrash(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	_, err := store.ModifyBook(id, func(b *database.Book) error {
		no := false
		b.MarkedForDeletion, b.MarkedForDeletionAt = &no, nil
		return nil
	})
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, merge.SoftDeleteBook(store, id))
}

func purgeOnce(t *testing.T, svc *AudiobookService) *PurgeResult {
	t.Helper()
	res, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.NoError(t, err)
	return res
}

// fs-regroup-xml's fragments apply soft-deletes the shell it empties and
// journals it as a book_soft_delete row whose NewValue is a note, not a stamp.
// The shell owns no book_file row (they moved to the survivor), and the
// revert needs the shell: it is held while the op can still be reverted.
func TestRunPurge_FsRegroupFragmentsShellIsHeldByTheLedger(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	softDeleted(t, store, "plain", "")
	c := journalSoftDelete(t, store, "op-fs", "shell", "merged into survivor")

	res := purgeOnce(t, svc)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, []string{"shell"}, res.KeptUndoPossibleIDs)
	assert.False(t, bookGone(t, store, "shell"))
	assert.True(t, bookGone(t, store, "plain"))

	// Once the op's row is reverted nothing needs the shell.
	require.NoError(t, store.MarkOperationChangesReverted("op-fs", []string{c.ID}))
	res = purgeOnce(t, svc)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.True(t, bookGone(t, store, "shell"))
}

// retire_into and the folder-books retire journal a stamped book_soft_delete
// row even for a book that had no book_file rows. Held while the stamp is the
// book's own; a book restored and deleted again later is not held.
func TestRunPurge_RetireIntoShellWithNoRowsIsHeldWhileStampMatches(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	row, err := store.GetBookByID("shell")
	require.NoError(t, err)
	journalSoftDelete(t, store, "op-retire", "shell", undo.SoftDeleteStamp(*row.MarkedForDeletionAt))

	res := purgeOnce(t, svc)
	assert.Zero(t, res.Purged)
	assert.Equal(t, []string{"shell"}, res.KeptUndoPossibleIDs)

	restoreAndRetrash(t, store, "shell")
	res = purgeOnce(t, svc)
	assert.Equal(t, 1, res.Purged, "a re-deleted book carries another stamp: the op's revert would not restore it, %+v", res)
	assert.True(t, bookGone(t, store, "shell"))
}

// A row its writer voided (the write was refused and never made) holds nothing.
func TestRunPurge_VoidedLedgerRowHoldsNothing(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	now := time.Now()
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-void", BookID: "shell", ChangeType: undo.ChangeTypeBookSoftDelete,
		FieldName: "marked_for_deletion", NewValue: "note", Voided: true, RevertedAt: &now,
	}))
	res := purgeOnce(t, svc)
	assert.Equal(t, 1, res.Purged, "%+v", res)
}

// Only book_soft_delete rows hold: an ordinary unreverted metadata edit on a
// trashed book does not keep it in the trash.
func TestRunPurge_OtherLedgerRowsDoNotHold(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "edited", "")
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-meta", BookID: "edited", ChangeType: "metadata_update",
		FieldName: "title", OldValue: "a", NewValue: "b",
	}))
	res := purgeOnce(t, svc)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Zero(t, res.KeptUndoPossible)
}

// An operation that journals the shell after the pass built its env is still
// seen: the ledger is read per book under the merge lock, not snapshotted.
func TestPurgeOne_LedgerRowWrittenMidPassKeepsTheShell(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	row, err := store.GetBookByID("shell")
	require.NoError(t, err)

	journalSoftDelete(t, store, "op-mid", "shell", "merged into survivor")

	out := svc.purgeOne(row, false, env)
	assert.Equal(t, purgeKeptUndo, out.kind, "%+v", out)
	assert.False(t, bookGone(t, store, "shell"))
}

// The loser of an auto-merge that was not undone is held, provisional entries
// included; an undone entry holds nothing.
func TestRunPurge_AutoMergeLoserIsHeldUntilUndone(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "open", "")
	softDeleted(t, store, "prov", "")
	softDeleted(t, store, "undone", "")
	softDeleted(t, store, "plain", "")
	journalAutoMerge(t, store, "open", false, 0)
	journalAutoMerge(t, store, "prov", true, 0)
	journalAutoMerge(t, store, "undone", false, time.Now().UnixNano())

	res := purgeOnce(t, svc)
	assert.Equal(t, 2, res.Purged, "%+v", res)
	assert.Equal(t, []string{"open", "prov"}, res.KeptUndoPossibleIDs)
	assert.True(t, bookGone(t, store, "undone"))
	assert.True(t, bookGone(t, store, "plain"))
}

// An auto-merge journaled after the env was built is seen at the decision.
func TestPurgeOne_AutoMergeJournaledMidPassKeepsTheLoser(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "loser", "")
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	row, err := store.GetBookByID("loser")
	require.NoError(t, err)

	journalAutoMerge(t, store, "loser", true, 0)

	out := svc.purgeOne(row, false, env)
	assert.Equal(t, purgeKeptUndo, out.kind, "%+v", out)
	assert.False(t, bookGone(t, store, "loser"))
}

// A corrupt auto-merge journal row might name the book: fail closed.
func TestRunPurge_UnreadableAutoMergeJournalRefusesThePass(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "plain", "")
	require.NoError(t, store.DB().Set([]byte("dedup:automerge:ffffffffffffffff"), []byte("{not json"), nil))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto-merge journal")
	require.NotNil(t, res)
	assert.True(t, res.StoppedEarly)
	assert.False(t, bookGone(t, store, "plain"))
}

// Until the by-book ledger index is built and verified the purge refuses to
// run, whichever way it is started, and deletes nothing.
func TestPurge_FailsClosedUntilTheLedgerIndexIsBuilt(t *testing.T) {
	svc, store, _ := setupPurgeBoundaryUntrusted(t)
	softDeleted(t, store, "shell", "")
	journalSoftDelete(t, store, "op-fs", "shell", "merged into survivor")
	require.False(t, store.OpChangeByBookIndexUsable())

	for _, dry := range []bool{false, true} {
		res, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: dry})
		require.Error(t, err, "dry=%t", dry)
		assert.True(t, errors.Is(err, ErrUndoLedgerNotReady), "%v", err)
		assert.Contains(t, err.Error(), "maintenance.opchange-book-index-rebuild")
		require.NotNil(t, res)
		assert.True(t, res.StoppedEarly)
		assert.Contains(t, res.StopReason, "purge refused")
	}
	row, err := store.GetBookByID("shell")
	require.NoError(t, err)
	_, err = svc.purgeNow(row)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUndoLedgerNotReady), "%v", err)
	svc.auditOverride = &fakeRecorder{}
	_, err = svc.DiscardProgressAndPurge(context.Background(), "shell", "owner")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUndoLedgerNotReady), "%v", err)
	assert.False(t, bookGone(t, store, "shell"), "nothing may be deleted before the index is ready")

	// The backfill (the boot-time ensure) builds the index from the journal
	// rows already written, and the purge then runs and holds the shell.
	res2, err := store.EnsureOpChangeByBookIndex(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, res2.Trusted, "%+v", res2)
	require.True(t, store.OpChangeByBookIndexUsable())
	res3 := purgeOnce(t, svc)
	assert.Equal(t, []string{"shell"}, res3.KeptUndoPossibleIDs)
	assert.Zero(t, res3.Purged)
}

// The under-lock hold in purgeIfStateless is decided on the row as it is NOW.
// Called directly, so purgeGate's own rebuild cannot answer for it: a book that
// was held, restored, deleted again and absorbed by a NEW combine is held by
// the new stamp, not by the listing's copy.
func TestPurgeIfStateless_HoldsOnTheRowAsItIsUnderTheLock(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	holdByCombine(t, store, "shell") // J1 holds the first stamp
	listing, err := store.GetBookByID("shell")
	require.NoError(t, err)

	restoreAndRetrash(t, store, "shell") // new stamp: J1 no longer holds it
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	holdByCombine(t, store, "shell") // J2 absorbs it again, journaled after the env

	r := svc.purgeIfStateless(listing, env)
	require.NoError(t, r.undoErr)
	assert.True(t, r.keptUndo, "%+v", r)
	assert.False(t, bookGone(t, store, "shell"))
}

// The other side of the same check: a stale listing copy that J1 holds, whose
// row was since restored and deleted again, is NOT held by J1, so the purge
// goes ahead (the hold is not just "any journal names this book").
func TestPurgeIfStateless_StaleListingStampDoesNotHold(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	holdByCombine(t, store, "shell")
	listing, err := store.GetBookByID("shell")
	require.NoError(t, err)
	restoreAndRetrash(t, store, "shell")
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)

	r := svc.purgeIfStateless(listing, env)
	require.NoError(t, r.deleteErr)
	assert.False(t, r.keptUndo)
	assert.True(t, bookGone(t, store, "shell"))
}

// The carry path's precheck, called directly: a shell with listening state and
// a live sibling whose combine journals after the env was built is refused
// before anything moves.
func TestPurgeCarryingState_PrecheckHoldsAShellJournaledAfterTheEnv(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	listing, err := store.GetBookByID("dup")
	require.NoError(t, err)

	holdByCombine(t, store, "dup")

	out := svc.purgeCarryingState(listing, false, env)
	assert.Equal(t, purgeKeptUndo, out.kind, "%+v", out)
	assert.False(t, bookGone(t, store, "dup"))
	assert.Equal(t, 40, userPct(t, store, u.ID, "dup"), "the state must not have moved")
	assert.Zero(t, userPct(t, store, u.ID, "keep"))
}

// Same on the carry path for a ledger-held shell restored and re-absorbed: the
// op's row carries the new stamp.
func TestPurgeCarryingState_PrecheckHoldsALedgerShell(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	listing, err := store.GetBookByID("dup")
	require.NoError(t, err)
	restoreAndRetrash(t, store, "dup")
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	cur, err := store.GetBookByID("dup")
	require.NoError(t, err)
	journalSoftDelete(t, store, "op-retire", "dup", undo.SoftDeleteStamp(*cur.MarkedForDeletionAt))

	out := svc.purgeCarryingState(listing, false, env)
	assert.Equal(t, purgeKeptUndo, out.kind, "%+v", out)
	assert.False(t, bookGone(t, store, "dup"))
	assert.Equal(t, 40, userPct(t, store, u.ID, "dup"))
}

// A combine-journal row that cannot be decoded may be an undo that still needs
// a trashed book, and cannot say which. Owner decision 2026-10-22: it blocks
// every purge, whichever way it starts, until repaired, and the refusal names
// the row's key and the op that lists such rows. Nothing is deleted.
func TestPurge_UnreadableCombineJournalRowRefusesEveryPurgePath(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	softDeleted(t, store, "plain", "")
	holdByCombine(t, store, "shell")
	const badKey = "merge:combine-journal:01CORRUPTROW"
	require.NoError(t, store.SetRaw(badKey, []byte("{not json")))

	for _, dry := range []bool{false, true} {
		res, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: dry})
		require.Error(t, err, "dry=%t", dry)
		assert.Contains(t, err.Error(), badKey, "dry=%t", dry)
		assert.Contains(t, err.Error(), merge.CombineJournalCheckOp, "dry=%t", dry)
		var ue *merge.UnreadableCombineJournalsError
		assert.True(t, errors.As(err, &ue), "%v", err)
		require.NotNil(t, res)
		assert.True(t, res.StoppedEarly, "dry=%t", dry)
		assert.Contains(t, res.StopReason, "purge refused", "dry=%t", dry)
		assert.Contains(t, res.StopReason, badKey, "dry=%t", dry)
		assert.Zero(t, res.Purged, "dry=%t", dry)
	}
	row, err := store.GetBookByID("plain")
	require.NoError(t, err)
	_, err = svc.purgeNow(row)
	require.Error(t, err)
	assert.Contains(t, err.Error(), badKey)
	svc.auditOverride = &fakeRecorder{}
	_, err = svc.DiscardProgressAndPurge(context.Background(), "plain", "owner")
	require.Error(t, err)
	assert.Contains(t, err.Error(), badKey)
	assert.False(t, bookGone(t, store, "plain"), "nothing may be deleted while a journal row is unreadable")
	assert.False(t, bookGone(t, store, "shell"))
}

// With no corrupt rows the same store purges normally: the held shell is kept,
// the plain one is purged. Removing the bad row (the manual repair) is enough.
func TestPurge_RepairedCombineJournalRowPurgesNormally(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "shell", "")
	softDeleted(t, store, "plain", "")
	holdByCombine(t, store, "shell")
	const badKey = "merge:combine-journal:01CORRUPTROW"
	require.NoError(t, store.SetRaw(badKey, []byte("{not json")))
	_, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true})
	require.Error(t, err)

	require.NoError(t, store.DeleteRaw(badKey))
	res := purgeOnce(t, svc)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Equal(t, []string{"shell"}, res.KeptUndoPossibleIDs)
	assert.True(t, bookGone(t, store, "plain"))
	assert.False(t, bookGone(t, store, "shell"))
}
