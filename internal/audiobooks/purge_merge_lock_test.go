// file: internal/audiobooks/purge_merge_lock_test.go
// version: 1.2.0
// guid: e44858b2-5180-4f51-9842-e61721722f2f
// last-edited: 2026-10-18

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// mergeLockHeldByOther reports whether some other goroutine holds the merge
// lock right now: it tries to take the lock from a fresh goroutine and waits a
// moment. A goroutine left blocked takes and releases the lock once the holder
// lets go.
func mergeLockHeldByOther() bool {
	got := make(chan struct{})
	go func() {
		merge.LockMergeRMW()
		close(got)
		merge.UnlockMergeRMW()
	}()
	select {
	case <-got:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// lockWatchStore records, for the doomed book, whether the merge lock was
// held when the purge read its listening positions (the state check) and when
// it deleted the row.
type lockWatchStore struct {
	*database.PebbleStore
	doomed string

	mu                     sync.Mutex
	checkLocked, delLocked []bool
}

func (s *lockWatchStore) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	if bookID == s.doomed {
		held := mergeLockHeldByOther()
		s.mu.Lock()
		s.checkLocked = append(s.checkLocked, held)
		s.mu.Unlock()
	}
	return s.PebbleStore.ListUserPositionsForBook(userID, bookID)
}

func (s *lockWatchStore) DeleteBook(id string) error {
	if id == s.doomed {
		held := mergeLockHeldByOther()
		s.mu.Lock()
		s.delLocked = append(s.delLocked, held)
		s.mu.Unlock()
	}
	return s.PebbleStore.DeleteBook(id)
}

// SF4 (#4064 round 3): the purge's listening-state check and its delete of a
// book with no state run in ONE hold of the merge lock. A merge revert puts a
// loser's state back on its trashed row under that lock; checked outside it,
// the purge could read "no state", lose the race to the revert, and delete
// the row the revert had just put a user's progress on.
func TestPurge_StateCheckAndDeleteHoldTheMergeLock(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "doomed", "")
	_, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)

	ws := &lockWatchStore{PebbleStore: store, doomed: "doomed"}
	res, err := NewAudiobookService(ws).PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Purged, "result %+v", res)

	ws.mu.Lock()
	defer ws.mu.Unlock()
	require.NotEmpty(t, ws.checkLocked, "the state check read the book's positions")
	for _, held := range ws.checkLocked {
		require.True(t, held, "the state check runs under the merge lock")
	}
	require.Equal(t, []bool{true}, ws.delLocked, "the delete runs in the same hold of the merge lock")
	b, err := store.GetBookByID("doomed")
	require.NoError(t, err)
	require.Nil(t, b)
}

// restoredMeanwhileStore restores the doomed book from the trash during the
// purge's file-row pre-check (step 0, before the merge lock), as a restore or
// a merge revert running at that moment would.
//
// With onSiblingLookup set it restores the book later instead: when the carry
// path looks up the version group for a sibling, after the stateless check's
// hold of the lock and before the carry's, so only the carry's own re-read
// can see it.
type restoredMeanwhileStore struct {
	*database.PebbleStore
	doomed          string
	onSiblingLookup bool
	once            sync.Once
}

func (s *restoredMeanwhileStore) restore() {
	s.once.Do(func() {
		_, _ = s.PebbleStore.ModifyBook(s.doomed, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt = nil, nil
			return nil
		})
	})
}

func (s *restoredMeanwhileStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	if bookID == s.doomed && !s.onSiblingLookup {
		s.restore()
	}
	return s.PebbleStore.GetBookFiles(bookID)
}

func (s *restoredMeanwhileStore) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	if s.onSiblingLookup {
		s.restore()
	}
	return s.PebbleStore.GetBooksByVersionGroup(groupID)
}

// SF4 follow-up: the purge re-reads the book under the merge lock. One that
// was restored between the listing and the lock is left alone: with no file
// rows and no listening state, DeleteBook would not have refused it, and the
// purge hard-deleted a book that had just come back.
func TestPurge_BookRestoredMeanwhileIsNotPurged(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "back", "")
	res, err := NewAudiobookService(&restoredMeanwhileStore{PebbleStore: store, doomed: "back"}).
		PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Zero(t, res.Purged, "result %+v", res)
	require.Empty(t, res.Errors, "a book restored meanwhile is not an error")
	require.False(t, bookGone(t, store, "back"), "the restored book is still there")
}

// The carry path re-reads the doomed book under its own hold of the lock: a
// book with listening state restored after the stateless check is neither
// carried from nor deleted.
func TestPurge_BookWithStateRestoredMeanwhileIsNotCarried(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	res, err := NewAudiobookService(&restoredMeanwhileStore{PebbleStore: store, doomed: "dup", onSiblingLookup: true}).
		PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Zero(t, res.Purged, "result %+v", res)
	require.Zero(t, res.CarriedToSibling)
	require.Zero(t, res.CarryFailed)
	require.Empty(t, res.Errors)
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"), "the progress stays on the restored book")
}

// "Purge now" on a book restored meanwhile answers ErrNotInTrash.
func TestPurgeNow_BookRestoredMeanwhileIsNotInTrash(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "back", "")
	svc := NewAudiobookService(&restoredMeanwhileStore{PebbleStore: store, doomed: "back"})
	_, err := svc.DeleteAudiobook(context.Background(), "back", &DeleteAudiobookOptions{})
	require.True(t, errors.Is(err, ErrNotInTrash), "err = %v", err)
	require.False(t, bookGone(t, store, "back"))
}

// Round-4 S1 (#4064): the stateless purge's hold of the merge lock is not
// counted in merge.MergeLockAcquisitions. Counted once per trashed book, it
// made the chapter-fragment fixer list the whole library again (~460 MB on
// prod) before nearly every row of an apply running beside a purge. The
// hold is still exclusive (TestPurge_StateCheckAndDeleteHoldTheMergeLock);
// only the count is skipped, because all it writes is the book delete the
// book change log records.
func TestPurge_StatelessHoldsAreNotCounted(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	const n = 25
	for i := 0; i < n; i++ {
		softDeleted(t, store, fmt.Sprintf("doomed-%02d", i), "")
	}
	before := merge.MergeLockAcquisitions()
	res, err := NewAudiobookService(store).PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, n, res.Purged, "result %+v", res)
	require.Zero(t, merge.MergeLockAcquisitions()-before, "a stateless purge adds no counted hold")
	for i := 0; i < n; i++ {
		require.True(t, bookGone(t, store, fmt.Sprintf("doomed-%02d", i)))
	}
}
