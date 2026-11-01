// file: internal/metafetch/claim_lock_race_test.go
// version: 1.0.0
// guid: 7e4a1c52-93d8-4b6f-a0c5-2f81d6b3e947
// last-edited: 2026-10-30

package metafetch

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// raceStore wraps a Pebble store with two injectable steps around the
// hand-picked apply: afterFirstStateRead runs once, right after the apply's
// first GetMetadataFieldStates returns (the lock read, outside any stripe),
// and afterModify runs right after ModifyBook returns (the old claim point,
// now still inside the commit's hold of the field-state stripe).
type raceStore struct {
	*database.PebbleStore
	mu                  sync.Mutex
	reads               int
	afterFirstStateRead func()
	afterModify         func()
}

func (r *raceStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	rows, err := r.PebbleStore.GetMetadataFieldStates(bookID)
	r.mu.Lock()
	r.reads++
	first := r.reads == 1
	r.mu.Unlock()
	if first && r.afterFirstStateRead != nil {
		r.afterFirstStateRead()
	}
	return rows, err
}

func (r *raceStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	b, err := r.PebbleStore.ModifyBook(id, fn)
	if err == nil && r.afterModify != nil {
		r.afterModify()
	}
	return b, err
}

// liftRepairLock is the revert's lock lift (audiobooks.RevertService.
// revertFieldLock): under the field-state stripe, a repair lock goes back to
// unlocked; any other row is refused (undo.CheckFieldLockCurrent). It reports
// whether it lifted.
func liftRepairLock(st *database.PebbleStore, bookID, field string, entered chan<- struct{}) (bool, error) {
	unlock := database.LockMetadataState(bookID)
	defer unlock()
	if entered != nil {
		close(entered)
	}
	rows, err := st.GetMetadataFieldStates(bookID)
	if err != nil {
		return false, err
	}
	for i := range rows {
		if rows[i].Field == field && rows[i].IsRepairLock() {
			rows[i].OverrideLocked, rows[i].LockSource = false, ""
			return true, st.UpsertMetadataFieldState(&rows[i])
		}
	}
	return false, nil
}

func repairLockedTitleBook(t *testing.T) (*raceStore, string) {
	t.Helper()
	st := snapshotStore(t)
	book, err := st.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: "/library/year-one.m4b"})
	require.NoError(t, err)
	require.NoError(t, repairLock(st, book.ID, database.FieldKeyTitle, "op-repair"))
	return &raceStore{PebbleStore: st}, book.ID
}

func applyTitle(t *testing.T, store *raceStore, id string) *database.Book {
	t.Helper()
	cand := MetadataCandidate{Title: "Batman: The Long Halloween", Source: "Audible"}
	_, err := NewService(store).ApplyMetadataCandidate(id, cand, []string{"title"})
	require.NoError(t, err)
	got, err := store.GetBookByID(id)
	require.NoError(t, err)
	return got
}

// A revert lifting the repair's lock after the apply read the locks and
// before it committed leaves nothing for the claim to clear. The apply then
// locks the title as the person's: their new value is never left unlocked.
func TestApplyMetadataCandidate_RevertLiftBeforeCommitRelocksAsPerson(t *testing.T) {
	store, id := repairLockedTitleBook(t)
	var lifted bool
	store.afterFirstStateRead = func() {
		var err error
		lifted, err = liftRepairLock(store.PebbleStore, id, database.FieldKeyTitle, nil)
		require.NoError(t, err)
	}
	got := applyTitle(t, store, id)
	require.True(t, lifted, "the revert's lift must land inside the apply")
	assert.Equal(t, "Batman: The Long Halloween", got.Title)
	row := stateRow(t, store.PebbleStore, id, database.FieldKeyTitle)
	require.NotNil(t, row)
	assert.True(t, row.OverrideLocked, "the person's new title must be locked")
	assert.Empty(t, row.LockSource, "and locked as the person, not as a repair")
}

// The interleave CLAIM-LOCK-RACE names: a revert's lift arriving between the
// commit and the claim. The claim now runs inside the commit's hold of the
// stripe, so the lift waits, then finds the person's lock and lifts nothing.
func TestApplyMetadataCandidate_RevertLiftAfterCommitWaitsForTheClaim(t *testing.T) {
	store, id := repairLockedTitleBook(t)
	entered := make(chan struct{})
	done := make(chan struct{})
	var lifted bool
	var liftErr error
	store.afterModify = func() {
		go func() {
			defer close(done)
			lifted, liftErr = liftRepairLock(store.PebbleStore, id, database.FieldKeyTitle, entered)
		}()
		// The lift cannot enter the stripe while the apply still holds it.
		select {
		case <-entered:
			t.Error("the revert's lift took the field-state stripe between the commit and the claim")
		case <-time.After(150 * time.Millisecond):
		}
	}
	got := applyTitle(t, store, id)
	<-done
	require.NoError(t, liftErr)
	assert.False(t, lifted, "the lift ran after the claim and found no repair lock")
	assert.Equal(t, "Batman: The Long Halloween", got.Title)
	row := stateRow(t, store.PebbleStore, id, database.FieldKeyTitle)
	require.NotNil(t, row)
	assert.True(t, row.OverrideLocked)
	assert.Empty(t, row.LockSource, "the lock is the person's")
}
