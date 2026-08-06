// file: internal/linkintegrity/report_test.go
// version: 1.0.0
// guid: 6b2f81ac-540d-4e37-a9c1-73e5920bd48f
// last-edited: 2026-08-05

package linkintegrity

import (
	"strings"
	"testing"
)

// TestPhaseResultReconciles is the guard against silent filtering — the bug
// class the existing ops' RECONCILE log lines exist to catch. A phase that
// examines N must account for all N.
func TestPhaseResultReconciles(t *testing.T) {
	tests := []struct {
		name string
		p    PhaseResult
		want bool
	}{
		{"exact", PhaseResult{Examined: 10, Actioned: 7, Skipped: 2, Errors: 1}, true},
		{"all skipped", PhaseResult{Examined: 5, Skipped: 5}, true},
		{"empty", PhaseResult{}, true},
		{"lost items", PhaseResult{Examined: 100, Actioned: 30, Skipped: 20}, false},
		{"over-counted", PhaseResult{Examined: 5, Actioned: 9}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Reconciles(); got != tc.want {
				t.Errorf("Reconciles() = %v, want %v (examined=%d actioned=%d skipped=%d errors=%d)",
					got, tc.want, tc.p.Examined, tc.p.Actioned, tc.p.Skipped, tc.p.Errors)
			}
		})
	}
}

func TestReportUnreconciledPhases(t *testing.T) {
	r := Report{
		LibraryTotal: 100,
		Phases: []PhaseResult{
			{Name: "relink", Examined: 10, Actioned: 10},
			{Name: "broken", Examined: 50, Actioned: 1}, // loses 49
			{Name: "orphans", Examined: 3, Skipped: 3},
		},
	}
	bad := r.UnreconciledPhases()
	if len(bad) != 1 || bad[0] != "broken" {
		t.Fatalf("UnreconciledPhases() = %v, want [broken]", bad)
	}
	if !strings.Contains(r.Summary(), "DOES NOT RECONCILE") {
		t.Error("Summary() must visibly flag an unreconciled phase")
	}
}

func TestReportCounts(t *testing.T) {
	r := Report{
		LibraryTotal: 44886,
		DryRun:       true,
		Phases: []PhaseResult{{
			Name:     "unlinked-books",
			Examined: 4,
			Actioned: 2,
			Skipped:  2,
			Findings: []Finding{
				{BookID: "a", Shape: ShapeFile, Action: DispositionLink},
				{BookID: "b", Shape: ShapeFile, Action: DispositionLink},
				{BookID: "c", Shape: ShapeDirectory, Action: DispositionReview},
				{BookID: "d", Shape: ShapeMissing, Action: DispositionReportOnly},
			},
		}},
	}
	sc := r.ShapeCounts()
	if sc[ShapeFile] != 2 || sc[ShapeDirectory] != 1 || sc[ShapeMissing] != 1 {
		t.Errorf("ShapeCounts() = %v", sc)
	}
	ac := r.ActionCounts()
	if ac[DispositionLink] != 2 || ac[DispositionReview] != 1 || ac[DispositionReportOnly] != 1 {
		t.Errorf("ActionCounts() = %v", ac)
	}
}

// TestSummaryStatesDryRun: a report must never be mistakable for an applied run.
// The whole safety model is "dry-run by default, apply is a second explicit
// press" (owner decision D3), so the distinction has to be legible at a glance.
func TestSummaryStatesDryRun(t *testing.T) {
	dry := Report{LibraryTotal: 10, DryRun: true}.Summary()
	if !strings.Contains(dry, "DRY RUN") || !strings.Contains(dry, "no writes") {
		t.Errorf("dry-run summary must say so, got: %q", dry)
	}
	applied := Report{LibraryTotal: 10, DryRun: false}.Summary()
	if !strings.Contains(applied, "APPLIED") {
		t.Errorf("applied summary must say so, got: %q", applied)
	}
	if strings.Contains(applied, "DRY RUN") {
		t.Error("applied summary must not contain DRY RUN")
	}
}

// TestSummaryDeterministic: two identical reports must render byte-identically,
// so operators can diff consecutive runs. Map iteration order would break this.
func TestSummaryDeterministic(t *testing.T) {
	mk := func() Report {
		return Report{
			LibraryTotal: 5,
			DryRun:       true,
			Phases: []PhaseResult{{
				Name: "p", Examined: 3, Actioned: 3,
				Findings: []Finding{
					{Shape: ShapeFile, Action: DispositionLink},
					{Shape: ShapeDirectory, Action: DispositionReview},
					{Shape: ShapeMissing, Action: DispositionReportOnly},
				},
			}},
		}
	}
	for i := 0; i < 20; i++ {
		if a, b := mk().Summary(), mk().Summary(); a != b {
			t.Fatalf("Summary() not deterministic:\n%q\nvs\n%q", a, b)
		}
	}
}
