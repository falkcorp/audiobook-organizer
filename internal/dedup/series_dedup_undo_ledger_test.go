// file: internal/dedup/series_dedup_undo_ledger_test.go
// version: 1.0.0
// guid: 5ad9f286-bc61-40ee-b7ea-e6a1e771ee63
// last-edited: 2026-09-10

// Package dedup — regression gates for the two halves of the destructive-op
// checklist that DedupSeries was missing (TODO.md L4967): its apply path wrote
// no undo-ledger rows, so a dry_run=false run could not be reversed by
// internal/undo, and it did not stand the library scanner down, so a concurrent
// library.scan could clobber the reassignments it had just made.
package dedup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDedupOpID is the operation id every direct DedupSeries call in these
// tests runs under. It is deliberately non-empty: an apply with no op id is
// refused, because its ledger rows would carry OperationID "" and nothing could
// find them again.
const testDedupOpID = "01JSERIESDEDUPTESTOP0000"

// fakeScanController is a ScanStandDownController for direct DedupSeries calls.
//
// It is NOT a no-op stand-in: the counters are what the tests below assert on.
// acquireErr models the real failure the gate can return — a running
// library.scan that will not park within the lease — and renewOK=false models a
// lease that lapsed mid-run, which the registry's contract says must abort the
// remaining writes.
type fakeScanController struct {
	mu         sync.Mutex
	acquireErr error
	renewOK    bool
	acquires   []string
	releases   int
	renews     int
}

// newFakeScanController returns a controller that grants the gate and keeps
// renewing it — the ordinary case, so a test that cares about a failure has to
// opt into it explicitly.
func newFakeScanController() *fakeScanController {
	return &fakeScanController{renewOK: true}
}

func (f *fakeScanController) AcquireScanStandDown(_ context.Context, holderOpID, _ string) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	f.acquires = append(f.acquires, holderOpID)
	return func() {
		f.mu.Lock()
		f.releases++
		f.mu.Unlock()
	}, nil
}

func (f *fakeScanController) RenewScanStandDown(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	return f.renewOK
}

func (f *fakeScanController) acquireCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acquires)
}

func (f *fakeScanController) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

// ledgerRecorder is the write-and-journal tape for one DedupSeries run: every
// mutating call the store received, in order. Tests assert on the TAPE rather
// than on the returned counters, because the defect being pinned is precisely a
// run that reports a clean merge while having journalled nothing.
type ledgerRecorder struct {
	updated []string
	deleted []int
	changes []database.OperationChange
}

// newLedgerFixture builds a two-series duplicate group ("Foundation" 1 and 2,
// both author-less, so series 1 is the canonical and series 2 is merged away)
// holding one book, and wires every write on the store to the tape.
func newLedgerFixture(rec *ledgerRecorder) *database.MockStore {
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "Foundation"},
			{ID: 2, Name: "Foundation"},
		}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 2 {
			sid := 2
			return []database.BookCore{{ID: "BOOK1", SeriesID: &sid}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 2
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		rec.updated = append(rec.updated, id)
		return b, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		rec.deleted = append(rec.deleted, id)
		return nil
	}
	mock.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		rec.changes = append(rec.changes, *c)
		return nil
	}
	return mock
}

