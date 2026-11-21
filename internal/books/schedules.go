// file: internal/books/schedules.go
// version: 0.1.0
// guid: b14a5be4-450b-41ad-a920-68a02040f179
// last-edited: 2026-11-21

package books

import (
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// Books' schedules (F5-14; 05-tasks §5T.3.8). One declaration per HEAD
// TaskDefinition (internal/scheduler/tasks.go) that fires a books kind, on the
// kind HEAD's def converts to ("books." + taskV2DefIDs[name]), each read from
// modules.books.scheduler.schedules.<key> (internal/books/config.go). The key
// carries what HEAD read from its scheduled.<entry> settings (enabled, the
// interval) and the nightly-window placement HEAD's maintenanceOrder gave the
// task; everything else HEAD's IsEnabled, RunOnStart and RunInMaintenanceWindow
// read is a gate here, read live from books' config through ScheduleDeps.
//
// Three HEAD tasks are not here: transcode (deleted, it never fired),
// batch_poller (core.ai.batch-poll owns core.ai.schedules.batch_poll) and
// purge_old_logs (core.log.journal.prune owns core.log.schedules.journal_prune).
// db_optimize is here for its books half only, books.maintenance.aux-store-compact;
// its core half, core.storage.compact, reads core.storage.schedules.compact.
//
// None of the 15 v2 OperationDef.Schedule cron strings carries over (Q-C, D15):
// the 14 defs a TaskDefinition fires are scheduled by that task's row below,
// and deluge.protected-paths-sync, which no task fires, is manual only.
//
// TestScheduleParity_AllDefinitions (schedule_parity_test.go) runs HEAD's
// scheduler and core's over the same config and holds them to the same
// sequence of starts, clock advances and window members.

// ScheduleDeps is what books' schedules read outside their own keys. Config is
// module.Deps.Config (only Lookup is used); the presence functions are HEAD's
// SchedulerDeps of the same names; the two Pending functions are HEAD's idle
// checks, which skip a fire while there is nothing to do.
type ScheduleDeps struct {
	Config scheduleConfigReads
	// Log receives the idle checks' errors (HEAD logged them as a failed
	// scheduled task). nil discards them.
	Log                 *slog.Logger
	HasDedupPlugin      func() bool
	HasActivitySvc      func() bool
	HasMetadataFetchSvc func() bool
	// IdentificationPending reports whether the identification dirty set
	// holds a book (HEAD's identDirtySetNonEmpty: true when the store has no
	// dirty pager). nil is "pending".
	IdentificationPending func() (bool, error)
	// CollectionsRefreshPending reports whether a relative-date dynamic
	// collection exists and the search index is open. nil is "pending", as
	// HEAD did no check when its dep was nil.
	CollectionsRefreshPending func() (bool, error)
}

// scheduleConfigReads is the part of module.ConfigReads the gates use.
type scheduleConfigReads interface {
	Lookup(p config.Path) (config.KeyState, bool)
}

// scheduleKeyPrefix is the section every books schedule key lives in.
const scheduleKeyPrefix = "modules.books.scheduler.schedules."

// The gate inputs: books (and one core) config paths HEAD's tasks read
// outside their scheduled.<entry> settings. Every modules.books path here is
// declared by booksSettings (TestScheduleFor_KeysDeclaredAndValid).
const (
	gateScanOnStartup         config.Path = "modules.books.scan.on_startup"
	gateScanPeriodicEnabled   config.Path = "modules.books.scan.periodic_enabled"
	gateEnableAIParsing       config.Path = "modules.books.ai.enable_ai_parsing"
	gateIdentificationAdvance config.Path = "modules.books.metadata.identification_advance_enabled"
	gateNightlyCompaction     config.Path = "modules.books.activity.log_nightly_compaction_enabled"
	gateCatalogEnabled        config.Path = "modules.books.catalog.enabled"
	gatePurgeSoftDeletedDays  config.Path = "modules.books.purge.soft_deleted_after_days"
	gateAIJournalRetention    config.Path = "core.ai.journal_retention"
	gateMaintLibraryScan      config.Path = "modules.books.maintenance.library_scan"
	gateMaintLibraryOrganize  config.Path = "modules.books.maintenance.library_organize"
	gateMaintLibrarySize      config.Path = "modules.books.maintenance.library_size_refresh"
	gateMaintDedupRefresh     config.Path = "modules.books.maintenance.dedup_refresh"
	gateMaintSeriesPrune      config.Path = "modules.books.maintenance.series_prune"
	gateMaintAuthorSplit      config.Path = "modules.books.maintenance.author_split"
	gateMaintDBOptimize       config.Path = "modules.books.maintenance.db_optimize"
	gateMaintAcoustIDOnline   config.Path = "modules.books.maintenance.acoustid_online_lookup"
	gateMaintAcoustIDLimit    config.Path = "modules.books.maintenance.acoustid_nightly_limit"
	gateMaintPurgeDeleted     config.Path = "modules.books.maintenance.purge_deleted"
	gateMaintTombstoneCleanup config.Path = "modules.books.maintenance.tombstone_cleanup"
	gateMaintReconcile        config.Path = "modules.books.maintenance.reconcile"
)

// scheduleGateInputs lists every gate input, for the declaration test.
var scheduleGateInputs = []config.Path{
	gateScanOnStartup, gateScanPeriodicEnabled, gateEnableAIParsing, gateIdentificationAdvance,
	gateNightlyCompaction, gateCatalogEnabled, gatePurgeSoftDeletedDays, gateAIJournalRetention,
	gateMaintLibraryScan, gateMaintLibraryOrganize, gateMaintLibrarySize, gateMaintDedupRefresh,
	gateMaintSeriesPrune, gateMaintAuthorSplit, gateMaintDBOptimize, gateMaintAcoustIDOnline,
	gateMaintAcoustIDLimit, gateMaintPurgeDeleted, gateMaintTombstoneCleanup, gateMaintReconcile,
}

// scheduleMode is the run mode a scheduled start takes. HEAD's TriggerFn
// params decide it: an op that resolves an absent dry_run as a preview and
// was sent none ran a preview; dry_run=false (opmode.Live()) ran live; a
// read-only kind states none (05-tasks §5T.3.1).
type scheduleMode uint8

const (
	modeNone scheduleMode = iota
	modeLive
	modePreview
)

// scheduleDecl is one HEAD task's declaration.
type scheduleDecl struct {
	task string     // HEAD's TaskDefinition name
	kind tasks.Kind // the books kind it fires
	key  string     // under modules.books.scheduler.schedules
	mode scheduleMode
	// gates adds HEAD's gates outside the key (nil: none).
	gates func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule
}

// scheduleDecls is every books schedule, in HEAD's registration order.
var scheduleDecls = []scheduleDecl{
	// library_scan, ruling (A): the key stays enabled (its enabled field is not
	// the switch; modules.books.scan.periodic_enabled is). HEAD's IsEnabled was
	// scheduled.library_scan.enabled || scan_on_startup: the interval ticked on
	// the first alone, the start ran on the second alone, and the window member
	// needed either one plus maintenance.library_scan.
	{task: "library_scan", kind: "books.library.scan", key: "library_scan", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			periodic, sos, maint := d.flag(gateScanPeriodicEnabled), d.flag(gateScanOnStartup), d.flag(gateMaintLibraryScan)
			return s.IntervalWhen(periodic).
				OnStartWhen(sos).
				WindowWhen(func() bool { return maint() && (periodic() || sos()) })
		}},
	{task: "library_scan_full", kind: "books.library.scan-full", key: "library_scan_full", mode: modeLive},
	{task: "library_organize", kind: "books.library.organize", key: "library_organize", mode: modeLive,
		gates: windowToggle(gateMaintLibraryOrganize)},
	{task: "library_size_refresh", kind: "books.library.size-refresh", key: "library_size_refresh", mode: modeNone,
		gates: windowToggle(gateMaintLibrarySize)},
	{task: "dedup_refresh", kind: "books.dedup.author-scan", key: "dedup_refresh", mode: modeNone,
		gates: windowToggle(gateMaintDedupRefresh)},
	{task: "label_refinement", kind: "books.dedup.label-refinement", key: "label_refinement", mode: modeNone,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, present(d.HasDedupPlugin)) }},
	{task: "dedup_llm_review", kind: "books.dedup.llm-review", key: "dedup_llm_review", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, present(d.HasDedupPlugin)) }},
	{task: "series_prune", kind: "books.dedup.series-prune", key: "series_prune", mode: modeLive,
		gates: windowToggle(gateMaintSeriesPrune)},
	// series_normalize: HEAD's IsEnabled was a constant false (manual only);
	// the key's default is off.
	{task: "series_normalize", kind: "books.dedup.series-normalize", key: "series_normalize", mode: modeLive},
	{task: "asin_backfill", kind: "books.metafetch.asin-backfill", key: "asin_backfill", mode: modeLive},
	{task: "candidate_fetch", kind: "books.metadata.candidate-fetch", key: "candidate_fetch", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			markDirty := d.flag(gateIdentificationAdvance)
			return s.ParamsFunc(func() any {
				return candidateFetchSchedParams{Unfetched: true, MarkDirtyOnly: markDirty()}
			})
		}},
	{task: "identification_advance", kind: "books.identification.advance", key: "identification_advance", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			// The flag is HEAD's IsEnabled (enabledBy); the idle check ran in
			// TriggerFn after the clock moved (When).
			enabled, pending := d.flag(gateIdentificationAdvance), d.pending("identification_advance", d.IdentificationPending)
			return enabledBy(s, enabled).When(pending)
		}},
	{task: "collections_refresh", kind: "books.collections.refresh-relative-dynamic", key: "collections_refresh", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			return s.When(d.pending("collections_refresh", d.CollectionsRefreshPending))
		}},
	{task: "catalog_harvest", kind: "books.catalog.harvest-authors", key: "catalog_harvest", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, d.flag(gateCatalogEnabled)) }},
	{task: "acoustid_backfill", kind: "books.acoustid.backfill", key: "acoustid_backfill", mode: modeLive},
	{task: "temp_file_cleanup", kind: "books.maintenance.temp-file-cleanup", key: "temp_file_cleanup", mode: modeLive,
		gates: onStart},
	{task: "transcode_temp_cleanup", kind: "books.maintenance.transcode-temp-cleanup", key: "transcode_temp_cleanup", mode: modeLive,
		gates: onStart},
	{task: "trash_cleanup", kind: "books.maintenance.trash-cleanup", key: "trash_cleanup", mode: modeLive},
	{task: "reports_prune", kind: "books.maintenance.reports-prune", key: "reports_prune", mode: modeLive},
	{task: "metadata_upgrade", kind: "books.scheduler.metadata-upgrade", key: "metadata_upgrade", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, present(d.HasMetadataFetchSvc)) }},
	{task: "author_split_scan", kind: "books.maintenance.author-split-scan", key: "author_split", mode: modePreview,
		gates: windowToggle(gateMaintAuthorSplit)},
	{task: "db_optimize", kind: "books.maintenance.aux-store-compact", key: "db_optimize", mode: modeLive,
		gates: windowToggle(gateMaintDBOptimize)},
	// acoustid_online_lookup: HEAD gated it on maintenance.acoustid_online_lookup
	// with no interval and left it out of maintenanceOrder, so no timer and no
	// window ever reached it (manual only). The key's default is off, with no
	// window; the gate and the params stay for an operator who schedules it.
	{task: "acoustid_online_lookup", kind: "books.acoustid.lookup-online", key: "acoustid_online_lookup", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			limit := d.intAt(gateMaintAcoustIDLimit)
			return enabledBy(s, d.flag(gateMaintAcoustIDOnline)).ParamsFunc(func() any {
				return acoustIDLookupSchedParams{Limit: limit(), Force: false}
			})
		}},
	{task: "cleanup_old_backups", kind: "books.maintenance.cleanup-old-backups", key: "cleanup_old_backups", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, d.flag(gateMaintDBOptimize)) }},
	{task: "purge_deleted", kind: "books.maintenance.purge-deleted", key: "purge_deleted", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			// HEAD's IsEnabled was the retention alone; it gated the interval,
			// the start run and (with the toggle) the window. The start also
			// needs the key's own enabled flag, which a plain OnStart reads but
			// OnStartWhen does not: a disabled key runs no purge at boot.
			days, maint, key := d.intAt(gatePurgeSoftDeletedDays), d.flag(gateMaintPurgeDeleted), d.scheduleOn("purge_deleted")
			retained := func() bool { return days() > 0 }
			return s.IntervalWhen(retained).
				OnStartWhen(func() bool { return key() && retained() }).
				WindowWhen(func() bool { return retained() && maint() })
		}},
	{task: "tombstone_cleanup", kind: "books.maintenance.tombstone-cleanup", key: "tombstone_cleanup", mode: modeLive,
		gates: windowToggle(gateMaintTombstoneCleanup)},
	{task: "file_integrity_check", kind: "books.maintenance.file-integrity-check", key: "file_integrity_check", mode: modeNone},
	{task: "orphan_book_files_cleanup", kind: "books.maintenance.orphan-book-files-cleanup", key: "orphan_book_files_cleanup", mode: modeNone},
	{task: "resolve_production_authors", kind: "books.maintenance.resolve-production-authors", key: "resolve_production_authors", mode: modePreview},
	{task: "metadata_refresh", kind: "books.maintenance.metadata-refresh", key: "metadata_refresh", mode: modeNone},
	{task: "reconcile_scan", kind: "books.reconcile.scan", key: "reconcile", mode: modeNone,
		gates: windowToggle(gateMaintReconcile)},
	{task: "ai_dedup_batch", kind: "books.maintenance.ai-dedup-batch", key: "ai_dedup_batch", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, d.flag(gateEnableAIParsing)) }},
	{task: "cleanup_activity_log", kind: "books.maintenance.cleanup-activity-log", key: "cleanup_activity_log", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return enabledBy(s, present(d.HasActivitySvc)) }},
	{task: "nightly_activity_compaction", kind: "books.maintenance.nightly-compact-activity-log", key: "nightly_activity_compaction", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			svc, flag := present(d.HasActivitySvc), d.flag(gateNightlyCompaction)
			return enabledBy(s, func() bool { return svc() && flag() })
		}},
	{task: "ai_journal_prune", kind: "books.maintenance.prune-ai-journal", key: "ai_journal_prune", mode: modeLive,
		gates: func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule {
			retention := d.durationAt(gateAIJournalRetention)
			return enabledBy(s, func() bool { return retention() > 0 })
		}},
}

