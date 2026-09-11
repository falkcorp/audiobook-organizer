// file: internal/plugins/maintenance/compact_activity_log.go
// version: 1.3.0
// guid: 3c8f5a92-6d1b-4e7a-9f04-8b2d6c1e5a37
// last-edited: 2026-09-10

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

// CompactActivityLogParams is the op's params payload.
type CompactActivityLogParams struct {
	// OlderThanDays selects rows older than now minus this many days. Zero
	// compacts everything up to now — the same semantics the synchronous
	// handler had.
	OlderThanDays int `json:"older_than_days"`
}

// CompactActivityLogResult is the op's persisted result: the cross-backend
// totals plus each backend's own counters, so a run that compacted Pebble but
// failed on SQLite is readable as exactly that.
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
// has never been compacted — and it now runs on BOTH activity backends, the
// Pebble one being the path the migrating store used to route around as
// "unbounded". 6h is a ceiling for that worst case, not an estimate; a run
// that is making progress is not cut off by it, and one that is stuck is
// caught by ProgressTimeout long before.
//
// LIVENESS. Declared LivenessManual, and it earns that: the compaction store
// emits a progress event per chunk and per day (database.WithCompactProgress),
// which compactProgressToReporter forwards to reporter.UpdateProgress. Log
// lines alone would not do — reporter.Log does not stamp the watchdog clock
// (registry/types.go, LivenessManual). ProgressTimeout is raised from the 5m
// default to 20m because a single chunk statement on a 19.5 GB SQLite file
// under load has been measured in the minutes (see optimizeActivityDBDef).
//
// CONCURRENCY. Shares cleanup-activity-log's key so the nightly job and a
// button press never compact the same rows at once.
func (p *Plugin) compactActivityLogDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              CompactActivityLogDefID,
		Liveness:        sdk.LivenessManual,
		ProgressTimeout: 20 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Compact activity log",
		Description:     "Collapses activity entries older than the chosen cutoff into daily digests, on every activity database.",
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
	var params CompactActivityLogParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("compact-activity-log: bad params: %w", err)
		}
	}
	if params.OlderThanDays < 0 {
		return fmt.Errorf("compact-activity-log: older_than_days must be zero or positive, got %d", params.OlderThanDays)
	}
	cutoff := time.Now().AddDate(0, 0, -params.OlderThanDays)

	startMsg := fmt.Sprintf("Compacting activity entries older than %s (%d days) on every activity database",
		cutoff.UTC().Format(time.RFC3339), params.OlderThanDays)
	if params.OlderThanDays == 0 {
		startMsg = fmt.Sprintf("Compacting every activity entry up to %s on every activity database",
			cutoff.UTC().Format(time.RFC3339))
	}
	logging.Info(ctx, startMsg)
	_ = reporter.Log(slog.LevelInfo, startMsg)
	// First liveness stamp BEFORE the store is called: on a backend with
	// nothing to do there are no chunk events, and the watchdog's
	// never_reported strike must not fire on an op that finished in
	// milliseconds but was slow to persist.
	_ = reporter.UpdateProgress(0, 0, "starting compaction")

	result := CompactActivityLogResult{Cutoff: cutoff.UTC(), Backends: map[string]CompactBackendOutcome{}}
	var mu sync.Mutex
	forward := compactProgressToReporter(reporter)
	progress := func(ev database.CompactProgressEvent) {
		forward(ev)
		if !ev.Done {
			return
		}
		outcome := CompactBackendOutcome{DaysCompacted: ev.Result.DaysCompacted, EntriesDeleted: ev.Result.EntriesDeleted}
		msg := fmt.Sprintf("%s: compacted %d days, removed %d entries", ev.Backend, ev.Result.DaysCompacted, ev.Result.EntriesDeleted)
		if ev.Err != nil {
			outcome.Error = ev.Err.Error()
			msg = fmt.Sprintf("%s: FAILED after compacting %d days, removing %d entries: %v",
				ev.Backend, ev.Result.DaysCompacted, ev.Result.EntriesDeleted, ev.Err)
			_ = reporter.Log(slog.LevelError, msg)
		} else {
			_ = reporter.Log(slog.LevelInfo, msg)
		}
		logging.Info(ctx, msg)
		mu.Lock()
		result.Backends[ev.Backend] = outcome
		mu.Unlock()
	}

	totals, err := p.deps.CompactActivityEntries(ctx, cutoff, progress)
	mu.Lock()
	result.DaysCompacted = totals.DaysCompacted
	result.EntriesDeleted = totals.EntriesDeleted
	if len(result.Backends) == 0 {
		// A single-backend deployment (no migrating wrapper) emits no Done
		// events; record the one outcome under a neutral label so the result
		// never reads as "no backend ran".
		outcome := CompactBackendOutcome{DaysCompacted: totals.DaysCompacted, EntriesDeleted: totals.EntriesDeleted}
		if err != nil {
			outcome.Error = err.Error()
		}
		result.Backends["active"] = outcome
	}
	mu.Unlock()

	totalMsg := fmt.Sprintf("Activity compaction totals: %d days compacted, %d entries removed across %d backend(s)",
		result.DaysCompacted, result.EntriesDeleted, len(result.Backends))
	logging.Info(ctx, totalMsg)
	_ = reporter.Log(slog.LevelInfo, totalMsg)
	_ = reporter.UpdateProgress(result.EntriesDeleted, result.EntriesDeleted, totalMsg)

	if setErr := opsregistry.ReporterSetResult(reporter, result); setErr != nil {
		if err != nil {
			return fmt.Errorf("compact-activity-log: %w (and persisting the result failed: %v)", err, setErr)
		}
		return fmt.Errorf("compact-activity-log: persist result: %w", setErr)
	}
	if err != nil {
		return fmt.Errorf("compact-activity-log: %w", err)
	}
	return nil
}

