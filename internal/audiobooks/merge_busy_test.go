// file: internal/audiobooks/merge_busy_test.go
// version: 2.0.0
// guid: 0c4f7a19-3e8d-4b52-a6d1-7e92b5c08f34
// last-edited: 2026-10-22

package audiobooks

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// holdMergeLock simulates a running combine (which holds the merge lock for
// its whole run) and shortens the interactive wait so the test is quick. It
// returns the release function (also registered as cleanup).
func holdMergeLock(t *testing.T) (release func()) {
	t.Helper()
	prev := merge.InteractiveMergeLockWait
	merge.InteractiveMergeLockWait = 50 * time.Millisecond
	merge.LockMergeRMW()
	released := false
	release = func() {
		if !released {
			released = true
			merge.UnlockMergeRMW()
		}
	}
	t.Cleanup(func() {
		release()
		merge.InteractiveMergeLockWait = prev
	})
	return release
}

// mergeLockGateBound is how long a test waits for an interactive action that
// should have answered at once before it calls the gate missing.
const mergeLockGateBound = 2 * time.Second

// within runs fn in a goroutine and fails the test, from the test goroutine,
// when fn has not returned inside mergeLockGateBound. fn must not call t: its
// results go into variables the test reads after within returns. A fn left
// blocked by a failure is released by the cleanup that frees the held lock.
func within(t *testing.T, failure string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(mergeLockGateBound):
		t.Fatalf("%s", failure)
	}
}

const gateMissing = "blocked on the merge lock: gate missing"
const lockRetaken = "did not return: blocked on the merge lock (a ...Locked path takes it again, or the gate is missing)"

// purgeNowUnderHold is purgeNowLocked called the way its handlers do, under one
// bounded hold of the merge lock.
func purgeNowUnderHold(t *testing.T, svc *AudiobookService, book *database.Book) (map[string]any, error) {
	t.Helper()
	unlock, err := merge.TryLockMergeRMWWithin(time.Second)
	require.NoError(t, err)
	defer unlock()
	return svc.purgeNowLocked(book)
}

// requireLockFree fails unless the merge lock can be taken at once: an action
// that returned must have released it.
func requireLockFree(t *testing.T, what string) {
	t.Helper()
	unlock, err := merge.TryLockMergeRMWWithin(time.Second)
	require.NoError(t, err, "%s left the merge lock held", what)
	unlock()
}

// The four interactive actions that run under the merge lock (restore, the
// single hard delete of a live or a trashed book, "Purge now", "Discard
// progress and purge") hung for a combine's whole run. Each takes the lock with
// a bound and answers merge.ErrMergeBusy, changing nothing. A missing gate
// shows as a call that never returns, which fails within 2s.
func TestInteractiveActions_AnswerMergeBusyWhileACombineRuns(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "trashed", "")
	softDeleted(t, store, "discard", "")
	svc.auditOverride = &fakeRecorder{}
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live"})
	require.NoError(t, err)
	release := holdMergeLock(t)

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"restore", func() error { _, err := svc.RestoreAudiobook(context.Background(), "trashed"); return err }},
		{"hard delete of a trashed book (Purge now)", func() error {
			_, err := svc.DeleteAudiobook(context.Background(), "trashed", &DeleteAudiobookOptions{})
			return err
		}},
		{"hard delete of a live book", func() error {
			_, err := svc.DeleteAudiobook(context.Background(), "live", &DeleteAudiobookOptions{})
			return err
		}},
		{"discard progress and purge", func() error {
			_, err := svc.DiscardProgressAndPurge(context.Background(), "discard", "owner")
			return err
		}},
	} {
		var got error
		within(t, tc.name+" "+gateMissing, func() { got = tc.run() })
		assert.ErrorIs(t, got, merge.ErrMergeBusy, tc.name)
	}

	assert.False(t, bookGone(t, store, "trashed"), "a busy answer must not purge")
	assert.False(t, bookGone(t, store, "live"))
	assert.False(t, bookGone(t, store, "discard"))
	b, _ := store.GetBookByID("trashed")
	require.NotNil(t, b)
	assert.True(t, b.IsSoftDeleted(), "a busy answer must not restore")

	// Once the combine is done the same calls go through.
	release()
	_, err = svc.RestoreAudiobook(context.Background(), "trashed")
	require.NoError(t, err)
	b, _ = store.GetBookByID("trashed")
	require.NotNil(t, b)
	assert.False(t, b.IsSoftDeleted())
}

