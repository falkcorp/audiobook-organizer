// file: internal/merge/undo_shell_index_test.go
// version: 1.2.0
// guid: 9e4b1d60-7a35-4c82-b0f9-2d6e8a3c5b17
// last-edited: 2026-10-22

package merge

import (
	"errors"
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

// An undecodable combine-journal row may be an undo that still needs a trashed
// book, and it cannot say which: building the index fails closed (owner
// decision 2026-10-22, like OpenAutoMergeLosers) and the error names the key
// and the op that lists such rows. Skipping the row instead would let the
// purge delete a shell the undo needs.
func TestUndoShellIndex_UnreadableJournalRowFailsClosedNamingTheKey(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	journalWith(t, kv, CombineJournalApplied, true, "shell-ok", stamp)
	const badKey = combineJournalPrefix + "01CORRUPT"
	kv.m[badKey] = []byte("{not json")

	idx, err := NewUndoShellIndex(kv)
	require.Error(t, err)
	assert.Nil(t, idx)
	var ue *UnreadableCombineJournalsError
	require.True(t, errors.As(err, &ue), "%v", err)
	require.Len(t, ue.Rows, 1)
	assert.Equal(t, badKey, ue.Rows[0].Key)
	assert.Contains(t, err.Error(), badKey)
	assert.Contains(t, err.Error(), CombineJournalCheckOp)
	assert.Contains(t, err.Error(), "every purge is refused")

	// Repairing (removing) the row restores normal behaviour.
	delete(kv.m, badKey)
	idx, err = NewUndoShellIndex(kv)
	require.NoError(t, err)
	_, held := idx.Holds(shellRow("shell-ok", stamp))
	assert.True(t, held)
}

// With no corrupt rows the index behaves as before, including a store that
// holds unrelated undecodable keys outside the combine-journal prefix.
func TestUndoShellIndex_NoUnreadableRowsBehavesNormally(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{"merge:other:x": []byte("{not json")}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	journalWith(t, kv, CombineJournalApplied, true, "shell", stamp)
	idx, err := NewUndoShellIndex(kv)
	require.NoError(t, err)
	_, held := idx.Holds(shellRow("shell", stamp))
	assert.True(t, held)
}

// Many bad rows: the message names the first few and counts the rest.
func TestUnreadableCombineJournalsError_CapsTheKeysItNames(t *testing.T) {
	var rows []UnreadableCombineJournal
	for i := 0; i < 8; i++ {
		rows = append(rows, UnreadableCombineJournal{Key: combineJournalPrefix + string(rune('a'+i))})
	}
	msg := (&UnreadableCombineJournalsError{Rows: rows}).Error()
	assert.Contains(t, msg, "8 combine journal row(s)")
	assert.Contains(t, msg, `"`+combineJournalPrefix+`a"`)
	assert.NotContains(t, msg, `"`+combineJournalPrefix+`h"`)
	assert.Contains(t, msg, "and 3 more")
}

// CheckCombineJournals lists the unreadable rows by key, sorted, and counts
// every row; the lenient lister (informational listings, pointer backfill)
// still skips them. If NewUndoShellIndex were changed to use the lenient
// lister, TestUndoShellIndex_UnreadableJournalRowFailsClosedNamingTheKey fails.
func TestCheckCombineJournals_ListsUnreadableRowsAndLenientListerStillSkips(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	stamp := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	journalWith(t, kv, CombineJournalApplied, true, "shell", stamp)
	kv.m[combineJournalPrefix+"02B"] = []byte("[]")
	kv.m[combineJournalPrefix+"01A"] = []byte("{not json")

	check, err := CheckCombineJournals(kv)
	require.NoError(t, err)
	assert.Equal(t, 3, check.Rows)
	require.Len(t, check.Unreadable, 2)
	assert.Equal(t, combineJournalPrefix+"01A", check.Unreadable[0].Key)
	assert.Equal(t, combineJournalPrefix+"02B", check.Unreadable[1].Key)
	assert.NotEmpty(t, check.Unreadable[0].Err)

	listed, err := listCombineJournals(kv, 0)
	require.NoError(t, err)
	assert.Len(t, listed, 1, "the lenient lister skips undecodable rows (warning only)")
}

func TestCheckCombineJournals_CleanStoreReportsNone(t *testing.T) {
	kv := &kvFake{m: map[string][]byte{}}
	check, err := CheckCombineJournals(kv)
	require.NoError(t, err)
	assert.Zero(t, check.Rows)
	assert.Empty(t, check.Unreadable)
}
