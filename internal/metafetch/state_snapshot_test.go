// file: internal/metafetch/state_snapshot_test.go
// version: 1.1.0
// guid: 0b9d0f57-6fc7-45ea-9c21-992a7ba92708
// last-edited: 2026-10-03

package metafetch

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func snapshotStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func stateRow(t *testing.T, st *database.PebbleStore, bookID, field string) *database.MetadataFieldState {
	t.Helper()
	rows, err := st.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	for i := range rows {
		if rows[i].Field == field {
			return &rows[i]
		}
	}
	return nil
}

func repairLock(st *database.PebbleStore, bookID, field, op string) error {
	unlock := database.LockMetadataState(bookID)
	defer unlock()
	rows, err := st.GetMetadataFieldStates(bookID)
	if err != nil {
		return err
	}
	row := database.MetadataFieldState{BookID: bookID, Field: field}
	for i := range rows {
		if rows[i].Field == field {
			row = rows[i]
		}
	}
	row.OverrideLocked, row.LockSource = true, database.RepairLockSource(op)
	return st.UpsertMetadataFieldState(&row)
}

// A snapshot read before a repair lock and saved after it keeps the lock: the
// caller never saw it and did not change that field's lock. A repair lock on a
// field the snapshot does not hold is kept too.
func TestSaveStateSnapshot_KeepsLockWrittenSinceTheRead(t *testing.T) {
	st := snapshotStore(t)
	fv := `"Provider Title"`
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "title",
		FetchedValue: &fv, UpdatedAt: time.Now()}))
	rows, err := st.GetMetadataFieldStates("b1")
	require.NoError(t, err)
	stale := StateFromRows(rows)

	require.NoError(t, repairLock(st, "b1", "title", "op-1"))
	require.NoError(t, repairLock(st, "b1", "author_name", "op-1"))

	e := stale["title"]
	e.FetchedValue = "Newer Provider Title"
	stale["title"] = e
	require.NoError(t, SaveStateSnapshot(st, "b1", stale))

	title := stateRow(t, st, "b1", "title")
	require.NotNil(t, title)
	assert.True(t, title.IsRepairLock(), "the stale snapshot must not erase the repair lock")
	assert.Equal(t, `"Newer Provider Title"`, *title.FetchedValue)
	author := stateRow(t, st, "b1", "author_name")
	require.NotNil(t, author, "a repair lock row the snapshot never read is not deleted")
	assert.True(t, author.IsRepairLock())
}

// A person who unlocks, locks or edits a repair-locked field makes it
// theirs: the save writes what they did, with no repair source.
func TestSaveStateSnapshot_PersonTakesOverARepairLock(t *testing.T) {
	st := snapshotStore(t)
	require.NoError(t, repairLock(st, "b1", "title", "op-1"))
	require.NoError(t, repairLock(st, "b1", "narrator", "op-1"))
	require.NoError(t, repairLock(st, "b1", "publisher", "op-1"))
	mss := NewMetadataStateService(st)

	require.NoError(t, mss.UnlockOverride("b1", "title"))
	title := stateRow(t, st, "b1", "title")
	require.NotNil(t, title)
	assert.False(t, title.OverrideLocked)
	assert.Empty(t, title.LockSource)

	require.NoError(t, mss.SetOverride("b1", "narrator", "Typed Narrator", true))
	narr := stateRow(t, st, "b1", "narrator")
	assert.True(t, narr.OverrideLocked)
	assert.Empty(t, narr.LockSource, "a person's value makes the lock theirs")
	assert.False(t, narr.IsRepairLock())

	require.NoError(t, mss.ClearOverride("b1", "publisher"))
	assert.Nil(t, stateRow(t, st, "b1", "publisher"), "a cleared field is deleted, repair lock or not")
}

// The snapshot save and a repair lock race on one book: every lock written
// must survive every concurrent save. Run with -race.
func TestSaveStateSnapshot_RaceWithRepairLock(t *testing.T) {
	st := snapshotStore(t)
	svc := NewService(st)
	fields := []string{"title", "author_name", "narrator", "publisher", "language", "genre", "isbn10", "isbn13"}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			assert.NoError(t, svc.updateFetchedMetadataState("b1", map[string]any{"description": i}))
		}
	}()
	go func() {
		defer wg.Done()
		for _, f := range fields {
			assert.NoError(t, repairLock(st, "b1", f, "op-race"))
		}
	}()
	wg.Wait()
	for _, f := range fields {
		row := stateRow(t, st, "b1", f)
		require.NotNil(t, row, f)
		assert.True(t, row.IsRepairLock(), "%s lost its lock to a concurrent snapshot save", f)
	}
}

// A metadata apply a person picked by hand may overwrite a field only a
// repair locked; an automatic apply may not, and a person's lock holds both.
func TestApplyMetadataCandidate_RepairLockYieldsToHandPickedApply(t *testing.T) {
	repairRows := func(src string) []database.MetadataFieldState {
		return []database.MetadataFieldState{{BookID: "b-locks", Field: database.FieldKeyTitle, OverrideLocked: true, LockSource: src}}
	}
	t.Run("hand-picked overwrites a repair lock", func(t *testing.T) {
		store, updated := candidateStore(t)
		store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
			return repairRows(database.RepairLockSource("op-1")), nil
		}
		var claimed []database.MetadataFieldState
		store.UpsertMetadataFieldStateFunc = func(st *database.MetadataFieldState) error {
			if st.Field == database.FieldKeyTitle {
				claimed = append(claimed, *st)
			}
			return nil
		}
		resp, err := NewService(store).ApplyMetadataCandidate("b-locks", candidateFor(""), nil)
		require.NoError(t, err)
		require.NotNil(t, updated())
		assert.NotEqual(t, curatedBook().Title, updated().Title)
		assert.NotContains(t, resp.SkippedLockedFields, database.FieldKeyTitle)
		// The lock becomes the person's: still locked, no repair source.
		require.NotEmpty(t, claimed)
		assert.True(t, claimed[0].OverrideLocked)
		assert.Empty(t, claimed[0].LockSource)
	})
	t.Run("automatic apply honours a repair lock", func(t *testing.T) {
		store, updated := candidateStore(t)
		store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
			return repairRows(database.RepairLockSource("op-1")), nil
		}
		resp, err := NewService(store).ApplyMetadataCandidateWithOptions("b-locks", candidateFor(""), nil, ApplyOptions{UnseenCandidate: true})
		require.NoError(t, err)
		require.NotNil(t, updated())
		assert.Equal(t, curatedBook().Title, updated().Title)
		assert.Contains(t, resp.SkippedLockedFields, database.FieldKeyTitle)
	})
	t.Run("hand-picked honours a person's lock", func(t *testing.T) {
		store, updated := candidateStore(t)
		store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
			return repairRows(""), nil
		}
		resp, err := NewService(store).ApplyMetadataCandidate("b-locks", candidateFor(""), nil)
		require.NoError(t, err)
		require.NotNil(t, updated())
		assert.Equal(t, curatedBook().Title, updated().Title)
		assert.Contains(t, resp.SkippedLockedFields, database.FieldKeyTitle)
	})
	t.Run("scanner-style ApplyMetadataToBook honours a repair lock", func(t *testing.T) {
		store := lockStore(t)
		store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
			return repairRows(database.RepairLockSource("op-1")), nil
		}
		book := curatedBook()
		skipped, err := NewService(store).ApplyMetadataToBook(book, fetchedMetaFor(""))
		require.NoError(t, err)
		assert.Equal(t, curatedBook().Title, book.Title)
		assert.Contains(t, skipped, database.FieldKeyTitle)
	})
}
