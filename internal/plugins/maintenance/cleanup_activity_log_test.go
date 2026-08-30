// file: internal/plugins/maintenance/cleanup_activity_log_test.go
// version: 1.0.0
// guid: 5c8b1f37-92ad-4e60-b3d1-8a4f26c0e7b9
// last-edited: 2026-08-29

package maintenance

import (
	"context"
	"strings"
	"testing"
)

// activityCleanupDeps overrides fakeDeps' CompactActivityLog so the cleanup job
// sees a run that removed orphaned activity index entries.
type activityCleanupDeps struct {
	fakeDeps
	indexOrphans int64
}

func (d activityCleanupDeps) CompactActivityLog(_ context.Context, _, _, _ int) (int, int, int, int64, error) {
	return 2, 3, 4, d.indexOrphans, nil
}

// TestCleanupActivityLog_ReportsIndexOrphanCount pins the reporting half of the
// index-repair wiring. The repair pass runs inside CompactActivityLog and its
// count is a separate return value; dropping it on the floor here would leave a
// nightly job that silently reclaims hundreds of megabytes and tells the
// operator nothing, which is indistinguishable from a job that did nothing.
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
