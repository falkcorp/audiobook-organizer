// file: internal/plugins/maintenance/compact_activity_log_test.go
// version: 2.0.0
// guid: 6e1a9c47-2b5d-4f83-a0e6-9d3c7b2f5e18
// last-edited: 2026-10-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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

// compactDeps overrides CompactActivityEntries to behave like the Pebble
// store: chunk events with running totals, then the final totals as the
// return value (and storeErr, if set, after the work it did complete).
type compactDeps struct {
	fakeDeps
	gotCutoff time.Time
	storeErr  error
}

func (d *compactDeps) CompactActivityEntries(_ context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error) {
	d.gotCutoff = cutoff
	progress(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 1, EntriesDeleted: 500}})
	progress(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 2, EntriesDeleted: 900}})
	progress(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 3, EntriesDeleted: 1200}})
	return database.CompactResult{DaysCompacted: 3, EntriesDeleted: 1200}, d.storeErr
}

func TestCompactActivityLog_LogsAndPersistsTotals(t *testing.T) {
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
		"totals: 3 days compacted, 1200 entries removed",
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
	// The persisted shape keeps its backends map, with the one store's outcome
	// under "active", so old and new results decode the same way.
	if len(res.Backends) != 1 || res.Backends["active"].EntriesDeleted != 1200 || res.Backends["active"].DaysCompacted != 3 {
		t.Errorf("backends = %+v, want exactly one \"active\" outcome carrying the totals", res.Backends)
	}
}

func TestCompactActivityLog_StoreFailureIsSurfacedAndRecorded(t *testing.T) {
	deps := &compactDeps{storeErr: errors.New("disk full")}
	p := New(deps)
	rep := &resultReporter{}

	err := p.runCompactActivityLog(context.Background(), json.RawMessage(`{"older_than_days": 0}`), rep)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("run error = %v, want the store failure surfaced", err)
	}
	joined := strings.Join(rep.logs, "\n")
	if !strings.Contains(joined, "FAILED after 3 days compacted, 1200 entries removed: disk full") {
		t.Errorf("log missing the failure line with the work already done; got:\n%s", joined)
	}
	res, ok := rep.result.(CompactActivityLogResult)
	if !ok {
		t.Fatalf("result not persisted on failure: %T", rep.result)
	}
	if got := res.Backends["active"]; got.Error != "disk full" || got.EntriesDeleted != 1200 {
		t.Errorf("outcome = %+v, want the error recorded alongside the work that committed", got)
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

// TestCompactActivityLog_RejectsOutOfRangeAndMissingDays: a day count above
// MaxCompactDays overflowed AddDate into a cutoff near now (compact
// EVERYTHING), and a missing/null field decoded to 0 (also everything). Both
// must fail before the store is touched.
func TestCompactActivityLog_RejectsOutOfRangeAndMissingDays(t *testing.T) {
	for _, raw := range []string{
		`{"older_than_days": 36501}`,
		`{"older_than_days": 213503982334601}`,
		`{"older_than_days": 9223372036854775807}`,
		`{}`,
		`{"older_than_days": null}`,
		``,
	} {
		t.Run(raw, func(t *testing.T) {
			deps := &compactDeps{}
			p := New(deps)
			err := p.runCompactActivityLog(context.Background(), json.RawMessage(raw), &resultReporter{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !deps.gotCutoff.IsZero() {
				t.Errorf("the store was called with cutoff %s despite invalid params", deps.gotCutoff)
			}
		})
	}
}

func TestCompactActivityLog_AcceptsMaxDays(t *testing.T) {
	deps := &compactDeps{}
	p := New(deps)
	before := time.Now()
	err := p.runCompactActivityLog(context.Background(), json.RawMessage(`{"older_than_days": 36500}`), &resultReporter{})
	if err != nil {
		t.Fatalf("36500 days: %v", err)
	}
	want := before.AddDate(0, 0, -MaxCompactDays)
	if d := deps.gotCutoff.Sub(want); d < -time.Minute || d > time.Minute {
		t.Errorf("cutoff = %s, want about %s", deps.gotCutoff, want)
	}
}

// TestCompactCutoff_RefusesWrappedCutoff pins the defensive check behind the
// bound: AddDate wraps on overflow, and a wrapped cutoff must be refused.
func TestCompactCutoff_RefusesWrappedCutoff(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, days := range []int{213503982334601, math.MaxInt64} {
		if got, err := compactCutoff(now, days); err == nil {
			t.Errorf("days=%d: cutoff %s accepted; want an overflow error", days, got)
		}
	}
	if got, err := compactCutoff(now, 7); err != nil || !got.Equal(now.AddDate(0, 0, -7)) {
		t.Errorf("days=7: got %s, %v", got, err)
	}
	if got, err := compactCutoff(now, 0); err != nil || !got.Equal(now) {
		t.Errorf("days=0 must mean everything up to now: got %s, %v", got, err)
	}
}

// compactProgressReporter records UpdateProgress numerators.
type compactProgressReporter struct {
	resultReporter
	currents []int
}

func (r *compactProgressReporter) UpdateProgress(cur, total int, msg string) error {
	r.currents = append(r.currents, cur)
	return r.resultReporter.UpdateProgress(cur, total, msg)
}

// TestCompactProgressToReporter_ForwardsRunningTotals pins the adapter: each
// chunk event stamps progress with the store's running total for the call, and
// the message names the day when the event carries one.
func TestCompactProgressToReporter_ForwardsRunningTotals(t *testing.T) {
	rep := &compactProgressReporter{}
	fwd := compactProgressToReporter(rep)
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)

	fwd(database.CompactProgressEvent{Backend: "pebble", Result: database.CompactResult{DaysCompacted: 1, EntriesDeleted: 1719}})
	fwd(database.CompactProgressEvent{Backend: "pebble", Day: day, Result: database.CompactResult{DaysCompacted: 2, EntriesDeleted: 6719}})

	if want := []int{1719, 6719}; !equalInts(rep.currents, want) {
		t.Errorf("UpdateProgress numerators = %v, want %v", rep.currents, want)
	}
	joined := strings.Join(rep.progress, "\n")
	for _, want := range []string{
		"pebble: 1 days compacted, 1719 entries removed so far",
		"pebble: 2 days compacted, 6719 entries removed so far (day 2026-09-07)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress messages missing %q; got:\n%s", want, joined)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		"pebble: 1 days compacted, 40 entries removed",
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