// candidateFetchSchedParams is HEAD's candidateFetchTaskParams: the scheduled
// fetch takes only unfetched books, and while identification_advance_enabled
// is on it marks them for identification.advance instead of fetching.
type candidateFetchSchedParams struct {
	Unfetched     bool `json:"unfetched"`
	MarkDirtyOnly bool `json:"mark_dirty_only,omitempty"`
}

// acoustIDLookupSchedParams is what HEAD's acoustid_online_lookup sent:
// maintenance.acoustid_nightly_limit, never forced.
type acoustIDLookupSchedParams struct {
	Limit int  `json:"limit"`
	Force bool `json:"force"`
}

// NewSchedules returns the sched hook F5-18 passes to every conversion's list
// function: the schedule of a books kind, or nil (every core.* kind, and every
// books kind HEAD did not schedule).
func NewSchedules(d ScheduleDeps) func(tasks.Kind) *tasks.Schedule {
	byKind := make(map[tasks.Kind]*tasks.Schedule, len(scheduleDecls))
	for _, decl := range scheduleDecls {
		byKind[decl.kind] = decl.schedule(d)
	}
	return func(k tasks.Kind) *tasks.Schedule { return byKind[k] }
}

// schedule builds decl's schedule over d.
func (decl scheduleDecl) schedule(d ScheduleDeps) *tasks.Schedule {
	s := tasks.ScheduleFromConfig(scheduleKeyPrefix + decl.key)
	switch decl.mode {
	case modeLive:
		s = s.Live()
	case modePreview:
		s = s.Preview()
	}
	if decl.gates != nil {
		s = decl.gates(d, s)
	}
	return s
}

