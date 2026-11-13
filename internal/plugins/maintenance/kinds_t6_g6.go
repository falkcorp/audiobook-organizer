// file: internal/plugins/maintenance/kinds_t6_g6.go
// version: 1.0.0
// guid: d78a6104-de76-4b10-ab6f-1cf177912760
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"golang.org/x/sync/errgroup"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Native books Task kinds for T6 group 6 of the maintenance plugin (F5-11,
// 05-tasks 5T.8.2 group T2): 14 definitions, one per file. They are DARK:
// nothing registers them before the tasks flip (F5-18), which also removes the
// HEAD v2 shells.
//
// Every definition has two shells over one core. HEAD's Run (the v2 shell) keeps
// its param decoding and its Reporter and calls <name>Core; the native shell is
// a tasks.Task and calls the same core. A core reaches the store only through
// the env it is handed: reads through env.Store, writes through env.Writer, and
// everything else (logging, progress, per-item pools) through the hooks on
// env. The HEAD shell fills the env from the plugin's deps; the native shell
// fills it from the run's context, with the writer the kernel gates
// (deps.NewWriter(rc.Writer())).

const (
	// t6g6KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	t6g6KindPrefix = "books."

	// t6g6SignalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false).
	t6g6SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t6g6SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (internal/books/atoms.go). internal/books imports this package through
// its import graph, so the table cannot be read from here; it is the same copy
// internal/maintenance/jobs/kinds.go keeps, and drifts the same way.
var t6g6SettingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// t6g6StartRequirement is rule R6 for all 14 definitions (every one is gated on
// settings.manage at HEAD, directly or through an empty Permissions list that
// the registry defaults to it).
func t6g6StartRequirement() rbac.Requirement {
	return rbac.AllOf(t6g6SettingsManageScopes...)
}

// t6g6Env is what every core takes from its shell besides the stores.
type t6g6Env struct {
	// Live is false for a dry run (HEAD) or a Preview run (native).
	Live bool
	// Log writes a run-log line at level.
	Log func(level slog.Level, msg string, attrs ...slog.Attr)
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// Canceled reports whether the run was canceled (HEAD: Reporter.IsCanceled).
	Canceled func() bool
	// Logger is the run's logger, for the cores that log with attributes.
	Logger *slog.Logger
	// OpID is the id of the run: HEAD's operation id ("" when the reporter cannot
	// say) or the native RunID.
	OpID string
	// Items runs a per-item loop on the shell's pool.
	Items t6g6ItemsFunc
	// SetResult persists a result document: HEAD's ReporterSetResult. Nil in a
	// native shell, whose Task returns the result instead.
	SetResult func(v any) error
	// Reporter is the sdk.Reporter the shared helpers that take one (the batch
	// writer's liveness stamps, the pre-write heartbeat) are handed: HEAD's own
	// Reporter, or a sink over Progress for a native run (whose executor stamps
	// liveness from progress itself).
	Reporter sdk.Reporter
	// StandDown parks the library scan for a live apply's write phase. Nil means
	// no interlock is available (a core that needs one refuses to apply).
	StandDown func(ctx context.Context, reason string) (t6g6Hold, error)
}

// t6g6Hold is a held scan stand-down.
type t6g6Hold struct {
	// Lost renews the lease and reports whether it lapsed (the caller must stop
	// writing). Native: always false, the kernel fences the writer instead.
	Lost func() bool
	// Release gives the hold back; safe to call when nothing was held.
	Release func()
}

// t6g6ItemsOptions are the per-loop settings a core hands its pool.
type t6g6ItemsOptions struct {
	// Collect runs every item and joins the item errors (HEAD ErrModeCollect);
	// otherwise the first error stops dispatch and is returned.
	Collect bool
	// ProgressBase and ProgressTotal place a slice of a larger set on the
	// progress bar.
	ProgressBase, ProgressTotal int
	// Label is HEAD's per-item progress label. Called inside the worker
	// goroutines, so it reads counters atomically. The native pool labels
	// nothing from it.
	Label func(i, total int) string
}

// t6g6ItemsFunc runs fn over the indexes 0..n-1 on the shell's pool with the
// parallelism par (the core's own, R4).
type t6g6ItemsFunc func(ctx context.Context, par tasks.Parallelism, n int, o t6g6ItemsOptions, fn func(ctx context.Context, i int) error) error

// t6g6HeadItems is HEAD's pool: registry.RunItems over the indexes with the
// worker count the parallelism names (RunItems clamps below 1 up to 1, so a
// Sequential parallelism is the plain loop it was).
func t6g6HeadItems(rep sdk.Reporter) t6g6ItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, o t6g6ItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		mode := opsregistry.ErrModeFail
		if o.Collect {
			mode = opsregistry.ErrModeCollect
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, opsregistry.RunItemsOptions{
			Concurrency:    par.Workers(),
			ErrMode:        mode,
			ProgressOffset: o.ProgressBase,
			ProgressTotal:  o.ProgressTotal,
			Label:          o.Label,
		})
	}
}

