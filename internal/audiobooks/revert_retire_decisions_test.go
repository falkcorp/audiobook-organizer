// file: internal/audiobooks/revert_retire_decisions_test.go
// version: 1.0.0
// guid: b3c85e17-9d42-4a60-8f1e-27d6a0c49b58
// last-edited: 2026-10-28

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

func (fx *retireRevertFx) history(t *testing.T) []decisions.Record {
	t.Helper()
	key, err := decisions.RetiredVersionsSubjectKey(fx.shell.ID)
	require.NoError(t, err)
	kv, ok := database.AsCapability[decisions.KV](fx.st)
	require.True(t, ok)
	hist, err := decisions.New(kv).History(decisions.OpKindRetiredVersions, key)
	require.NoError(t, err)
	return hist
}

// Reverting a retire deletes its version once every row is back, and writes
// one `undone` record for the row: the shell is the entity, the holder the
// target, and (version, retire op) the identity.
func TestRevertRetire_WritesTheUndoneDecisionRecord(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-undone"), "ver-undone")
	require.Empty(t, fx.history(t), "the retire itself writes no record (the fixer's Apply does)")

	_, err := NewRevertService(fx.st).RevertOperation("op-undone")
	require.NoError(t, err)

	hist := fx.history(t)
	require.Len(t, hist, 1)
	rec := hist[0]
	require.Equal(t, decisions.DecisionUndone, rec.Decision)
	require.Equal(t, []string{fx.shell.ID}, rec.EntityIDs)
	require.Equal(t, fx.shell.ID, rec.Snapshot.Current["shell_id"])
	require.Equal(t, fx.holder.ID, rec.Snapshot.Current["target_id"])
	require.Equal(t, "ver-undone", rec.Snapshot.Current["version_id"])
	require.Equal(t, "op-undone", rec.Snapshot.Current["op_id"])
	require.Equal(t, decisions.SourceAuto, rec.Source)

	_, err = NewRevertService(fx.st).RevertOperation("op-undone")
	require.Error(t, err, "already reverted")
	require.Len(t, fx.history(t), 1, "a repeat writes no second record")
}

// The write is idempotent on (version, retire op): the repeat a cut-off revert
// makes finds the record there.
func TestRevertRetire_UndoneRecordIsIdempotent(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-idem"), "ver-idem")
	changes, err := fx.st.GetOperationChanges("op-idem")
	require.NoError(t, err)
	var create *database.OperationChange
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookVersionCreate {
			create = c
		}
	}
	require.NotNil(t, create)

	rs := NewRevertService(fx.st)
	rs.recordRetireUndone(create)
	rs.recordRetireUndone(create)
	require.Len(t, fx.history(t), 1)
}

// A revert that is refused writes nothing: the version is not deleted, so the
// retire is still in force.
func TestRevertRetire_RefusedRevertWritesNoUndoneRecord(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-refused"), "ver-refused")
	// The version was edited since: deleting it would lose the edit.
	v, err := fx.st.GetBookVersion("ver-refused")
	require.NoError(t, err)
	require.NotNil(t, v)
	v.Format = "m4b"
	err = fx.st.UpdateBookVersion(v)
	require.NoError(t, err)

	_, err = NewRevertService(fx.st).RevertOperation("op-refused")
	require.Error(t, err)
	still, err := fx.st.GetBookVersion("ver-refused")
	require.NoError(t, err)
	require.NotNil(t, still, "the version stays")
	require.Empty(t, fx.history(t))
}
