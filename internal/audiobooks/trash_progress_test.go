// file: internal/audiobooks/trash_progress_test.go
// version: 1.2.0
// guid: 98427cba-9b8a-4313-ace0-44b5115e8d6c
// last-edited: 2026-10-05

package audiobooks

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// groupBook creates a book in version group gid. trashed soft-deletes it;
// listed makes it a book Audiobookshelf lists (organized primary).
func groupBook(t *testing.T, store database.Store, id, gid string, trashed, listed bool) {
	t.Helper()
	b := &database.Book{ID: id, Title: id, Format: "m4b", VersionGroupID: &gid}
	if trashed {
		yes := true
		now := time.Now().Add(-time.Hour)
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
		b.LibraryState = new("deleted")
	}
	if listed {
		b.IsPrimaryVersion = new(true)
		b.LibraryState = new("organized")
	} else if !trashed {
		b.IsPrimaryVersion = new(false)
		b.LibraryState = new("imported")
	}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
}

func userPct(t *testing.T, store database.Store, userID, bookID string) int {
	t.Helper()
	st, err := store.GetUserBookState(userID, bookID)
	require.NoError(t, err)
	if st == nil {
		return 0
	}
	return st.ProgressPct
}

func bookGone(t *testing.T, store database.Store, id string) bool {
	t.Helper()
	b, err := store.GetBookByID(id)
	if err != nil {
		return true
	}
	return b == nil
}

// A trashed book holding a user's progress, with a live version in its
// group, has the progress carried there and is then purged.
func TestPurge_CarriesStateToLiveSiblingThenPurges(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 1, res.Purged)
	require.Equal(t, 1, res.CarriedToSibling)
	require.Equal(t, []string{"dup"}, res.CarriedToSiblingIDs)
	require.Zero(t, res.KeptHasProgress)
	require.Zero(t, res.CarryFailed)
	require.True(t, bookGone(t, store, "dup"), "the trashed book is purged")
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"), "the progress is on the live version")
	pos, err := store.ListUserPositionsForBook(u.ID, "keep")
	require.NoError(t, err)
	require.NotEmpty(t, pos, "the position moved too")
}

// The state goes to a version Audiobookshelf lists over one it does not,
// even when the unlisted one sorts first by id.
func TestPurge_CarryPrefersABSListedSibling(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "a-unlisted", "g1", false, false)
	groupBook(t, store, "z-listed", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.CarriedToSibling, "result %+v", res)
	require.Equal(t, 40, userPct(t, store, u.ID, "z-listed"))
	require.Zero(t, userPct(t, store, u.ID, "a-unlisted"))
}

// A group whose only other member is also in the trash has no live version:
// the book is kept, flagged, and nothing moves.
func TestPurge_TrashedSiblingIsNotACarryTarget(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "other", "g1", true, false)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.KeptHasProgress, "result %+v", res)
	require.Equal(t, []string{"dup"}, res.KeptHasProgressIDs)
	require.Equal(t, 1, res.Purged, "the stateless trashed sibling purges as before")
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
}

// purgeFaultStore fails SetUserBookState onto one book, so a carry there
// cannot complete.
type purgeFaultStore struct {
	*database.PebbleStore
	failStateOn string
}

func (s *purgeFaultStore) SetUserBookState(st *database.UserBookState) error {
	if st.BookID == s.failStateOn {
		return fmt.Errorf("injected SetUserBookState failure on %s", st.BookID)
	}
	return s.PebbleStore.SetUserBookState(st)
}

// A carry that does not complete keeps the book, with the state put back on
// it, and is reported as carry_failed with the reason in Errors.
func TestPurge_CarryFailureKeepsBook(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&purgeFaultStore{PebbleStore: store, failStateOn: "keep"})

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.CarryFailed, "result %+v", res)
	require.Equal(t, []string{"dup"}, res.CarryFailedIDs)
	require.Zero(t, res.Purged)
	require.Zero(t, res.CarriedToSibling)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0], "not purged, listening state kept on it")
	require.False(t, bookGone(t, store, "dup"), "fail closed: the book stays")
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"), "every user's state is back on it")
	tomb, err := store.GetBookTombstone("dup")
	require.NoError(t, err)
	require.Nil(t, tomb, "no purge side effect ran for a book that was kept")
}

