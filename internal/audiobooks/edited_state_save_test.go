// file: internal/audiobooks/edited_state_save_test.go
// version: 1.1.0
// guid: 45968a65-7a57-4c9e-b71b-7ee203c64c8a
// last-edited: 2026-10-03

package audiobooks

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

	_, err = svc.saveEditedMetadataState("b1", loaded, edited)
	require.NoError(t, err)
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

func editedStateFixture(t *testing.T) (*database.PebbleStore, *AudiobookService) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, NewAudiobookService(store)
}

func fieldStatesByField(t *testing.T, store *database.PebbleStore, bookID string) map[string]database.MetadataFieldState {
	t.Helper()
	rows, err := store.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	by := map[string]database.MetadataFieldState{}
	for _, r := range rows {
		by[r.Field] = r
	}
	return by
}

func jsonValue(t *testing.T, v any) *string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	s := string(raw)
	return &s
}

// A person locks the title while a fetch records a new provider title for the
// same field: the save writes only the lock (the sub-field the edit changed),
// so the provider value the fetch wrote survives. Until 2026-10-03 the save
// wrote the edit's whole entry and the title ended with the stale "Provider
// A" (the adversarial review's TestProbe_SameFieldFetchedValueLost).
func TestSaveEditedMetadataState_SameFieldConcurrentFetchedValueSurvives(t *testing.T) {
	store, svc := editedStateFixture(t)
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "title",
		FetchedValue: jsonValue(t, "Provider A")}))
	loaded, err := svc.loadMetadataState("b1")
	require.NoError(t, err)
	edited := copyMetadataState(loaded)
	e := edited["title"]
	e.OverrideValue, e.OverrideLocked, e.UpdatedAt = "Mine", true, time.Now()
	edited["title"] = e

	// The concurrent fetch, after the edit read the state.
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "title",
		FetchedValue: jsonValue(t, "Provider B")}))

	before, err := svc.saveEditedMetadataState("b1", loaded, edited)
	require.NoError(t, err)
	require.Equal(t, `"Provider B"`, *fieldStatesByField(t, store, "b1")["title"].FetchedValue,
		"the fetch's provider value on the edited field was lost")
	state, err := svc.loadMetadataState("b1")
	require.NoError(t, err)
	require.Equal(t, "Mine", state["title"].OverrideValue)
	require.True(t, state["title"].OverrideLocked)
	require.Equal(t, "Provider B", before["title"].FetchedValue, "the returned state is the one read under the stripe")
}

// A row deleted after the edit read it is not brought back by a save that
// did not change it, nor by one that changed only a sub-field the deleter
// cleared: only the edit's own sub-field changes land, on an empty entry.
func TestSaveEditedMetadataState_ConcurrentlyDeletedRowIsNotResurrected(t *testing.T) {
	store, svc := editedStateFixture(t)
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "publisher",
		FetchedValue: jsonValue(t, "Tor")}))
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "genre",
		FetchedValue: jsonValue(t, "SF")}))
	loaded, err := svc.loadMetadataState("b1")
	require.NoError(t, err)
	edited := copyMetadataState(loaded)
	// publisher: only re-timestamped by the edit. genre: the person locks it.
	p := edited["publisher"]
	p.UpdatedAt = time.Now().Add(time.Hour)
	edited["publisher"] = p
	g := edited["genre"]
	g.OverrideLocked = true
	edited["genre"] = g

	require.NoError(t, store.DeleteMetadataFieldState("b1", "publisher"))
	require.NoError(t, store.DeleteMetadataFieldState("b1", "genre"))

	_, err = svc.saveEditedMetadataState("b1", loaded, edited)
	require.NoError(t, err)
	by := fieldStatesByField(t, store, "b1")
	_, hasPublisher := by["publisher"]
	require.False(t, hasPublisher, "a deleted row the edit did not change came back")
	require.True(t, by["genre"].OverrideLocked, "the person's lock is kept")
	require.Nil(t, by["genre"].FetchedValue, "the deleted row's provider value came back with the lock")
}

// A timestamp-only difference is no change: it does not clobber a repair
// lock written on that field after the edit read it.
func TestSaveEditedMetadataState_TimestampOnlyChangeKeepsAConcurrentRepairLock(t *testing.T) {
	store, svc := editedStateFixture(t)
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "author_name",
		FetchedValue: jsonValue(t, "A")}))
	loaded, err := svc.loadMetadataState("b1")
	require.NoError(t, err)
	edited := copyMetadataState(loaded)
	a := edited["author_name"]
	a.UpdatedAt = time.Now().Add(time.Hour)
	edited["author_name"] = a
	src := database.RepairLockSource("op-9")
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "author_name",
		FetchedValue: jsonValue(t, "A"), OverrideLocked: true, LockSource: src}))

	_, err = svc.saveEditedMetadataState("b1", loaded, edited)
	require.NoError(t, err)
	require.True(t, fieldStatesByField(t, store, "b1")["author_name"].IsRepairLock(), "the repair lock was clobbered")
}

// A field the edit removed is deleted only while its row still holds what
// the edit read; a row changed since stays as it is.
func TestSaveEditedMetadataState_RemovedFieldChangedSinceIsKept(t *testing.T) {
	store, svc := editedStateFixture(t)
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "language",
		OverrideValue: jsonValue(t, "en"), OverrideLocked: true}))
	loaded, err := svc.loadMetadataState("b1")
	require.NoError(t, err)
	edited := copyMetadataState(loaded)
	delete(edited, "language")
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "b1", Field: "language",
		OverrideValue: jsonValue(t, "de"), OverrideLocked: true}))

	_, err = svc.saveEditedMetadataState("b1", loaded, edited)
	require.NoError(t, err)
	require.Equal(t, `"de"`, *fieldStatesByField(t, store, "b1")["language"].OverrideValue)
}

// The merge compares and writes every field of MetadataFieldState but
// UpdatedAt by reflection. This pins the field set: a new field must be one
// the merge may copy from the edit, or the merge needs a rule for it.
func TestMergeEditedMetadataState_FieldSetIsKnown(t *testing.T) {
	var names []string
	ty := reflect.TypeOf(metadataFieldState{})
	for i := 0; i < ty.NumField(); i++ {
		names = append(names, ty.Field(i).Name)
	}
	require.Equal(t, []string{"FetchedValue", "OverrideValue", "OverrideLocked", "LockSource", "UpdatedAt"}, names)
}
