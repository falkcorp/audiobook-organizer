// file: internal/scheduler/tasks.go
// version: 1.34.1
// guid: 9b4c7e21-a5f3-4d08-b2e6-3c8d1f7a0e54
// last-edited: 2026-10-16

// Package scheduler — task registrations.
// Every registered task is defined here. Each task's TriggerFn and
// IsEnabled read from SchedulerDeps (not *Server) so the scheduler package
// remains independent of the server package.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// ---- param types -------------------------------------------------------
// These types mirror the JSON wire shapes defined in server/{library_core_ops,
// duplicates_ops}.go. They are intentionally minimal — only the fields used
// when the scheduler triggers the operations.

type libraryOrganizeParams struct{}

type librarySizeRefreshParams struct{}

// The three dedup ops below take their params types from internal/dedup
// directly rather than mirroring them here. These used to be local copies
// carrying a legacy_op_id the ops stopped reading on 2026-08-22, and nothing
// could catch that: a mirror is coupled to the real type only by JSON tags, so
// it drifts silently. seriesPruneOpParams had drifted twice over — it declared
// legacy_op_id and omitted Detail, the field the real type actually has.

// schedulerExtraOpParams carries the v1 operation ID into the Run func.
type schedulerExtraOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

// asin_backfill task: the def it enqueues, its interval, and the params it
// sends. asinBackfillTaskParams mirrors the dry_run key of the metafetch
// plugin's (unexported) asinBackfillParams; it carries ONLY that key, set to
// false, so the scheduled run writes. Every other param takes the op's own
// default (whole library, retry_after_days 30, 4 workers, no limit).
const (
	asinBackfillTaskName = "asin_backfill"
	asinBackfillOpID     = "metafetch.asin-backfill"
)

// asinBackfillInterval is scheduled.asin_backfill.interval (minutes, default
// 360 = 6h; 0 disables the task). A run after the first walks only books
// still missing an ASIN or ISBN, and skips without a request every one whose
// no-match marker is inside retry_after_days, so a 6h tick costs reads plus
// the books that changed since.
func asinBackfillInterval() time.Duration {
	mins := config.AppConfig.Scheduled.ASINBackfill.Interval
	if mins <= 0 {
		return 0
	}
	return time.Duration(mins) * time.Minute
}

// candidate_fetch task: metadata.candidate-fetch over the books nothing has
// fetched for. candidateFetchTaskParams mirrors the unfetched key of
// metabatch.FetchOpParams; the op selects its own books from it.
const (
	candidateFetchTaskName = "candidate_fetch"
	candidateFetchOpID     = "metadata.candidate-fetch"
)

type candidateFetchTaskParams struct {
	Unfetched bool `json:"unfetched"`
	// MarkDirtyOnly mirrors metabatch.FetchOpParams.MarkDirtyOnly: set while
	// identification_advance_enabled is on, so the op marks its selection
	// for identification.advance instead of fetching it. omitempty, so with
	// the flag off the params are exactly {"unfetched":true}, as before.
	MarkDirtyOnly bool `json:"mark_dirty_only,omitempty"`
}

// candidateFetchParams is what the candidate_fetch tick sends: with the
// identification driver on, the tick is its safety net and only marks.
func candidateFetchParams() candidateFetchTaskParams {
	return candidateFetchTaskParams{Unfetched: true, MarkDirtyOnly: config.AppConfig.IdentificationAdvanceEnabled}
}

// identification_advance task: enqueues identification.advance (spec 02 PR
// 14) every identificationAdvanceInterval while the identification dirty set
// holds a book and no run is queued or running. Live, like catalog_harvest:
// the op defaults to a dry run.
const (
	identificationAdvanceTaskName = "identification_advance"
	identificationAdvanceOpID     = "identification.advance"
	identificationAdvanceInterval = 5 * time.Minute
)

type identificationAdvanceTaskParams struct {
	DryRun *bool `json:"dry_run"`
}

// identDirtyPager is the store capability the identification_advance tick
// reads (database.PebbleStore.IdentDirtyPage).
type identDirtyPager interface {
	IdentDirtyPage(after string, limit int) ([]database.IdentDirtyEntry, error)
}

// identDirtySetNonEmpty reports whether the identification dirty set holds
// at least one book: one key read. A store that does not expose the set (not
// Pebble-backed) answers true: the tick cannot tell, so it enqueues and the
// op itself refuses with the reason, visibly in the op log, rather than the
// tick skipping in silence forever.
func (ts *TaskScheduler) identDirtySetNonEmpty() (bool, error) {
	store := ts.deps.Store()
	if store == nil {
		return false, nil
	}
	pager, ok := database.AsCapability[identDirtyPager](store)
	if !ok {
		return true, nil
	}
	page, err := pager.IdentDirtyPage("", 1)
	if err != nil {
		return false, err
	}
	return len(page) > 0, nil
}

// candidateFetchInterval is scheduled.candidate_fetch.interval (minutes,
// default 360 = 6h; 0 disables the task). A tick that finds nothing to fetch
// costs one library read and no provider request.
func candidateFetchInterval() time.Duration {
	mins := config.AppConfig.Scheduled.CandidateFetch.Interval
	if mins <= 0 {
		return 0
	}
	return time.Duration(mins) * time.Minute
}

// catalog_harvest task: catalog.harvest-authors over every library author.
// catalogHarvestTaskParams mirrors the dry_run key of the server's
// (unexported) catalogHarvestParams; it carries ONLY that key, set to false,
// so the scheduled run writes. Every other param takes the op's default:
// every in-scope author (catalog.BuildScope: the authors of live books with a
// present file), the 30-day re-harvest interval, 4 workers sharing one
// sub-limiter at catalog.harvest_rate_fraction (0.5) of Audible's budget, on
// top of Audible's shared token bucket.
const (
	catalogHarvestTaskName = "catalog_harvest"
	catalogHarvestOpID     = "catalog.harvest-authors"
)

type catalogHarvestTaskParams struct {
	DryRun *bool `json:"dry_run"`
}

// catalogHarvestInterval is scheduled.catalog_harvest.interval (minutes,
// default 360 = 6h; 0 disables the task). The durable interval clock
// (interval_clock.go) carries the cadence across restarts.
func catalogHarvestInterval() time.Duration {
	mins := config.AppConfig.Scheduled.CatalogHarvest.Interval
	if mins <= 0 {
		return 0
	}
	return time.Duration(mins) * time.Minute
}