// t6g6NativeItems is the native pool: tasks.RunItems over the indexes. A failed
// item's own error comes back, as at HEAD (the first one, or every one joined
// in Collect mode), not the ItemsError around it.
func t6g6NativeItems(rc *tasks.RC) t6g6ItemsFunc {
	return func(_ context.Context, par tasks.Parallelism, n int, o t6g6ItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		mode := tasks.FailFast
		if o.Collect {
			mode = tasks.Collect
		}
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error {
			return fn(ic.Context(), i)
		}, tasks.ItemsOptions[int]{OnError: mode, ProgressBase: o.ProgressBase, ProgressTotal: o.ProgressTotal})
		var ie *tasks.ItemsError
		if errors.As(err, &ie) {
			if o.Collect && len(ie.Errs) > 0 {
				return errors.Join(ie.Errs...)
			}
			if ie.First != nil {
				return ie.First
			}
		}
		return err
	}
}

// t6g6HeadEnv fills the env from HEAD's Reporter.
func t6g6HeadEnv(rep opsregistry.Reporter, live bool) t6g6Env {
	return t6g6Env{
		Live: live,
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			_ = rep.Log(level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			_ = rep.UpdateProgress(current, total, msg)
		},
		Canceled:  rep.IsCanceled,
		Logger:    rep.Logger(),
		OpID:      opsregistry.ReporterOpID(rep),
		Items:     t6g6HeadItems(rep),
		SetResult: func(v any) error { return opsregistry.ReporterSetResult(rep, v) },
		Reporter:  rep,
	}
}

