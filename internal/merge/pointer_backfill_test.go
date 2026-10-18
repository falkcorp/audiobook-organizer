// file: internal/merge/pointer_backfill_test.go
// version: 1.0.0
// guid: 3052d5ca-020e-4197-8003-3e5e527e0938
// last-edited: 2026-10-18

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The backfill offers a pointer only to a journal some undo would accept;
// these mirror the status gates of undoPreconditions and UndoSiblingMove.
func TestCombineNotUndoableReason(t *testing.T) {
	full := []CombineAbsorbed{{BookID: "a"}}
	cases := []struct {
		name     string
		j        CombineJournal
		undoable bool
	}{
		{"applied with absorbed books", CombineJournal{SurvivorID: "k", Status: CombineJournalApplied, Absorbed: full}, true},
		{"applied, only survivor files", CombineJournal{SurvivorID: "k", Status: CombineJournalApplied, SurvivorFiles: []CombineFileMove{{FileID: "f"}}}, true},
		{"applied, refused and emptied", CombineJournal{SurvivorID: "k", Status: CombineJournalApplied, Warnings: []string{"refused before its first write"}}, false},
		{"undo failed: a retry", CombineJournal{SurvivorID: "k", Status: CombineJournalUndoFailed, Absorbed: full}, true},
		{"pending, step journaled", CombineJournal{SurvivorID: "k", Status: CombineJournalPending, StepJournaled: true, Absorbed: full}, true},
		{"pending CombineBooks journal", CombineJournal{SurvivorID: "k", Status: CombineJournalPending, Absorbed: full}, false},
		{"undone", CombineJournal{SurvivorID: "k", Status: CombineJournalUndone, Absorbed: full}, false},
		{"no survivor", CombineJournal{Status: CombineJournalApplied, Absorbed: full}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := combineNotUndoableReason(&tc.j)
			require.Equal(t, tc.undoable, reason == "", "reason %q", reason)
		})
	}
}

func TestSiblingNotUndoableReason(t *testing.T) {
	for status, undoable := range map[string]bool{
		SiblingJournalPending: true,
		SiblingJournalApplied: true,
		SiblingJournalUndone:  false,
		SiblingJournalAborted: false,
		"mystery":             false,
	} {
		reason := siblingNotUndoableReason(&SiblingMoveJournal{SurvivorID: "k", Status: status})
		require.Equal(t, undoable, reason == "", "status %q: reason %q", status, reason)
	}
	require.NotEmpty(t, siblingNotUndoableReason(&SiblingMoveJournal{Status: SiblingJournalApplied}))
}

// Only a live row of the right type naming the journal counts. A voided row
// (a refused merge's, or one the test voids) does not, and neither does a
// pointer to another journal on the same survivor.
func TestLivePointerOperation(t *testing.T) {
	row := func(op, typ, field, journal string, voided bool) *database.OperationChange {
		return &database.OperationChange{OperationID: op, ChangeType: typ, FieldName: field, NewValue: journal, Voided: voided}
	}
	changes := []*database.OperationChange{
		nil,
		row("op-void", CombineJournalPointerType, JournalPointerField, "J1", true),
		row("op-other", CombineJournalPointerType, JournalPointerField, "J2", false),
		row("op-sib", SiblingJournalPointerType, JournalPointerField, "J1", false),
		row("op-field", CombineJournalPointerType, "title", "J1", false),
	}
	require.Empty(t, livePointerOperation(changes, CombineJournalPointerType, "J1"))
	require.Equal(t, "op-other", livePointerOperation(changes, CombineJournalPointerType, "J2"))
	require.Equal(t, "op-sib", livePointerOperation(changes, SiblingJournalPointerType, "J1"))

	changes = append(changes, row("op-live", CombineJournalPointerType, JournalPointerField, "J1", false))
	require.Equal(t, "op-live", livePointerOperation(changes, CombineJournalPointerType, "J1"))
}

// A store with no undo ledger refuses before taking the merge lock or reading
// a journal.
func TestBackfillJournalPointer_RefusesWithoutLedger(t *testing.T) {
	ms := &Service{db: nil}
	_, err := ms.BackfillJournalPointer(PointerBackfillItem{ChangeType: CombineJournalPointerType, JournalID: "J1"}, false)
	require.ErrorContains(t, err, "no undo ledger")
}