// ScheduledKinds lists, sorted, every books kind NewSchedules returns a
// schedule for. It reads no dependency.
func ScheduledKinds() []tasks.Kind {
	out := make([]tasks.Kind, 0, len(scheduleDecls))
	for _, decl := range scheduleDecls {
		out = append(out, decl.kind)
	}
	slices.Sort(out)
	return out
}

// onStart is HEAD's RunOnStart: true.
func onStart(_ ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return s.OnStart() }

// windowToggle is HEAD's RunInMaintenanceWindow toggle on a task whose
// IsEnabled is its key: the toggle takes the task out of the nightly window
// and leaves its interval alone.
func windowToggle(p config.Path) func(ScheduleDeps, *tasks.Schedule) *tasks.Schedule {
	return func(d ScheduleDeps, s *tasks.Schedule) *tasks.Schedule { return s.WindowWhen(d.flag(p)) }
}

// enabledBy is a gate HEAD read in IsEnabled. IsEnabled decided at Start
// whether the task got a ticker at all, so a closed gate left its interval
// clock unwritten (or frozen where it stood) and the first boot that found it
// open caught up once on an overdue clock: IntervalWhen, not When, whose
// closed fires advance the clock. The window op re-read IsEnabled for each
// member, so the same gate closes the window (WindowWhen, re-read when the
// window reaches the member). Only a check HEAD made inside TriggerFn, after
// claimIntervalRun moved the clock, is a When gate (pending).
func enabledBy(s *tasks.Schedule, gate func() bool) *tasks.Schedule {
	return s.IntervalWhen(gate).WindowWhen(gate)
}

