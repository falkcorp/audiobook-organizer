// file: internal/plugins/maintenance/db.go
// version: 1.5.1
// guid: d4e5f6a7-b8c9-0123-def0-345678901234
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// dbOptimizeDef is the only registration of the database compaction.
// scheduler.db-optimize ran the same compaction under its own ID and
// ConcurrencyKey until 2026-10-10, so the two could compact the same stores at
// once; it resolves here as a FormerID, and the db_optimize scheduler task
// enqueues this def. Permissions and the 2h Timeout are the stricter gate and
// longer budget the merged def carried (a full main-store compaction measured
// 28m31s on production).
func (p *Plugin) dbOptimizeDef() sdk.OperationDef {
	sched := "0 2 * * 0" // 02:00 every Sunday
	return sdk.OperationDef{
		ID:              "maintenance.db-optimize",
		FormerIDs:       []string{"scheduler.db-optimize"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Optimize database",
		Description:     "Runs VACUUM/ANALYZE/WAL-checkpoint on all database stores.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.db-optimize",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDBOptimize,
	}
}

func (p *Plugin) runDBOptimize(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	runDBOptimizeSteps(ctx, store, p.deps, reporter, storage.DefaultInterval)
	return nil
}

// runDBOptimizeSteps compacts the main, AI-scan and OpenLibrary stores, each
// through storage.RunStep so the op log shows what Pebble is doing
// every interval and a before/after line when each store finishes. Before
// 2026-10-02 the only lines were "Compacting main database..." and, ~28
// minutes later, "Main database optimized in ...".
func runDBOptimizeSteps(ctx context.Context, store compactableStore, aux StoreOptimizer, reporter sdk.Reporter, interval time.Duration) {
	storesOptimized := 0
	const storesTotal = 3
	startTotal := time.Now()

	// 1. Main store
	//
	// The log line here said "VACUUM, ANALYZE, WAL checkpoint" until 2026-09-08.
	// That is SQLite vocabulary; the main store is Pebble and Optimize() is a
	// full `db.Compact(nil, 0xff)`. Nobody re-read it when the engine changed.
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"Compacting main database (Pebble, full keyspace); engine counters are logged every %s", interval))
	_ = reporter.UpdateProgress(0, storesTotal, "Compacting main database...")
	if err := optimizeMainWithHeartbeat(ctx, store, reporter, storesTotal, interval); err == nil {
		storesOptimized++
	}

	// 2 and 3. The AI scan and OpenLibrary stores: the core shared with
	// books.maintenance.aux-store-compact (auxStoreCompactDef).
	aux2 := compactAuxStores(ctx, aux, auxCompactEnv{
		Rep:      reporter,
		Frame:    func(current int, msg string) { _ = reporter.UpdateProgress(current, storesTotal, msg) },
		First:    1,
		Total:    storesTotal,
		Interval: interval,
		Log:      compactLog(),
	})
	storesOptimized += aux2.Optimized

	_ = reporter.UpdateProgress(storesTotal, storesTotal,
		fmt.Sprintf("Database optimization complete: %d/%d stores in %s",
			storesOptimized, storesTotal, time.Since(startTotal).Round(time.Millisecond)))
}

// auxCompactEnv is what the shared auxiliary-store core takes from its shell.
// HEAD's shell (runDBOptimizeSteps) fills it from its Reporter; the native
// kind's shell fills it from the run (05-tasks 5T.8.2 conversion rules 2 and 3).
type auxCompactEnv struct {
	// Rep receives the compaction steps' log lines and current-item labels
	// (storage.RunStep's reporter).
	Rep storage.LogReporter
	// Frame publishes a progress frame: current is the zero-based index of the
	// step in the shell's own sequence. It is called with a status line before
	// each store and, from inside a running compaction, only when an engine
	// counter moved (storage.Moved), so it is the one call that stamps liveness.
	Frame func(current int, msg string)
	// First is the zero-based index of the AI scan step in the shell's
	// sequence (HEAD 1, after the main store; native 0) and Total the length
	// of that sequence (HEAD 3, native 2). They place the steps in the "(n/m)"
	// labels and in Frame's current.
	First, Total int
	// Interval is how often a running compaction is sampled.
	Interval time.Duration
	// Log receives each step's completion line and a sampler panic.
	Log *slog.Logger
}

