// file: internal/audiobooks/progress_safety_followups_test.go
// version: 1.0.0
// guid: 47073aff-f39f-47da-8927-07cdae028edb
// last-edited: 2026-10-05

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// Review follow-ups to #3771 / #3772 (trash progress carry).

// fileRowAfterFirstReadStore adds a book_file row to bookID on the second
// read of it: DiscardProgressAndPurge reads the book (1), checks its file
// rows up front, then summarizes its progress (TrashProgress reads it again,
// 2) before taking the merge lock. So the row lands after the up-front
// check and before the precheck under the lock.
type fileRowAfterFirstReadStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string
	mu     sync.Mutex
	reads  int
}

func (s *fileRowAfterFirstReadStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id != s.bookID {
		return b, err
	}
	s.mu.Lock()
	s.reads++
	add := s.reads == 2
	s.mu.Unlock()
	if add {
		require.NoError(s.t, s.PebbleStore.CreateBookFile(&database.BookFile{BookID: id, FilePath: "/x/" + id + ".m4b", Format: "m4b"}))
	}
	return b, err
}

// G1: a file row that lands after the up-front check refuses the discard in
// the precheck under the merge lock, BEFORE any state is cleared. Without
// the re-check the state was discarded and only then did DeleteBook refuse.
func TestDiscardProgressAndPurge_FileRowMeanwhileRefusesBeforeClearing(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	svc := NewAudiobookService(&fileRowAfterFirstReadStore{PebbleStore: store, t: t, bookID: "held"})
	rec := &fakeRecorder{}
	svc.auditOverride = rec

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "held"))
	require.Equal(t, 40, userPct(t, store, u.ID, "held"), "no state was cleared")
	pos, err := store.ListUserPositionsForBook(u.ID, "held")
	require.NoError(t, err)
	require.NotEmpty(t, pos, "the position is still there")
	require.Empty(t, rec.entries)
}

// siblingReadFailsStore fails reads of the sibling once the purge has
// listed the version group, so only the carry precheck's re-read fails.
type siblingReadFailsStore struct {
	*database.PebbleStore
	sibling string
	mu      sync.Mutex
	armed   bool
}

func (s *siblingReadFailsStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
	return s.PebbleStore.GetBooksByVersionGroup(gid)
}

func (s *siblingReadFailsStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	armed := s.armed
	s.mu.Unlock()
	if armed && id == s.sibling {
		return nil, fmt.Errorf("injected read failure for %s", id)
	}
	return s.PebbleStore.GetBookByID(id)
}

// G2: the carry precheck failing to re-read the sibling is a failed carry
// reported with its error, not a quiet kept_has_progress.
func TestPurge_SiblingReadErrorIsCarryFailed(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&siblingReadFailsStore{PebbleStore: store, sibling: "keep"})

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.CarryFailed, "result %+v", res)
	require.Equal(t, []string{"dup"}, res.CarryFailedIDs)
	require.Zero(t, res.KeptHasProgress)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0], "injected read failure")
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
}

// A: "Purge now" (DeleteAudiobook's hard path) on a trashed book with
// progress and a listed copy carries the progress there, then purges.
func TestPurgeNow_CarriesToListedCopyThenPurges(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")

	res, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})
	require.NoError(t, err)
	require.Equal(t, true, res["progress_moved"])
	require.True(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"))
	tomb, err := store.GetBookTombstone("dup")
	require.NoError(t, err)
	require.Nil(t, tomb, "the purge's tombstone was cleaned up like the nightly purge's")
}

// A: "Purge now" on a trashed book with progress and no listed copy is
// refused with ErrBookHasProgress, pointing to Discard progress and purge;
// the book and its progress stay. (Until 2026-10-05 it hard-deleted the book
// and dropped the progress.)
func TestPurgeNow_RefusesWithoutListedCopy(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")

	_, err := svc.DeleteAudiobook(context.Background(), "held", &DeleteAudiobookOptions{})
	require.ErrorIs(t, err, ErrBookHasProgress)
	require.ErrorContains(t, err, "Discard progress and purge")
	require.False(t, bookGone(t, store, "held"))
	require.Equal(t, 40, userPct(t, store, u.ID, "held"))
}