// Many groups at once (the worker pool): every trashed book's state lands
// on its own group's live version, and nothing crosses groups.
func TestPurge_CarryAcrossManyGroups(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	const groups = 12
	for g := range groups {
		gid := fmt.Sprintf("g%02d", g)
		groupBook(t, store, "keep-"+gid, gid, false, true)
		for d := range 2 {
			id := fmt.Sprintf("dup-%s-%d", gid, d)
			groupBook(t, store, id, gid, true, false)
			require.NoError(t, store.SetUserBookState(&database.UserBookState{
				UserID: u.ID, BookID: id, Status: database.UserBookStatusInProgress,
				ProgressPct: 10 + g + d, LastActivityAt: time.Now().Add(time.Duration(d) * time.Second),
			}))
		}
	}

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 2*groups, res.CarriedToSibling)
	require.Equal(t, 2*groups, res.Purged)
	for g := range groups {
		gid := fmt.Sprintf("g%02d", g)
		require.NotZero(t, userPct(t, store, u.ID, "keep-"+gid), "group %s got its own state", gid)
	}
}

func TestPurgePartitions_GroupsStayTogether(t *testing.T) {
	g1, g2 := "g1", "g2"
	books := []database.Book{
		{ID: "a", VersionGroupID: &g1}, {ID: "b"}, {ID: "c", VersionGroupID: &g2},
		{ID: "d", VersionGroupID: &g1}, {ID: "e"},
	}
	parts := purgePartitions(books)
	var got [][]string
	for _, p := range parts {
		var ids []string
		for _, b := range p {
			ids = append(ids, b.ID)
		}
		got = append(got, ids)
	}
	require.Equal(t, [][]string{{"a", "d"}, {"b"}, {"c"}, {"e"}}, got)
}

type fakeRecorder struct {
	mu      sync.Mutex
	entries []database.ActivityEntry
	err     error
}

func (r *fakeRecorder) Record(e database.ActivityEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.entries = append(r.entries, e)
	return nil
}

func TestTrashProgress_FlagsAndSummarizes(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	softDeleted(t, store, "clean", "")
	u := purgeSeedProgress(t, store, "held")
	_ = u

	info, err := svc.TrashProgress(context.Background(), []string{"held", "clean"})
	require.NoError(t, err)
	require.True(t, info["held"].HasProgress)
	require.Equal(t, "reader: 40%, at 0:40", info["held"].Summary)
	require.False(t, info["clean"].HasProgress)
	require.False(t, info["clean"].Unknown)
	require.Empty(t, info["clean"].Summary)
}

func TestDescribeUserState(t *testing.T) {
	require.Equal(t, "finished", describeUserState(&database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100}, nil, 0))
	require.Equal(t, "at 1:02:03", describeUserState(nil, []database.UserPosition{{PositionSeconds: 3723}}, 0))
	require.Equal(t, "hidden from continue listening", describeUserState(&database.UserBookState{HideFromContinueListening: true}, nil, 0))
	require.Empty(t, describeUserState(&database.UserBookState{}, nil, 0))
	require.Equal(t, "1 bookmark", describeUserState(nil, nil, 1))
	require.Equal(t, "finished, 3 bookmarks", describeUserState(&database.UserBookState{Status: database.UserBookStatusFinished}, nil, 3))
}

