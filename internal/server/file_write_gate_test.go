// file: internal/server/file_write_gate_test.go
// version: 1.1.0
// guid: 2e1c84ce-1e1a-4338-8131-c0d71248fb76
// last-edited: 2026-08-27

package server

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func TestFileWriteGateBlocksUntilSlotReleased(t *testing.T) {
	gate := newFileWriteGate(1)
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire first slot: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		releaseSecond, secondErr := gate.acquire(context.Background())
		if secondErr == nil {
			releaseSecond()
			close(acquired)
		}
	}()

	select {
	case <-acquired:
		t.Fatal("second writer acquired a full gate")
	case <-time.After(25 * time.Millisecond):
	}

	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second writer did not acquire after release")
	}
}

func TestWriteBackWorkersCapsConfiguredValue(t *testing.T) {
	previous := config.AppConfig.MetadataScoring.WriteBackWorkers
	t.Cleanup(func() { config.AppConfig.MetadataScoring.WriteBackWorkers = previous })
	config.AppConfig.MetadataScoring.WriteBackWorkers = maxWriteBackWorkers + 1

	if got := writeBackWorkers(); got != maxWriteBackWorkers {
		t.Fatalf("writeBackWorkers() = %d, want cap %d", got, maxWriteBackWorkers)
	}
}

func TestFileWriteGateHonorsCancellation(t *testing.T) {
	gate := newFileWriteGate(1)
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire first slot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.acquire(ctx); err != context.Canceled {
		t.Fatalf("acquire canceled context = %v, want context.Canceled", err)
	}
}
