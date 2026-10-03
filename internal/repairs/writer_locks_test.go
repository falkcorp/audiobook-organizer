// file: internal/repairs/writer_locks_test.go
// version: 1.0.0
// guid: 89ff8b34-1ba1-405c-bf6b-cfe54095629c
// last-edited: 2026-10-03

package repairs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// fieldStateFake is a field-state store plus the credit fake's op journal.
type fieldStateFake struct {
	creditFake
	states map[string]map[string]database.MetadataFieldState
	prefs  map[string]string
}

func (f *fieldStateFake) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []database.MetadataFieldState
	for _, st := range f.states[id] {
		out = append(out, st)
	}
	return out, nil
}

func (f *fieldStateFake) UpsertMetadataFieldState(st *database.MetadataFieldState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states[st.BookID] == nil {
		f.states[st.BookID] = map[string]database.MetadataFieldState{}
	}
	f.states[st.BookID][st.Field] = *st
	return nil
}

func (f *fieldStateFake) GetUserPreference(key string) (*database.UserPreference, error) {
	v, ok := f.prefs[key]
	if !ok {
		return nil, nil
	}
	return &database.UserPreference{Key: key, Value: &v}, nil
}

func lockWriter(f *fieldStateFake) *Writer {
	return NewWriter(newMemStore(), newMemStore(), "junk", "bulk_update", "rp-").
		WithJournal(nil, f, "op-1").WithFieldStates(f)
}

// LockFields journals the lock first, then sets OverrideLocked keeping the
// row's provider value and its UpdatedAt.
func TestWriter_LockFields_JournalsAndKeepsProviderState(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fv := `"Provider Title"`
	f := &fieldStateFake{states: map[string]map[string]database.MetadataFieldState{
		"b1": {"title": {BookID: "b1", Field: "title", FetchedValue: &fv, UpdatedAt: at}},
	}}
	w := lockWriter(f)
	require.NoError(t, w.LockFields("b1", database.FieldKeyTitle, database.FieldKeyAuthorName))

	title := f.states["b1"]["title"]
	require.True(t, title.OverrideLocked)
	require.Nil(t, title.OverrideValue, "a bare lock, no override value")
	require.Equal(t, &fv, title.FetchedValue)
	require.True(t, at.Equal(title.UpdatedAt), "UpdatedAt is the provider's time, not the lock's")
	require.True(t, f.states["b1"]["author_name"].OverrideLocked, "a field with no row gets one")

	require.Len(t, f.journal, 2)
	for _, c := range f.journal {
		require.Equal(t, undo.ChangeTypeFieldLock, c.ChangeType)
		require.Equal(t, undo.FieldLockUnlocked, c.OldValue)
		require.Equal(t, undo.FieldLockLocked, c.NewValue)
	}
}

// A field a person already locked, a book whose state is still in the
// pre-migration blob, or an unknown key is refused and nothing is written.
func TestWriter_LockFields_Refusals(t *testing.T) {
	ov := `"Hand Typed"`
	f := &fieldStateFake{states: map[string]map[string]database.MetadataFieldState{
		"b1": {"title": {BookID: "b1", Field: "title", OverrideValue: &ov, OverrideLocked: true}},
	}, prefs: map[string]string{metastate.Key("b2"): `{"title":{"override_locked":true}}`}}
	w := lockWriter(f)
	require.ErrorIs(t, w.LockFields("b1", database.FieldKeyTitle), ErrChangedSincePlan)
	require.Error(t, w.LockFields("b2", database.FieldKeyTitle))
	require.Error(t, w.LockFields("b3", "not_a_field"))
	require.Empty(t, f.journal)
	require.Empty(t, f.states["b2"])
	require.Empty(t, f.states["b3"])

	// Without a field-state store LockFields fails rather than skip the lock.
	bare := NewWriter(newMemStore(), newMemStore(), "junk", "bulk_update", "rp-").WithJournal(nil, f, "op-1")
	require.Error(t, bare.LockFields("b4", database.FieldKeyTitle))
}
