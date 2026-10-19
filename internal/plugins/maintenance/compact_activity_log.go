// file: internal/plugins/maintenance/compact_activity_log.go
// version: 2.0.0
// guid: 3c8f5a92-6d1b-4e7a-9f04-8b2d6c1e5a37
// last-edited: 2026-10-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// CompactActivityLogDefID is the def id of the user-triggered activity-log
// compaction. POST /api/v1/activity/compact enqueues it; the handler and the
// UI refer to it by this constant.
const CompactActivityLogDefID = "maintenance.compact-activity-log"

// MaxCompactDays is the largest OlderThanDays the compaction accepts (100
// years). It is enforced by the op itself, by POST /api/v1/activity/compact,
// and mirrored as MAX_COMPACT_DAYS in web/src/components/activity/compactDays.ts.
// Without it a huge value overflows time.Time.AddDate and wraps: 213503982334601
// days lands at about now and math.MaxInt64 lands tomorrow, so an absurd
// "keep everything newer than N days" silently compacted EVERYTHING.
const MaxCompactDays = 36500

// CompactActivityLogParams is the op's params payload.
type CompactActivityLogParams struct {
	// OlderThanDays selects rows older than now minus this many days. Zero
	// compacts everything up to now — the same semantics the synchronous
	// handler had.
	OlderThanDays int `json:"older_than_days"`
}

// CompactActivityLogResult is the op's persisted result: the totals, plus the
// same counters under Backends["active"]. Backends dates from the dual-backend
// era, when it held one entry per store so a run that compacted Pebble but
// failed on SQLite read as exactly that; it is kept so results persisted before
// and after the SQLite backend's removal decode to the same shape.
type CompactActivityLogResult struct {
	Cutoff         time.Time                        `json:"cutoff"`
	DaysCompacted  int                              `json:"days_compacted"`
	EntriesDeleted int                              `json:"entries_deleted"`
	Backends       map[string]CompactBackendOutcome `json:"backends"`
}

// CompactBackendOutcome is one backend's share of a CompactActivityLogResult.
type CompactBackendOutcome struct {
	DaysCompacted  int    `json:"days_compacted"`
	EntriesDeleted int    `json:"entries_deleted"`
	Error          string `json:"error,omitempty"`
}

// compactActivityLogDef is the "Compact" button on the Activity page.
//
// WHY AN OP AND NOT A HANDLER. Until 2026-09-10 POST /activity/compact ran the
// whole compaction inside the HTTP request. On production's 13M-row activity
// store that is minutes to hours, and the browser gave up long before —
// leaving the user with a timeout and no way to tell whether anything was
// still happening. As an op the request returns in milliseconds with an op id,
// and the work is visible in the operations list with a live log.
//
// TIMEOUT. The nightly cleanup-activity-log uses 30m for compact + summarize +
// prune + repair, and it only ever has one day's backlog to work through. This
// op has whatever window the user chose — "Everything (now)" on a store that
// has never been compacted. 6h is a ceiling for that worst case, not an
// estimate; a run that is making progress is not cut off by it, and one that
// is stuck is caught by ProgressTimeout long before.
//
// LIVENESS. Declared LivenessManual, and it earns that: the compaction store
// emits a progress event per chunk and per day (database.WithCompactProgress),
// which compactProgressToReporter forwards to reporter.UpdateProgress. Log
// lines alone would not do — reporter.Log does not stamp the watchdog clock
// (registry/types.go, LivenessManual). ProgressTimeout is raised from the 5m
// default to 20m. It was sized for a single chunk statement on the 19.5 GB
// SQLite activity file, measured in minutes under load; that store is gone
// (2026-10-19) and a Pebble chunk is far shorter, but the value is kept rather
// than tightened without a measurement of the Pebble worst case.
//
// CONCURRENCY. Shares cleanup-activity-log's key — as does
// nightly-compact-activity-log — so no two activity compactions (or a
// compaction and the cleanup's summarize/prune/repair) ever run at once.
func (p *Plugin) compactActivityLogDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              CompactActivityLogDefID,
		Liveness:        sdk.LivenessManual,
		ProgressTimeout: 20 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Compact activity log",
		Description:     "Collapses activity entries older than the chosen cutoff into daily digests.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCompactActivityLog,
	}
}

