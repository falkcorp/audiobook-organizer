// file: internal/plugins/maintenance/optimize_backfill_gate_test.go
// version: 1.0.0
// guid: 8e3a1c96-5b70-4d2f-9a41-c7f0d6b28e53
// last-edited: 2026-08-12

package maintenance

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// optimizeGateDeps records which child ops the optimize sweep actually enqueues.
// Named for this test specifically so it cannot collide with the shared fakeDeps.
type optimizeGateDeps struct {
	fakeDeps
	enqueued []string
}

func (d *optimizeGateDeps) EnqueueOp(_ context.Context, defID string, _ any) (string, error) {
	d.enqueued = append(d.enqueued, defID)
	return "child-" + defID, nil
}

// The whole point of maintenance.acoustid_backfill is that the op's load phase pulls the
// full book table into memory (~862 MB live heap in production, three OOM kills in one
// night). A flag that is declared and defaulted but never read would report "backfill
// disabled" while the sweep kept firing it — so this asserts on what was ENQUEUED, not on
// the flag's value.
func TestOptimize_AcoustIDBackfillRespectsTheFlag(t *testing.T) {
	prev := config.AppConfig.Maintenance.AcoustIDBackfill
	t.Cleanup(func() { config.AppConfig.Maintenance.AcoustIDBackfill = prev })

	run := func(t *testing.T, enabled bool) (*optimizeGateDeps, *fakeReporter) {
		t.Helper()
		config.AppConfig.Maintenance.AcoustIDBackfill = enabled
		deps := &optimizeGateDeps{}
		rep := &fakeReporter{}
		if err := New(deps).runOptimize(context.Background(), nil, rep); err != nil {
			t.Fatalf("runOptimize: %v", err)
		}
		return deps, rep
	}

	contains := func(hay []string, needle string) bool {
		for _, s := range hay {
			if s == needle {
				return true
			}
		}
		return false
	}

	t.Run("disabled excludes the child", func(t *testing.T) {
		deps, rep := run(t, false)
		if contains(deps.enqueued, "acoustid.backfill") {
			t.Fatalf("acoustid.backfill was enqueued with the flag OFF: %v", deps.enqueued)
		}
		// The other children must still run — this gates one op, it does not disable the sweep.
		for _, want := range []string{"maintenance.temp-file-cleanup", "acoustid.fingerprint-rescan", "acoustid.scan"} {
			if !contains(deps.enqueued, want) {
				t.Errorf("%s should still run with the backfill gated off, got %v", want, deps.enqueued)
			}
		}
		// A silent exclusion is the failure mode this whole track is about.
		var announced bool
		for _, msg := range rep.logs {
			if strings.Contains(msg, "acoustid-backfill") && strings.Contains(msg, "Skipping") {
				announced = true
			}
		}
		if !announced {
			t.Errorf("the exclusion must be reported to the operator, got logs %v", rep.logs)
		}
		// Progress totals must not count a child that was never going to run.
		for _, msg := range rep.logs {
			if strings.Contains(msg, "/4:") {
				t.Errorf("progress still counts 4 children after excluding one: %q", msg)
			}
		}
	})

	t.Run("enabled includes the child", func(t *testing.T) {
		deps, _ := run(t, true)
		if !contains(deps.enqueued, "acoustid.backfill") {
			t.Fatalf("acoustid.backfill must run with the flag ON, got %v", deps.enqueued)
		}
	})
}

// The default is the safety property: a fresh install must not fingerprint the whole
// library on boot.
func TestAcoustIDBackfillDefaultsOff(t *testing.T) {
	var c config.MaintenanceConfig
	if c.AcoustIDBackfill {
		t.Fatal("zero value must be false")
	}
}
