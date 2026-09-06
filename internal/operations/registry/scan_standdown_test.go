// file: internal/operations/registry/scan_standdown_test.go
// version: 1.0.0
// guid: b4e7c9a1-2d63-4f85-9c07-1a6e8d2f5b3c
// last-edited: 2026-09-06

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

const testScanDefID = "library.scan"

// fakeSettingsStore is a minimal in-memory database.SettingsStore for exercising
// the persisted scan stand-down marker.
type fakeSettingsStore struct {
	mu   sync.Mutex
	vals map[string]database.Setting
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{vals: make(map[string]database.Setting)}
}

func (s *fakeSettingsStore) GetSetting(key string) (*database.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vals[key]
	if !ok {
		return nil, nil
	}
	cp := v
	return &cp, nil
}

func (s *fakeSettingsStore) SetSetting(key, value, typ string, isSecret bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals[key] = database.Setting{Key: key, Value: value, Type: typ, IsSecret: isSecret}
	return nil
}

// scanDef returns a library.scan-shaped def whose Run blocks on ctx until the
// scan is quiesced. onExit (if non-nil) records that Run has returned, so a test
// can assert AcquireScanStandDown only returned after the scan goroutine parked.
func scanDef(onStart chan<- struct{}, onExit *atomic.Bool) registry.OperationDef {
	var startOnce sync.Once
	d := makeValidDef(testScanDefID)
	d.DisplayName = "Library Scan"
	d.ResumePolicy = registry.ResumeRestart
	d.ConcurrencyKey = testScanDefID
	d.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		if onExit != nil {
			defer onExit.Store(true)
		}
		// Report progress once so HighWaterProgress>0 (mirrors a scan that has
		// done real work; keeps checkInfiniteRestart from ever applying).
		rep.UpdateProgress(1, 100, "scanning")
		if onStart != nil {
			startOnce.Do(func() { close(onStart) })
		}
		<-runCtx.Done()
		return runCtx.Err()
	}
	return d
}

func TestScanStandDown_NoRunningScan_AcquireRelease(t *testing.T) {
	r, _ := newTestRegistry(t)
	const holder = "holder-1"

	release, err := r.AcquireScanStandDown(t.Context(), holder, "test")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	if !r.ScanStandDownValid(holder) {
		t.Fatal("expected holder to be valid while held")
	}
	release()
	if r.ScanStandDownValid(holder) {
		t.Fatal("expected holder to be invalid after release")
	}
	// Idempotent: a second release must not panic or re-invalidate anything.
	release()
}