// auxCompactOutcome is what the core did: how many stores it compacted (a
// store with no database of its own that was asked and answered counts, as at
// HEAD), how many it failed on and why. HEAD's shell ignores Failed and Errs,
// as its op always has; the native kind turns them into the run's error.
type auxCompactOutcome struct {
	Optimized, Failed int
	Errs              []error
}

// compactAuxStores is steps 2 and 3 of HEAD's maintenance.db-optimize, shared
// by that definition and books.maintenance.aux-store-compact: the AI scan store
// and the OpenLibrary cache, each through storage.RunStep so the log shows
// what Pebble is doing every interval and a before/after line when a store
// finishes. It reaches the stores only through aux, the shell's own handle.
func compactAuxStores(ctx context.Context, aux StoreOptimizer, env auxCompactEnv) auxCompactOutcome {
	var out auxCompactOutcome
	fail := func(err error) {
		out.Failed++
		out.Errs = append(out.Errs, err)
	}

	// The AI scan store.
	env.Frame(env.First, "Optimizing AI scan database...")
	if _, owned := aux.AIScanStoreCompactionStats(); !owned {
		// Absent, or (the production wiring) sharing the main database, in
		// which case the main store's compaction already did its keys and
		// Optimize is a no-op.
		if err := aux.OptimizeAIScanStore(ctx); err != nil {
			_ = env.Rep.Log(slog.LevelError, fmt.Sprintf("AI scan DB optimization failed: %v", err))
			fail(fmt.Errorf("AI scan store: %w", err))
		} else {
			out.Optimized++
			_ = env.Rep.Log(slog.LevelInfo,
				"AI scan store has no separate database (absent or shares the main one); nothing separate to compact")
		}
	} else {
		res := storage.RunStep(ctx, env.Rep, storage.Step{
			Label:    fmt.Sprintf("Compacting AI scan database (%d/%d)", env.First+1, env.Total),
			Run:      aux.OptimizeAIScanStore,
			Stats:    func() storage.Stats { st, _ := aux.AIScanStoreCompactionStats(); return storage.Stats(st) },
			Frame:    func(msg string) { env.Frame(env.First, msg) },
			Interval: env.Interval,
			Log:      env.Log,
		})
		if res.Err == nil {
			out.Optimized++
		} else {
			fail(fmt.Errorf("AI scan store: %w", res.Err))
		}
	}

	// The OpenLibrary store.
	env.Frame(env.First+1, "Optimizing OpenLibrary cache...")
	if _, ok := aux.OLStoreCompactionStats(); !ok {
		_ = env.Rep.Log(slog.LevelInfo, "OpenLibrary cache not initialized, skipping")
	} else {
		res := storage.RunStep(ctx, env.Rep, storage.Step{
			Label:    fmt.Sprintf("Compacting OpenLibrary cache (%d/%d)", env.First+2, env.Total),
			Run:      aux.OptimizeOLStore,
			Stats:    func() storage.Stats { st, _ := aux.OLStoreCompactionStats(); return storage.Stats(st) },
			Frame:    func(msg string) { env.Frame(env.First+1, msg) },
			Interval: env.Interval,
			Log:      env.Log,
		})
		if res.Err == nil {
			out.Optimized++
		} else {
			fail(fmt.Errorf("OpenLibrary cache: %w", res.Err))
		}
	}
	return out
}

// auxStoreCompactKind is the native kind over compactAuxStores (05-tasks 5T.8.2:
// the half of maintenance.db-optimize that compacts books' separate AI-scan and
// OpenLibrary stores; the main store is core.storage.compact).
const auxStoreCompactKind tasks.Kind = "books.maintenance.aux-store-compact"

