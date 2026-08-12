// file: internal/organizer/organize_outcome_test.go
// version: 1.0.0
// guid: 5a9e2c71-4d38-4b6f-90ac-1e7f3b28d605
// last-edited: 2026-08-12

// Regression tests for how a finished organize run reports itself.
//
// PerformOrganize used to end in an unconditional `return nil`, and the logged
// summary listed organized / re-organized / already-correct / skipped but NOT
// failed. Together those meant a run in which every single book failed
// returned success to its caller AND printed
//
//	Organize complete: 0 organized, 0 re-organized, 0 already correct (stamped), 0 skipped
//
// which is indistinguishable from a run that had nothing to do. A cancelled run
// was equally invisible: it also said "complete".
//
// These tests pin both halves — the error contract and the exact user-visible
// text — so neither can quietly regress to reporting success.

package organizer

import (
	"strings"
	"testing"
)

func TestOrganizeOutcomeError(t *testing.T) {
	cases := []struct {
		name    string
		stats   Stats
		wantErr bool
		wantIn  string
	}{
		{
			name:    "total failure is an error",
			stats:   Stats{Failed: 3194, Total: 3194},
			wantErr: true,
			wantIn:  "failed for all 3194 books",
		},
		{
			name:    "partial failure is NOT an error",
			stats:   Stats{Organized: 2999, Failed: 1, Total: 3000},
			wantErr: false,
		},
		{
			name:    "one success among many failures is NOT an error",
			stats:   Stats{Organized: 1, Failed: 2999, Total: 3000},
			wantErr: false,
		},
		{
			name:    "already-correct counts as success, so not a total failure",
			stats:   Stats{AlreadyCorrect: 5, Failed: 10, Total: 15},
			wantErr: false,
		},
		{
			name:    "re-organized counts as success too",
			stats:   Stats{ReOrganized: 2, Failed: 10, Total: 12},
			wantErr: false,
		},
		{
			name:    "nothing to do is success, not failure",
			stats:   Stats{Total: 0},
			wantErr: false,
		},
		{
			name:    "all skipped with no failures is success",
			stats:   Stats{Skipped: 40, Total: 40},
			wantErr: false,
		},
		{
			name:    "cancelled is an error even when nothing failed",
			stats:   Stats{Organized: 12, Total: 3000, Canceled: true},
			wantErr: true,
			wantIn:  "canceled",
		},
		{
			// Cancellation must win over total failure: a cancelled run has an
			// unknown outcome, not a bad one, and reporting it as "failed for
			// all N books" would misdescribe books it never reached.
			name:    "cancelled takes precedence over total failure",
			stats:   Stats{Failed: 4, Total: 3000, Canceled: true},
			wantErr: true,
			wantIn:  "canceled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := organizeOutcomeError(&tc.stats)
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error for %+v, got nil — this is the "+
					"'reports success on total failure' defect", tc.stats)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected success for %+v, got %v — a partial failure "+
					"must not fail the whole operation", tc.stats, err)
			}
			if tc.wantIn != "" && !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q should mention %q", err.Error(), tc.wantIn)
			}
		})
	}
}

func TestOrganizeOutcomeErrorNilStats(t *testing.T) {
	if err := organizeOutcomeError(nil); err != nil {
		t.Fatalf("nil stats should not manufacture an error, got %v", err)
	}
}

// TestFormatOrganizeSummaryAlwaysReportsFailures is the core regression on the
// user-visible text: the failure count must be present even when every other
// counter is zero, which is exactly the shape a total failure produces.
func TestFormatOrganizeSummaryAlwaysReportsFailures(t *testing.T) {
	got := formatOrganizeSummary(&Stats{Failed: 3194, Total: 3194})

	if !strings.Contains(got, "3194 FAILED") {
		t.Errorf("summary must state the failure count; got %q", got)
	}
	if !strings.Contains(got, "3194 total") {
		t.Errorf("summary must state the total attempted; got %q", got)
	}
	// The old text for this exact case. If it ever comes back, a run where
	// every book failed once again reads as a harmless no-op.
	if got == "Organize complete: 0 organized, 0 re-organized, 0 already correct (stamped), 0 skipped" {
		t.Error("summary regressed to the pre-fix text that hid a total failure")
	}
}

func TestFormatOrganizeSummaryDistinguishesCancellation(t *testing.T) {
	finished := formatOrganizeSummary(&Stats{Organized: 10, Total: 10})
	if !strings.Contains(finished, "Organize complete") {
		t.Errorf("a finished run should say complete; got %q", finished)
	}

	canceled := formatOrganizeSummary(&Stats{Organized: 10, Total: 3000, Canceled: true})
	if strings.Contains(canceled, "Organize complete") {
		t.Errorf("a cancelled run must NOT claim completion; got %q", canceled)
	}
	if !strings.Contains(canceled, "CANCELED") {
		t.Errorf("a cancelled run should say so; got %q", canceled)
	}
}
