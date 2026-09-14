// file: internal/operations/registry/scan_standdown_marker_test.go
// version: 1.0.0
// guid: 9d4a7e21-6c38-4b0f-8e15-3f2b6c90a7d4
// last-edited: 2026-09-13

package registry_test

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// slowMarkerStore blocks the stand-down marker's clear (an empty value) inside
// SetSetting until unblock is closed: a synced store write stuck on a slow disk.
type slowMarkerStore struct {
	*fakeSettingsStore
	entered     chan struct{}
	unblock     chan struct{}
	enterOnce   sync.Once
	unblockOnce sync.Once
}

func (s *slowMarkerStore) SetSetting(key, value, typ string, isSecret bool) error {
	if key == "registry.scan_standdown" && value == "" {
		s.enterOnce.Do(func() { close(s.entered) })
		<-s.unblock
	}
	return s.fakeSettingsStore.SetSetting(key, value, typ, isSecret)
}

func (s *slowMarkerStore) release() { s.unblockOnce.Do(func() { close(s.unblock) }) }

// TestScanStandDown_SlowMarkerWriteDoesNotBlockGate: the last release clears the
// persisted marker with a synced store write. That write must not hold
// scanGate.mu, which the dispatcher claim path and the worker pickup take
// through scanStandDownActive: before 2026-09-13 every op dispatch stalled
// behind the disk sync on each release.
func TestScanStandDown_SlowMarkerWriteDoesNotBlockGate(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	ms := &slowMarkerStore{
		fakeSettingsStore: newFakeSettingsStore(),
		entered:           make(chan struct{}),
		unblock:           make(chan struct{}),
	}
	r.SetScanStandDownStore(ms)
	started := make(chan struct{})
	if err := r.RegisterOp(scanDef(started, nil)); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)
	t.Cleanup(func() {
		ms.release()
		_ = r.Shutdown(t.Context())
	})

	release, err := r.AcquireScanStandDown(ctx, "holder-slow-disk", "test")
	if err != nil {
		t.Fatalf("AcquireScanStandDown: %v", err)
	}
	released := make(chan struct{})
	go func() { release(); close(released) }()
	select {
	case <-ms.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the last release never started clearing the marker")
	}

	// The clear is stuck in SetSetting. A gate reader must not wait for it.
	valid := make(chan struct{})
	go func() { _ = r.ScanStandDownValid("someone-else"); close(valid) }()
	select {
	case <-valid:
	case <-time.After(time.Second):
		t.Fatal("ScanStandDownValid blocked behind the marker write: scanGate.mu is held across the store Set")
	}

	// The dispatcher and worker pickup (both read scanStandDownActive) still
	// start a library scan while the write is stuck.
	if _, err := r.EnqueueOp(ctx, testScanDefID, nil); err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("library.scan did not dispatch while the marker write was stuck")
	}

	ms.release()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not return after the marker write finished")
	}
	if row, _ := ms.GetSetting("registry.scan_standdown"); row == nil || row.Value != "" {
		t.Fatalf("marker after the last release = %+v; want cleared", row)
	}
}

// TestScanStandDown_StaleClearNeverWipesNewerMarker: a clear decided before a
// new acquire, but written after it, must not erase the new holder's marker.
func TestScanStandDown_StaleClearNeverWipesNewerMarker(t *testing.T) {
	ctx := t.Context()
	r := registry.New(newFakeStore(), slog.Default(), 4, nil)
	ms := &slowMarkerStore{
		fakeSettingsStore: newFakeSettingsStore(),
		entered:           make(chan struct{}),
		unblock:           make(chan struct{}),
	}
	r.SetScanStandDownStore(ms)
	r.Start(ctx)
	t.Cleanup(func() {
		ms.release()
		_ = r.Shutdown(t.Context())
	})

	releaseA, err := r.AcquireScanStandDown(ctx, "holder-a", "test")
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	doneA := make(chan struct{})
	go func() { releaseA(); close(doneA) }()
	<-ms.entered // A's clear is decided and stuck inside SetSetting

	// B acquires while A's clear is in flight. B's marker write waits on
	// markerMu behind A's clear, then lands after it.
	doneB := make(chan func())
	go func() {
		rel, err := r.AcquireScanStandDown(ctx, "holder-b", "test")
		if err != nil {
			t.Errorf("acquire B: %v", err)
		}
		doneB <- rel
	}()
	time.Sleep(50 * time.Millisecond)
	ms.release()
	<-doneA
	releaseB := <-doneB
	if releaseB == nil {
		return
	}
	defer releaseB()

	row, _ := ms.GetSetting("registry.scan_standdown")
	if row == nil || row.Value == "" {
		t.Fatal("holder B's marker was wiped by holder A's earlier clear")
	}
}