// auxStoreCompactSteps is the length of the native kind's own step sequence.
const auxStoreCompactSteps = 2

type auxStoreCompactParams struct{}

type auxStoreCompactResult struct {
	Optimized int `json:"optimized"`
	Failed    int `json:"failed"`
}

// auxStoreCompactDef returns books.maintenance.aux-store-compact over aux, the
// owner-declared handle to the auxiliary stores (conversion rule 3: the kind
// reaches them only through it). sched is the books key's schedule (F5-14
// declares modules.books.scheduler.schedules.db_optimize, a nightly member;
// F5-18 passes it in): nil gives an unscheduled kind (conversion rule 4). The
// kind has no imported runs, so it needs no FormerIDs.
//
// HEAD's definition swallowed every store failure and finished clean; the
// native run fails when a store did, after trying both. A nil aux yields a
// definition whose Run is nil, which tasks.Problems reports, so registration
// fails closed.
func auxStoreCompactDef(aux StoreOptimizer, sched *tasks.Schedule) tasks.Definition {
	return auxStoreCompactDefEvery(aux, sched, storage.DefaultInterval)
}

// auxStoreCompactKindList is the books kind as the kind list tasklint's
// unregistered-kind rule counts as a registration root (a list function without
// a receiver, named by non-test code: the blank var below does, as t6g1KindList's
// does). F5-18's Registrar.Tasks call replaces both; F5-18 deletes them.
func auxStoreCompactKindList(aux StoreOptimizer, sched *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{auxStoreCompactDef(aux, sched)}
}

var _ = auxStoreCompactKindList

// auxStoreCompactDefEvery is auxStoreCompactDef with the compaction sampling
// interval a test can shorten.
func auxStoreCompactDefEvery(aux StoreOptimizer, sched *tasks.Schedule, interval time.Duration) tasks.Definition {
	c := t6g1Common(auxStoreCompactKind, func(tasks.Kind) *tasks.Schedule { return sched })
	c.Title = "Compact the AI scan and OpenLibrary stores"
	c.Help = "Compacts books' separate AI scan database and OpenLibrary cache, logging what the engine is doing as it goes. A store that has no database of its own is skipped."
	c.Effects = tasks.Writes(tasks.Res("books", string(ops.ResAuxStores)))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.Timeout = 2 * time.Hour
	var run func(rc *tasks.RC, _ auxStoreCompactParams) (auxStoreCompactResult, error)
	if aux != nil {
		run = func(rc *tasks.RC, _ auxStoreCompactParams) (auxStoreCompactResult, error) {
			out := compactAuxStores(rc.Context(), aux, auxCompactEnv{
				Rep: auxRunReporter{rc: rc},
				// The frame is the one call that stamps liveness. RunStep sends it
				// from inside a compaction only when an engine counter moved.
				Frame: func(current int, msg string) {
					rc.Progress().Set(int64(current), tasks.Known(auxStoreCompactSteps))
					rc.Progress().Note(msg)
				},
				First:    0,
				Total:    auxStoreCompactSteps,
				Interval: interval,
				Log:      rc.Log(),
			})
			res := auxStoreCompactResult{Optimized: out.Optimized, Failed: out.Failed}
			if len(out.Errs) > 0 {
				return res, fmt.Errorf("compact auxiliary stores: %w", errors.Join(out.Errs...))
			}
			return res, nil
		}
	}
	return tasks.Task(auxStoreCompactKind, tasks.TaskSpec[auxStoreCompactParams, auxStoreCompactResult]{Common: c, Run: run})
}

// auxRunReporter adapts a native run to storage.LogReporter. Log goes to the
// run's logger, which does not stamp liveness. SetCurrentItem is dropped: a
// label every tick would publish progress the engine did not make.
type auxRunReporter struct{ rc *tasks.RC }

func (r auxRunReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	r.rc.Log().LogAttrs(r.rc.Context(), level, message, attrs...)
	return nil
}

func (auxRunReporter) SetCurrentItem(string) {}

