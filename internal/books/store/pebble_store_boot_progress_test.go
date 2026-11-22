// file: internal/books/store/pebble_store_boot_progress_test.go
// version: 1.0.0
// guid: 0b5e2c9d-3a64-4f3b-9d7e-5c1a8f6e2d40
// last-edited: 2026-11-22

package store

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/startup"
)

// seedStaleRuns writes n ops runs whose opv2 row an older binary changed, so
// the reconcile has n runs to rebuild.
func seedStaleRuns(t *testing.T, p *PebbleStore, n int) {
	t.Helper()
	now := time.Now().UTC()
	for i := range n {
		id := newRunID()
		if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: fmt.Sprintf("test.batman-%d", i%5), Plugin: "test", Status: "queued", QueuedAt: now}); err != nil {
			t.Fatal(err)
		}
		row, err := p.GetOperationV2(id)
		if err != nil || row == nil {
			t.Fatal(err)
		}
		row.Status, row.StartedAt = "running", &now
		rawSetOp(t, p, *row)
	}
}

// TestReconcileOpsV3ReportsProgressPerPageAndBatch: the boot's ops reconcile
// reads the ops rows in pages on parallel workers and writes the stale runs
// in batches; it raises one progress unit per page read and per batch written
// (05-health §5H.3.4: the boot asks for start time per unit of work), so a
// longer ops history is more units. A store with nothing stale reports only
// its detection pages.
func TestReconcileOpsV3ReportsProgressPerPageAndBatch(t *testing.T) {
	old := opsV3ReconcileWorkers
	opsV3ReconcileWorkers = 4
	t.Cleanup(func() { opsV3ReconcileWorkers = old })
	units := func(rows int) (int64, OpsV3ReconcileReport) {
		p := newTimelineStore(t)
		seedStaleRuns(t, p, rows)
		var n atomic.Int64
		ctx := startup.WithProgress(context.Background(), func() { n.Add(1) })
		rep, err := p.ReconcileOpsV3WithV2(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n.Load(), rep
	}
	few, repFew := units(10)
	many, repMany := units(opsV3ReconcilePage + 200)
	t.Logf("progress units: %d for %d stale runs, %d for %d", few, repFew.Stale, many, repMany.Stale)
	// One page of opv2 rows, one batch of rebuilds, one page of opv3 rows.
	if few < 3 {
		t.Errorf("10 stale runs reported %d units, want at least 3 (a page, a batch, a page)", few)
	}
	// 1200 rows: two pages of opv2 rows, three batches of 500, two pages of opv3 rows.
	if many < 7 {
		t.Errorf("1200 stale runs reported %d units, want at least 7", many)
	}
	if many <= few {
		t.Errorf("1200 stale runs reported %d units, 10 reported %d: the count must grow with the history", many, few)
	}
}

// TestRunBootRepairsReportsEachRepairAsAUnit: the boot repairs raise a unit
// before each of their steps, so under startup.RunWithProgress each repair is
// preceded by a request for start time, and a store with no ops history still
// reports the two steps.
func TestRunBootRepairsReportsEachRepairAsAUnit(t *testing.T) {
	p := newTimelineStore(t)
	var n atomic.Int64
	ctx := startup.WithProgress(context.Background(), func() { n.Add(1) })
	if err := p.RunBootRepairs(ctx); err != nil {
		t.Fatal(err)
	}
	if n.Load() < 2 {
		t.Fatalf("RunBootRepairs reported %d units, want at least 2 (the open repairs and the reconcile)", n.Load())
	}
}

// TestScanJournalTopAsksForStartTimePerUnit: the open-time journal scan reads
// every journal row, so a long journal is a long step; it calls the store's
// boot extender once per journalScanLogEvery rows it reads (05-health
// §5H.3.4), and a store with no extender (every command but serve) reads
// the same rows without one.
func TestScanJournalTopAsksForStartTimePerUnit(t *testing.T) {
	old := journalScanLogEvery
	journalScanLogEvery = 10
	t.Cleanup(func() { journalScanLogEvery = old })
	p := newTimelineStore(t)
	for i := range 35 {
		if err := p.SetRaw(fmt.Sprintf("%s%04d", opChangeKeyPrefix, i), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	var asks []string
	p.bootExtend = func(step string) { asks = append(asks, step) }
	_, rows, err := p.scanJournalTop()
	if err != nil || rows != 35 {
		t.Fatalf("scanned %d rows, err %v; want 35", rows, err)
	}
	if len(asks) != 3 {
		t.Fatalf("35 rows at 10 per unit asked %d times, want 3: %q", len(asks), asks)
	}
	p.bootExtend = nil
	if _, rows, err = p.scanJournalTop(); err != nil || rows != 35 {
		t.Fatalf("without an extender: %d rows, err %v", rows, err)
	}
}
