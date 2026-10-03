// file: internal/repairs/writer_locks_test.go
// version: 1.1.0
// guid: 89ff8b34-1ba1-405c-bf6b-cfe54095629c
// last-edited: 2026-10-03

package repairs

import (
	"errors"
	"fmt"
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

func (f *fieldStateFake) DeleteUserPreference(key string) error {
	delete(f.prefs, key)
	return nil
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
	require.Equal(t, database.RepairLockSource("op-1"), title.LockSource, "the lock names its operation")
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

// A field a person already locked, or an unknown key, is refused and nothing
// is written. A blob that does not parse cannot be migrated: refused, and the
// journal row written for it is voided.
func TestWriter_LockFields_Refusals(t *testing.T) {
	ov := `"Hand Typed"`
	f := &fieldStateFake{states: map[string]map[string]database.MetadataFieldState{
		"b1": {"title": {BookID: "b1", Field: "title", OverrideValue: &ov, OverrideLocked: true}},
	}, prefs: map[string]string{metastate.Key("b5"): `{not json`}}
	w := lockWriter(f)
	require.ErrorIs(t, w.LockFields("b1", database.FieldKeyTitle), ErrChangedSincePlan)
	require.Error(t, w.LockFields("b3", "not_a_field"))
	require.Empty(t, f.journal)
	require.Empty(t, f.states["b3"])

	require.Error(t, w.LockFields("b5", database.FieldKeyTitle))
	require.Empty(t, f.states["b5"])
	require.Len(t, f.journal, 1)
	require.NotNil(t, f.journal[0].RevertedAt, "the row of a lock that was never written is voided")
	require.True(t, f.journal[0].Voided)

	// Without a field-state store LockFields fails rather than skip the lock.
	bare := NewWriter(newMemStore(), newMemStore(), "junk", "bulk_update", "rp-").WithJournal(nil, f, "op-1")
	require.Error(t, bare.LockFields("b4", database.FieldKeyTitle))
}

// A book whose state still lives in the pre-migration blob is migrated to
// rows first, so the blob's locks stay locks, and the blob is retired.
func TestWriter_LockFields_MigratesLegacyBlob(t *testing.T) {
	f := &fieldStateFake{states: map[string]map[string]database.MetadataFieldState{},
		prefs: map[string]string{metastate.Key("b2"): `{"narrator":{"override_locked":true,"override_value":"Kim"}}`}}
	w := lockWriter(f)
	require.NoError(t, w.LockFields("b2", database.FieldKeyTitle))
	require.True(t, f.states["b2"]["narrator"].HasUserOverride(), "the blob's lock is a row now")
	require.True(t, f.states["b2"]["title"].IsRepairLock())
	_, blob := f.prefs[metastate.Key("b2")]
	require.False(t, blob, "the blob is retired")
}

// Another repair operation's lock is taken over under this op's id, and the
// journal records it so the revert hands it back; a person's lock is never
// taken; this op's own lock is idempotent.
func TestWriter_LockFields_ResumeAndForeignLock(t *testing.T) {
	f := &fieldStateFake{states: map[string]map[string]database.MetadataFieldState{
		"b1": {
			"title":    {BookID: "b1", Field: "title", OverrideLocked: true, LockSource: database.RepairLockSource("op-other")},
			"narrator": {BookID: "b1", Field: "narrator", OverrideLocked: true},
		},
	}}
	w := lockWriter(f)
	require.NoError(t, w.LockFields("b1", database.FieldKeyTitle))
	require.Equal(t, database.RepairLockSource("op-1"), f.states["b1"]["title"].LockSource)
	require.Equal(t, undo.FieldLockTakenFrom(database.RepairLockSource("op-other")), f.journal[0].OldValue)
	require.ErrorIs(t, w.LockFields("b1", database.FieldKeyNarrator), ErrChangedSincePlan, "a person's lock is never taken")
	require.NoError(t, w.LockFields("b1", database.FieldKeyAuthorName))
	require.NoError(t, w.LockFields("b1", database.FieldKeyAuthorName), "a resumed run finds its own lock")
	require.Len(t, f.journal, 2)
}

// JournalStep voids its row when the write is refused as changed since plan,
// and keeps it for any other failure (the write may have happened).
func TestWriter_JournalStep_VoidsRefusedWrite(t *testing.T) {
	f := &fieldStateFake{states: map[string]map[string]database.MetadataFieldState{}}
	w := lockWriter(f)
	e := UndoEntry{ChangeType: "metadata_update", Field: "title", Old: "a", New: "b"}
	err := w.JournalStep("b1", e, func() error { return fmt.Errorf("%w: moved", ErrChangedSincePlan) })
	require.ErrorIs(t, err, ErrChangedSincePlan)
	require.Len(t, f.journal, 1)
	require.NotNil(t, f.journal[0].RevertedAt)
	// The same step later journals a fresh row (the voided one is not a dedupe hit).
	require.NoError(t, w.JournalStep("b1", e, func() error { return nil }))
	require.Len(t, f.journal, 2)
	require.Nil(t, f.journal[1].RevertedAt)
	require.Error(t, w.JournalStep("b2", e, func() error { return errors.New("commit failed") }))
	require.True(t, f.journal[2].Voided, "any failed write wrote nothing: its row is voided too")
}