// asinBackfillScopeParams reads only the book_ids key of a
// metafetch.asin-backfill row's params.
type asinBackfillScopeParams struct {
	BookIDs []string `json:"book_ids"`
}

// hasActiveASINBackfillWalk reports whether a queued or running
// metafetch.asin-backfill op is a FULL walk. A per-book run (book_ids set,
// enqueued by the metadata-apply queue) does not count: it covers a handful
// of books, and treating it as a walk would let a steady stream of applies
// starve the scheduled walk indefinitely. Params that are empty or do not
// parse count as a full walk (a resumed walk's params carry only its
// cursor), so the tick skips rather than stacking a second walk.
func (ts *TaskScheduler) hasActiveASINBackfillWalk() bool {
	store := ts.deps.Store()
	if store == nil {
		return false
	}
	ops, err := store.ListActiveOperationsV2()
	if err != nil {
		return false
	}
	canonical := func(id string) string { return id }
	if ts.deps.OpRegistry != nil {
		canonical = ts.deps.OpRegistry.CanonicalDefID
	}
	want := canonical(asinBackfillOpID)
	for _, op := range ops {
		if canonical(op.DefID) != want {
			continue
		}
		var p asinBackfillScopeParams
		if op.Params == "" || json.Unmarshal([]byte(op.Params), &p) != nil || len(p.BookIDs) == 0 {
			return true
		}
	}
	return false
}

type asinBackfillTaskParams struct {
	DryRun *bool `json:"dry_run"`
}

// ---- registration -------------------------------------------------------

// v2ScheduledOp is what a scheduled task returns now that it no longer writes a
// legacy operations row.
//
// The 5 tasks below used to do this:
//
//	opID := ulid.Make().String()
//	op, _ := store.CreateOperation(opID, "scan", nil)   // legacy row, status=pending
//	v2ID, _ := ts.deps.OpRegistry.EnqueueOp(ctx, "library.scan", ...)  // DIFFERENT id
//	return op, nil
//
// The legacy row and the real v2 operation got SEPARATE ids, nothing linked
// them, and nothing ever updated the legacy row — so it sat at "pending"
// forever. One orphan per scheduled tick, which is precisely the 183-of-200
// pending rows measured against production on 2026-08-16, going back six days:
// purge-deleted, temp-file-cleanup, scan, isbn-enrichment, author-dedup-scan.
//
// The return value is consumed for exactly one thing — the scheduler's
// "started operation" log line (scheduler.go) and the /tasks/:name/run response.
// Both were therefore reporting an id that NO endpoint could resolve, because
// the id that exists in v2 is the other one. Synthesizing the row from the v2 id
// fixes the log and the response as a side effect of removing the orphan.
func v2ScheduledOp(v2ID, opType string) *database.Operation {
	return &database.Operation{
		ID:        v2ID,
		Type:      opType,
		Status:    "queued",
		CreatedAt: time.Now().UTC(),
	}
}

