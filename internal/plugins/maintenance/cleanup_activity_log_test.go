// file: internal/plugins/maintenance/cleanup_activity_log_test.go
// version: 1.4.0
// guid: 5c8b1f37-92ad-4e60-b3d1-8a4f26c0e7b9
// last-edited: 2026-09-13

package maintenance

import (
	"context"

	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// activityCleanupDeps overrides fakeDeps' CompactActivityLog so the cleanup job
// sees a run that removed orphaned activity index entries.
type activityCleanupDeps struct {
	fakeDeps
	indexOrphans int64
	nightly      bool
	compactCalls *int
}

func (d activityCleanupDeps) ActivityLogNightlyCompactionEnabled() bool { return d.nightly }

func (d activityCleanupDeps) CompactActivityEntries(_ context.Context, _ time.Time, progress database.CompactProgress) (database.CompactResult, error) {
	if d.compactCalls != nil {
		*d.compactCalls++
	}
	// Emit one event the way a real backend would, so the test below can pin
	// that the scheduled op forwards it to UpdateProgress.
	if progress != nil {
		progress(database.CompactProgressEvent{Backend: "sqlite", Result: database.CompactResult{DaysCompacted: 1, EntriesDeleted: 40}})
	}
	return database.CompactResult{DaysCompacted: 2, EntriesDeleted: 40}, nil
}

func (d activityCleanupDeps) MaintainActivityLog(ctx context.Context, _, _ int) (int, int, int64, error) {
	// One summarize progress event the way a store would, via the maintenance
	// hook the op is expected to have attached to ctx.
	database.ReportMaintenanceProgress(ctx, database.MaintenancePhaseSummarize, "pebble", 3)
	return 3, 4, d.indexOrphans, nil
}

// TestCleanupActivityLog_SkipsCompactionWhenNightlyEnabled pins requirement 3:
// with nightly compaction on, the midnight cleanup must not run a second,
// differently-cut compaction — while summarize/prune/repair still run.
func TestCleanupActivityLog_SkipsCompactionWhenNightlyEnabled(t *testing.T) {
	for _, tc := range []struct {
		nightly   bool
		wantCalls int
	}{{nightly: true, wantCalls: 0}, {nightly: false, wantCalls: 1}} {
		calls := 0
		p := New(activityCleanupDeps{indexOrphans: 5, nightly: tc.nightly, compactCalls: &calls})
		rep := &fakeReporter{}
		if err := p.runCleanupActivityLog(context.Background(), nil, rep); err != nil {
			t.Fatalf("nightly=%v: %v", tc.nightly, err)
		}
		if calls != tc.wantCalls {
			t.Errorf("nightly=%v: CompactActivityEntries called %d times, want %d", tc.nightly, calls, tc.wantCalls)
		}
		joined := strings.Join(rep.logs, "\n")
		if !strings.Contains(joined, "summarized 3") || !strings.Contains(joined, "pruned 4") {
			t.Errorf("nightly=%v: summarize/prune must still run; logs:\n%s", tc.nightly, joined)
		}
	}
}

func (d activityCleanupDeps) OptimizeActivityStatistics(_ context.Context) (database.ActivityOptimizeResult, error) {
	return database.ActivityOptimizeResult{Supported: true, TablesAnalyzed: 6}, nil
}

// TestCleanupActivityLog_ReportsIndexOrphanCount pins the reporting half of the
// index-repair wiring. The repair pass runs inside CompactActivityLog and its
// count is a separate return value; dropping it on the floor here would leave a
// nightly job that silently deletes millions of index keys and tells the
// operator nothing, which is indistinguishable from a job that did nothing.
// (Deliberately "keys", not "megabytes": a Pebble delete writes a tombstone and
// frees no space until a later compaction.)
func TestCleanupActivityLog_ReportsIndexOrphanCount(t *testing.T) {
	p := New(activityCleanupDeps{indexOrphans: 7411})
	rep := &fakeReporter{}

	if err := p.runCleanupActivityLog(context.Background(), nil, rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(rep.logs, "\n")
	if !strings.Contains(joined, "7411") {
		t.Errorf("cleanup must report how many orphaned index entries it removed; logs were:\n%s", joined)
	}
	// The other three counters must still be reported, so this test cannot pass
	// by a message that reports only the new field.
	for _, want := range []string{"compacted 2", "summarized 3", "pruned 4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("cleanup message lost %q; logs were:\n%s", want, joined)
		}
	}
}