// TestDedupSeries_ApplyJournalsUndoLedgerRows is the regression gate for the
// first half of TODO.md L4967: the apply path wrote no OperationChange rows at
// all, so a dry_run=false run was not undoable via internal/undo — git revert
// restores the code and nothing restores the data.
//
// It asserts CONTENT, not just a count. A row whose OldValue is the series the
// book was moved TO is worse than no row: undo would "restore" the merged-into
// id and the reassignment would look reversed while nothing moved back.
func TestDedupSeries_ApplyJournalsUndoLedgerRows(t *testing.T) {
	var rec ledgerRecorder
	mock := newLedgerFixture(&rec)

	result, err := DedupSeries(context.Background(), mock, testDedupOpID, newFakeScanController(), nil, false)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 1, result.TotalMerged)
	require.Equal(t, []string{"BOOK1"}, rec.updated, "the book must actually be reassigned")
	require.Equal(t, []int{2}, rec.deleted, "the duplicate series must actually be deleted")

	// One ledger row per write that landed — no more, no fewer. A missing row
	// is an un-undoable write; a surplus row is an undo that touches a row this
	// run never changed.
	require.Len(t, rec.changes, len(rec.updated)+len(rec.deleted),
		"every UpdateBook and every DeleteSeries on the apply path must journal exactly one change row")

	var reassign, deletion *database.OperationChange
	for i := range rec.changes {
		switch rec.changes[i].ChangeType {
		case "metadata_update":
			reassign = &rec.changes[i]
		case "series_delete":
			deletion = &rec.changes[i]
		}
	}

	require.NotNil(t, reassign, "the UpdateBook that repointed BOOK1 must journal a metadata_update row")
	assert.Equal(t, testDedupOpID, reassign.OperationID, "the row must be attributed to the run that made it")
	assert.NotEmpty(t, reassign.ID, "a change row needs its own id or the ledger cannot address it")
	assert.Equal(t, "BOOK1", reassign.BookID)
	assert.Equal(t, "series_id", reassign.FieldName)
	assert.Equal(t, "2", reassign.OldValue,
		"the before-image must be the series the book held BEFORE the merge; capturing it after the mutation journals the merged-into id and undo restores nothing")
	assert.Equal(t, "1", reassign.NewValue)

	require.NotNil(t, deletion, "the DeleteSeries must journal a series_delete row")
	assert.Equal(t, testDedupOpID, deletion.OperationID)
	assert.Equal(t, "series", deletion.FieldName)
	assert.Equal(t, "2:Foundation", deletion.OldValue,
		"the deleted series' id AND name are the only record left of the row that was removed")
	assert.Equal(t, "merged_into:1", deletion.NewValue)
}

// TestDedupSeries_ApplyReportsAFailedLedgerWrite pins the error channel on the
// journal itself. A swallowed CreateOperationChange failure leaves exactly the
// state this item exists to remove: the data changed and nothing can undo it.
func TestDedupSeries_ApplyReportsAFailedLedgerWrite(t *testing.T) {
	var rec ledgerRecorder
	mock := newLedgerFixture(&rec)
	mock.CreateOperationChangeFunc = func(*database.OperationChange) error {
		return errors.New("pebble write failed")
	}

	result, err := DedupSeries(context.Background(), mock, testDedupOpID, newFakeScanController(), nil, false)
	require.NoError(t, err)
	require.NotEmpty(t, result.Errors, "a ledger row that could not be written must be reported, never swallowed")

	var sawLedgerError bool
	for _, e := range result.Errors {
		if strings.Contains(e, "undo-ledger") {
			sawLedgerError = true
		}
	}
	assert.True(t, sawLedgerError,
		"the reported error must name the undo ledger; got %v", result.Errors)
}

// TestDedupSeries_DryRunJournalsNothingAndDoesNotParkTheScanner is the
// Idempotency/Rollback proof required of anything that touches an apply path.
//
// It is not covered by the existing dry-run test: that one asserts no
// UpdateBook and no DeleteSeries reached the store, and a journal row is
// NEITHER. A fix that journalled unconditionally would leave the store
// unchanged and still write a ledger row claiming a merge that never happened.
//
// It also asserts the dry run never acquires the stand-down. dry_run defaults
// to TRUE for this op, so acquiring on the preview path would cancel and
// re-queue the production library.scan every time anyone asked for a preview.
func TestDedupSeries_DryRunJournalsNothingAndDoesNotParkTheScanner(t *testing.T) {
	var rec ledgerRecorder
	mock := newLedgerFixture(&rec)
	mock.UpdateBookFunc = func(id string, _ *database.Book) (*database.Book, error) {
		t.Fatalf("dry run wrote book %s", id)
		return nil, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		t.Fatalf("dry run deleted series %d", id)
		return nil
	}

	scan := newFakeScanController()
	result, err := DedupSeries(context.Background(), mock, testDedupOpID, scan, nil, true)
	require.NoError(t, err)
	assert.True(t, result.DryRun)

	// The preview must still be informative, or "writes nothing" is satisfied
	// by a function that does nothing at all.
	assert.Equal(t, 1, result.TotalMerged, "one series WOULD be merged")
	assert.Equal(t, 1, result.TotalBooksReassigned, "one book WOULD move")

	assert.Empty(t, rec.changes, "a dry run must not write a single undo-ledger row")
	assert.Zero(t, scan.acquireCount(),
		"a dry run must not park the library scanner: it writes nothing, and dry_run is this op's default")
}

