// file: internal/merge/pointer_backfill_test.go
// version: 1.3.0
// guid: 3052d5ca-020e-4197-8003-3e5e527e0938
// last-edited: 2026-10-18

package merge

import (
	"fmt"
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
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

// The wide store carries every role the backfill and the chokepoint pointer
// write need. Pinned here, in a test, so production code depends only on the
// narrow role interfaces (database/store_consumers_test.go ratchets the rest).
var (
	_ PointerBackfillStore = database.Store(nil)
	_ JournalPointerWriter = database.Store(nil)
	_ opChangeScanner      = (*database.PebbleStore)(nil)
)

// noScanStore has the backfill's whole static surface but cannot scan the
// undo ledger (ScanOperationChanges is a PebbleStore capability, not on any
// interface).
type noScanStore struct{ PointerBackfillStore }

// A backfiller with no store refuses before taking the merge lock or reading
// a journal, and so does a store that cannot scan the ledger for the index.
func TestPointerBackfiller_Refuses(t *testing.T) {
	_, err := NewPointerBackfiller(nil).Backfill(&PointerBackfillIndex{}, PointerBackfillItem{ChangeType: CombineJournalPointerType, JournalID: "J1"}, false)
	require.ErrorContains(t, err, "no store")
	_, err = NewPointerBackfiller(nil).Backfill(nil, PointerBackfillItem{}, false)
	require.ErrorContains(t, err, "no pointer index")
	_, err = NewPointerBackfiller(noScanStore{}).NewIndex()
	require.ErrorContains(t, err, "cannot scan the undo ledger")
}

// countBookChanges counts GetBookChanges, the per-survivor ledger read.
type countBookChanges struct {
	*database.PebbleStore
	n *int
}

func (s countBookChanges) GetBookChanges(bookID string) ([]*database.OperationChange, error) {
	*s.n++
	return s.PebbleStore.GetBookChanges(bookID)
}

// The index is read once outside the merge lock. A journal older than it is
// decided from it with no ledger read under the lock; only a journal created
// after the index (a merge during the backfill) reads its survivor's ledger,
// and its chokepoint pointer is found there, not duplicated.
func TestPointerBackfiller_LedgerReadOnlyForJournalsNewerThanIndex(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	combine := func() *CombineResult {
		ids := []string{ulid.Make().String(), ulid.Make().String()}
		for i, id := range ids {
			_, err := store.CreateBook(&database.Book{ID: id, Title: "B", Format: "mp3", FilePath: fmt.Sprintf("/tmp/pb/%s-%d.mp3", id, i)})
			require.NoError(t, err)
		}
		res, err := NewService(store).CombineBooks(ids, ids[0], nil)
		require.NoError(t, err)
		return res
	}
	old := combine()

	n := 0
	pb := NewPointerBackfiller(countBookChanges{store, &n})
	idx, err := pb.NewIndex()
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond) // the next journal is created strictly after asOf
	newer := combine()

	got, err := pb.Backfill(idx, PointerBackfillItem{ChangeType: CombineJournalPointerType, JournalID: old.JournalID}, false)
	require.NoError(t, err)
	require.Equal(t, PointerBackfillHasPointer, got.Verdict)
	require.Equal(t, old.OperationID, got.OperationID)
	require.Zero(t, n, "an older journal is decided from the index, with no ledger read under the lock")

	got, err = pb.Backfill(idx, PointerBackfillItem{ChangeType: CombineJournalPointerType, JournalID: newer.JournalID}, false)
	require.NoError(t, err)
	require.Equal(t, PointerBackfillHasPointer, got.Verdict, "the newer journal's own pointer is found in the ledger")
	require.Equal(t, newer.OperationID, got.OperationID)
	require.Equal(t, 1, n)
}

// lockProbeScanner checks, from inside the ledger scan, that the merge lock
// is free: the scan reads the whole ledger and must not stall every merge.
type lockProbeScanner struct {
	*database.PebbleStore
	scanned *bool
}

func (s lockProbeScanner) ScanOperationChanges(fn func(*database.OperationChange) error) error {
	*s.scanned = true
	if !mergeSerializeMu.mu.TryLock() {
		return fmt.Errorf("the ledger scan ran inside the merge lock")
	}
	mergeSerializeMu.mu.Unlock()
	return s.PebbleStore.ScanOperationChanges(fn)
}

// NewIndex holds the merge lock only to stamp asOf; the ledger scan runs
// outside it.
func TestPointerBackfiller_NewIndexScansOutsideTheMergeLock(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	scanned := false
	_, err := NewPointerBackfiller(lockProbeScanner{store, &scanned}).NewIndex()
	require.NoError(t, err)
	require.True(t, scanned)
}