// The owner's discard clears every user's state, positions and bookmarks on
// the trashed book, purges it, and writes an audit row naming who asked.
func TestDiscardProgressAndPurge_ClearsStateAndPurges(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	rec := &fakeRecorder{}
	svc.auditOverride = rec
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	syncID, err := database.AsSyncIdentityStore(store).MintOrGetSyncID("held")
	require.NoError(t, err)
	require.NoError(t, database.AsBookmarkStore(store).CreateBookmark(progress.Bookmark{UserID: u.ID, ItemID: syncID, TimeSec: 12, Title: "mark"}))

	res, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner (u1)")
	require.NoError(t, err)
	require.Equal(t, 1, res.UsersCleared)
	require.Equal(t, 1, res.BookmarksCleared)
	require.Equal(t, "reader: 40%, at 0:40, 1 bookmark", res.ProgressSummary)
	require.Empty(t, res.Warnings)
	require.True(t, bookGone(t, store, "held"))
	st, err := store.GetUserBookState(u.ID, "held")
	require.NoError(t, err)
	require.Nil(t, st, "the state row is gone")
	pos, err := store.ListUserPositionsForBook(u.ID, "held")
	require.NoError(t, err)
	require.Empty(t, pos)
	marks, err := database.AsBookmarkStore(store).ListBookmarks(u.ID, syncID)
	require.NoError(t, err)
	require.Empty(t, marks)
	require.Len(t, rec.entries, 1)
	require.Equal(t, "audit", rec.entries[0].Tier)
	require.Equal(t, "held", rec.entries[0].BookID)
	require.Equal(t, "owner (u1)", rec.entries[0].Details["actor"])
}

// A book that is not in the trash is refused and nothing changes: not its
// state, not the row, no audit row.
func TestDiscardProgressAndPurge_RefusesNonTrashed(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	rec := &fakeRecorder{}
	svc.auditOverride = rec
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live", Format: "m4b"})
	require.NoError(t, err)
	u := purgeSeedProgress(t, store, "live")

	_, err = svc.DiscardProgressAndPurge(context.Background(), "live", "owner")
	require.ErrorIs(t, err, ErrNotInTrash)
	require.False(t, bookGone(t, store, "live"))
	require.Equal(t, 40, userPct(t, store, u.ID, "live"))
	require.Empty(t, rec.entries)

	_, err = svc.DiscardProgressAndPurge(context.Background(), "missing", "owner")
	require.ErrorIs(t, err, ErrAudiobookNotFound)
}

// A trashed book that still owns book_file rows is refused before any state
// is cleared (the purge never deletes such a book).
func TestDiscardProgressAndPurge_RefusesBookOwningFiles(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: "held", FilePath: "/x/held.m4b", Format: "m4b"}))

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "held"))
	require.Equal(t, 40, userPct(t, store, u.ID, "held"))
}

// With no activity log to record it in, the destructive action is refused.
func TestDiscardProgressAndPurge_RefusesUnrecorded(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, ErrAuditUnavailable)
	require.Equal(t, 40, userPct(t, store, u.ID, "held"))
}

// A pending user-state repair naming the book refuses the discard before
// anything is cleared.
func TestDiscardProgressAndPurge_RefusesWhileRepairPending(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	require.NoError(t, store.SetRaw(merge.PendingUserStateRepairPrefix+"held:other", []byte(`{"loser_book_id":"held","winner_book_id":"other"}`)))

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, ErrDiscardRefused)
	require.False(t, bookGone(t, store, "held"))
	require.Equal(t, 40, userPct(t, store, u.ID, "held"))
}

// A book that is not in the trash is refused as such even when no activity
// log is wired: the caller learns the real reason (409), not "no audit" (503).
func TestDiscardProgressAndPurge_NotInTrashBeforeAuditCheck(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live", Format: "m4b"})
	require.NoError(t, err)

	_, err = svc.DiscardProgressAndPurge(context.Background(), "live", "owner")
	require.ErrorIs(t, err, ErrNotInTrash)
}

// restoreRaceStore restores the book (out of the trash) right after the
// first read of it, as a restore landing between the service's first check
// and the merge lock would.
type restoreRaceStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string
	mu     sync.Mutex
	reads  int
}