// t6g6NativeEnv fills the env from the native run.
func t6g6NativeEnv(rc *tasks.RC) t6g6Env {
	e := t6g6Env{
		Live: rc.Mode() == tasks.ModeLive,
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			rc.Log().LogAttrs(rc.Context(), level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			t := tasks.Unknown()
			if total > 0 {
				t = tasks.Known(int64(total))
			}
			rc.Progress().Set(int64(current), t)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		Canceled: rc.Canceled,
		Logger:   rc.Log(),
		OpID:     string(rc.RunID()),
		Items:    t6g6NativeItems(rc),
	}
	e.Reporter = t6g6ProgressSink{fn: e.Progress}
	return e
}

// t6g6NativeStandDown is the native stand-down: rc.StandDown over key. An empty
// key refuses, so a live apply never runs without the interlock. The kernel
// fences the run's writer if the lease is lost, so Lost is always false.
func t6g6NativeStandDown(rc *tasks.RC, key tasks.Key) func(ctx context.Context, reason string) (t6g6Hold, error) {
	return func(_ context.Context, reason string) (t6g6Hold, error) {
		if key == "" {
			return t6g6Hold{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t6g6Hold{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t6g6Hold{Lost: func() bool { return false }, Release: h.Release}, nil
	}
}

// t6g6HeadStandDown is HEAD's stand-down (acquireScanStandDownForApply over the
// plugin's scan controller).
func t6g6HeadStandDown(scan ScanController, rep sdk.Reporter) func(ctx context.Context, reason string) (t6g6Hold, error) {
	return func(ctx context.Context, reason string) (t6g6Hold, error) {
		holderID, held, release, err := acquireScanStandDownForApply(ctx, scan, rep, reason)
		if err != nil {
			return t6g6Hold{}, err
		}
		return t6g6Hold{
			Lost:    func() bool { return scanStandDownLostForApply(scan, holderID, held) },
			Release: release,
		}, nil
	}
}

// t6g6KindFor is the kind name rule R3 gives a HEAD def ID.
func t6g6KindFor(headID string) tasks.Kind { return tasks.Kind(t6g6KindPrefix + headID) }

// t6g6Common is the Common block every definition shares; the caller fills the
// per-definition fields (Title, Help, Effects, Resume, Priority, Timeout...).
func t6g6Common(kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Perms: tasks.Permissions{Start: t6g6StartRequirement()},
		Ready: []tasks.ReadySignal{t6g6SignalMemDB},
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// t6g6LiveOnly is the refusal a live-only writer gives a Preview run: HEAD has
// no preview for it, and a native Preview must not write.
func t6g6LiveOnly(rc *tasks.RC, kind tasks.Kind) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// t6g6ProgressSink adapts an env.Progress hook to the sdk.Reporter that
// sdk.NewProgress takes; NewProgress only calls UpdateProgress.
type t6g6ProgressSink struct {
	sdk.Reporter
	fn func(current, total int, msg string)
}

func (s t6g6ProgressSink) UpdateProgress(current, total int, msg string) error {
	s.fn(current, total, msg)
	return nil
}

// t6g6NewProgress is sdk.NewProgress over the env's progress hook.
func t6g6NewProgress(env t6g6Env, n int) *sdk.Progress {
	return sdk.NewProgress(t6g6ProgressSink{fn: env.Progress}, n)
}

// t6g6ErrgroupItems is a pool for the cores whose HEAD body ran its own bounded
// errgroup rather than registry.RunItems: the first error stops dispatch and is
// returned, as errgroup.Group.Wait does. HEAD's shell uses it so the loop keeps
// its HEAD behaviour; the native shells hand the core the executor's pool
// instead.
func t6g6ErrgroupItems(ctx context.Context, par tasks.Parallelism, n int, _ t6g6ItemsOptions, fn func(ctx context.Context, i int) error) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(par.Workers(), 1))
	for i := range n {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			return fn(gctx, i)
		})
	}
	return g.Wait()
}

// t6g6Store is every read the 14 definitions make, composed from each
// definition's own narrow interface. A method two definitions share has one
// signature, so the embeds overlap cleanly. Nothing here writes.
type t6g6Store interface {
	orphanBookFilesCleanupStore
	orphanRepointPlanStore
	opChangeBookIndexStore
	probeDirectoryBooksStore
	recoverMissingFilesStore
	regroupShatteredAIStore
	relinkUnlinkedBooksStore
	repairTranscribeStatusStore
	rbfReader
}

// t6g6Writer is every write the 14 definitions make, composed from each
// definition's own narrow interface. The native shells build it with
// t6g6Deps.NewWriter over the run's kernel-gated tasks.Writer, so every write
// goes through the gate (and is recorded, not executed, in a Preview run).
type t6g6Writer interface {
	nightlyCompactActivityLogWriter
	opChangeBookIndexWriter
	recompactActivityDigestsWriter
	itunesHealWriter
	recoverMissingFilesWriter
	regroupShatteredAIWriter
	database.ReviewItemPendingDeleter
	relinkUnlinkedBooksWriter
	probeDirectoryBooksWriter
	repairMergedUserStateWriter
	repairTranscribeStatusWriter
	rbfWriter
}

// t6g6Deps is what the native kinds are built from. Every field is set once, by
// the F5-18 registration; nothing here is read from a plugin receiver.
type t6g6Deps struct {
	// Store is the read side of the library store.
	Store t6g6Store
	// NewWriter builds the write side over the run's gated writer. Per run: the
	// writer it wraps belongs to that run's executor, which is why this is a
	// constructor and not a field of the store (conversion rule 3).
	NewWriter func(tasks.Writer) t6g6Writer
	// ActivityLogFullDetailDays is the configured activity_log_full_detail_days.
	ActivityLogFullDetailDays func() int
	// StandDownKey is the concurrency key of the library scan kinds, which a live
	// apply parks while it writes (rc.StandDown). Empty refuses a live apply that
	// needs the stand-down: it never runs without it. F5-18 sets it.
	StandDownKey tasks.Key
	// RootDir is the library root.
	RootDir func() string
	// ResolveReportPath resolves where a per-run report file goes (the plugin's
	// resolveReportPath: explicit path, or fileName under the root's report dir).
	ResolveReportPath func(explicit, fileName string) (string, error)
	// ITunesRoots resolves the protected iTunes roots (merge.ITunesProtectedRoots
	// over the configured iTunes settings).
	ITunesRoots func() ([]string, error)
}

// t6g6HeadStore is HEAD's reads: the plugin's OpsStore plus the reads that are a
// helper over it rather than a method of it. OpsStore is not a database.Store,
// so a wrapper over it hides capability methods; the ones a definition needs are
// resolved here through database.AsCapability.
type t6g6HeadStore struct{ OpsStore }

// AllBookFilesCoreIncludingRetired is every book_file row, retired included.
func (s t6g6HeadStore) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.OpsStore)
}

