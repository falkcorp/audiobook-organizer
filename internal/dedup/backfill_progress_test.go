// file: internal/dedup/backfill_progress_test.go
// version: 1.1.0
// guid: a7b8c9d0-e1f2-a3b4-c5d6-e7f8a9b0c1d2
// last-edited: 2026-07-04

package dedup

import (
	"fmt"
	"testing"
)

func TestNewDedupScanProgressLoggerLogsAtIntervals(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, "logged")
	}

	progressFn := NewDedupScanProgressLogger(10, logf)

	// At done=10, should log
	progressFn("scan", 10, 100)
	if len(logs) != 1 {
		t.Errorf("expected 1 log at done=10, got %d", len(logs))
	}

	// At done=15, no new log
	progressFn("scan", 15, 100)
	if len(logs) != 1 {
		t.Errorf("expected no new log at done=15, got %d total logs", len(logs))
	}

	// At done=20, should log
	progressFn("scan", 20, 100)
	if len(logs) != 2 {
		t.Errorf("expected 2 logs at done=20, got %d", len(logs))
	}
}

func TestNewDedupScanProgressLoggerCompletion(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, "logged")
	}

	progressFn := NewDedupScanProgressLogger(100, logf)

	// At total=100, done=100, should log completion
	progressFn("scan", 100, 100)
	if len(logs) != 1 {
		t.Errorf("expected 1 log at completion, got %d", len(logs))
	}
}

// TestNewDedupScanProgressLoggerPhaseTransitionResetsBucket verifies the
// bucket-crossing threshold resets when the phase changes, so the "score"
// phase (which FullScan now reports after previously reporting nothing at
// all) gets its own full interval of silence budget instead of inheriting
// the "scan" phase's already-advanced threshold and staying silent for its
// own first `interval` books.
func TestNewDedupScanProgressLoggerPhaseTransitionResetsBucket(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	progressFn := NewDedupScanProgressLogger(10, logf)

	// Drive the "scan" phase to completion (done=total=20): should log once
	// at the bucket crossing (10) and once at completion (20).
	progressFn("scan", 10, 20)
	progressFn("scan", 20, 20)
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs after scan phase, got %d: %v", len(logs), logs)
	}

	// Transition to "score": done=10 should log again immediately (a phase
	// transition log for "scan"->"score", then the score phase's own
	// bucket-10 crossing) even though done=10 was already logged in the
	// "scan" phase. If the bucket state weren't reset, done=10 for "score"
	// would never fire (nextLog would already be at 20+ from "scan").
	progressFn("score", 10, 20)
	if len(logs) < 3 {
		t.Fatalf("expected at least 3 logs after phase transition, got %d: %v", len(logs), logs)
	}
	last := logs[len(logs)-1]
	if last != "[INFO] Dedup scan progress (score): 10/20" {
		t.Errorf("expected score-phase bucket log, got %q", last)
	}

	progressFn("score", 20, 20)
	lastLog := logs[len(logs)-1]
	if lastLog != "[INFO] Dedup scan progress (score): 20/20" {
		t.Errorf("expected score-phase completion log, got %q", lastLog)
	}
}

func TestBackfillVersionMarkerConstant(t *testing.T) {
	if BackfillVersionMarker == "" {
		t.Error("BackfillVersionMarker should not be empty")
	}
	if !contains(BackfillVersionMarker, "v") {
		t.Error("BackfillVersionMarker should contain version marker")
	}
}

func contains(s, substr string) bool {
	for i := 0; i < len(s); i++ {
		if s[i:] >= substr && len(s[i:]) >= len(substr) {
			match := true
			for j := 0; j < len(substr); j++ {
				if s[i+j] != substr[j] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}