// A: a bookmark is progress too: a trashed book whose only state is a
// bookmark is refused, not deleted.
func TestPurgeNow_BookmarkOnlyIsProgress(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	syncID, err := database.AsSyncIdentityStore(store).MintOrGetSyncID("held")
	require.NoError(t, err)
	require.NoError(t, store.CreateBookmark(progress.Bookmark{UserID: u.ID, ItemID: syncID, TimeSec: 5, Title: "m"}))

	_, err = svc.DeleteAudiobook(context.Background(), "held", &DeleteAudiobookOptions{})
	require.ErrorIs(t, err, ErrBookHasProgress)
	require.False(t, bookGone(t, store, "held"))
	info, err := svc.TrashProgress(context.Background(), []string{"held"})
	require.NoError(t, err)
	require.True(t, info["held"].HasProgress)
	require.Equal(t, "reader: 1 bookmark", info["held"].Summary)
}

// A: a live book's hard delete with progress and no other listed copy is
// refused before any side effect (no hash block); without progress it
// deletes as before.
func TestHardDeleteLiveBook_CarryOrRefuse(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hash := "h-live"
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live", Format: "m4b", FileHash: &hash})
	require.NoError(t, err)
	u := purgeSeedProgress(t, store, "live")

	_, err = svc.DeleteAudiobook(context.Background(), "live", &DeleteAudiobookOptions{BlockHash: true})
	require.ErrorIs(t, err, ErrBookHasProgress)
	require.False(t, bookGone(t, store, "live"))
	require.Equal(t, 40, userPct(t, store, u.ID, "live"))
	blocked, err := store.IsHashBlocked(hash)
	require.NoError(t, err)
	require.False(t, blocked, "a refused delete blocks no hash")

	_, err = store.CreateBook(&database.Book{ID: "plain", Title: "plain", Format: "m4b"})
	require.NoError(t, err)
	_, err = svc.DeleteAudiobook(context.Background(), "plain", &DeleteAudiobookOptions{})
	require.NoError(t, err)
	require.True(t, bookGone(t, store, "plain"))
}

// A: a live non-primary copy's hard delete carries its progress to the
// group's listed primary and deletes.
func TestHardDeleteLiveBook_CarriesToListedPrimary(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "copy", "g1", false, false)
	u := purgeSeedProgress(t, store, "copy")

	res, err := svc.DeleteAudiobook(context.Background(), "copy", &DeleteAudiobookOptions{})
	require.NoError(t, err)
	require.Equal(t, "keep", res["progress_moved_to"])
	require.True(t, bookGone(t, store, "copy"))
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"))
}

// G8: an iTunes-only copy (primary, carrying the iTunes track, but not
// organized, so Audiobookshelf does not list it) is not a carry target:
// the trashed book keeps its progress.
func TestPurge_ITunesOnlySiblingIsNotACarryTarget(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	gid := "g1"
	pid := "ABCDEF0123456789"
	_, err := store.CreateBook(&database.Book{ID: "itunes-copy", Title: "t", Format: "m4b", VersionGroupID: &gid,
		IsPrimaryVersion: new(true), LibraryState: new("imported"), ITunesPersistentID: &pid})
	require.NoError(t, err)
	groupBook(t, store, "dup", gid, true, false)
	u := purgeSeedProgress(t, store, "dup")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.KeptHasProgress, "result %+v", res)
	require.Zero(t, res.CarriedToSibling)
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
	require.Zero(t, userPct(t, store, u.ID, "itunes-copy"))
	info, err := svc.TrashProgress(context.Background(), []string{"dup"})
	require.NoError(t, err)
	require.Empty(t, info["dup"].ListedCopyID, "the listing says there is no copy to move it to")
}

