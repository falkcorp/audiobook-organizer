// file: internal/plugins/maintenance/nightly_compact_activity_log.go
// version: 1.0.0
// guid: 0b7e4d92-5c1a-4f38-9e26-d8a3f17c6b45
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// NightlyCompactActivityLogDefID is the def the nightly_activity_compaction
// scheduler task enqueues.
const NightlyCompactActivityLogDefID = "maintenance.nightly-compact-activity-log"

// nightlyCompactActivityLogDef compacts every activity entry from before the
// kept days into daily digests, on every activity backend (owner decision
// 2026-09-13: keep only today in full detail).
//
// SAME MACHINERY AS THE MANUAL OP. It computes a different cutoff and then
// calls compactAndRecord, which is CompactActivityEntries plus the per-chunk
// liveness and per-backend result the manual compact-activity-log op reports.
// There is no second compactor.
//
// SCHEDULE. The Schedule field below is documentation and the EnqueueOp
// dedupe arm only: nothing reads OperationDef.Schedule (no cron library in the
// module). The run is driven by the nightly_activity_compaction task in
// internal/scheduler/tasks.go. Because the cutoff is recomputed from the clock
// when the op RUNS, the firing time changes only how stale the oldest full-
// detail entry can get, never what is kept: today is never compacted.
//
// TIMEOUT 6h, like the manual op: a production backlog of ~5.9M rows drains at
// ~10k rows/min, so the first nights are catch-up nights. Each chunk commits
// atomically on both backends (CompactByDay), so a run cut off by the timeout
// leaves nothing half-done and the next night resumes from the data.
//
// CONCURRENCY. Shares the cleanup-activity-log key with the manual op and the
// midnight cleanup, so no two of them ever touch activity rows at once.
// optimize-activity-db (03:40-ish, via the maintenance window) keeps its own
// key: it only refreshes planner statistics (one ANALYZE / PRAGMA optimize),
// deletes nothing, and SQLite serializes its write against a compaction chunk.
// Statistics taken mid-catch-up are merely staler; the next night refreshes
// them.
func (p *Plugin) nightlyCompactActivityLogDef() sdk.OperationDef {
	sched := "10 0 * * *" // documentation only — see above
	return sdk.OperationDef{
		ID:              NightlyCompactActivityLogDefID,
		Liveness:        sdk.LivenessManual,
		ProgressTimeout: 20 * time.Minute, // same reason as compactActivityLogDef
		Plugin:          "maintenance",
		DisplayName:     "Nightly activity compaction",
		Description:     "Collapses every activity entry from before the kept full-detail days (default: before today) into daily digests, on every activity database.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runNightlyCompactActivityLog,
	}
}

// nightlyNow is the op's clock. A variable so tests can pin "now" (including
// its location) without a clock interface on ServerDeps.
var nightlyNow = time.Now

func (p *Plugin) runNightlyCompactActivityLog(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	keep := p.deps.ActivityLogFullDetailDays()
	cutoff, err := nightlyCompactionCutoff(nightlyNow(), keep)
	if err != nil {
		return err
	}
	startMsg := fmt.Sprintf("Nightly compaction: collapsing every activity entry before %s (local midnight, keeping %d full day(s) before today) on every activity database",
		cutoff.Format(time.RFC3339), keep)
	return p.compactAndRecord(ctx, cutoff, startMsg, "nightly-compact-activity-log", reporter)
}

// nightlyCompactionCutoff returns the start of now's local calendar day, moved
// back keepDays local calendar days. Location comes from now, so the server's
// timezone governs.
//
// DST. The day is built with time.Date(y, m, d, 0, 0, 0, 0, loc) and moved with
// AddDate, both of which work in local calendar terms. Never Truncate(24h)
// (that is UTC midnight, wrong in any other zone) and never keepDays*24h (a
// spring-forward day is 23 hours and a fall-back day is 25, so the cutoff
// would drift an hour off midnight). In a zone whose DST jump happens AT
// midnight, local midnight does not exist on that day; time.Date then
// normalizes to the first instant that does (e.g. 01:00), which is still the
// start of that local day.
//
// UTC DIGEST DAYS. Both activity backends bucket digests by UTC day. A local
// midnight is mid-UTC-day in most zones, so the boundary UTC day is compacted
// in two parts on consecutive nights; CompactByDay clamps each pass at the
// cutoff and merges the second pass into the existing digest. Do not "fix"
// this back to UTC midnight: that would compact part of today in zones east of
// UTC, or keep hours of yesterday in zones west of it.
func nightlyCompactionCutoff(now time.Time, keepDays int) (time.Time, error) {
	if keepDays < 0 || keepDays > MaxCompactDays {
		return time.Time{}, fmt.Errorf("nightly-compact-activity-log: activity_log_full_detail_days must be between 0 and %d, got %d",
			MaxCompactDays, keepDays)
	}
	y, m, d := now.Date()
	startOfToday := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return startOfToday.AddDate(0, 0, -keepDays), nil
}