// OpChangeIndexVerifier resolves the index's verify surface through the
// decorator chain; desc is the store's type for the refusal messages.
func (s t6g6HeadStore) OpChangeIndexVerifier() (opChangeIndexVerifier, string, bool) {
	v, ok := database.AsCapability[opChangeIndexVerifier](s.OpsStore)
	return v, fmt.Sprintf("%T", s.OpsStore), ok
}

// t6g6HeadWriter is HEAD's writes: what each HEAD shell called, on the object it
// called it on (the plugin's deps for the activity compactions, the store for the
// rows and the index).
type t6g6HeadWriter struct {
	deps  ServerDeps
	store OpsStore
}

func (w t6g6HeadWriter) CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error) {
	return w.deps.CompactActivityEntries(ctx, cutoff, progress)
}

func (w t6g6HeadWriter) RecompactActivityDigests(ctx context.Context) (database.RecompactResult, error) {
	return w.deps.RecompactActivityDigests(ctx)
}

func (w t6g6HeadWriter) CreateBookFile(file *database.BookFile) error {
	return w.store.CreateBookFile(file)
}

func (w t6g6HeadWriter) builder() (opChangeIndexBuilder, error) {
	b, ok := database.AsCapability[opChangeIndexBuilder](w.store)
	if !ok {
		return nil, fmt.Errorf("store %T has no opchange_by_book index", w.store)
	}
	return b, nil
}

func (w t6g6HeadWriter) RebuildOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookBackfillResult, error) {
	b, err := w.builder()
	if err != nil {
		return database.OpChangeByBookBackfillResult{}, err
	}
	return b.RebuildOpChangeByBookIndex(ctx, progress)
}

func (w t6g6HeadWriter) EnsureOpChangeByBookIndexWith(ctx context.Context, opts database.OpChangeEnsureOptions) (database.OpChangeByBookEnsureResult, error) {
	b, err := w.builder()
	if err != nil {
		return database.OpChangeByBookEnsureResult{}, err
	}
	return b.EnsureOpChangeByBookIndexWith(ctx, opts)
}

// t6g6Kinds returns the 14 native definitions of group 6 in file order. sched
// hands a kind its schedule (nil, or a nil result, means none): the schedule of
// a kind is the registration's to give, not this file's (R5).
func (p *Plugin) t6g6Kinds(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.nightlyCompactActivityLogNative(deps, sched),
		p.opChangeBookIndexNative(deps, sched),
		p.optimizeNative(deps, sched),
		p.orphanBookFilesCleanupNative(deps, sched),
		p.orphanBookFilesRepointPlanNative(deps, sched),
		p.probeDirectoryBooksNative(deps, sched),
		p.recompactActivityDigestsNative(deps, sched),
		p.itunesHealNative(deps, sched),
		p.recoverMissingFilesNative(deps, sched),
		p.regroupShatteredAINative(deps, sched),
		p.relinkUnlinkedBooksNative(deps, sched),
		p.repairMergedUserStateNative(deps, sched),
		p.repairTranscribeStatusNative(deps, sched),
		p.repointBookFileRowsNative(deps, sched),
	}
}

// F5-18 registers the kinds and deletes this line with the HEAD shells.
var _ = (*Plugin).t6g6Kinds