func (p *Plugin) runCompactActivityLog(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeCompactActivityLogParams(raw)
	if err != nil {
		return err
	}
	cutoff, err := compactCutoff(time.Now(), params.OlderThanDays)
	if err != nil {
		return err
	}

	startMsg := fmt.Sprintf("Compacting activity entries older than %s (%d days)",
		cutoff.UTC().Format(time.RFC3339), params.OlderThanDays)
	if params.OlderThanDays == 0 {
		startMsg = fmt.Sprintf("Compacting every activity entry up to %s",
			cutoff.UTC().Format(time.RFC3339))
	}
	return p.compactAndRecord(ctx, cutoff, startMsg, "compact-activity-log", reporter)
}

// compactAndRecord is the reporting half shared by the manual
// compact-activity-log op and the nightly nightly-compact-activity-log op:
// one call to CompactActivityEntries (the single compaction implementation),
// with per-chunk progress forwarded to the watchdog and a persisted
// CompactActivityLogResult. The two ops differ only in how they compute
// cutoff; everything after that is this function, so a fix to the liveness or
// result shape cannot land on one and miss the other.
func (p *Plugin) compactAndRecord(ctx context.Context, cutoff time.Time, startMsg, opName string, reporter sdk.Reporter) error {
	logging.Info(ctx, startMsg)
	_ = reporter.Log(slog.LevelInfo, startMsg)
	// First liveness stamp BEFORE the store is called: with nothing to do
	// there are no chunk events, and the watchdog's never_reported strike must
	// not fire on an op that finished in milliseconds but was slow to persist.
	_ = reporter.UpdateProgress(0, 0, "starting compaction")

	totals, err := p.deps.CompactActivityEntries(ctx, cutoff, compactProgressToReporter(reporter))

	outcome := CompactBackendOutcome{DaysCompacted: totals.DaysCompacted, EntriesDeleted: totals.EntriesDeleted}
	if err != nil {
		outcome.Error = err.Error()
	}
	result := CompactActivityLogResult{
		Cutoff:         cutoff.UTC(),
		DaysCompacted:  totals.DaysCompacted,
		EntriesDeleted: totals.EntriesDeleted,
		Backends:       map[string]CompactBackendOutcome{"active": outcome},
	}

	totalMsg := fmt.Sprintf("Activity compaction totals: %d days compacted, %d entries removed",
		result.DaysCompacted, result.EntriesDeleted)
	if err != nil {
		totalMsg = fmt.Sprintf("Activity compaction FAILED after %d days compacted, %d entries removed: %v",
			result.DaysCompacted, result.EntriesDeleted, err)
		_ = reporter.Log(slog.LevelError, totalMsg)
	} else {
		_ = reporter.Log(slog.LevelInfo, totalMsg)
	}
	logging.Info(ctx, totalMsg)
	_ = reporter.UpdateProgress(result.EntriesDeleted, result.EntriesDeleted, totalMsg)

	if setErr := opsregistry.ReporterSetResult(reporter, result); setErr != nil {
		if err != nil {
			return fmt.Errorf("%s: %w (and persisting the result failed: %v)", opName, err, setErr)
		}
		return fmt.Errorf("%s: persist result: %w", opName, setErr)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", opName, err)
	}
	return nil
}

