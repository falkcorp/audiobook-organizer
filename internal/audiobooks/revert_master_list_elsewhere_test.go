// file: internal/audiobooks/revert_master_list_elsewhere_test.go
// version: 1.0.0
// guid: 4d19b7e2-a603-4c58-9f72-e8b0d1c35a96
// last-edited: 2026-10-23

package audiobooks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// elsewhereRevertLab is a real Pebble store with one journaled
// read-elsewhere creation, the way the master-list import's apply leaves it:
// the record stored, the create row journaled on no book.
func elsewhereRevertLab(t *testing.T) (*database.PebbleStore, *database.MasterListStore, database.MasterListElsewhere) {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	st := database.NewMasterListStoreFromStore(ps)
	require.NotNil(t, st)

	rec := database.MasterListElsewhere{
		ID: database.ElsewhereID("u1", "row-1"), UserID: "u1", ListID: "list-1",
		Title: "A Test Title", Authors: []string{"Test Author"}, Status: "finished",
	}
	require.NoError(t, st.CreateElsewhere(rec))
	stored, err := st.GetElsewhere(rec.ID)
	require.NoError(t, err)
	js, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, ps.CreateOperationChange(&database.OperationChange{
		OperationID: "op-els", BookID: "", ChangeType: undo.ChangeTypeMasterListElsewhereCreate,
		FieldName: undo.ElsewhereField(rec.ID), OldValue: undo.ElsewhereAbsent, NewValue: string(js),
	}))
	return ps, st, *stored
}

// The journaled create is restorable on a row naming no book, and the revert
// deletes exactly the record the apply made.
func TestRevertMasterListElsewhereCreate_DeletesTheRecord(t *testing.T) {
	ps, st, rec := elsewhereRevertLab(t)
	other := database.MasterListElsewhere{ID: "keep", UserID: "u1", ListID: "list-1", Title: "Other"}
	require.NoError(t, st.CreateElsewhere(other))

	res, err := audiobooks.NewRevertService(ps).RevertOperation("op-els")
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 1, res.Restored)
	assert.Zero(t, res.Failed)
	assert.Zero(t, res.NotRestorable)

	_, err = st.GetElsewhere(rec.ID)
	assert.ErrorIs(t, err, database.ErrElsewhereNotFound)
	_, err = st.GetElsewhere("keep")
	assert.NoError(t, err, "a record this operation did not create is untouched")
}

// A record that is already gone counts restored; the revert writes nothing.
func TestRevertMasterListElsewhereCreate_AlreadyGoneIsRestored(t *testing.T) {
	ps, st, rec := elsewhereRevertLab(t)
	require.NoError(t, st.DeleteElsewhere(rec.ID))

	res, err := audiobooks.NewRevertService(ps).RevertOperation("op-els")
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 1, res.Restored)
	assert.Zero(t, res.Failed)
}

// A record that is no longer what the import created (replaced since) is
// refused, never deleted.
func TestRevertMasterListElsewhereCreate_ChangedSinceIsRefused(t *testing.T) {
	ps, st, rec := elsewhereRevertLab(t)
	require.NoError(t, st.DeleteElsewhere(rec.ID))
	changed := rec
	changed.Title = "Edited since"
	require.NoError(t, st.CreateElsewhere(changed))

	res, _ := audiobooks.NewRevertService(ps).RevertOperation("op-els")
	require.NotNil(t, res)
	assert.Equal(t, 1, res.Failed)
	got, err := st.GetElsewhere(rec.ID)
	require.NoError(t, err)
	assert.Equal(t, "Edited since", got.Title)
}

func TestDecodeMasterListElsewhereCreate_RefusesBadRows(t *testing.T) {
	js := `{"id":"x1","user_id":"u","list_id":"l"}`
	for name, c := range map[string]*database.OperationChange{
		"no prefix":    {ID: "c", FieldName: "x1", OldValue: undo.ElsewhereAbsent, NewValue: js},
		"empty id":     {ID: "c", FieldName: "elsewhere:", OldValue: undo.ElsewhereAbsent, NewValue: js},
		"not a create": {ID: "c", FieldName: "elsewhere:x1", OldValue: "something", NewValue: js},
		"bad json":     {ID: "c", FieldName: "elsewhere:x1", OldValue: undo.ElsewhereAbsent, NewValue: "{"},
		"id mismatch":  {ID: "c", FieldName: "elsewhere:x2", OldValue: undo.ElsewhereAbsent, NewValue: js},
	} {
		_, err := undo.DecodeMasterListElsewhereCreate(c)
		assert.Error(t, err, name)
	}
	good := &database.OperationChange{ID: "c", ChangeType: undo.ChangeTypeMasterListElsewhereCreate,
		FieldName: "elsewhere:x1", OldValue: undo.ElsewhereAbsent, NewValue: js}
	_, err := undo.DecodeMasterListElsewhereCreate(good)
	assert.NoError(t, err)
	assert.True(t, undo.IsRestorable(good), "a valid create row is restorable")
}
