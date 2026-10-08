// file: internal/organizer/checkpoint_test.go
// version: 1.0.0
// guid: 8a9b0c1d-2e3f-4a70-b8c5-3d7e0f1b9a99

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestCheckpoint_SetAndHas(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if HasCheckpoint(store, "b1", PhaseRename) {
		t.Error("should not have checkpoint before set")
	}

	SetCheckpoint(store, "b1", PhaseRename)

	if !HasCheckpoint(store, "b1", PhaseRename) {
		t.Error("should have checkpoint after set")
	}
	if HasCheckpoint(store, "b1", PhaseTags) {
		t.Error("different phase should not have checkpoint")
	}
	if HasCheckpoint(store, "b2", PhaseRename) {
		t.Error("different book should not have checkpoint")
	}
}

func TestCheckpoint_ClearAll(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	SetCheckpoint(store, "b1", PhaseRename)
	SetCheckpoint(store, "b1", PhaseTags)
	SetCheckpoint(store, "b1", legacyPhaseITunes)

	ClearCheckpoints(store, "b1")

	for _, phase := range []string{PhaseRename, PhaseTags, legacyPhaseITunes} {
		if HasCheckpoint(store, "b1", phase) {
			t.Errorf("phase %s should be cleared", phase)
		}
	}
}

func TestCheckpoint_Idempotent(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	SetCheckpoint(store, "b1", PhaseRename)
	SetCheckpoint(store, "b1", PhaseRename) // double-set
	if !HasCheckpoint(store, "b1", PhaseRename) {
		t.Error("double-set should still have checkpoint")
	}

	ClearCheckpoints(store, "b1")
	ClearCheckpoints(store, "b1") // double-clear
	if HasCheckpoint(store, "b1", PhaseRename) {
		t.Error("double-clear should still be cleared")
	}
}