func TestScanStandDown_QuiescesRunningScanAndWaitsForPark(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	started := make(chan struct{})
	var exited atomic.Bool
	if err := r.RegisterOp(scanDef(started, &exited)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)

	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	<-started // scan is running and blocked on ctx

	release, err := r.AcquireScanStandDown(ctx, "holder-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	// The load-bearing guarantee: Acquire only returns once the scan goroutine
	// has truly exited (parked), so an apply can safely start writing.
	if !exited.Load() {
		t.Fatal("AcquireScanStandDown returned before the scan goroutine parked")
	}
	// The quiesced scan must be recorded resumable, not "canceled".
	awaitStatus(t, store, scanID, "interrupted_quiesced", 3*time.Second)

	// Releasing the last holder re-queues the scan from its checkpoint; the
	// dispatcher then runs it again.
	release()
	awaitStatus(t, store, scanID, "running", 5*time.Second)
}

func TestScanStandDown_BlocksNewScanWhileHeldThenDispatches(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	if err := r.RegisterOp(scanDef(nil, nil)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)

	// Hold the gate BEFORE any scan exists.
	release, err := r.AcquireScanStandDown(ctx, "holder-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}

	scanID, err := r.EnqueueOp(ctx, testScanDefID, nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	// Gate 3.5: the scan must stay queued while the gate is held. Give the
	// dispatcher several cycles to (wrongly) pick it up.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s := store.statusOf(scanID); s != "queued" {
			t.Fatalf("scan dispatched while gate held: status=%s", s)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Release → the scan is free to dispatch.
	release()
	awaitStatus(t, store, scanID, "running", 5*time.Second)
}

func TestScanStandDown_LeaseExpiryInvalidatesHolder(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 4,
		registry.Options{ScanStandDownLease: 60 * time.Millisecond})

	release, err := r.AcquireScanStandDown(t.Context(), "holder-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	defer release()

	if !r.ScanStandDownValid("holder-1") {
		t.Fatal("expected valid immediately after acquire")
	}
	time.Sleep(120 * time.Millisecond)
	if r.ScanStandDownValid("holder-1") {
		t.Fatal("expected holder invalid after lease expiry")
	}
}

func TestScanStandDown_RenewExtendsLeaseAndFailsAfterRelease(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 4,
		registry.Options{ScanStandDownLease: 100 * time.Millisecond})

	release, err := r.AcquireScanStandDown(t.Context(), "holder-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	// Renew a few times across most of a lease each time; the holder must stay
	// valid throughout, proving renewal actually extends the lease.
	for i := 0; i < 3; i++ {
		time.Sleep(70 * time.Millisecond)
		if !r.RenewScanStandDown("holder-1") {
			t.Fatalf("renew %d returned false while still held", i)
		}
	}
	if !r.ScanStandDownValid("holder-1") {
		t.Fatal("expected valid after renewals")
	}
	release()
	if r.RenewScanStandDown("holder-1") {
		t.Fatal("expected renew to fail after release")
	}
}

func TestScanStandDown_MarkerPersistedAndClearedOnRelease(t *testing.T) {
	store := newFakeStore()
	settings := newFakeSettingsStore()
	r := registry.New(store, slog.Default(), 4, nil)
	r.SetScanStandDownStore(settings)

	release, err := r.AcquireScanStandDown(t.Context(), "holder-1", "apply")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	if got, _ := settings.GetSetting("registry.scan_standdown"); got == nil || got.Value == "" {
		t.Fatal("expected a persisted marker while held")
	}
	release()
	if got, _ := settings.GetSetting("registry.scan_standdown"); got != nil && got.Value != "" {
		t.Fatalf("expected marker cleared after release, got %q", got.Value)
	}
}

func TestScanStandDown_BootConsultDefersScanResume(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	settings := newFakeSettingsStore()

	// Simulate a reboot mid-apply: an interrupted_quiesced library.scan row plus
	// a surviving stand-down marker naming it.
	const scanID = "scan-op-1"
	store.insertQueuedAtomic(database.OperationV2Row{
		ID:     scanID,
		DefID:  testScanDefID,
		Plugin: "test",
		Status: "interrupted_quiesced",
	})
	marker, _ := json.Marshal(map[string]any{
		"holder_op_id":           "holder-dead",
		"scan_op_id":             scanID,
		"lease_expiry_unix_nano": time.Now().Add(time.Minute).UnixNano(),
	})
	_ = settings.SetSetting("registry.scan_standdown", string(marker), "json", false)

	r := registry.New(store, slog.Default(), 4, nil)
	r.SetScanStandDownStore(settings)
	if err := r.RegisterOp(scanDef(nil, nil)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx) // runs resumeAfterStartup

	// The scan must NOT be resumed this boot; it stays interrupted_quiesced and
	// the marker is cleared for the next boot to reconcile.
	time.Sleep(300 * time.Millisecond)
	if s := store.statusOf(scanID); s != "interrupted_quiesced" {
		t.Fatalf("expected scan left interrupted_quiesced at boot, got %s", s)
	}
	if got, _ := settings.GetSetting("registry.scan_standdown"); got != nil && got.Value != "" {
		t.Fatalf("expected marker cleared at boot, got %q", got.Value)
	}
}