// With the lock free every action takes it ONCE and runs under that hold. The
// lock is not reentrant, so an action whose body still reached a function that
// takes it (a ...Locked variant swapped for its locking twin, or the purge's
// own per-book hold) deadlocks on itself: that shows as a call that does not
// return within 2s. Each action must also leave the lock free.
func TestInteractiveActions_RunUnderOneHoldAndReleaseIt(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "restore-me", "")
	softDeleted(t, store, "purge-me", "")
	softDeleted(t, store, "discard-me", "")
	svc.auditOverride = &fakeRecorder{}
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live"})
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"restore", func() error { _, err := svc.RestoreAudiobook(context.Background(), "restore-me"); return err }},
		{"purge now", func() error {
			_, err := svc.DeleteAudiobook(context.Background(), "purge-me", &DeleteAudiobookOptions{})
			return err
		}},
		{"hard delete of a live book", func() error {
			_, err := svc.DeleteAudiobook(context.Background(), "live", &DeleteAudiobookOptions{})
			return err
		}},
		{"discard progress and purge", func() error {
			_, err := svc.DiscardProgressAndPurge(context.Background(), "discard-me", "owner")
			return err
		}},
	} {
		var got error
		within(t, tc.name+" "+lockRetaken, func() { got = tc.run() })
		require.NoError(t, got, tc.name)
		requireLockFree(t, tc.name)
	}
	for _, id := range []string{"purge-me", "live", "discard-me"} {
		assert.True(t, bookGone(t, store, id), "%s should be gone", id)
	}
	b, _ := store.GetBookByID("restore-me")
	require.NotNil(t, b)
	assert.False(t, b.IsSoftDeleted(), "restore-me should be restored")
}

// A hard delete branches on the row as it is UNDER the lock, not on the one read
// before it: a book restored (or trashed) while the handler waited for the
// lock must take the delete path its present state calls for.
func TestDeleteAudiobook_HardDeleteBranchesOnTheRowReadUnderTheLock(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "raced", "")
	// The row is trashed when the handler first reads it. A concurrent restore
	// (which takes the merge lock) lands while it waits: the test holds the lock,
	// starts the delete, restores the book through the real restore, then
	// releases.
	prev := merge.InteractiveMergeLockWait
	merge.InteractiveMergeLockWait = 5 * time.Second
	t.Cleanup(func() { merge.InteractiveMergeLockWait = prev })
	unlock, err := merge.TryLockMergeRMWWithin(time.Second)
	require.NoError(t, err)
	var once sync.Once
	release := func() { once.Do(unlock) }
	t.Cleanup(release)

	var delErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, delErr = svc.DeleteAudiobook(context.Background(), "raced", &DeleteAudiobookOptions{})
	}()
	time.Sleep(100 * time.Millisecond) // the delete has read the trashed row and is waiting for the lock
	_, err = store.ModifyBook("raced", func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = nil, nil
		return nil
	})
	require.NoError(t, err)
	release()
	select {
	case <-done:
	case <-time.After(mergeLockGateBound):
		t.Fatalf("DeleteAudiobook %s", lockRetaken)
	}
	// Restored while it waited, the row is live, so the live hard-delete path
	// ran (the trashed path would have refused it as not in the trash).
	require.NoError(t, delErr)
	assert.True(t, bookGone(t, store, "raced"))
}

// The batch endpoint is a synchronous request too: its restores and hard
// deletes wait once (not once per item) for a running combine, then every
// lock-taking item answers merge.ErrMergeBusy and nothing is changed.
func TestBatchOperations_AnswerMergeBusyOncePerBatchWhileACombineRuns(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		softDeleted(t, store, id, "")
	}
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live"})
	require.NoError(t, err)
	bs := batch.NewBatchService(store, batch.WithTrashPurger(svc))
	holdMergeLock(t)
	merge.InteractiveMergeLockWait = 300 * time.Millisecond

	start := time.Now()
	var resp *batch.BatchResponse
	within(t, "batch "+gateMissing, func() {
		resp = bs.ExecuteOperations(&batch.BatchOperationsRequest{Operations: []batch.BatchOperationItem{
			{ID: "t1", Action: "delete", HardDelete: true},
			{ID: "t2", Action: "delete", HardDelete: true},
			{ID: "t3", Action: "restore"},
			{ID: "live", Action: "delete", HardDelete: true},
			{ID: "t4", Action: "update", Updates: map[string]any{"marked_for_deletion": false}},
		}})
	})
	elapsed := time.Since(start)
	require.Len(t, resp.Results, 5)
	assert.Equal(t, 0, resp.Success, "%+v", resp)
	for _, r := range resp.Results {
		assert.Contains(t, r.Error, "merge or combine is running", "%+v", r)
	}
	assert.Less(t, elapsed, 800*time.Millisecond, "one bounded wait for the batch, not one per item")
	for _, id := range []string{"t1", "t2", "t3", "t4", "live"} {
		assert.False(t, bookGone(t, store, id), id)
	}
}

