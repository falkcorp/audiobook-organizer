// file: internal/metafetch/state_snapshot_test.go
// version: 1.3.0
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
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
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

// The stripe is held from the read through the save: a person's override
// (or a repair lock) landing while a snapshot is being modified waits, and
// survives the save. Before, a snapshot read outside the stripe deleted a
// newer override and resurrected a cleared one.
func TestWithStateSnapshot_HoldsTheStripeFromReadToSave(t *testing.T) {
	st := snapshotStore(t)
	fv := `"Prov"`
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "description",
		FetchedValue: &fv, UpdatedAt: time.Now()}))
	// The concurrent override targets a field the snapshot already holds:
	// without the stripe, the save's upsert of that field from the stale
	// snapshot would wipe it.
	done := make(chan error, 1)
	err := WithStateSnapshot(st, "b1", func(state map[string]MetadataFieldState) error {
		go func() { done <- database.RecordUserOverrides(st, "b1", map[string]any{"description": "Typed"}) }()
		time.Sleep(50 * time.Millisecond) // the writer is now waiting on the stripe
		require.Nil(t, stateRow(t, st, "b1", "description").OverrideValue, "the override must wait for the save")
		e := state["description"]
		e.FetchedValue = "newer"
		state["description"] = e
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, <-done)
	desc := stateRow(t, st, "b1", "description")
	require.NotNil(t, desc.OverrideValue, "the person's override written during the snapshot survives")
	assert.Equal(t, `"Typed"`, *desc.OverrideValue)
	assert.True(t, desc.OverrideLocked)
	assert.Equal(t, `"newer"`, *desc.FetchedValue)

	// A cleared override is not resurrected by a later save.
	mss := NewMetadataStateService(st)
	require.NoError(t, mss.SetOverride("b1", "publisher", "Typed Pub", true))
	require.NoError(t, mss.ClearOverride("b1", "publisher"))
	require.NoError(t, mss.UpdateFetchedMetadata("b1", map[string]any{"description": "newest"}))
	assert.Nil(t, stateRow(t, st, "b1", "publisher"))
}

// A person who unlocks, locks or edits a repair-locked field makes it
// theirs: the save writes what they did, with no repair source.
func TestWithStateSnapshot_PersonTakesOverARepairLock(t *testing.T) {
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
func TestWithStateSnapshot_RaceWithRepairLock(t *testing.T) {
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

// A person's override on a book still on the pre-migration blob migrates the
// blob first: the blob's own locks stay locks, and a later save keeps them.
func TestRecordUserOverrides_MigratesTheBlobFirst(t *testing.T) {
	st := snapshotStore(t)
	blob := `{"title":{"override_value":"Owner Title","override_locked":true,"updated_at":"2026-01-01T00:00:00Z"}}`
	require.NoError(t, st.SetUserPreference(metastate.Key("b1"), blob))
	require.NoError(t, database.RecordUserOverrides(st, "b1", map[string]any{"narrator": "N"}))
	locked, err := database.LockedUserFields(st, "b1")
	require.NoError(t, err)
	assert.True(t, locked["title"], "the blob's title lock survives the first override row")
	assert.True(t, locked["narrator"])
	require.NoError(t, NewService(st).updateFetchedMetadataState("b1", map[string]any{"description": "d"}))
	locked, err = database.LockedUserFields(st, "b1")
	require.NoError(t, err)
	assert.True(t, locked["title"], "and a later snapshot save keeps it")
}

// A hand-picked apply whose candidate agrees with the repaired value leaves
// the repair's lock the repair's: only a field whose value changed is claimed.
func TestApplyMetadataCandidate_ClaimsOnlyChangedFields(t *testing.T) {
	store, updated := candidateStore(t)
	store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{BookID: "b-locks", Field: database.FieldKeyTitle, OverrideLocked: true,
			LockSource: database.RepairLockSource("op-1")}}, nil
	}
	var claimed []database.MetadataFieldState
	store.UpsertMetadataFieldStateFunc = func(st *database.MetadataFieldState) error {
		if st.Field == database.FieldKeyTitle {
			claimed = append(claimed, *st)
		}
		return nil
	}
	cand := candidateFor("")
	cand.Title = curatedBook().Title
	_, err := NewService(store).ApplyMetadataCandidate("b-locks", cand, nil)
	require.NoError(t, err)
	require.NotNil(t, updated())
	require.Equal(t, curatedBook().Title, updated().Title)
	for _, c := range claimed {
		assert.NotEmpty(t, c.LockSource, "an unchanged title must not have its repair lock claimed: %+v", c)
	}
}
