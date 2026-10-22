// file: internal/merge/undo_shell_index_test.go
// version: 1.1.0
// guid: 9e4b1d60-7a35-4c82-b0f9-2d6e8a3c5b17
// last-edited: 2026-10-22

package merge

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kvFake is an in-memory raw-key store: the journal writer and reader.
type kvFake struct{ m map[string][]byte }

func (k *kvFake) SetRaw(key string, v []byte) error { k.m[key] = v; return nil }
func (k *kvFake) GetRaw(key string) ([]byte, error) { return k.m[key], nil }
func (k *kvFake) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for key, v := range k.m {
		if strings.HasPrefix(key, prefix) {
			out = append(out, database.KVPair{Key: key, Value: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func journalWith(t *testing.T, kv *kvFake, status string, stepJournaled bool, shell string, stamp time.Time) *CombineJournal {
	t.Helper()
	j := NewCombineJournal("survivor")
	j.Status = status
	j.StepJournaled = stepJournaled
	j.Absorbed = []CombineAbsorbed{{BookID: shell, MarkedForDeletionAt: &stamp}}
	require.NoError(t, WriteCombineJournal(kv, j))
	return j
}

func shellRow(id string, stamp time.Time) *database.Book {
	return &database.Book{ID: id, MarkedForDeletion: new(true), MarkedForDeletionAt: &stamp}
}

func TestUndoShellIndex_HoldsShellsOfJournalsThatCanStillUndo(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status string
		steps  bool
		held   bool
	}{
		{"applied", CombineJournalApplied, true, true},
		{"undo_failed re-runs", CombineJournalUndoFailed, true, true},
		{"pending with every step journaled", CombineJournalPending, true, true},
		{"pending that does not record its steps is not undoable", CombineJournalPending, false, false},
		{"undone", CombineJournalUndone, true, false},
	}
	for _, tc := range cases {
		journalWith(t, kv, tc.status, tc.steps, "shell-"+tc.name, stamp)
	}
	idx, err := NewUndoShellIndex(kv)
	require.NoError(t, err)
	for _, tc := range cases {
		_, held := idx.Holds(shellRow("shell-"+tc.name, stamp))
		assert.Equal(t, tc.held, held, tc.name)
	}
}

// UndoCombine restores only the row that still carries the journal's stamp
// (retiredByJournal): a shell restored and re-trashed later is another event
// and is not held.
func TestUndoShellIndex_OnlyTheStampedShellIsHeld(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	j := journalWith(t, kv, CombineJournalApplied, true, "shell", stamp)
	idx, err := NewUndoShellIndex(kv)
	require.NoError(t, err)

	id, held := idx.Holds(shellRow("shell", stamp))
	assert.True(t, held)
	assert.Equal(t, j.ID, id)

	_, held = idx.Holds(shellRow("shell", stamp.Add(time.Hour)))
	assert.False(t, held, "re-deleted since: another stamp")
	live := shellRow("shell", stamp)
	live.MarkedForDeletion, live.MarkedForDeletionAt = nil, nil
	_, held = idx.Holds(live)
	assert.False(t, held, "restored: not in the trash")
	_, held = idx.Holds(shellRow("someone-else", stamp))
	assert.False(t, held)
	_, held = (*UndoShellIndex)(nil).Holds(shellRow("shell", stamp))
	assert.False(t, held, "a nil index holds nothing")
}

func TestUndoShellIndex_LeftLiveAndUnstampedAbsorbedAreNotHeld(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	j := NewCombineJournal("survivor")
	j.Status = CombineJournalApplied
	j.Absorbed = []CombineAbsorbed{
		{BookID: "left-live", MarkedForDeletionAt: &stamp, LeftLive: true},
		{BookID: "unstamped"},
	}
	require.NoError(t, WriteCombineJournal(kv, j))
	idx, err := NewUndoShellIndex(kv)
	require.NoError(t, err)
	assert.Zero(t, idx.Len())
}

// A snapshot goes stale exactly when a combine journal is written after its
// scan began, so a purge deciding under the merge lock knows to rebuild it.
func TestUndoShellIndex_StaleAfterAJournalWrite(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	idx, err := NewUndoShellIndex(kv)
	require.NoError(t, err)
	assert.False(t, idx.Stale(), "nothing was written since the scan")
	_, held := idx.Holds(shellRow("late", stamp))
	assert.False(t, held)

	journalWith(t, kv, CombineJournalApplied, true, "late", stamp)
	assert.True(t, idx.Stale(), "a journal written after the scan makes the snapshot stale")
	_, held = idx.Holds(shellRow("late", stamp))
	assert.False(t, held, "the stale snapshot does not know the new journal; callers must rebuild")

	fresh, err := NewUndoShellIndex(kv)
	require.NoError(t, err)
	assert.False(t, fresh.Stale())
	_, held = fresh.Holds(shellRow("late", stamp))
	assert.True(t, held)

	var nilIdx *UndoShellIndex
	assert.True(t, nilIdx.Stale(), "no index at all is never current")
}
