// file: internal/audiobooks/purge_merge_lock_test.go
// version: 1.0.0
// guid: e44858b2-5180-4f51-9842-e61721722f2f
// last-edited: 2026-10-18

package audiobooks

import (
	"context"
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

	mu                    sync.Mutex
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
