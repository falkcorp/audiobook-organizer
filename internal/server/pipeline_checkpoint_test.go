// file: internal/server/pipeline_checkpoint_test.go
// version: 2.1.0
// last-edited: 2026-08-15
// guid: 8a9b0c1d-2e3f-4a70-b8c5-3d7e0f1b9a99
//
// Tests for the server's pipeline checkpoint forwarding layer.
// The actual logic is tested in internal/organizer/checkpoint_test.go.

package server

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

func TestCheckpointForwarding(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Test that server functions forward to organizer package
	if hasCheckpoint(store, "b1", phaseRename) {
		t.Error("should not have checkpoint before set")
	}

	setCheckpoint(store, "b1", phaseRename)

	if !hasCheckpoint(store, "b1", phaseRename) {
		t.Error("should have checkpoint after set")
	}

	clearCheckpoints(store, "b1")

	if hasCheckpoint(store, "b1", phaseRename) {
		t.Error("should be cleared after clearCheckpoints")
	}
}

func TestCleanupStaleCheckpointsForwarding(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Test that the cleanup function is properly forwarded
	count := CleanupStaleCheckpoints(store)
	if count < 0 {
		t.Errorf("CleanupStaleCheckpoints returned negative count: %d", count)
	}
}

func TestConstantsForwarded(t *testing.T) {
	// Verify constants are correctly forwarded
	if phaseRename != organizer.PhaseRename {
		t.Error("phaseRename constant mismatch")
	}
	if phaseTags != organizer.PhaseTags {
		t.Error("phaseTags constant mismatch")
	}
	if phaseITunes != organizer.PhaseITunes {
		t.Error("phaseITunes constant mismatch")
	}
}