// G3: the trash listing says when a listed copy exists (so the UI offers
// "Move progress and purge" instead of discarding) and whether the nightly
// purge would take the book now.
func TestTrashProgress_ListedCopyAndEligibility(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	prev := config.AppConfig.PurgeSoftDeletedAfterDays
	t.Cleanup(func() { config.AppConfig.PurgeSoftDeletedAfterDays = prev })
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false) // trashed an hour ago
	purgeSeedProgress(t, store, "dup")

	config.AppConfig.PurgeSoftDeletedAfterDays = 30
	info, err := svc.TrashProgress(context.Background(), []string{"dup"})
	require.NoError(t, err)
	require.Equal(t, "keep", info["dup"].ListedCopyID)
	require.False(t, info["dup"].PurgeEligible, "trashed an hour ago, retention 30 days")

	b, err := store.GetBookByID("dup")
	require.NoError(t, err)
	old := time.Now().AddDate(0, 0, -31)
	_, err = store.ModifyBook("dup", func(row *database.Book) error { row.MarkedForDeletionAt = &old; return nil })
	require.NoError(t, err)
	_ = b
	info, err = svc.TrashProgress(context.Background(), []string{"dup"})
	require.NoError(t, err)
	require.True(t, info["dup"].PurgeEligible)

	config.AppConfig.PurgeSoftDeletedAfterDays = 0
	info, err = svc.TrashProgress(context.Background(), []string{"dup"})
	require.NoError(t, err)
	require.False(t, info["dup"].PurgeEligible, "the nightly purge is off")
}

// G5: a viewer sees their own progress line and a count of the others; a
// viewer who may manage users sees every line.
func TestTrashProgressInfo_ForViewer(t *testing.T) {
	info := TrashProgressInfo{HasProgress: true, Summary: "alice: 40%; bob: finished",
		users: []userProgressLine{{userID: "a", line: "alice: 40%"}, {userID: "b", line: "bob: finished"}}}
	own := info.ForViewer("a", false)
	require.Equal(t, "alice: 40%", own.Summary)
	require.Equal(t, 1, own.OtherUsers)
	none := info.ForViewer("c", false)
	require.Empty(t, none.Summary)
	require.Equal(t, 2, none.OtherUsers)
	anon := info.ForViewer("", false)
	require.Empty(t, anon.Summary)
	require.Equal(t, 2, anon.OtherUsers)
	all := info.ForViewer("c", true)
	require.Equal(t, info.Summary, all.Summary)
	require.Zero(t, all.OtherUsers)
}

// G7: the purge's iTunes removes skip a PID a live copy still holds (the
// same legacy track on a sibling), and run only after the row is gone.
func TestPurgeDeleteRow_ITunesRemovesSpareSharedPIDAndFollowTheDelete(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	enq := &fakeITunesEnqueuer{}
	svc.SetITunesEnqueuer(enq)
	gid := "g1"
	shared, own := "SHAREDPID0000001", "OWNPID0000000002"
	_, err := store.CreateBook(&database.Book{ID: "keep", Title: "k", Format: "m4b", VersionGroupID: &gid,
		IsPrimaryVersion: new(true), LibraryState: new("organized"), ITunesPersistentID: &shared})
	require.NoError(t, err)
	groupBook(t, store, "dup", gid, true, false)
	_, err = store.ModifyBook("dup", func(b *database.Book) error { b.ITunesPersistentID = &shared; return nil })
	require.NoError(t, err)
	groupBook(t, store, "solo", "g2", true, false)
	_, err = store.ModifyBook("solo", func(b *database.Book) error { b.ITunesPersistentID = &own; return nil })
	require.NoError(t, err)

	dup, err := store.GetBookByID("dup")
	require.NoError(t, err)
	require.NoError(t, svc.purgeDeleteRow(dup))
	require.Empty(t, enq.pids, "the live copy's iTunes track is not removed")

	solo, err := store.GetBookByID("solo")
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: "solo", FilePath: "/x/solo.m4b", Format: "m4b"}))
	err = svc.purgeDeleteRow(solo)
	require.True(t, errors.Is(err, database.ErrBookOwnsFiles), "err = %v", err)
	require.Empty(t, enq.pids, "a delete that failed queued no iTunes remove")
	require.False(t, bookGone(t, store, "solo"))
}
