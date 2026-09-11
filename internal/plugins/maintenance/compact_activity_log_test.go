// file: internal/plugins/maintenance/compact_activity_log_test.go
// version: 1.1.0
// guid: 6e1a9c47-2b5d-4f83-a0e6-9d3c7b2f5e18
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// resultReporter is fakeReporter plus SetResult, so ReporterSetResult has a
// sink and the test can read what the op persisted. It also records every
// UpdateProgress call, which is the liveness signal the watchdog reads.
type resultReporter struct {
	fakeReporter
	result   any
	progress []string
}

func (r *resultReporter) SetResult(v any) error { r.result = v; return nil }
func (r *resultReporter) UpdateProgress(_, _ int, msg string) error {
	r.progress = append(r.progress, msg)
	return nil
}

// compactDeps overrides CompactActivityEntries to behave like the migrating
// store: chunk events, then a Done event per backend, then the summed totals.
type compactDeps struct {
	fakeDeps
	gotCutoff    time.Time
	secondaryErr error
}

func (d *compactDeps) CompactActivityEntries(_ context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error) {
	d.gotCutoff = cutoff
	progress(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 1, EntriesDeleted: 500}})
	progress(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 2, EntriesDeleted: 900}})
	progress(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 2, EntriesDeleted: 900}, Done: true})
	progress(database.CompactProgressEvent{Backend: "sqlite", Result: database.CompactResult{DaysCompacted: 1, EntriesDeleted: 300}})
	progress(database.CompactProgressEvent{Backend: "sqlite", Result: database.CompactResult{DaysCompacted: 1, EntriesDeleted: 300}, Done: true, Err: d.secondaryErr})
	total := database.CompactResult{DaysCompacted: 3, EntriesDeleted: 1200}
	if d.secondaryErr != nil {
		return total, d.secondaryErr
	}
	return total, nil
}

func TestCompactActivityLog_LogsPerBackendAndPersistsTotals(t *testing.T) {
	deps := &compactDeps{}
	p := New(deps)
	rep := &resultReporter{}

	err := p.runCompactActivityLog(context.Background(), json.RawMessage(`{"older_than_days": 7}`), rep)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The cutoff is now minus the requested days.
	want := time.Now().AddDate(0, 0, -7)
	if d := deps.gotCutoff.Sub(want); d < -time.Minute || d > time.Minute {
		t.Errorf("cutoff = %s, want ~%s", deps.gotCutoff, want)
	}

	joined := strings.Join(rep.logs, "\n")
	for _, wantLine := range []string{
		"older than",
		"pebble: compacted 2 days, removed 900 entries",
		"sqlite: compacted 1 days, removed 300 entries",
		"totals: 3 days compacted, 1200 entries removed across 2 backend(s)",
	} {
		if !strings.Contains(joined, wantLine) {
			t.Errorf("log missing %q; got:\n%s", wantLine, joined)
		}
	}

	// Liveness: every chunk event became an UpdateProgress call, plus the
	// initial and final stamps. Three chunk events here.
	if len(rep.progress) < 3+2 {
		t.Errorf("UpdateProgress calls = %d (%v), want the 3 chunk events plus start and end", len(rep.progress), rep.progress)
	}

	res, ok := rep.result.(CompactActivityLogResult)
	if !ok {
		t.Fatalf("persisted result is %T, want CompactActivityLogResult", rep.result)
	}
	if res.DaysCompacted != 3 || res.EntriesDeleted != 1200 {
		t.Errorf("totals = %d/%d, want 3/1200", res.DaysCompacted, res.EntriesDeleted)
	}
	if res.Backends["pebble"].EntriesDeleted != 900 || res.Backends["sqlite"].EntriesDeleted != 300 {
		t.Errorf("per-backend outcomes = %+v", res.Backends)
	}
}

func TestCompactActivityLog_BackendFailureIsSurfacedAndRecorded(t *testing.T) {
	deps := &compactDeps{secondaryErr: errors.New("disk full")}
	p := New(deps)
	rep := &resultReporter{}

	err := p.runCompactActivityLog(context.Background(), json.RawMessage(`{"older_than_days": 0}`), rep)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("run error = %v, want the backend failure surfaced", err)
	}
	joined := strings.Join(rep.logs, "\n")
	if !strings.Contains(joined, "sqlite: FAILED") {
		t.Errorf("log missing the per-backend failure line; got:\n%s", joined)
	}
	res, ok := rep.result.(CompactActivityLogResult)
	if !ok {
		t.Fatalf("result not persisted on failure: %T", rep.result)
	}
	if res.Backends["sqlite"].Error != "disk full" {
		t.Errorf("sqlite outcome = %+v, want the error recorded", res.Backends["sqlite"])
	}
	// The other backend's work is still on record.
	if res.Backends["pebble"].EntriesDeleted != 900 {
		t.Errorf("pebble outcome lost on secondary failure: %+v", res.Backends["pebble"])
	}
}

func TestCompactActivityLog_RejectsNegativeDays(t *testing.T) {
	deps := &compactDeps{}
	p := New(deps)
	err := p.runCompactActivityLog(context.Background(), json.RawMessage(`{"older_than_days": -1}`), &resultReporter{})
	if err == nil {
		t.Fatal("expected an error for a negative day count")
	}
	if !deps.gotCutoff.IsZero() {
		t.Error("the store was called despite invalid params")
	}
}

// TestCleanupActivityLog_ForwardsProgressToReporter pins the same liveness
// wiring on the NIGHTLY op: it declared LivenessManual and never called
// UpdateProgress, so a compaction backlog longer than ProgressTimeout was
// cancelled as never_reported.
func TestCleanupActivityLog_ForwardsProgressToReporter(t *testing.T) {
	p := New(activityCleanupDeps{indexOrphans: 1})
	rep := &resultReporter{}
	if err := p.runCleanupActivityLog(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.progress) == 0 {
		t.Fatal("the scheduled cleanup forwarded no compaction progress to UpdateProgress; the watchdog would strike it never_reported")
	}
	joined := strings.Join(rep.progress, "\n")
	for _, want := range []string{
		"sqlite: 1 days compacted, 40 entries removed",
		// The summarize/prune/repair passes report through the second hook;
		// the nightly op must attach it, or the first night over an
		// unsummarized history is a silent stretch the watchdog strikes.
		"summarize: pebble 3 rows so far",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress missing %q; got:\n%s", want, joined)
		}
	}
}