// optimizeMainWithHeartbeat runs the main-store compaction while reporting real
// progress from Pebble's own counters.
//
// WHY THIS EXISTS. store.Optimize() is a single blocking `db.Compact(nil, 0xff)`
// with no callback, and runDBOptimize only called UpdateProgress at its three
// step boundaries. The operations registry cancels an op that reports nothing
// for five minutes, so on any database large enough for a full compaction to
// exceed 5m -- 31 GB on production -- db-optimize was killed EVERY time, and it
// is scheduled weekly (0 2 * * 0). Measured on prod 2026-09-08: enqueued
// 10:25:45, "canceling stuck op ... no progress for 5m9s" at 10:30:54.
//
// Two things made that worse than a plain failure. The op's own Timeout was then 60m,
// so the deadline the author chose never applied -- a different, shorter clock
// fired first. And the cancellation did not stop anything: the op is declared
// Cancellable:false and PebbleStore.Optimize hardcoded context.Background(), so
// the compaction ran on unsupervised for another 20 minutes while the registry
// reported it dropped. An op whose status says "dead" while its work continues
// is worse than either outcome on its own.
//
// WHY COUNTERS AND NOT A TICKER. A frame on every tick regardless of work
// would satisfy the watchdog and destroy it at the same time: a genuinely
// wedged compaction would keep reporting healthy forever. So progress FRAMES
// go out only when Pebble says work happened (storage.Moved: bytes
// written by a compaction, a compaction completed, or debt fell). Since
// 2026-10-02 the sampling itself lives in storage.RunStep, which also
// LOGS a descriptive line every tick -- logs do not stamp liveness, so that
// adds visibility without masking a wedge.
//
// compactableStore is the two methods the heartbeat actually needs. Narrow on
// purpose: OpsStore is 55 methods, and a fake for it would be 53 stubs written
// to exercise two.
type compactableStore interface {
	Optimize(ctx context.Context) error
	CompactionStats() database.CompactionStats
}

func optimizeMainWithHeartbeat(ctx context.Context, store compactableStore, reporter sdk.Reporter, storesTotal int, interval time.Duration) error {
	res := storage.RunStep(ctx, reporter, storage.Step{
		Label:    fmt.Sprintf("Compacting main database (1/%d)", storesTotal),
		Run:      store.Optimize,
		Stats:    func() storage.Stats { return storage.Stats(store.CompactionStats()) },
		Frame:    func(msg string) { _ = reporter.UpdateProgress(0, storesTotal, msg) },
		Interval: interval,
		Log:      compactLog(),
	})
	return res.Err
}

// compactLog is the logger of a compaction step's own lines (its completion
// line and a sampler panic): the "db-optimize" subsystem logger the sampler
// held as a package variable before it moved into core's storage, whose
// RunStep takes a *slog.Logger instead (04 §8.5 R1). Each line still reads
// "db-optimize: <line>" on stdout and in the activity log's system rows.
func compactLog() *slog.Logger {
	return slog.New(standardLoggerHandler{l: logger.New("db-optimize")})
}

// standardLoggerHandler writes slog records through an internal/logger
// StandardLogger at the record's level. The message is passed as a "%s"
// argument, never as a format, and attributes are appended in
// logger.FormatAttrs form; groups are flattened.
type standardLoggerHandler struct {
	l     *logger.StandardLogger
	attrs []slog.Attr
}

func (h standardLoggerHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h standardLoggerHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := slices.Clone(h.attrs)
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	msg := r.Message + logger.FormatAttrs(attrs)
	switch {
	case r.Level >= slog.LevelError:
		h.l.Error("%s", msg)
	case r.Level >= slog.LevelWarn:
		h.l.Warn("%s", msg)
	case r.Level >= slog.LevelInfo:
		h.l.Info("%s", msg)
	default:
		h.l.Debug("%s", msg)
	}
	return nil
}

func (h standardLoggerHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return standardLoggerHandler{l: h.l, attrs: append(slices.Clone(h.attrs), as...)}
}

func (h standardLoggerHandler) WithGroup(string) slog.Handler { return h }
