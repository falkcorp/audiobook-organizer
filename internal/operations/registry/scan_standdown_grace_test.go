// file: internal/operations/registry/scan_standdown_grace_test.go
// version: 1.1.1
// guid: 6f2d9a41-83c5-4b7e-a0d6-19e4c7b35f82
// last-edited: 2026-09-14

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// slowStartupScanDef is a library.scan whose startup takes startup and does
// NOT observe cancellation (the shape of the pre-2026-09-13 works load), then
// blocks until canceled. ready is closed-once-per-run via the counter: each
// run increments starts, and readyRuns counts runs that finished startup.
func slowStartupScanDef(startup time.Duration, starts, readyRuns *atomic.Int32) registry.OperationDef {
	d := makeValidDef(testScanDefID)
	d.DisplayName = "Library Scan"
	d.ResumePolicy = registry.ResumeRestart
	d.ConcurrencyKey = testScanDefID
	d.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		starts.Add(1)
		rep.UpdateProgress(1, 100, "starting")
		time.Sleep(startup) // uninterruptible startup phase
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		readyRuns.Add(1)
		<-runCtx.Done()
		return runCtx.Err()
	}
	return d
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// TestScanStandDown_BurstDoesNotPayScanStartupPerCycle is the 2026-09-13
// production symptom: N one-book applies in a row, each standing the scan down.
// Before the grace, every release re-queued the scan, the scan re-ran its whole
// startup (2s here, ~57s in production) and the next acquire waited for it to
// park, so N cycles cost about (N-1) startups. With the grace, only the first
// acquire parks a running scan; the rest find nothing running.
//
// The assertion is structural (how many times the scan started), not
// wall-clock, so a loaded CI runner cannot flake it. The grace is far longer
// than the gaps between cycles so a stalled runner does not let it fire early.
func TestScanStandDown_BurstDoesNotPayScanStartupPerCycle(t *testing.T) {
	const (
		startup = 2 * time.Second
		cycles  = 5
	)
	ctx := t.Context()
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 4,
		registry.Options{ScanStandDownGrace: 5 * time.Second})
	var starts, readyRuns atomic.Int32
	if err := r.RegisterOp(slowStartupScanDef(startup, &starts, &readyRuns)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	waitFor(t, "scan past its startup", 10*time.Second, func() bool { return readyRuns.Load() == 1 })

	begin := time.Now()
	for i := 0; i < cycles; i++ {
		release, err := r.AcquireScanStandDown(ctx, "apply-"+string(rune('a'+i)), "apply")
		if err != nil {
			t.Fatalf("cycle %d: AcquireScanStandDown: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond) // the apply's own work
		release()
		time.Sleep(50 * time.Millisecond) // the owner moving to the next book
	}
	// Timing is logged for the record only; the checks below are counts.
	t.Logf("%d acquire/release cycles took %s (scan startup %s)", cycles, time.Since(begin), startup)
	if got := starts.Load(); got != 1 {
		t.Fatalf("scan started %d times during the burst; want 1 (each cycle is paying the scan's restart)", got)
	}
	if s := store.statusOf(scanID); s != "interrupted_quiesced" {
		t.Fatalf("scan status during grace = %s; want interrupted_quiesced", s)
	}

	// The grace re-queues the scan exactly once after the burst.
	awaitStatus(t, store, scanID, "running", 20*time.Second)
	time.Sleep(2 * time.Second)
	if got := starts.Load(); got != 2 {
		t.Fatalf("scan started %d times after the grace; want exactly 2 (one re-queue)", got)
	}
}

// TestScanStandDown_GraceHoldsQueuedScanThenDispatchesOnce: a scan queued while
// the gate is held must also wait out the grace (otherwise the next click pays
// a park again), then dispatch once.
func TestScanStandDown_GraceHoldsQueuedScanThenDispatchesOnce(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 4,
		registry.Options{ScanStandDownGrace: 600 * time.Millisecond})
	var starts, readyRuns atomic.Int32
	if err := r.RegisterOp(slowStartupScanDef(0, &starts, &readyRuns)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	release, err := r.AcquireScanStandDown(ctx, "holder-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	release()
	releasedAt := time.Now()

	for time.Since(releasedAt) < 400*time.Millisecond {
		if s := store.statusOf(scanID); s != "queued" {
			t.Fatalf("scan dispatched during the grace: status=%s after %s", s, time.Since(releasedAt))
		}
		time.Sleep(20 * time.Millisecond)
	}
	awaitStatus(t, store, scanID, "running", 5*time.Second)
	// The worker marks the row running before it calls Run, where starts is
	// counted, so wait for the start itself rather than reading the counter
	// the instant the status flips. Then hold briefly: a second dispatch
	// would show up as a second start.
	waitFor(t, "the scan's Run to start", 5*time.Second, func() bool { return starts.Load() >= 1 })
	time.Sleep(200 * time.Millisecond)
	if got := starts.Load(); got != 1 {
		t.Fatalf("scan started %d times; want 1", got)
	}
}

// TestScanStandDown_AcquireDuringGraceIsImmediateAndInheritsScan: the second
// click must not wait, and its marker must name the scan the first click
// parked so a crash during the second apply still defers that scan at boot.
func TestScanStandDown_AcquireDuringGraceIsImmediateAndInheritsScan(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	settings := newFakeSettingsStore()
	r := registry.NewWithOptions(store, slog.Default(), 4,
		registry.Options{ScanStandDownGrace: time.Hour})
	r.SetScanStandDownStore(settings)
	started := make(chan struct{})
	if err := r.RegisterOp(scanDef(started, nil)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	<-started
	rel1, err := r.AcquireScanStandDown(ctx, "apply-1", "apply")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	rel1()
	if got, _ := settings.GetSetting("registry.scan_standdown"); got != nil && got.Value != "" {
		t.Fatalf("marker not cleared at last release: %q", got.Value)
	}

	begin := time.Now()
	rel2, err := r.AcquireScanStandDown(ctx, "apply-2", "apply")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if d := time.Since(begin); d > 200*time.Millisecond {
		t.Fatalf("acquire during grace took %s; want immediate", d)
	}
	got, _ := settings.GetSetting("registry.scan_standdown")
	if got == nil || got.Value == "" {
		t.Fatal("second holder persisted no marker")
	}
	var m struct {
		ScanOpID string `json:"scan_op_id"`
	}
	if err := json.Unmarshal([]byte(got.Value), &m); err != nil || m.ScanOpID != scanID {
		t.Fatalf("second holder's marker scan_op_id = %q (err %v); want the inherited %q", m.ScanOpID, err, scanID)
	}
	rel2()
}

// TestScanStandDown_CrashDuringGraceResumesScanAtStartup: if the process dies
// inside the grace, nothing in memory survives, so the startup resume sweep
// must re-queue the parked scan on its own. It can because the marker was
// cleared at the last release and the row is interrupted_quiesced.
func TestScanStandDown_CrashDuringGraceResumesScanAtStartup(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	settings := newFakeSettingsStore()

	r1 := registry.NewWithOptions(store, slog.Default(), 4,
		registry.Options{ScanStandDownGrace: time.Hour})
	r1.SetScanStandDownStore(settings)
	started := make(chan struct{})
	if err := r1.RegisterOp(scanDef(started, nil)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r1.Start(ctx)
	scanID, err := r1.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	<-started
	release, err := r1.AcquireScanStandDown(ctx, "apply-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	release() // grace starts (1h): nothing will re-queue the scan in this process
	awaitStatus(t, store, scanID, "interrupted_quiesced", 3*time.Second)
	if got, _ := settings.GetSetting("registry.scan_standdown"); got != nil && got.Value != "" {
		t.Fatalf("marker present during grace (%q): a crash now would defer the scan for a whole boot", got.Value)
	}
	// "Crash": this process's in-memory grace is gone. Shutdown stops the
	// timer and must not re-queue anything itself.
	_ = r1.Shutdown(context.Background())
	if s := store.statusOf(scanID); s != "interrupted_quiesced" {
		t.Fatalf("status after shutdown during grace = %s; want interrupted_quiesced", s)
	}

	// Next boot on the same store.
	r2 := registry.New(store, slog.Default(), 4, nil)
	r2.SetScanStandDownStore(settings)
	if err := r2.RegisterOp(scanDef(nil, nil)); err != nil {
		t.Fatalf("RegisterOp (boot 2): %v", err)
	}
	r2.Start(ctx)
	t.Cleanup(func() { _ = r2.Shutdown(context.Background()) })
	awaitStatus(t, store, scanID, "running", 5*time.Second)
}