// decodeCompactActivityLogParams decodes and bounds the op's params.
//
// older_than_days is REQUIRED: a missing or null value used to decode to 0,
// and 0 means "compact everything up to now" — the most destructive choice
// was the default for an empty payload. The only enqueuer is
// POST /api/v1/activity/compact, which always sends the field (explicit 0 is
// still valid), so requiring it here changes nothing for a real caller.
func decodeCompactActivityLogParams(raw json.RawMessage) (CompactActivityLogParams, error) {
	var wire struct {
		OlderThanDays *int `json:"older_than_days"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &wire); err != nil {
			return CompactActivityLogParams{}, fmt.Errorf("compact-activity-log: bad params: %w", err)
		}
	}
	if wire.OlderThanDays == nil {
		return CompactActivityLogParams{}, fmt.Errorf("compact-activity-log: older_than_days is required (0 compacts everything up to now)")
	}
	days := *wire.OlderThanDays
	if days < 0 {
		return CompactActivityLogParams{}, fmt.Errorf("compact-activity-log: older_than_days must be zero or positive, got %d", days)
	}
	if days > MaxCompactDays {
		return CompactActivityLogParams{}, fmt.Errorf("compact-activity-log: older_than_days must be between 0 and %d, got %d", MaxCompactDays, days)
	}
	return CompactActivityLogParams{OlderThanDays: days}, nil
}

// compactCutoff returns now minus days. Defensive second line behind the
// MaxCompactDays bound: AddDate wraps silently on overflow, so a positive day
// count whose cutoff is not strictly before now is refused rather than
// compacting everything.
func compactCutoff(now time.Time, days int) (time.Time, error) {
	cutoff := now.AddDate(0, 0, -days)
	if days > 0 && !cutoff.Before(now) {
		return time.Time{}, fmt.Errorf("compact-activity-log: older_than_days %d overflows the cutoff (got %s, not before %s)",
			days, cutoff.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return cutoff, nil
}

// compactProgressToReporter adapts the compaction store's progress events to
// reporter.UpdateProgress. Total is unknown up front (the store bounds its
// loop by the data, not by a pre-count), so it is reported as 0 and the UI
// shows an indeterminate bar with the running count. ev.Result is the store's
// running total for this CompactByDay call, so it is forwarded as is.
//
// Done and Heartbeat handling lived here while a migrating wrapper fanned
// compaction out across a Pebble and a SQLite store (per-backend completion
// stamps, a cross-backend cumulative counter, liveness-only SQLite scan
// windows). The single Pebble store emits none of those, so they went with
// the SQLite backend on 2026-10-19.
func compactProgressToReporter(reporter sdk.Reporter) database.CompactProgress {
	return func(ev database.CompactProgressEvent) {
		msg := fmt.Sprintf("%s: %d days compacted, %d entries removed so far",
			ev.Backend, ev.Result.DaysCompacted, ev.Result.EntriesDeleted)
		if !ev.Day.IsZero() {
			msg += fmt.Sprintf(" (day %s)", ev.Day.UTC().Format("2006-01-02"))
		}
		_ = reporter.UpdateProgress(ev.Result.EntriesDeleted, 0, msg)
	}
}

// maintenanceProgressToReporter adapts the summarize/prune/repair passes'
// progress events to reporter.UpdateProgress, for the nightly cleanup.
//
// The progress COUNTER is cumulative across the whole op, which ev.Rows is not:
// the store restarts its count at zero for each phase, so forwarding ev.Rows
// raw made the op's progress bar jump backwards at every phase boundary. The
// store's emitters (PebbleActivityStore.Summarize and .Prune) report a running
// total within their own phase, so keeping the latest value per phase and
// summing those is the row count actually deleted so far. The map is guarded
// because the hook rides a context and nothing promises a pass stays
// single-goroutine.
func maintenanceProgressToReporter(reporter sdk.Reporter) database.MaintenanceProgress {
	type phaseBackend struct{ phase, backend string }
	var (
		mu     sync.Mutex
		latest = map[phaseBackend]int64{}
	)
	return func(ev database.MaintenanceProgressEvent) {
		mu.Lock()
		latest[phaseBackend{ev.Phase, ev.Backend}] = ev.Rows
		var total int64
		for _, n := range latest {
			total += n
		}
		mu.Unlock()

		var msg string
		if ev.Backend == "" {
			// A phase boundary stamped by the caller (see Server.MaintainActivityLog):
			// the first liveness a pass has before its first batch.
			msg = fmt.Sprintf("%s: starting", ev.Phase)
		} else {
			msg = fmt.Sprintf("%s: %s %d rows so far", ev.Phase, ev.Backend, ev.Rows)
		}
		_ = reporter.UpdateProgress(int(total), 0, msg)
	}
}
