// file: internal/metafetch/pipeline_checkpoint.go
// version: 2.1.0
// guid: 7f8a9b0c-1d2e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-08-18
//
// Thin forwarding layer — the real implementation now lives in
// internal/organizer/checkpoint.go.

package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// Constants for phase names (forwarded from organizer).
const (
	checkpointPrefix  = organizer.CheckpointPrefix
	phaseRename       = organizer.PhaseRename
	phaseTags         = organizer.PhaseTags
	phaseITunes       = organizer.PhaseITunes
	checkpointTTLDays = organizer.CheckpointTTLDays
)

// setCheckpoint marks a phase as complete for a book (forwarded to organizer).
func setCheckpoint(store database.UserPreferenceStore, bookID, phase string) {
	organizer.SetCheckpoint(store, bookID, phase)
}

// hasCheckpoint returns true if the phase was already completed (forwarded to organizer).
func hasCheckpoint(store database.UserPreferenceStore, bookID, phase string) bool {
	return organizer.HasCheckpoint(store, bookID, phase)
}

// clearCheckpoints removes all phase markers for a book (forwarded to organizer).
func clearCheckpoints(store database.UserPreferenceStore, bookID string) {
	organizer.ClearCheckpoints(store, bookID)
}

// CleanupStaleCheckpoints forwards to organizer.CleanupStaleCheckpoints.
var CleanupStaleCheckpoints = organizer.CleanupStaleCheckpoints