// One single-item batch per lock-taking action: with the lock held each answers
// ErrMergeBusy within 2s (a missing gate never returns); with it free each
// completes within 2s and leaves the lock free (a ...Locked path that takes the
// lock again never returns).
func TestBatchSingleItem_GatesAndRunsUnderOneHoldPerAction(t *testing.T) {
	type action struct {
		name  string
		setup func(t *testing.T, store *database.PebbleStore)
		run   func(bs *batch.BatchService) *batch.BatchResponse
		done  func(t *testing.T, store *database.PebbleStore)
	}
	restored := func(id string) func(t *testing.T, store *database.PebbleStore) {
		return func(t *testing.T, store *database.PebbleStore) {
			b, _ := store.GetBookByID(id)
			require.NotNil(t, b)
			assert.False(t, b.IsSoftDeleted(), "%s should be restored", id)
		}
	}
	gone := func(id string) func(t *testing.T, store *database.PebbleStore) {
		return func(t *testing.T, store *database.PebbleStore) {
			assert.True(t, bookGone(t, store, id), "%s should be gone", id)
		}
	}
	trashed := func(id string) func(t *testing.T, store *database.PebbleStore) {
		return func(t *testing.T, store *database.PebbleStore) { softDeleted(t, store, id, "") }
	}
	live := func(id string) func(t *testing.T, store *database.PebbleStore) {
		return func(t *testing.T, store *database.PebbleStore) {
			_, err := store.CreateBook(&database.Book{ID: id, Title: id})
			require.NoError(t, err)
		}
	}
	ops := func(items ...batch.BatchOperationItem) func(bs *batch.BatchService) *batch.BatchResponse {
		return func(bs *batch.BatchService) *batch.BatchResponse {
			return bs.ExecuteOperations(&batch.BatchOperationsRequest{Operations: items})
		}
	}
	actions := []action{
		{"restore", trashed("b1"), ops(batch.BatchOperationItem{ID: "b1", Action: "restore"}), restored("b1")},
		{"update with marked_for_deletion=false", trashed("b2"),
			ops(batch.BatchOperationItem{ID: "b2", Action: "update", Updates: map[string]any{"marked_for_deletion": false}}), restored("b2")},
		{"bulk update with marked_for_deletion=false", trashed("b3"),
			func(bs *batch.BatchService) *batch.BatchResponse {
				return bs.UpdateAudiobooks(&batch.BatchUpdateRequest{IDs: []string{"b3"}, Updates: map[string]any{"marked_for_deletion": false}})
			}, restored("b3")},
		{"hard delete of a trashed book", trashed("b4"), ops(batch.BatchOperationItem{ID: "b4", Action: "delete", HardDelete: true}), gone("b4")},
		{"hard delete of a live book", live("b5"), ops(batch.BatchOperationItem{ID: "b5", Action: "delete", HardDelete: true}), gone("b5")},
	}
	for _, a := range actions {
		t.Run(a.name+"/lock held answers merge busy", func(t *testing.T) {
			svc, store, _ := setupPurgeBoundary(t)
			a.setup(t, store)
			bs := batch.NewBatchService(store, batch.WithTrashPurger(svc))
			holdMergeLock(t)
			var resp *batch.BatchResponse
			within(t, a.name+" "+gateMissing, func() { resp = a.run(bs) })
			require.Len(t, resp.Results, 1, "%+v", resp)
			assert.Equal(t, 0, resp.Success, "%+v", resp)
			assert.Contains(t, resp.Results[0].Error, "merge or combine is running", "%+v", resp)
		})
		t.Run(a.name+"/lock free runs under one hold", func(t *testing.T) {
			svc, store, _ := setupPurgeBoundary(t)
			a.setup(t, store)
			bs := batch.NewBatchService(store, batch.WithTrashPurger(svc))
			var resp *batch.BatchResponse
			within(t, a.name+" "+lockRetaken, func() { resp = a.run(bs) })
			require.Len(t, resp.Results, 1, "%+v", resp)
			require.Equal(t, 1, resp.Success, "%+v", resp)
			requireLockFree(t, fmt.Sprintf("batch %s", a.name))
			a.done(t, store)
		})
	}
}