func (ts *TaskScheduler) registerAllTasks() {
	// --- Library tasks ---

	// library_scan is the ONLY unattended discovery path for new books, so it
	// is the one task in this file that ships with a real, default-on interval.
	//
	// It used to be completely inert: GetInterval returned a hard-coded 0 (so
	// scheduler.Start's `IsEnabled() && GetInterval() > 0` guard never created
	// a ticker), IsEnabled/RunOnStart both read scan_on_startup which defaults
	// false, and maintenance.library_scan was unreachable because "library_scan"
	// was missing from maintenanceOrder. Net effect: a book copied into an
	// import path was never noticed until somebody pressed Scan by hand.
	ts.registerTask(TaskDefinition{
		Name:        "library_scan",
		Description: "Scan library for new/changed audiobooks (incremental by default, use force_update for full rescan)",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// Skip this tick if the previous scan we enqueued has not finished.
			// library.scan's ConcurrencyKey makes the dispatcher SERIALIZE a
			// duplicate rather than reject it (registry/dispatcher.go: "the op
			// stays QUEUED"), so a scan that outruns the interval would other-
			// wise pile up one queued op — plus one orphan legacy operation
			// row — on every tick. Guarding here rather than in the generic
			// ticker keeps the behaviour of the seven other interval tasks
			// unchanged.
			if prev := ts.previousRunID("library_scan"); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						slog.Info("library_scan: previous scan still active, skipping this tick",
							"op", prev, "status", row.Status, "source", source)
						return nil, nil
					}
				}
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.scan", scanner.LibraryScanParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.scan: %w", enqErr)
			}
			ts.setPreviousRunID("library_scan", v2ID)
			return v2ScheduledOp(v2ID, "scan"), nil
		},
		IsEnabled: func() bool {
			// IsEnabled gates the ticker, the startup run AND maintenance-window
			// eligibility, so it must stay true for anyone who only ever set the
			// legacy scan_on_startup flag — otherwise flipping to the new key
			// would silently take away their startup scan.
			return config.AppConfig.Scheduled.LibraryScan.Enabled || config.AppConfig.ScanOnStartup
		},
		GetInterval: func() time.Duration {
			if !config.AppConfig.Scheduled.LibraryScan.Enabled {
				return 0
			}
			mins := config.AppConfig.Scheduled.LibraryScan.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart: func() bool {
			return config.AppConfig.Scheduled.LibraryScan.OnStartup || config.AppConfig.ScanOnStartup
		},
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibraryScan },
	})

	// library_scan_full is the WEEKLY FULL SWEEP: a library.scan with both
	// force_update and include_root_dir set, so every file is re-read and
	// re-hashed instead of being skipped as unchanged.
	//
	// Its schedule does NOT come from GetInterval. GetInterval here is only a
	// DUE-CHECK cadence (default hourly); the actual weekly gap is enforced in
	// TriggerFn against a timestamp persisted in the settings store. See
	// full_sweep.go for why: scheduler.Start's ticker is in-memory, so any
	// interval longer than the process uptime silently never fires, and a
	// deploy-per-day cadence makes 168h unreachable.
	//
	// It shares library.scan's ConcurrencyKey with the incremental task. That
	// is deliberate and safe: EnqueueOp's dedupe falls through to a
	// byte-for-byte comparison of the MARSHALLED PARAMS, and the incremental
	// enqueues {} while this enqueues
	// {"force_update":true,"include_root_dir":true}. Unequal params queue a
	// second row rather than collapsing into the running op, so a sweep that
	// lands mid-incremental is DELAYED, never dropped.
	//
	// That byte-compare is reached only because library.scan sets neither
	// DedupeQueuedRuns nor Schedule -- either one short-circuits sameWork to
	// true (registry.go, EnqueueOp) and would silently collapse the sweep into
	// the running incremental, i.e. dropped rather than delayed. Adding either
	// to library.scan breaks this task. TestFullSweepEnqueuesForceUpdateParams
	// pins the params half; the def-shape half is a standing constraint.
	ts.registerTask(TaskDefinition{
		Name:        "library_scan_full",
		Description: "Weekly full library sweep — re-reads and re-hashes every file. The interval is how often it CHECKS whether a sweep is due; the gap between sweeps is scheduled.library_scan_full.period_hours (default 168h).",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			period := time.Duration(config.AppConfig.Scheduled.LibraryScanFull.PeriodHours) * time.Hour
			if period <= 0 {
				// Declining is right -- a zero period would otherwise mean
				// "always due", i.e. a full re-hash of the library every
				// due-check. But it must not decline QUIETLY: the task reports
				// itself enabled with a live ticker, so without this line a
				// mistyped period_hours looks exactly like a healthy schedule
				// that never happens to fire.
				slog.Warn("library_scan_full: period_hours is not positive, so the full sweep can NEVER run; "+
					"set scheduled.library_scan_full.period_hours (168 = weekly)",
					"periodHours", config.AppConfig.Scheduled.LibraryScanFull.PeriodHours, "source", source)
				return nil, nil
			}

			// Serialize the whole load-check-enqueue-stamp sequence: a ticker
			// tick and a manual trigger arriving together could otherwise both
			// read "due" and enqueue two full sweeps.
			ts.fullSweepMu.Lock()
			defer ts.fullSweepMu.Unlock()

			// A manual trigger means a person pressed Run, and it BYPASSES the
			// due-check. Applying the schedule to a manual press made the
			// button a no-op that still answered 202 "task triggered" on any
			// day but the due one, and left no way at all to force a sweep.
			manual := source == operations.TriggerManual

			last, found, loadErr := ts.loadLastFullSweep()
			switch {
			case loadErr != nil:
				// Decline WITHOUT seeding. Seeding here would push the sweep
				// out a full period on every transient store hiccup, which is
				// enough to stop it ever firing.
				slog.Warn("library_scan_full: cannot read the last-run timestamp; declining this tick "+
					"WITHOUT re-seeding the clock, so a transient store error does not postpone the sweep",
					"err", loadErr, "source", source)
				return nil, nil
			case !found && manual:
				// No clock yet, but a person asked for it explicitly.
				slog.Info("library_scan_full: no prior sweep recorded; running now because this was triggered manually",
					"source", source)
			case !found:
				// Seed and skip: the first tick after a deploy must not kick
				// off an unannounced multi-hour re-hash of the whole library.
				ts.seedFullSweepClock(time.Now())
				slog.Info("library_scan_full: no prior sweep recorded; seeding the clock and waiting a full period",
					"periodHours", config.AppConfig.Scheduled.LibraryScanFull.PeriodHours, "source", source)
				return nil, nil
			case !fullSweepDue(last, time.Now(), period) && !manual:
				return nil, nil
			}

			// Same skip-if-active guard as the incremental scan, keyed to this
			// task's own previous run.
			if prev := ts.previousRunID("library_scan_full"); prev != "" {
				row, err := store.GetOperationV2(prev)
				switch {
				case err != nil:
					// Proceeding is right (EnqueueOp's dedupe still covers the
					// case where that op is genuinely active), but saying
					// nothing would hide a store fault behind a normal-looking
					// sweep.
					slog.Warn("library_scan_full: could not read the previous sweep's status; "+
						"proceeding, so a sweep may be enqueued while one is already active",
						"op", prev, "err", err, "source", source)
				case row != nil && (row.Status == "queued" || row.Status == "running"):
					slog.Info("library_scan_full: previous sweep still active, skipping this tick",
						"op", prev, "status", row.Status, "source", source)
					return nil, nil
				}
			}

			// Stamp BEFORE enqueueing, and refuse to enqueue if the stamp
			// fails. Enqueue-then-stamp fails OPEN: a swallowed stamp failure
			// leaves the timestamp stale, and once the sweep COMPLETES it is in
			// neither ListActiveOperationsV2 nor the ConcurrencyKey's active
			// set, so nothing dedupes the next due-check. The result is a
			// whole-library force_update re-hash running back to back forever,
			// announced once an hour as a warning nobody reads. Skipping one
			// sweep is the cheaper failure.
			if stampErr := ts.stampFullSweepRun(time.Now()); stampErr != nil {
				return nil, fmt.Errorf("library_scan_full: refusing to enqueue a sweep whose clock cannot be "+
					"persisted, because it would re-enqueue on every due check: %w", stampErr)
			}

			forceUpdate, includeRoot := true, true
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.scan", scanner.LibraryScanParams{
				ForceUpdate:    &forceUpdate,
				IncludeRootDir: &includeRoot,
			})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue full library.scan: %w", enqErr)
			}
			ts.setPreviousRunID("library_scan_full", v2ID)
			slog.Info("library_scan_full: enqueued full sweep", "op", v2ID, "since", last, "source", source)
			return v2ScheduledOp(v2ID, "scan"), nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.Scheduled.LibraryScanFull.Enabled
		},
		// Required, not optional: ListTasks calls RunOnStart() unconditionally,
		// so leaving it nil panics the tasks page. Always false here -- a full
		// sweep must never be triggered by a restart, which on this host
		// happens several times a day.
		RunOnStart: func() bool { return false },
		GetInterval: func() time.Duration {
			if !config.AppConfig.Scheduled.LibraryScanFull.Enabled {
				return 0
			}
			// Due-check cadence, NOT the sweep period.
			mins := config.AppConfig.Scheduled.LibraryScanFull.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
	})

	ts.registerTask(TaskDefinition{
		Name:        "library_organize",
		Description: "Organize audiobooks into folder structure",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.organize", libraryOrganizeParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.organize: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "organize"), nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibraryOrganize },
	})

	ts.registerTask(TaskDefinition{
		Name:        "library_size_refresh",
		Description: "Walk library + import-path trees to refresh on-disk size cache",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.size-refresh", librarySizeRefreshParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.size-refresh: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "library-size-refresh"), nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibrarySizeRefresh },
	})

	ts.registerTask(TaskDefinition{
		Name:        "transcode",
		Description: "Transcode audiobooks to target format",
		Category:    "library",
		// TriggerFn always fails: its only argument is the trigger source, so
		// there is no way to route a book_id through it, and transcoding without
		// one is meaningless.
		//
		// It used to express that by creating a legacy operations row purely to
		// stamp an error on it and hand it back. RunTask answers 202 Accepted for
		// a non-nil op, so every caller got "accepted" for work that had already
		// definitively failed, with the reason buried in the row. Returning the
		// error instead takes the handler's error branch — 400 with the message
		// where the caller can see it — and writes no row at all.
		TriggerFn: func(source string) (*database.Operation, error) {
			slog.Warn("transcode task triggered without params — use the operations API", "source", source)
			return nil, fmt.Errorf("transcode requires parameters — use the operations API directly")
		},
		// NOT enabled, and this is not a regression: the TriggerFn above fails
		// by design because a scheduled trigger has no book_id to transcode.
		// Reporting Enabled=true for a task whose every automatic invocation
		// creates a failed operation is a lie the UI repeats on the tasks page.
		// runTask does not consult IsEnabled, so manual/API invocation with real
		// params is unaffected — only the automatic paths and the displayed
		// state change.
		IsEnabled:              func() bool { return false },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// --- Sync tasks ---
	// iTunes sync and import are now registered via UOS plugin (UOS-10)

	// --- Maintenance tasks ---

	ts.registerTask(TaskDefinition{
		Name:        "dedup_refresh",
		Description: "Refresh author & series dedup cache",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.author-scan", dedup.AuthorDedupScanParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.author-scan: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "author-dedup-scan"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.DedupRefresh.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.DedupRefresh.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.DedupRefresh.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DedupRefresh },
	})

	// label_refinement (INIT-1 T6): built-in-DISABLED scheduled task that, only
	// when an owner sets scheduled.label_refinement.enabled=true, periodically
	// enqueues the dedup.label-refinement parent op (04-P9). That op runs
	// dedup.rebuild-gold-labels then dedup.calibrate-composite as followed
	// children in DRY-RUN mode and writes NOTHING — there is no apply on this
	// path. Deliberately minimal; INIT-6 WF-3 is expected to subsume scheduled.* keys.
	ts.registerTask(TaskDefinition{
		Name:        "label_refinement",
		Description: "Dry-run gold-label refinement chain: rebuild-gold-labels → calibrate-composite (report only; applies are operator-gated)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			if ts.deps.Store() == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			if ts.deps.OpRegistry == nil {
				return nil, fmt.Errorf("operations registry not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.label-refinement", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue label-refinement: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "label_refinement"), nil
		},
		// The parent is registered only when the dedup plugin is built, which
		// needs the dedup engine AND the embedding store (dedup/register.go);
		// without the gate the task could enqueue an unregistered def id.
		IsEnabled: func() bool {
			return config.AppConfig.Scheduled.LabelRefinement.Enabled &&
				ts.deps.HasDedupPlugin != nil && ts.deps.HasDedupPlugin()
		},
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.LabelRefinement.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart: func() bool { return config.AppConfig.Scheduled.LabelRefinement.OnStartup },
		// Intentionally NOT part of the maintenance window (also absent from
		// maintenanceOrder): keeps the task fully inert under default config.
		RunInMaintenanceWindow: func() bool { return false },
	})

	ts.registerTask(TaskDefinition{
		Name:        "dedup_llm_review",
		Description: "Run LLM review on ambiguous dedup candidates",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.llm-review", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.llm-review: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "dedup-llm-review"), nil
		},
		// dedup.llm-review is registered by the dedup plugin, which is built
		// only when the dedup engine AND the embedding store are wired
		// (dedup/register.go); gating on the engine alone would enqueue an
		// unregistered def id whenever the embedding store is missing.
		IsEnabled:              func() bool { return ts.deps.HasDedupPlugin != nil && ts.deps.HasDedupPlugin() },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "series_prune",
		Description: "Merge duplicate series and delete orphans",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.series-prune", dedup.SeriesPruneParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.series-prune: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "series-prune"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.SeriesPrune.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.SeriesPrune.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.SeriesPrune.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.SeriesPrune },
	})

	ts.registerTask(TaskDefinition{
		Name:        "series_normalize",
		Description: "Strip title/position contamination from series names and run write-back + organize for affected books",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.series-normalize", dedup.SeriesNormalizeParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.series-normalize: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "series-normalize"), nil
		},
		// Manual-only, and now says so. It has no interval and explicitly opts
		// out of the maintenance window, so IsEnabled: true described a task
		// that could never start itself. It also runs write-back + organize,
		// which moves files — turning it into a timer would be a behaviour
		// change nobody asked for, so the honest fix is to stop claiming it is
		// enabled. runTask ignores IsEnabled, so triggering it from the
		// operations API still works exactly as before.
		IsEnabled:              func() bool { return false },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// asin_backfill replaced isbn_enrichment on 2026-10-02. The old task ran
	// scheduler.isbn-enrichment, which searched Google Books / Open Library
	// and wrote ASINs by title-prefix matching -- the source of most of the
	// wrong ASINs metafetch.asin-backfill now lists as asin_suspect. The
	// owner chose metafetch.asin-backfill (Audible only, ASIN + ISBN in one
	// op, no per-run cap) as its replacement, running LIVE on a timer.
	//
	// The op's params default to a dry run when dry_run is omitted
	// (opmode.ResolveDryRun), so the trigger sends dry_run=false explicitly;
	// an empty params struct would preview every tick and write nothing.
	// Repeat runs are cheap: a book Audible had no match for is skipped for
	// retry_after_days (default 30) without a request.
	//
	// Not in the maintenance window: a full walk is hours of rate-limited
	// Audible requests, and the window would sit behind it.
	ts.registerTask(TaskDefinition{
		Name:        "asin_backfill",
		Description: "Fill missing ASINs and audiobook ISBNs from Audible (metafetch.asin-backfill, live)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// The op's ConcurrencyKey makes the dispatcher QUEUE a duplicate
			// rather than reject it, so a walk that outlasts the interval
			// would stack another behind it. Two guards: the store's active
			// set survives a restart (the op is ResumeRestart, so a resumed
			// walk is active again while previousRunID, in memory, is empty);
			// previousRunID covers the moment between enqueue and the row
			// becoming visible. Only a FULL walk blocks the tick: per-book
			// runs from the metadata-apply queue (book_ids set) do not.
			if ts.hasActiveASINBackfillWalk() {
				schedLog.Info("%s: a full %s walk is already queued or running, skipping this tick (source=%s)",
					asinBackfillTaskName, asinBackfillOpID, source)
				return nil, nil
			}
			if prev := ts.previousRunID(asinBackfillTaskName); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						schedLog.Info("%s: previous run %s still %s, skipping this tick (source=%s)",
							asinBackfillTaskName, prev, row.Status, source)
						return nil, nil
					}
				}
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), asinBackfillOpID, asinBackfillTaskParams{DryRun: opmode.Live()})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue %s: %w", asinBackfillOpID, enqErr)
			}
			ts.setPreviousRunID(asinBackfillTaskName, v2ID)
			return v2ScheduledOp(v2ID, asinBackfillTaskName), nil
		},
		// Enabled when scheduled.asin_backfill.interval > 0 (default 6h; 0
		// turns it off, mirroring metadata_upgrade) AND the op exists in this
		// binary: the metafetch plugin was once listed but never linked into
		// the server, so its ops were absent in prod while tests passed.
		IsEnabled: func() bool {
			if ts.deps.OpRegistry == nil || asinBackfillInterval() <= 0 {
				return false
			}
			_, ok := ts.deps.OpRegistry.Def(asinBackfillOpID)
			return ok
		},
		GetInterval: asinBackfillInterval,
		// Off at startup: memdb warmup runs asynchronously for minutes after
		// a restart, and the interval picks the walk up soon enough.
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// candidate_fetch: metadata.candidate-fetch was started only by hand
	// (POST .../batch-fetch-candidates), so a book added after the last hand
	// run was never searched (613 books on 2026-10-05), and neither was a
	// book whose candidates a retitle or an ASIN backfill invalidated. This
	// task enqueues the op with unfetched=true; the op selects the live
	// primary books with no cache row, or an empty one answering questions a
	// search no longer asks (server.unfetchedCandidateBookIDs).
	//
	// FETCH ONLY: the op searches the providers and writes the candidate
	// cache and its result rows; it never applies a candidate or writes a
	// book. Its rate limiter and worker pool are sized from the enabled
	// sources' own budgets (metafetch.EnabledSourcesBudget: Audible 8/s).
	//
	// Ships ON at 6h, like asin_backfill, its fetch-only sibling: an empty
	// selection costs no provider call. Not on startup (memdb warmup) and not
	// in the maintenance window (a large selection is hours of rate-limited
	// requests).
	ts.registerTask(TaskDefinition{
		Name:        candidateFetchTaskName,
		Description: "Fetch metadata candidates for books never fetched or whose candidates were invalidated (metadata.candidate-fetch, fetch only)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// The op's ConcurrencyKey QUEUES a duplicate, so a run that
			// outlasts the interval would stack another behind it -- and a
			// hand-started fetch already covers some of the same books.
			// Any active candidate fetch skips the tick; previousRunID covers
			// the moment before the enqueued row is visible.
			if ts.hasActiveV2Op(candidateFetchOpID) {
				schedLog.Info("%s: a %s run is already queued or running, skipping this tick (source=%s)",
					candidateFetchTaskName, candidateFetchOpID, source)
				return nil, nil
			}
			if prev := ts.previousRunID(candidateFetchTaskName); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						schedLog.Info("%s: previous run %s still %s, skipping this tick (source=%s)",
							candidateFetchTaskName, prev, row.Status, source)
						return nil, nil
					}
				}
			}
			params := candidateFetchParams()
			if params.MarkDirtyOnly {
				schedLog.Info("%s: identification_advance_enabled: this tick marks its selection for %s instead of fetching (the op logs safety_net_marked=N; source=%s)",
					candidateFetchTaskName, identificationAdvanceOpID, source)
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), candidateFetchOpID, params)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue %s: %w", candidateFetchOpID, enqErr)
			}
			ts.setPreviousRunID(candidateFetchTaskName, v2ID)
			return v2ScheduledOp(v2ID, candidateFetchTaskName), nil
		},
		// Enabled when scheduled.candidate_fetch.interval > 0 AND the op is
		// registered in this binary.
		IsEnabled: func() bool {
			if ts.deps.OpRegistry == nil || candidateFetchInterval() <= 0 {
				return false
			}
			_, ok := ts.deps.OpRegistry.Def(candidateFetchOpID)
			return ok
		},
		GetInterval:            candidateFetchInterval,
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// identification_advance: the identification driver's tick. Writers
	// (a scan that changes a book's title or author, a new primary book, a
	// merge, a retitle, a catalog harvest that changed an author's catalog)
	// mark books in the identification dirty set; this task enqueues
	// identification.advance to drain it, every 5 minutes, only when the set
	// holds a book and no run is queued or running. Off unless
	// identification_advance_enabled is set; then candidate_fetch above only
	// marks (its safety net). Not on startup (memdb warmup) and not in the
	// maintenance window (it runs on its own short interval).
	ts.registerTask(TaskDefinition{
		Name:        identificationAdvanceTaskName,
		Description: "Advance the identification of books a writer marked dirty (identification.advance, fetch only)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			dirty, err := ts.identDirtySetNonEmpty()
			if err != nil {
				return nil, fmt.Errorf("%s: read the identification dirty set: %w", identificationAdvanceTaskName, err)
			}
			if !dirty {
				schedLog.Debug("%s: the identification dirty set is empty, skipping this tick (source=%s)",
					identificationAdvanceTaskName, source)
				return nil, nil
			}
			if ts.hasActiveV2Op(identificationAdvanceOpID) {
				schedLog.Debug("%s: a %s run is already queued or running, skipping this tick (source=%s)",
					identificationAdvanceTaskName, identificationAdvanceOpID, source)
				return nil, nil
			}
			if prev := ts.previousRunID(identificationAdvanceTaskName); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						return nil, nil
					}
				}
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), identificationAdvanceOpID,
				identificationAdvanceTaskParams{DryRun: opmode.Live()})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue %s: %w", identificationAdvanceOpID, enqErr)
			}
			ts.setPreviousRunID(identificationAdvanceTaskName, v2ID)
			return v2ScheduledOp(v2ID, identificationAdvanceTaskName), nil
		},
		// Enabled when identification_advance_enabled is set AND the op is
		// registered in this binary.
		IsEnabled: func() bool {
			if ts.deps.OpRegistry == nil || !config.AppConfig.IdentificationAdvanceEnabled {
				return false
			}
			_, ok := ts.deps.OpRegistry.Def(identificationAdvanceOpID)
			return ok
		},
		GetInterval:            func() time.Duration { return identificationAdvanceInterval },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// catalog_harvest: catalog.harvest-authors was registered at every
	// startup but nothing ever enqueued it (prod journal, 14 days to
	// 2026-10-07: catalog.enabled=true, zero runs), so the author catalog the
	// Candidates view's author search reads was never filled. This task runs
	// it LIVE over every library author.
	//
	// The op defaults to a dry run when dry_run is omitted
	// (opmode.ResolveDryRun), so the trigger sends dry_run=false explicitly;
	// an empty params struct would census every tick and write nothing.
	//
	// Repeat runs are cheap: an author whose last harvest is complete is
	// re-listed only after 30 days (catalog.Due), so after the first pass a
	// 6h tick lists only the new and the due authors. The op's DependsOn holds
	// it QUEUED while metadata.candidate-fetch or metafetch.asin-backfill
	// runs (all three spend Audible's budget), and they wait on it the same
	// way. Not on startup (memdb warmup) and not in the maintenance window
	// (the first pass is hours of rate-limited requests).
	ts.registerTask(TaskDefinition{
		Name:        catalogHarvestTaskName,
		Description: "List every Audible title by each library author into the author catalog (catalog.harvest-authors, live)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// The op's ConcurrencyKey QUEUES a duplicate, so a harvest that
			// outlasts the interval would stack another behind it.
			if ts.hasActiveV2Op(catalogHarvestOpID) {
				schedLog.Info("%s: a %s run is already queued or running, skipping this tick (source=%s)",
					catalogHarvestTaskName, catalogHarvestOpID, source)
				return nil, nil
			}
			if prev := ts.previousRunID(catalogHarvestTaskName); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						schedLog.Info("%s: previous run %s still %s, skipping this tick (source=%s)",
							catalogHarvestTaskName, prev, row.Status, source)
						return nil, nil
					}
				}
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), catalogHarvestOpID, catalogHarvestTaskParams{DryRun: opmode.Live()})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue %s: %w", catalogHarvestOpID, enqErr)
			}
			ts.setPreviousRunID(catalogHarvestTaskName, v2ID)
			return v2ScheduledOp(v2ID, catalogHarvestTaskName), nil
		},
		// Enabled when scheduled.catalog_harvest.interval > 0, catalog.enabled
		// is on (the op refuses to run otherwise, so a tick would only fail),
		// and the op is registered in this binary.
		IsEnabled: func() bool {
			if ts.deps.OpRegistry == nil || catalogHarvestInterval() <= 0 || !config.AppConfig.Catalog.Enabled {
				return false
			}
			_, ok := ts.deps.OpRegistry.Def(catalogHarvestOpID)
			return ok
		},
		GetInterval:            catalogHarvestInterval,
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// acoustid_backfill is the real schedule for acoustid.backfill. The op def
	// used to declare Schedule "0 3 * * *", which nothing reads (see the
	// optimize_activity_db comment below), so the backfill never ran
	// unattended. Ships DISABLED — scheduled.acoustid_backfill.enabled=false —
	// because library-wide fingerprinting reads every un-fingerprinted file
	// and turning that on is an owner decision. Not in maintenanceOrder for
	// the same reason.
	ts.registerTask(TaskDefinition{
		Name:        "acoustid_backfill",
		Description: "Generate raw AcoustID fingerprints for files that have none (disabled by default)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// Same guard as library_scan: acoustid.backfill's ConcurrencyKey
			// makes the dispatcher QUEUE a duplicate rather than reject it, so
			// a run that outlasts the interval would otherwise stack a second
			// full pass behind itself on every tick.
			if prev := ts.previousRunID("acoustid_backfill"); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						slog.Info("acoustid_backfill: previous run still active, skipping this tick",
							"op", prev, "status", row.Status, "source", source)
						return nil, nil
					}
				}
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "acoustid.backfill", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue acoustid.backfill: %w", enqErr)
			}
			ts.setPreviousRunID("acoustid_backfill", v2ID)
			return v2ScheduledOp(v2ID, "acoustid_backfill"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.AcoustIDBackfill.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.AcoustIDBackfill.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.AcoustIDBackfill.OnStartup },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// iTunes position sync is now registered via UOS plugin (UOS-10)

	ts.registerTask(TaskDefinition{
		Name:        "temp_file_cleanup",
		Description: "Remove orphaned *.tmp.m4b / *.tmp.m4a files left by crashed ffmpeg operations",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.temp-file-cleanup", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.temp-file-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "temp-file-cleanup"), nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return true },
		RunInMaintenanceWindow: func() bool { return true },
	})

	// Hourly sweep of stale transcode temp files under every import path. It
	// replaces the per-import-path transcode cleanup ticker goroutines
	// server_lifecycle.go used to start once at boot: the same predicate (temp
	// files older than 2h) and cadence, now with an operation row, and the
	// import paths are read on every run. It enqueues its OWN def, not
	// maintenance.temp-file-cleanup: that def has a cron Schedule, so the
	// registry would dedupe the two on def id alone. The defs share a
	// ConcurrencyKey, which serializes them. Not in the maintenance window: the
	// cadence is the hourly interval.
	ts.registerTask(TaskDefinition{
		Name:        "transcode_temp_cleanup",
		Description: "Remove stale transcode temp files (older than 2h) under every import path",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.transcode-temp-cleanup", map[string]any{"dry_run": false})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.transcode-temp-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "transcode-temp-cleanup"), nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 1 * time.Hour },
		RunOnStart:             func() bool { return true },
		RunInMaintenanceWindow: func() bool { return false },
	})

	ts.registerTask(TaskDefinition{
		Name:        "trash_cleanup",
		Description: "Purge trashed book versions past their 14-day TTL",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.trash-cleanup", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.trash-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "trash-cleanup"), nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "metadata_upgrade",
		Description: "Replace metadata from lower-ranked sources (Open Library, Google Books, Wikipedia) with a higher-ranked source's (Hardcover, Audible) when its match passes the bulk-apply gate; user-locked fields are kept",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.metadata-upgrade", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.metadata-upgrade: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "metadata-upgrade"), nil
		},
		// Its own interval, independent of the maintenance window (owner
		// 2026-09-27). Until then it had no interval (GetInterval 0) and ran
		// only in the window, gated on maintenance.metadata_refresh -- a
		// different task's toggle, off in production -- so it never ran. It
		// is scheduled whenever scheduled.metadata_upgrade.interval > 0, which
		// PUT /api/v1/tasks/metadata_upgrade {"interval_minutes":N} sets; there
		// is no separate enabled switch (see ScheduledTasksConfig.MetadataUpgrade).
		// It is no longer in maintenanceOrder, so the window never runs it.
		IsEnabled: func() bool {
			return ts.deps.HasMetadataFetchSvc() && config.AppConfig.Scheduled.MetadataUpgrade.Interval > 0
		},
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.MetadataUpgrade.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.MetadataUpgrade.OnStartup },
		RunInMaintenanceWindow: func() bool { return false },
	})

	ts.registerTask(TaskDefinition{
		Name:        "author_split_scan",
		Description: "Find & split composite author names",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// EMPTY ON PURPOSE (2026-09-25): with no dry_run key the op
			// defaults to a dry run, so a scheduled run previews and writes
			// nothing. The schedule was disabled in prod when this changed.
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.author-split-scan", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.author-split-scan: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "author-split-scan"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.AuthorSplit.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.AuthorSplit.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.AuthorSplit.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.AuthorSplit },
	})

	ts.registerTask(TaskDefinition{
		Name:        "db_optimize",
		Description: "Optimize database (VACUUM/compact)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.db-optimize", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.db-optimize: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "db-optimize"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.DbOptimize.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.DbOptimize.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.DbOptimize.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DbOptimize },
	})

	// Nightly AcoustID online lookup. Off unless the user explicitly
	// enables MaintenanceWindowAcoustIDOnlineLookup. The op itself
	// refuses to run without ACOUSTID_API_KEY, so flipping the toggle
	// without setting the env is a no-op.
	ts.registerTask(TaskDefinition{
		Name:        "acoustid_online_lookup",
		Description: "Look up un-queried fingerprints on acoustid.org (rate-limited, bounded)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			params := map[string]any{
				"limit": config.AppConfig.Maintenance.AcoustIDNightlyLimit,
				"force": false,
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "acoustid.lookup-online", params)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue acoustid.lookup-online: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "acoustid-online-lookup"), nil
		},
		IsEnabled:              func() bool { return config.AppConfig.Maintenance.AcoustIDOnlineLookup },
		GetInterval:            func() time.Duration { return 0 }, // run only inside the maintenance window
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.AcoustIDOnlineLookup },
	})

	ts.registerTask(TaskDefinition{
		Name:        "cleanup_old_backups",
		Description: "Remove old .bak-* backup files past retention",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.cleanup-old-backups", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.cleanup-old-backups: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "cleanup-old-backups"), nil
		},
		IsEnabled:              func() bool { return config.AppConfig.Maintenance.DbOptimize },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DbOptimize },
	})

	ts.registerTask(TaskDefinition{
		Name:        "purge_deleted",
		Description: "Purge soft-deleted books past retention",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.purge-deleted", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.purge-deleted: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "purge-deleted"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.PurgeSoftDeletedAfterDays > 0 },
		GetInterval: func() time.Duration {
			if config.AppConfig.PurgeSoftDeletedAfterDays > 0 {
				return 6 * time.Hour
			}
			return 0
		},
		RunOnStart:             func() bool { return config.AppConfig.PurgeSoftDeletedAfterDays > 0 },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.PurgeDeleted },
	})

	ts.registerTask(TaskDefinition{
		Name:        "tombstone_cleanup",
		Description: "Resolve author tombstone chains (A→B→C becomes A→C)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.tombstone-cleanup", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.tombstone-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "tombstone-cleanup"), nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.TombstoneCleanup },
	})

	ts.registerTask(TaskDefinition{
		Name:        "file_integrity_check",
		Description: "Report book_file rows whose hash drifted with no tag-write on record (report-only)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			if ts.deps.Store() == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// Empty params on purpose: the op is report-only and takes none.
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.file-integrity-check", map[string]any{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.file-integrity-check: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "file-integrity-check"), nil
		},
		IsEnabled: func() bool { return true },
		// 0 on purpose: window-only, like trash_cleanup. A positive interval would
		// also put the task on the durable interval clock and run it outside the window.
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "orphan_book_files_cleanup",
		Description: "Report book_file rows whose book no longer exists (report-only, never deletes)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			if ts.deps.Store() == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// Empty params on purpose: a delete key would be refused by the op,
			// and absence is the report-only default.
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.orphan-book-files-cleanup", map[string]any{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.orphan-book-files-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "orphan-book-files-cleanup"), nil
		},
		IsEnabled: func() bool { return true },
		// 0 on purpose: window-only, like trash_cleanup. A positive interval would
		// also put the task on the durable interval clock and run it outside the window.
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "resolve_production_authors",
		Description: "Resolve real authors for production company entries",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// EMPTY ON PURPOSE (2026-09-25): no dry_run key means a dry run;
			// see the author_split_scan trigger above.
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.resolve-production-authors", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.resolve-production-authors: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "resolve-production-authors"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.ResolveProductionAuthors.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.ResolveProductionAuthors.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	ts.registerTask(TaskDefinition{
		Name:        "metadata_refresh",
		Description: "Re-fetch metadata for incomplete books",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.metadata-refresh", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue maintenance.metadata-refresh: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "metadata-refresh"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.MetadataRefresh.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.MetadataRefresh.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.MetadataRefresh.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.MetadataRefresh },
	})

	// Reconcile — find broken file paths and match to untracked files on disk
	ts.registerTask(TaskDefinition{
		Name:        "reconcile_scan",
		Description: "Find books with missing files and match to untracked files on disk",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "reconcile.scan", struct{}{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue reconcile scan: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "reconcile_scan"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.Reconcile.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.Reconcile.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.Reconcile.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.Reconcile },
	})

	// AI Dedup Batch — uses OpenAI Batch API at 50% cost
	ts.registerTask(TaskDefinition{
		Name:        "ai_dedup_batch",
		Description: "Run AI author dedup via Batch API (50% cheaper, up to 24h)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.ai-dedup-batch", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue ai-dedup-batch: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "ai-dedup-batch"), nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.Scheduled.AIDedupBatch.Enabled && config.AppConfig.EnableAIParsing
		},
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.AIDedupBatch.Interval
			if mins <= 0 {
				return 24 * time.Hour
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.AIDedupBatch.OnStartup },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// Unified Batch Poller — discovers all project-tagged OpenAI batches and routes
	// completed ones to the appropriate handler (author_dedup, author_review,
	// diagnostics, pipeline, etc.)
	//
	// This inline task is the ONLY batch poller. There is deliberately no
	// operation-registry twin: maintenance.batch-poller was deleted (D26,
	// opscatalog.Tombstones) because it never ran: nothing enqueued it and its declared
	// */5 Schedule was never driven (Schedule is read by nothing, see below).
	ts.registerTask(TaskDefinition{
		Name:        "batch_poller",
		Description: "Poll OpenAI for completed batch jobs",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			if ts.deps.PollBatches == nil {
				return nil, nil
			}
			// The lifecycle context, not Background: collection starts AI-scan
			// enrichment and cross-validation that must stop with the server.
			processed, err := ts.deps.PollBatches(ts.lifecycleContext())
			if err != nil {
				slog.Warn("batch_poller", "error", err)
			}
			if processed > 0 {
				slog.Info("batch_poller processed completed batches", "processed", processed)
			}
			return nil, nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.OpenAIAPIKey != "" && ts.deps.HasBatchPoller()
		},
		GetInterval: func() time.Duration {
			return 5 * time.Minute
		},
		RunOnStart:             func() bool { return true },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// Log Retention Pruning — prune old system logs and legacy operation log
	// rows. Never the undo journal (opchange:), which gets its own undo window.
	ts.registerTask(TaskDefinition{
		Name:        "purge_old_logs",
		Description: "Prune system logs and legacy operation logs older than the retention period (never undo history)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.purge-old-logs", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue purge-old-logs: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "purge_old_logs"), nil
		},
		// 0 means the default (90), not "off"; only a negative value (-1)
		// disables the purge. See config.ResolveLogRetentionDays.
		IsEnabled:              func() bool { return config.ResolveLogRetentionDays(config.AppConfig.LogRetentionDays) > 0 },
		GetInterval:            func() time.Duration { return 7 * 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.PurgeOldLogs },
	})

	// Activity Log Cleanup — summarize old change entries and prune old debug entries
	ts.registerTask(TaskDefinition{
		Name:        "cleanup_activity_log",
		Description: "Summarize old change entries and prune old debug entries from activity log",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.cleanup-activity-log", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue cleanup-activity-log: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "cleanup_activity_log"), nil
		},
		IsEnabled:              func() bool { return ts.deps.HasActivitySvc() },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	// Nightly activity compaction — collapse every activity entry from before
	// the kept full-detail days (default: before today, local time) into daily
	// digests. It runs at 00:10 server-local time (DailyAt, see daily_at.go),
	// just after midnight, so each run compacts the day that just ended; the op
	// computes its cutoff as local midnight today when it runs, so the firing
	// time decides only staleness, never what is kept. A run missed while the
	// server was down fires once at the next startup.
	// Deliberately NOT in maintenanceOrder: a catch-up run can take the op's
	// full 6h, and the window runs its tasks sequentially and stops when the
	// window closes, so it would starve every task after it.
	ts.registerTask(TaskDefinition{
		Name:        "nightly_activity_compaction",
		Description: "Compact activity entries from before today into daily digests",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.nightly-compact-activity-log", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue nightly-compact-activity-log: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "nightly_activity_compaction"), nil
		},
		IsEnabled: func() bool {
			return ts.deps.HasActivitySvc() && config.AppConfig.ActivityLogNightlyCompactionEnabled
		},
		GetInterval:            func() time.Duration { return 0 }, // scheduled by DailyAt
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
		DailyAt:                "00:10",
	})

	// AI result journal prune — delete whisper results journalled more than
	// ai_journal_retention_days ago (internal/ai/resultjournal). The journal
	// gains one entry per distinct transcribed clip and only has to carry a
	// result across a restart or re-run; the transcript lives on the book row.
	// Cheap (paged, batched deletes over one keyspace), so it rides the
	// maintenance window with the other cleanups; retention 0 keeps entries
	// forever and disables the task.
	ts.registerTask(TaskDefinition{
		Name:        "ai_journal_prune",
		Description: "Delete AI result-journal (whisper) entries older than the retention window",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.prune-ai-journal", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue prune-ai-journal: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "ai_journal_prune"), nil
		},
		IsEnabled:              func() bool { return config.AppConfig.AIJournalRetentionDays > 0 },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	// Activity DB Optimize — refresh the activity store's query-planner statistics.
	//
	// Registered here, in the TaskScheduler, and NOT relied upon via the op
	// def's own Schedule field. OperationDef.Schedule is written to the
	// ScheduleCron column of op_definitions_v2 by registry.upsertDefToDB and is
	// read by nothing: there is no cron library in the module and nothing
	// enumerates defs by schedule. A def that declares Schedule and has no
	// TaskScheduler task here does not run.
	ts.registerTask(TaskDefinition{
		Name:        "optimize_activity_db",
		Description: "Refresh the activity store's query-planner statistics",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.optimize-activity-db", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue optimize-activity-db: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "optimize_activity_db"), nil
		},
		IsEnabled:              func() bool { return ts.deps.HasActivitySvc() },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})
}
