// file: internal/audiobooks/restore_trash_review_test.go
// version: 1.0.0
// guid: d002b2d9-da13-4681-9c42-b8845f6cacf1
// last-edited: 2026-10-01

package audiobooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// absWouldRender is the ABS mapper's drop rule (loadOneItemView): a book whose
// sync id resolves to another item is a merge loser and is dropped from every
// list with warnRedirectedItemDropped. A book that would render resolves to
// itself.
func absWouldRender(t *testing.T, s *database.PebbleStore, id string) bool {
	t.Helper()
	syncID, err := s.MintOrGetSyncID(id)
	require.NoError(t, err)
	item, err := s.ResolveSyncItem(syncID)
	require.NoError(t, err)
	return item != nil && item.SyncID == syncID
}

// Adversarial review of #3649, finding 1: a "restore" of a row that was never
// in the trash yielded its primary flag to whichever other member Incumbent
// named, so a live primary was demoted and replaced. A restore of a live row
// is a no-op.
func TestRestoreAudiobook_NeverTrashedPrimaryIsLeftAlone(t *testing.T) {
	f, svc := handoffFixture(t)
	prim := f.Book(t, vptest.Spec{ID: "prim", Group: "g", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g", Primary: "nil"})
	// No library root: the hand-off after a restore holds the group rather
	// than re-electing, so a yield the restore wrote is what stays.
	config.AppConfig.RootDir = ""

	_, err := svc.RestoreAudiobook(context.Background(), prim)
	require.NoError(t, err)

	require.Equal(t, "true", f.Flag(t, prim), "a never-trashed primary must keep its flag")
	require.NotEqual(t, "true", f.Flag(t, sib), "and must not be replaced")
	b := restoredRow(t, f, prim)
	require.Nil(t, b.MarkedForDeletion, "a no-op restore writes nothing")
}

// Adversarial review of #3649, finding 3: restoring a merge loser cleared
// MergedIntoBookID but left the loser -> survivor sync redirect, so the ABS
// mapper kept dropping the restored book. The redirect goes with the restore;
// the survivor keeps the user state the merge followed onto it.
func TestRestoreAudiobook_MergeLoserRendersInABSAgain(t *testing.T) {
	f, svc := handoffFixture(t)
	survivor := f.Book(t, vptest.Spec{ID: "survivor", Primary: "nil"})
	loser := f.Book(t, vptest.Spec{ID: "loser", Primary: "nil"})
	_, err := f.S.MintOrGetSyncID(loser)
	require.NoError(t, err)
	require.NoError(t, f.S.RecordSyncMerge(loser, survivor))
	_, err = f.S.ModifyBook(loser, func(b *database.Book) error {
		b.MergedIntoBookID = &survivor
		return nil
	})
	require.NoError(t, err)
	f.SoftDelete(t, loser)
	// A follow that could not move everything left its pending-repair record.
	rec, err := json.Marshal(merge.PendingUserStateRepair{LoserBookID: loser, WinnerBookID: survivor, RecordedAt: time.Now()})
	require.NoError(t, err)
	pendingKey := merge.PendingUserStateRepairPrefix + loser + ":" + survivor
	require.NoError(t, f.S.SetRaw(pendingKey, rec))
	// The survivor holds the progress the merge followed onto it.
	u, err := f.S.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.S.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: survivor, Status: "in_progress", ProgressPct: 40, LastActivityAt: time.Now().UTC().Truncate(time.Second)}))
	require.False(t, absWouldRender(t, f.S, loser), "precondition: the merge loser redirects")

	_, err = svc.RestoreAudiobook(context.Background(), loser)
	require.NoError(t, err)

	b := restoredRow(t, f, loser)
	require.Nil(t, b.MergedIntoBookID)
	require.True(t, database.ABSLibraryFilter().Matches(b), "the restored loser passes the ABS list filter")
	require.True(t, absWouldRender(t, f.S, loser), "the restored loser must render as its own ABS item, not be dropped as a redirect")
	survSync, err := f.S.MintOrGetSyncID(survivor)
	require.NoError(t, err)
	aliases, err := f.S.ListSyncAliases(survSync)
	require.NoError(t, err)
	require.Empty(t, aliases, "the survivor no longer lists the restored book as an alias")
	got, err := f.S.GetRaw(pendingKey)
	require.NoError(t, err)
	require.Nil(t, got, "a pending move onto the old survivor is dropped: the book is its own again")
	st, err := f.S.GetUserBookState(u.ID, survivor)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 40, st.ProgressPct, "user state followed onto the survivor stays there")
}

// Adversarial review of #3649, findings 2 and 3, on a real combine: the
// absorbed shell keeps its "organized" label and its sync redirect, but owns
// no files. Restored from the trash it is a book of its own, "imported" (not
// listed by ABS as an empty book), with no redirect.
func TestRestoreAudiobook_CombineShellComesBackImportedWithoutRedirect(t *testing.T) {
	f, svc := handoffFixture(t)
	s := f.Book(t, vptest.Spec{ID: "s", Primary: "nil"})
	a := f.Book(t, vptest.Spec{ID: "a", Primary: "nil"})
	_, err := f.S.MintOrGetSyncID(a)
	require.NoError(t, err)

	res, err := merge.NewService(f.S).CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.BooksDeleted)
	shell := restoredRow(t, f, a)
	require.True(t, shell.IsSoftDeleted())
	require.Nil(t, shell.MergedIntoBookID, "a combine shell records no MergedIntoBookID")
	require.False(t, absWouldRender(t, f.S, a), "precondition: the combine recorded the shell's sync redirect")

	_, err = svc.RestoreAudiobook(context.Background(), a)
	require.NoError(t, err)

	b := restoredRow(t, f, a)
	require.False(t, b.IsSoftDeleted())
	require.Equal(t, "imported", *b.LibraryState, "a shell with no files must not come back organized")
	require.False(t, database.ABSLibraryFilter().Matches(b))
	require.True(t, absWouldRender(t, f.S, a), "the restored shell no longer redirects to the survivor")
}
