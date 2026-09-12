// file: internal/operations/registry/scan_standdown_hold_test.go
// version: 1.0.0
// guid: 9a3f5d61-2c84-4e7b-8d19-6b0e4a7c3f58
// last-edited: 2026-09-12

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// countingScanDef is a library.scan def that signals every start (so a resumed
// run is observable) and parks on ctx like a well-behaved scan phase.
func countingScanDef(starts chan<- struct{}) registry.OperationDef {
	d := makeValidDef(testScanDefID)
	d.DisplayName = "Library Scan"
	d.ResumePolicy = registry.ResumeRestart
	d.ConcurrencyKey = testScanDefID
	d.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		rep.UpdateProgress(1, 100, "scanning")
		starts <- struct{}{}
		<-runCtx.Done()
		return runCtx.Err()
	}
	return d
}

func awaitStart(t *testing.T, starts <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-starts:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestScanStandDown_ResumedScanParksOnSecondAcquire verifies known limit 1 in
// code: a scan re-queued after a stand-down (the resumed run) is found and
// parked by the next acquire, just like a fresh one.
func TestScanStandDown_ResumedScanParksOnSecondAcquire(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	starts := make(chan struct{}, 8)
	if err := r.RegisterOp(countingScanDef(starts)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)
	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	awaitStart(t, starts, "first scan run")

	release1, err := r.AcquireScanStandDown(ctx, "holder-1", "apply")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	awaitStatus(t, store, scanID, "interrupted_quiesced", 3*time.Second)
	release1()
	awaitStart(t, starts, "resumed scan run")

	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release2, err := r.AcquireScanStandDown(actx, "holder-2", "apply")
	if err != nil {
		t.Fatalf("second acquire did not park the resumed scan: %v", err)
	}
	awaitStatus(t, store, scanID, "interrupted_quiesced", 3*time.Second)
	release2()
}

// TestTryAcquireScanStandDown_RefusesWhileScanRunsWithoutQuiescing is the
// request-path contract: no wait, ErrScanRunning, and the running scan is NOT
// interrupted. Once no scan is running the gate is taken and a new scan is held
// back until release.
func TestTryAcquireScanStandDown_RefusesWhileScanRunsWithoutQuiescing(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	starts := make(chan struct{}, 8)
	if err := r.RegisterOp(countingScanDef(starts)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)
	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	awaitStart(t, starts, "scan run")

	began := time.Now()
	if _, err := r.TryAcquireScanStandDown("http:apply:1", "apply"); !errors.Is(err, registry.ErrScanRunning) {
		t.Fatalf("while scanning: want ErrScanRunning, got %v", err)
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("TryAcquire waited %s; it must not wait", d)
	}
	if !strings.Contains(registry.ErrScanRunning.Error(), "try again when it finishes") {
		t.Fatalf("user-facing message changed: %q", registry.ErrScanRunning)
	}
	awaitStatus(t, store, scanID, "running", time.Second)

	if err := r.Cancel(scanID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	var release func()
	deadline := time.Now().Add(5 * time.Second)
	for release == nil {
		rel, err := r.TryAcquireScanStandDown("http:apply:2", "apply")
		if err == nil {
			release = rel
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TryAcquire still refused after the scan stopped: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := r.EnqueueOp(ctx, testScanDefID, nil); err != nil {
		t.Fatalf("EnqueueOp second scan: %v", err)
	}
	select {
	case <-starts:
		t.Fatal("a new scan started while a request held the stand-down")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	awaitStart(t, starts, "scan after release")
}

type fakeGate struct {
	acquireErr error
	renewOK    bool
	acquired   []string
	released   int
}

func (g *fakeGate) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	if g.acquireErr != nil {
		return nil, g.acquireErr
	}
	g.acquired = append(g.acquired, holder)
	return func() { g.released++ }, nil
}

func (g *fakeGate) RenewScanStandDown(string) bool { return g.renewOK }

// opReporter is a minimal reporter carrying an op id; unused methods panic via
// the nil embedded interface.
type opReporter struct {
	registry.Reporter
	id       string
	progress int
}

func (o *opReporter) OpID() string                          { return o.id }
func (o *opReporter) UpdateProgress(int, int, string) error { o.progress++; return nil }
func (o *opReporter) Log(slog.Level, string, ...slog.Attr) error {
	return nil
}

func TestHoldScanStandDown_RefusesWhenScanDoesNotPark(t *testing.T) {
	gate := &fakeGate{acquireErr: errors.New("scan stand-down: scan did not park within 5m0s")}
	_, err := registry.HoldScanStandDown(t.Context(), gate, &opReporter{id: "op-1"}, "metadata.batch-apply-cached apply")
	if err == nil || !strings.Contains(err.Error(), "library scan is running") || !strings.Contains(err.Error(), "did not park") {
		t.Fatalf("want a clear refusal naming the scan, got %v", err)
	}
}

func TestHoldScanStandDown_LostLeaseCancelsAndFinishReportsIt(t *testing.T) {
	gate := &fakeGate{renewOK: true}
	rep := &opReporter{id: "op-1"}
	hold, err := registry.HoldScanStandDown(t.Context(), gate, rep, "x apply")
	if err != nil {
		t.Fatalf("HoldScanStandDown: %v", err)
	}
	if len(gate.acquired) != 1 || gate.acquired[0] != "op-1" {
		t.Fatalf("gate not acquired under the op id: %v", gate.acquired)
	}
	_ = hold.Reporter().UpdateProgress(1, 2, "a")
	if hold.Context().Err() != nil || hold.Lost() {
		t.Fatal("renewed lease must not cancel")
	}
	if registry.ReporterOpID(hold.Reporter()) != "op-1" {
		t.Fatal("wrapped reporter hides OpID")
	}
	gate.renewOK = false
	_ = hold.Reporter().UpdateProgress(2, 2, "b")
	if !errors.Is(context.Cause(hold.Context()), registry.ErrScanStandDownLost) {
		t.Fatalf("lost lease must cancel ctx with ErrScanStandDownLost, got %v", context.Cause(hold.Context()))
	}
	if err := hold.Finish(nil); !errors.Is(err, registry.ErrScanStandDownLost) {
		t.Fatalf("Finish: want ErrScanStandDownLost, got %v", err)
	}
	if gate.released != 1 || rep.progress != 2 {
		t.Fatalf("released=%d progress=%d, want 1 and 2", gate.released, rep.progress)
	}
}

func TestHoldScanStandDown_NoOpIDOrGateIsUngated(t *testing.T) {
	gate := &fakeGate{acquireErr: errors.New("must not be called")}
	hold, err := registry.HoldScanStandDown(t.Context(), gate, &opReporter{}, "x apply")
	if err != nil || hold.Held() {
		t.Fatalf("no op id: want ungated hold, got held=%v err=%v", hold != nil && hold.Held(), err)
	}
	if err := hold.Finish(nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	hold, err = registry.HoldScanStandDown(t.Context(), nil, &opReporter{id: "op"}, "x apply")
	if err != nil || hold.Held() {
		t.Fatalf("nil gate: want ungated hold, got err=%v", err)
	}
}