func (s *restoreRaceStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id != s.bookID {
		return b, err
	}
	s.mu.Lock()
	s.reads++
	first := s.reads == 1
	s.mu.Unlock()
	if first {
		_, merr := s.PebbleStore.ModifyBook(id, func(row *database.Book) error {
			row.MarkedForDeletion = new(false)
			row.MarkedForDeletionAt = nil
			row.LibraryState = new("organized")
			return nil
		})
		require.NoError(s.t, merr)
	}
	return b, err
}

// A restore that lands after the first check but before the merge lock wins:
// the re-check under the lock refuses, and the restored book keeps its row
// and its listener's progress.
func TestDiscardProgressAndPurge_RestoreMeanwhileWins(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	svc := NewAudiobookService(&restoreRaceStore{PebbleStore: store, t: t, bookID: "held"})
	rec := &fakeRecorder{}
	svc.auditOverride = rec

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, ErrNotInTrash)
	require.False(t, bookGone(t, store, "held"))
	require.Equal(t, 40, userPct(t, store, u.ID, "held"), "the restored book keeps its progress")
	pos, err := store.ListUserPositionsForBook(u.ID, "held")
	require.NoError(t, err)
	require.NotEmpty(t, pos)
	require.Empty(t, rec.entries)
}

// Only hidden copies exist (not primary; organized primary but quarantined):
// the state is never carried onto a book Audiobookshelf does not list. The
// book stays in the trash with its "has progress" tag, nothing moves.
func TestPurge_HiddenSiblingIsNotACarryTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hidden func(t *testing.T, store database.Store)
	}{
		{"not primary", func(t *testing.T, store database.Store) {
			groupBook(t, store, "hidden", "g1", false, false)
		}},
		{"quarantined primary", func(t *testing.T, store database.Store) {
			groupBook(t, store, "hidden", "g1", false, true)
			_, err := store.ModifyBook("hidden", func(b *database.Book) error {
				now := time.Now()
				b.QuarantinedAt, b.QuarantineReason = &now, new("test")
				return nil
			})
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, _ := setupPurgeBoundary(t)
			tc.hidden(t, store)
			groupBook(t, store, "dup", "g1", true, false)
			u := purgeSeedProgress(t, store, "dup")

			res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
			require.NoError(t, err)
			require.Equal(t, 1, res.KeptHasProgress, "result %+v", res)
			require.Zero(t, res.CarriedToSibling)
			require.Zero(t, res.CarryFailed)
			require.False(t, bookGone(t, store, "dup"))
			require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
			require.Zero(t, userPct(t, store, u.ID, "hidden"), "nothing reached the hidden copy")
			info, err := svc.TrashProgress(context.Background(), []string{"dup"})
			require.NoError(t, err)
			require.True(t, info["dup"].HasProgress, "the tag is still shown")
		})
	}
}

// unlistAfterChoiceStore drops the sibling out of the ABS listing (no longer
// primary) right after the purge reads the version group, as a primary
// hand-off landing between the choice and the carry would.
type unlistAfterChoiceStore struct {
	*database.PebbleStore
	t       *testing.T
	sibling string
}

func (s *unlistAfterChoiceStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	members, err := s.PebbleStore.GetBooksByVersionGroup(gid)
	_, merr := s.PebbleStore.ModifyBook(s.sibling, func(b *database.Book) error {
		b.IsPrimaryVersion = new(false)
		return nil
	})
	require.NoError(s.t, merr)
	return members, err
}

// The sibling stops being listed after it was chosen: the re-check under the
// merge lock refuses, nothing is carried, and the book is kept as
// kept_has_progress.
func TestPurge_SiblingUnlistedBeforeCarryKeepsBook(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&unlistAfterChoiceStore{PebbleStore: store, t: t, sibling: "keep"})

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.KeptHasProgress, "result %+v", res)
	require.Zero(t, res.CarriedToSibling)
	require.Zero(t, res.Purged)
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
	require.Zero(t, userPct(t, store, u.ID, "keep"))
	tomb, err := store.GetBookTombstone("dup")
	require.NoError(t, err)
	require.Nil(t, tomb, "no purge side effect ran")
}
