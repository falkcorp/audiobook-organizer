// file: internal/plugins/maintenance/optimize_activity_db_def_test.go
// version: 1.0.0
// guid: 5918b106-671a-4c47-b11b-5435260e93d8
// last-edited: 2026-09-11

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The op's only work is one SQL statement that cannot report progress. Under
// LivenessManual the watchdog's default 5m ProgressTimeout killed the ANALYZE
// bootstrap on every run (prod, 2026-09-10 and 09-11), long before the 15m
// Timeout, so the statistics were never stored.
func TestOptimizeActivityDBDef_BudgetCoversTheBootstrap(t *testing.T) {
	def := (&Plugin{}).optimizeActivityDBDef()
	if def.Liveness != sdk.LivenessNone {
		t.Fatalf("Liveness = %v, want LivenessNone: the run never calls UpdateProgress", def.Liveness)
	}
	if def.ProgressTimeout < def.Timeout {
		t.Fatalf("ProgressTimeout %v < Timeout %v: the watchdog would kill the op before its own timeout",
			def.ProgressTimeout, def.Timeout)
	}
}