// present is a presence dependency; nil is absent (HEAD read a nil
// HasDedupPlugin as absent).
func present(f func() bool) func() bool {
	return func() bool { return f != nil && f() }
}

// pending is an idle check. HEAD's TriggerFn skipped the fire when it found
// nothing to do, and also when the check failed: it returned the error before
// enqueueing (tasks.go identification_advance, collections_refresh), after
// claimIntervalRun had already moved the clock. So an error reads as closed
// (When's gated fire: the clock advances, nothing starts) and is logged, as
// HEAD logged it. nil is open.
func (d ScheduleDeps) pending(task string, f func() (bool, error)) func() bool {
	return func() bool {
		if f == nil {
			return true
		}
		ok, err := f()
		if err != nil {
			d.logger().Warn("scheduled task skipped: its idle check failed", "task", task, "err", err)
			return false
		}
		return ok
	}
}

func (d ScheduleDeps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.New(slog.DiscardHandler)
}

// flag reads a boolean gate input at each call.
func (d ScheduleDeps) flag(p config.Path) func() bool {
	return func() bool {
		var b bool
		d.read(p, &b)
		return b
	}
}

// scheduleOn reads the enabled flag of the books schedule key name at each
// call (a closed gate when unreadable).
func (d ScheduleDeps) scheduleOn(name string) func() bool {
	p := config.Path(scheduleKeyPrefix + name)
	return func() bool {
		var c tasks.ScheduleConfig
		d.read(p, &c)
		return c.Enabled
	}
}

// intAt reads an integer gate input at each call.
func (d ScheduleDeps) intAt(p config.Path) func() int {
	return func() int {
		var n int
		d.read(p, &n)
		return n
	}
}

// durationAt reads a duration gate input at each call.
func (d ScheduleDeps) durationAt(p config.Path) func() config.Duration {
	return func() config.Duration {
		var v config.Duration
		d.read(p, &v)
		return v
	}
}

// read decodes p's effective value into v, leaving v at its zero value (a
// closed gate) when p is unregistered or its value does not decode; both are
// logged, as neither can happen for a declared key of the right type.
func (d ScheduleDeps) read(p config.Path, v any) {
	if d.Config == nil {
		return
	}
	ks, ok := d.Config.Lookup(p)
	if !ok {
		d.logger().Warn("schedule gate input is not a registered config key; the gate reads closed", "path", string(p))
		return
	}
	if len(ks.Effective) == 0 {
		return
	}
	if err := json.Unmarshal(ks.Effective, v); err != nil {
		d.logger().Warn("schedule gate input does not decode; the gate reads closed", "path", string(p), "err", err)
	}
}
