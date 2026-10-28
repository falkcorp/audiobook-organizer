// file: internal/audiobooks/revert_retire_decisions_test.go
// version: 1.1.0
// guid: b3c85e17-9d42-4a60-8f1e-27d6a0c49b58
// last-edited: 2026-10-28

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

func (fx *retireRevertFx) history(t *testing.T) []decisions.Record {
	t.Helper()
	return fx.historyOf(t, fx.shell.ID)
}

// historyOf is the decision chain of one shell's retired-versions proposal.
func (fx *retireRevertFx) historyOf(t *testing.T, shellID string) []decisions.Record {
	t.Helper()
	key, err := decisions.RetiredVersionsSubjectKey(shellID)
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

// A revert cut off after it deleted the version and before it wrote the record
// leaves rows already back and no version: the next revert finds both already
// restored, and writes the record, because the operation did retire rows into
// that version. This is the only path to the record then, so it also holds the
// ErrAlreadyRestored branch of revertBookVersionCreate in place.
func TestRevertRetire_CutOffRevertIsCompletedWithTheUndoneRecord(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-cutrevert"), "ver-cutrevert")
	// What the cut-off revert did: the rows went back and the version went.
	require.NoError(t, fx.st.UnretireBookFiles([]database.BookFileMove{{FileIDs: fx.ids(), SourceBookID: fx.holder.ID}},
		fx.shell.ID, "ver-cutrevert", nil))
	require.NoError(t, fx.st.DeleteBookVersion("ver-cutrevert"))
	require.Empty(t, fx.history(t))

	_, err := NewRevertService(fx.st).RevertOperation("op-cutrevert")
	require.NoError(t, err)

	hist := fx.history(t)
	require.Len(t, hist, 1, "exactly one undone record")
	require.Equal(t, decisions.DecisionUndone, hist[0].Decision)
	require.Equal(t, "ver-cutrevert", hist[0].Snapshot.Current["version_id"])
	require.Equal(t, "op-cutrevert", hist[0].Snapshot.Current["op_id"])
}

// A run cut off after it created the retired version and before RetireRows
// retired a row left a retire that never took effect. Reverting it removes the
// empty version and writes no `undone`: the owner never had an applied retire
// to undo, and a stray undone would sit on the shell's chain with no applied.
func TestRevertRetire_VersionWithNoRetiredRowsGetsNoUndoneRecord(t *testing.T) {
	cases := map[string]func(t *testing.T, fx *retireRevertFx){
		"the version is still there": func(t *testing.T, fx *retireRevertFx) {},
		"the version was deleted by hand": func(t *testing.T, fx *retireRevertFx) {
			require.NoError(t, fx.st.DeleteBookVersion("ver-empty"))
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newRetireRevertFx(t)
			// The version is created and journaled; the row named is on neither
			// book, so RetireRows is refused before it journals a retire row.
			err := fx.writer("op-empty").RetireVersion(repairs.RetireSpec{
				Shell: fx.shell.ID, Holder: fx.holder.ID, VersionID: "ver-empty", FileIDs: []string{"no-such-row"},
				Format: "mp3", Source: "retired_book",
			})
			require.Error(t, err)
			v, gerr := fx.st.GetBookVersion("ver-empty")
			require.NoError(t, gerr)
			require.NotNil(t, v, "the version was created before the retire was refused")
			changes, gerr := fx.st.GetOperationChanges("op-empty")
			require.NoError(t, gerr)
			for _, c := range changes {
				require.NotEqual(t, undo.ChangeTypeBookFileRetire, c.ChangeType, "no retire row was journaled")
			}
			arrange(t, fx)

			_, err = NewRevertService(fx.st).RevertOperation("op-empty")
			require.NoError(t, err)
			gone, gerr := fx.st.GetBookVersion("ver-empty")
			require.NoError(t, gerr)
			require.Nil(t, gone, "the empty version is removed")
			require.Empty(t, fx.history(t), "no retire was in force, so nothing was undone")
		})
	}
}
