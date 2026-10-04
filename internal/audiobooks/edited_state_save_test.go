// file: internal/audiobooks/edited_state_save_test.go
// version: 1.0.0
// guid: 45968a65-7a57-4c9e-b71b-7ee203c64c8a
// last-edited: 2026-10-03

package audiobooks

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A person's edit saves only what it changed, onto the rows as they stand:
// a repair lock written on another field after the edit read its state
// survives, a field the edit changed becomes the person's (no repair
// source), and a field the edit removed is deleted.
func TestSaveEditedMetadataState_AppliesOnlyTheEdit(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	svc := NewAudiobookService(store)
	src := database.RepairLockSource("op-1")
	for _, st := range []database.MetadataFieldState{
		{BookID: "b1", Field: "title", OverrideLocked: true, LockSource: src},
		{BookID: "b1", Field: "publisher", OverrideLocked: true},
	} {
		st := st
		require.NoError(t, store.UpsertMetadataFieldState(&st))
	}
	loaded, err := svc.loadMetadataState("b1")
	require.NoError(t, err)
	edited := copyMetadataState(loaded)
	// The person unlocks the title and clears the publisher.
	e := edited["title"]
	e.OverrideLocked = false
	edited["title"] = e
	delete(edited, "publisher")
	// Meanwhile a repair locks the author.
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "author_name",
		OverrideLocked: true, LockSource: src}))

	require.NoError(t, svc.saveEditedMetadataState("b1", loaded, edited))
	rows, err := store.GetMetadataFieldStates("b1")
	require.NoError(t, err)
	by := map[string]database.MetadataFieldState{}
	for _, r := range rows {
		by[r.Field] = r
	}
	require.False(t, by["title"].OverrideLocked)
	require.Empty(t, by["title"].LockSource)
	_, hasPublisher := by["publisher"]
	require.False(t, hasPublisher, "the cleared field is deleted")
	require.True(t, by["author_name"].IsRepairLock(), "a lock the edit never saw survives its save")
}