// compactProgressToReporter adapts the compaction store's progress events to
// reporter.UpdateProgress. Total is unknown up front (the store bounds its
// loop by the data, not by a pre-count), so it is reported as 0 and the UI
// shows an indeterminate bar with the running count. Done events are skipped:
// the caller logs those with the backend's final numbers.
func compactProgressToReporter(reporter sdk.Reporter) database.CompactProgress {
	return func(ev database.CompactProgressEvent) {
		if ev.Done {
			return
		}
		_ = reporter.UpdateProgress(ev.Result.EntriesDeleted, 0,
			fmt.Sprintf("%s: %d days compacted, %d entries removed so far",
				ev.Backend, ev.Result.DaysCompacted, ev.Result.EntriesDeleted))
	}
}

// maintenanceProgressToReporter adapts the summarize/prune/repair passes'
// progress events to reporter.UpdateProgress, for the nightly cleanup. Unlike
// compactProgressToReporter it forwards Done events too: the nightly op logs
// only the four final totals, so a per-backend completion is the only place a
// backend's own number (and failure) is ever seen, and it is a liveness stamp
// at the moment the next backend's silent stretch begins.
// The progress COUNTER is cumulative across the whole op, which ev.Rows is not:
// each backend restarts its count at zero for each phase, so forwarding ev.Rows
// raw made the op's progress bar jump backwards three times a run. Both
// non-terminal emitters (SQLActivityStore.Summarize, PebbleActivityStore
// .Summarize) report a running total within their own phase+backend, and the
// Done event carries that same backend's final number, so keeping the latest
// value per phase+backend and summing those is the row count actually deleted
// so far. The map is guarded because the hook rides a context and nothing
// promises the fan-out stays single-goroutine.
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
		switch {
		case ev.Done && ev.Err != nil:
			msg = fmt.Sprintf("%s: %s FAILED after %d rows: %v", ev.Phase, ev.Backend, ev.Rows, ev.Err)
			_ = reporter.Log(slog.LevelError, msg)
		case ev.Done:
			msg = fmt.Sprintf("%s: %s done, %d rows", ev.Phase, ev.Backend, ev.Rows)
		case ev.Backend == "":
			// A phase boundary stamped by the caller (see Server.CompactActivityLog):
			// the only liveness a context-free pass like Prune can have.
			msg = fmt.Sprintf("%s: starting", ev.Phase)
		default:
			msg = fmt.Sprintf("%s: %s %d rows so far", ev.Phase, ev.Backend, ev.Rows)
		}
		_ = reporter.UpdateProgress(int(total), 0, msg)
	}
}
