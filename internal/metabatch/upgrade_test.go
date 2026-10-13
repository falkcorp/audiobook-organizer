// file: internal/metabatch/upgrade_test.go
// version: 1.0.2
// guid: f1a2b3c4-d5e6-7f8a-9b0c-1d2e3f4a5b6c
// last-edited: 2026-10-13

package metabatch

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Gate threshold scenarios (TASK-03 requirements)
// ---------------------------------------------------------------------------

// TestUpgradeGate_TranscriptionMatchBelowGlobalThreshold verifies that a
// score of 0.87 passes when transcription confirms the candidate (gate=0.85)
// but would be rejected without transcription (gate=0.90).
func TestUpgradeGate_TranscriptionMatchBelowGlobalThreshold(t *testing.T) {
	score := 0.87
	// With transcription: gate is MinUpgradeConfidenceWithTranscription=0.85.
	if score < MinUpgradeConfidenceWithTranscription {
		t.Errorf("score %.2f should pass the transcription gate %.2f", score, MinUpgradeConfidenceWithTranscription)
	}
	// Without transcription: gate is MinUpgradeConfidence=0.90 — should fail.
	if score >= MinUpgradeConfidence {
		t.Errorf("score %.2f should NOT pass the global gate %.2f when transcription does not confirm", score, MinUpgradeConfidence)
	}
}

// TestUpgradeGate_NoTranscriptionBelowThreshold verifies that score=0.87
// without a transcription match is rejected by the global gate.
func TestUpgradeGate_NoTranscriptionBelowThreshold(t *testing.T) {
	score := 0.87
	gate := MinUpgradeConfidence // transcriptionConfirms=false
	if score >= gate {
		t.Errorf("score %.2f should be rejected by gate %.2f when transcription does not confirm", score, gate)
	}
}

// TestUpgradeGate_HighScoreAlwaysPasses verifies that score=0.95 passes
// both gates regardless of transcription.
func TestUpgradeGate_HighScoreAlwaysPasses(t *testing.T) {
	score := 0.95
	if score < MinUpgradeConfidence {
		t.Errorf("score %.2f should pass the global gate %.2f", score, MinUpgradeConfidence)
	}
	if score < MinUpgradeConfidenceWithTranscription {
		t.Errorf("score %.2f should also pass the transcription gate %.2f", score, MinUpgradeConfidenceWithTranscription)
	}
}