// TestDedupSeries_ApplyRefusedWhenScanWillNotStandDown is the regression gate
// for the second half of TODO.md L4967: the op did not refuse to start while a
// library.scan was running or queued, so a concurrent scan could clobber the
// reassignments.
//
// The load-bearing assertion is not the error — it is that the store saw ZERO
// writes. An error returned after the first UpdateBook would satisfy an
// error-only test while leaving exactly the half-applied, scanner-raced state
// the guard exists to prevent.
func TestDedupSeries_ApplyRefusedWhenScanWillNotStandDown(t *testing.T) {
	var rec ledgerRecorder
	mock := newLedgerFixture(&rec)

	scan := newFakeScanController()
	scan.acquireErr = errors.New("scan stand-down: scan did not park within 5m0s")

	_, err := DedupSeries(context.Background(), mock, testDedupOpID, scan, nil, false)
	require.Error(t, err, "an apply must fail closed when the scanner will not stand down")
	assert.Contains(t, err.Error(), "library.scan is running or queued")
	assert.Contains(t, err.Error(), "scan did not park", "the underlying gate error must be wrapped, not replaced")

	assert.Empty(t, rec.updated, "no book may be written before the gate is held")
	assert.Empty(t, rec.deleted, "no series may be deleted before the gate is held")
	assert.Empty(t, rec.changes, "a refused apply journals nothing")
}

// TestDedupSeries_ApplyRefusedWithoutAControllerOrOpID pins the fail-closed
// direction that the maintenance-plugin helper this pattern comes from gets
// wrong: acquireScanStandDownForApply returns "not held, no error" for both of
// these and runs with no interlock at all. On this apply path both are
// refusals — an apply with no op id cannot journal an attributable undo row,
// and an apply with no controller cannot know whether a scan is in flight.
func TestDedupSeries_ApplyRefusedWithoutAControllerOrOpID(t *testing.T) {
	t.Run("nil controller", func(t *testing.T) {
		var rec ledgerRecorder
		mock := newLedgerFixture(&rec)
		_, err := DedupSeries(context.Background(), mock, testDedupOpID, nil, nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "without a scan controller")
		assert.Empty(t, rec.updated)
		assert.Empty(t, rec.deleted)
	})

	t.Run("empty op id", func(t *testing.T) {
		var rec ledgerRecorder
		mock := newLedgerFixture(&rec)
		_, err := DedupSeries(context.Background(), mock, "", newFakeScanController(), nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "without an operation id")
		assert.Empty(t, rec.updated)
		assert.Empty(t, rec.deleted)
	})

	t.Run("dry run needs neither", func(t *testing.T) {
		// The positive control. A guard that refused everything would pass both
		// subtests above and silently disable the preview the op defaults to.
		var rec ledgerRecorder
		mock := newLedgerFixture(&rec)
		result, err := DedupSeries(context.Background(), mock, "", nil, nil, true)
		require.NoError(t, err, "a dry run writes nothing, so it needs no gate and no op id")
		assert.Equal(t, 1, result.TotalMerged)
	})
}

// TestDedupSeries_AbortsRemainingWritesWhenTheLeaseLapses pins the third part of
// the stand-down contract, which is the part that gets dropped when the shape is
// copied without the field: acquire, RENEW on the heartbeat, and treat a lost
// lease as a hard abort. A lapsed lease means the scanner has been resumed, so
// continuing to write means two writers on the same rows with no gate.
//
// Two duplicate groups, a controller that grants the gate and then refuses every
// renewal: exactly one group's writes may land before the run aborts.
func TestDedupSeries_AbortsRemainingWritesWhenTheLeaseLapses(t *testing.T) {
	var rec ledgerRecorder
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "Foundation"},
			{ID: 2, Name: "Foundation"},
			{ID: 3, Name: "Dune"},
			{ID: 4, Name: "Dune"},
		}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 2 || id == 4 {
			sid := id
			return []database.BookCore{{ID: fmt.Sprintf("BOOK%d", id), SeriesID: &sid}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 2
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		rec.updated = append(rec.updated, id)
		return b, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		rec.deleted = append(rec.deleted, id)
		return nil
	}
	mock.CreateOperationChangeFunc = func(c *database.OperationChange) error {
		rec.changes = append(rec.changes, *c)
		return nil
	}

	scan := newFakeScanController()
	scan.renewOK = false

	_, err := DedupSeries(context.Background(), mock, testDedupOpID, scan, nil, false)
	require.Error(t, err, "a lapsed stand-down lease must abort the run, not be logged and ignored")
	assert.Contains(t, err.Error(), "stand-down lease")

	assert.Len(t, rec.deleted, 1,
		"the run must stop after the group whose heartbeat found the lease gone; the second group must not be written")
	assert.Equal(t, 1, scan.releaseCount(), "the gate must be released on the way out, even on the abort path")
}
