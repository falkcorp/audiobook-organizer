// file: internal/operations/registry/scan_standdown_checkpoint_test.go
// version: 1.0.0
// guid: 1f6a9c3e-8b27-4d05-9e41-7c2d0a5b8f63
// last-edited: 2026-09-12

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// A per-item loop that runs longer than the lease keeps its hold: each item's
// Checkpoint renews it. No ticker is involved, so the loop's own pace is the
// only thing keeping the scanner parked.
func TestScanStandDownCheckpoint_SlowLoopLongerThanLeaseKeepsHold(t *testing.T) {
	const lease = 150 * time.Millisecond
	r := registry.NewWithOptions(newFakeStore(), slog.Default(), 1, registry.Options{ScanStandDownLease: lease})
	rep := &opReporter{id: "op-slow"}
	hold, err := registry.HoldScanStandDown(t.Context(), r, rep, "slow apply")
	if err != nil {
		t.Fatalf("HoldScanStandDown: %v", err)
	}
	began := time.Now()
	for i := range 8 {
		if err := registry.ScanStandDownCheckpoint(hold.Context()); err != nil {
			t.Fatalf("item %d: checkpoint failed after %s: %v", i, time.Since(began), err)
		}
		time.Sleep(lease / 3) // slow per-item network work
	}
	if total := time.Since(began); total < 2*lease {
		t.Fatalf("loop ran %s; it must outlast the %s lease to prove anything", total, lease)
	}
	if !r.ScanStandDownValid("op-slow") {
		t.Fatal("hold lapsed although every item beat it")
	}
	if err := hold.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if r.ScanStandDownValid("op-slow") {
		t.Fatal("Finish did not release the hold")
	}
}

// Without the per-item beat the same loop loses the hold: this is the failure
// the checkpoint exists to prevent, measured against the real registry.
func TestScanStandDownCheckpoint_LoopWithoutBeatLosesHold(t *testing.T) {
	const lease = 100 * time.Millisecond
	r := registry.NewWithOptions(newFakeStore(), slog.Default(), 1, registry.Options{ScanStandDownLease: lease})
	hold, err := registry.HoldScanStandDown(t.Context(), r, &opReporter{id: "op-quiet"}, "quiet apply")
	if err != nil {
		t.Fatalf("HoldScanStandDown: %v", err)
	}
	time.Sleep(2 * lease)
	if err := hold.Checkpoint(); !errors.Is(err, registry.ErrScanStandDownLost) {
		t.Fatalf("checkpoint after a lapsed lease: want ErrScanStandDownLost, got %v", err)
	}
	if err := hold.Finish(nil); !errors.Is(err, registry.ErrScanStandDownLost) {
		t.Fatalf("Finish must report the lost hold, got %v", err)
	}
}

// A lost hold stops the loop before the next write, in a hand-written loop and
// in RunItems (which beats the hold per item for every op run under one).
func TestScanStandDownCheckpoint_LostHoldStopsBeforeNextWrite(t *testing.T) {
	t.Run("loop", func(t *testing.T) {
		gate := &flakyGate{okFor: 3}
		hold, err := registry.HoldScanStandDown(t.Context(), gate, &opReporter{id: "op-loop"}, "apply")
		if err != nil {
			t.Fatalf("HoldScanStandDown: %v", err)
		}
		writes := 0
		for range 10 {
			if registry.ScanStandDownCheckpoint(hold.Context()) != nil {
				break
			}
			writes++
		}
		if writes != 3 {
			t.Fatalf("writes = %d, want 3 (the renewal that failed must stop the 4th write)", writes)
		}
		if !errors.Is(context.Cause(hold.Context()), registry.ErrScanStandDownLost) {
			t.Fatalf("hold context cause = %v, want ErrScanStandDownLost", context.Cause(hold.Context()))
		}
		_ = hold.Finish(nil)
	})
	t.Run("RunItems", func(t *testing.T) {
		gate := &flakyGate{okFor: 2}
		hold, err := registry.HoldScanStandDown(t.Context(), gate, &opReporter{id: "op-items"}, "apply")
		if err != nil {
			t.Fatalf("HoldScanStandDown: %v", err)
		}
		var writes atomic.Int32
		items := make([]int, 10)
		runErr := registry.RunItems(hold.Context(), &itemsReporter{}, items, func(context.Context, int) error {
			writes.Add(1)
			return nil
		})
		if got := writes.Load(); got != 2 {
			t.Fatalf("RunItems wrote %d items, want 2", got)
		}
		if !errors.Is(hold.Finish(runErr), registry.ErrScanStandDownLost) {
			t.Fatalf("op result must name the lost hold, got %v", runErr)
		}
	})
}

// The pickup race: a scan claimed by the dispatcher before a holder registered,
// and picked up by a worker after, is dropped as interrupted_quiesced. The last
// release must re-queue it; before the fix nothing recorded it and it stayed
// parked until the next startup sweep.
func TestScanStandDown_ScanDroppedAtPickupResumesOnRelease(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 1, nil)
	starts := make(chan struct{}, 8)
	if err := r.RegisterOp(countingScanDef(starts)); err != nil {
		t.Fatalf("RegisterOp scan: %v", err)
	}
	blockerStarted, unblock := make(chan struct{}), make(chan struct{})
	blocker := makeValidDef("test.blocker")
	blocker.ConcurrencyKey = "test.blocker"
	blocker.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		close(blockerStarted)
		<-unblock
		return nil
	}
	if err := r.RegisterOp(blocker); err != nil {
		t.Fatalf("RegisterOp blocker: %v", err)
	}
	r.Start(ctx)
	if _, err := r.EnqueueOp(ctx, "test.blocker", nil); err != nil {
		t.Fatalf("EnqueueOp blocker: %v", err)
	}
	<-blockerStarted // the only worker is busy from here on

	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp scan: %v", err)
	}
	// Wait until the dispatcher has claimed the scan (stub handle, no worker).
	// TryAcquire refuses on a claimed stub and leaves nothing registered.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rel, err := r.TryAcquireScanStandDown("probe", "probe")
		if errors.Is(err, registry.ErrScanRunning) {
			break
		}
		if err == nil {
			rel() // not claimed yet; let the dispatcher proceed
		}
		if time.Now().After(deadline) {
			t.Fatal("dispatcher never claimed the scan")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A blocking holder arrives now: stubs have no goroutine to park, so it
	// returns at once with the claimed scan still waiting for a worker.
	release, err := r.AcquireScanStandDown(ctx, "holder-op", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	close(unblock)
	awaitStatus(t, store, scanID, "interrupted_quiesced", 3*time.Second)
	select {
	case <-starts:
		t.Fatal("the scan ran while a holder held the stand-down")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	awaitStart(t, starts, "scan re-queued by the last release")
}

// flakyGate renews okFor times, then reports the lease gone.
type flakyGate struct {
	okFor  int
	renews int
}

func (g *flakyGate) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	return func() {}, nil
}

func (g *flakyGate) RenewScanStandDown(string) bool {
	g.renews++
	return g.renews <= g.okFor
}

type itemsReporter struct{ registry.Reporter }

func (itemsReporter) UpdateProgress(int, int, string) error { return nil }
func (itemsReporter) SetCurrentItem(string)                 {}
