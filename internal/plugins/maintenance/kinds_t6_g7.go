// file: internal/plugins/maintenance/kinds_t6_g7.go
// version: 1.0.3
// guid: 8e3a5d17-2c94-4b60-a1f8-6d07c9e4b352
// last-edited: 2026-11-19

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Native books Task kinds for T6 group 7 of the maintenance plugin (F5-12,
// 05-tasks 5T.8.2 group T2): 15 definitions, one per file. They are DARK:
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
	// t6g7KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	t6g7KindPrefix = "books."

	// t6g7SignalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false).
	t6g7SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t6g7SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (internal/books/atoms.go). internal/books imports this package through
// its import graph, so the table cannot be read from here; it is the same copy
// internal/maintenance/jobs/kinds.go keeps, and drifts the same way.
var t6g7SettingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// t6g7StartRequirement is rule R6 for all 15 definitions (every one is gated on
// settings.manage at HEAD, directly or through an empty Permissions list that
// the registry defaults to it).
func t6g7StartRequirement() rbac.Requirement {
	return rbac.AllOf(t6g7SettingsManageScopes...)
}

// t6g7Env is what every core takes from its shell besides the stores.
type t6g7Env struct {
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
	Items t6g7ItemsFunc
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
	StandDown func(ctx context.Context, reason string) (t6g7Hold, error)
	// Checkpoint saves state for the run's resume (rc.Checkpoint). Nil in a HEAD
	// shell, whose cores that checkpoint do so through their own Reporter; a core
	// that yields in place (ErrContinueInPlace) only does so when this is set.
	Checkpoint func(state any) error
	// Restore loads the state Checkpoint saved into v and reports whether there
	// was any (rc.Restore). Nil in a HEAD shell.
	Restore func(v any) (bool, error)
}

// t6g7Hold is a held scan stand-down.
type t6g7Hold struct {
	// Lost renews the lease and reports whether it lapsed (the caller must stop
	// writing). Native: always false, the kernel fences the writer instead.
	Lost func() bool
	// Release gives the hold back; safe to call when nothing was held.
	Release func()
}

// t6g7ItemsOptions are the per-loop settings a core hands its pool.
type t6g7ItemsOptions struct {
	// Collect runs every item and joins the item errors (HEAD ErrModeCollect);
	// otherwise the first error stops dispatch and is returned.
	Collect bool
	// ProgressBase and ProgressTotal place a slice of a larger set on the
	// progress bar.
	ProgressBase, ProgressTotal int
	// Label is HEAD's per-item progress label for a loop whose label reads
	// running counters. Called inside the worker goroutines, so it reads them
	// atomically. The native pool cannot carry it: tasks.ItemsOptions.Label is
	// pure (the item only), so a counter label is dropped natively (a recorded
	// seam in seams.d/F5-12.md: recover's three loops and the heal).
	Label func(i, total int) string
	// PureLabel is HEAD's label for a loop whose label reads only the item index
	// and the total ("Phase 1/3: checking book i/total"). Both pools carry it:
	// HEAD's as Label, the native as the item-only tasks.ItemsOptions.Label (the
	// item is the index). Set at most one of Label and PureLabel.
	PureLabel func(i, total int) string
}

// t6g7ItemsFunc runs fn over the indexes 0..n-1 on the shell's pool with the
// parallelism par (the core's own, R4).
type t6g7ItemsFunc func(ctx context.Context, par tasks.Parallelism, n int, o t6g7ItemsOptions, fn func(ctx context.Context, i int) error) error

// t6g7HeadItems is HEAD's pool: registry.RunItems over the indexes with the
// worker count the parallelism names (RunItems clamps below 1 up to 1, so a
// Sequential parallelism is the plain loop it was).
func t6g7HeadItems(rep sdk.Reporter) t6g7ItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, o t6g7ItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		mode := opsregistry.ErrModeFail
		if o.Collect {
			mode = opsregistry.ErrModeCollect
		}
		lbl := o.Label
		if lbl == nil {
			lbl = o.PureLabel
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, opsregistry.RunItemsOptions{
			Concurrency:    par.Workers(),
			ErrMode:        mode,
			ProgressOffset: o.ProgressBase,
			ProgressTotal:  o.ProgressTotal,
			Label:          lbl,
		})
	}
}

// t6g7NativeItems is the native pool: tasks.RunItems over the indexes. A failed
// item's own error comes back, as at HEAD (the first one, or every one joined
// in Collect mode), not the ItemsError around it.
func t6g7NativeItems(rc *tasks.RC) t6g7ItemsFunc {
	return func(_ context.Context, par tasks.Parallelism, n int, o t6g7ItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		mode := tasks.FailFast
		if o.Collect {
			mode = tasks.Collect
		}
		// The item is the index, so the pure label closes over the total HEAD's
		// label saw: the absolute ProgressTotal when set (run_items.go), else n.
		var label func(int) string
		if o.PureLabel != nil {
			labelTotal := n
			if o.ProgressTotal > 0 {
				labelTotal = o.ProgressTotal
			}
			label = func(i int) string { return o.PureLabel(i, labelTotal) }
		}
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error {
			return fn(ic.Context(), i)
		}, tasks.ItemsOptions[int]{OnError: mode, ProgressBase: o.ProgressBase, ProgressTotal: o.ProgressTotal, Label: label})
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

// t6g7HeadEnv fills the env from HEAD's Reporter.
func t6g7HeadEnv(rep opsregistry.Reporter, live bool) t6g7Env {
	return t6g7Env{
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
		Items:     t6g7HeadItems(rep),
		SetResult: func(v any) error { return opsregistry.ReporterSetResult(rep, v) },
		Reporter:  rep,
	}
}

// t6g7NativeEnv fills the env from the native run.
func t6g7NativeEnv(rc *tasks.RC) t6g7Env {
	e := t6g7Env{
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
		Canceled:   rc.Canceled,
		Logger:     rc.Log(),
		OpID:       string(rc.RunID()),
		Items:      t6g7NativeItems(rc),
		Checkpoint: rc.Checkpoint,
		Restore:    rc.Restore,
	}
	e.Reporter = t6g7ReporterSink{
		progress:   e.Progress,
		touch:      rc.Progress().Tick,
		log:        e.Log,
		logger:     e.Logger,
		canceled:   e.Canceled,
		checkpoint: e.Checkpoint,
	}
	return e
}

// t6g7NativeStandDown is the native stand-down: rc.StandDown over key. An empty
// key refuses, so a live apply never runs without the interlock. The kernel
// fences the run's writer if the lease is lost, so Lost is always false.
func t6g7NativeStandDown(rc *tasks.RC, key tasks.Key) func(ctx context.Context, reason string) (t6g7Hold, error) {
	return func(_ context.Context, reason string) (t6g7Hold, error) {
		if key == "" {
			return t6g7Hold{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t6g7Hold{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t6g7Hold{Lost: func() bool { return false }, Release: h.Release}, nil
	}
}

// t6g7Held is the scan stand-down hold of a core whose work runs under the
// hold's own context and progress sink: HEAD's registry.HoldScanStandDown hands
// back a context that is canceled when the lease is lost and a reporter whose
// progress stamps renew it. (t6g7Hold, above, is the lighter shape for a core
// that checkpoints the lease itself.)
type t6g7Held struct {
	// Ctx is the context the run's work uses.
	Ctx context.Context
	// Progress is the run's progress and log sink under the hold.
	Progress operations.ProgressReporter
	// Finish releases the hold and folds a lost lease into the run's error.
	Finish func(runErr error) error
}

// t6g7HoldFunc takes the scan stand-down for the whole run.
type t6g7HoldFunc func(ctx context.Context, reason string) (t6g7Held, error)

// t6g7HeadHold is HEAD's hold (registry.HoldScanStandDown over the plugin's
// scan controller).
func (p *Plugin) t6g7HeadHold(reporter sdk.Reporter) t6g7HoldFunc {
	return func(ctx context.Context, reason string) (t6g7Held, error) {
		h, err := opsregistry.HoldScanStandDown(ctx, p.deps, reporter, reason)
		if err != nil {
			return t6g7Held{}, err
		}
		return t6g7Held{Ctx: h.Context(), Progress: newOpsAdapter(h.Reporter()), Finish: h.Finish}, nil
	}
}

// t6g7NativeHold is the native hold: rc.StandDown over key. An empty key refuses,
// so a live apply never runs without the interlock. The kernel fences the run's
// writer if the lease is lost, so Finish only releases.
func t6g7NativeHold(rc *tasks.RC, key tasks.Key, env t6g7Env) t6g7HoldFunc {
	return func(ctx context.Context, reason string) (t6g7Held, error) {
		if key == "" {
			return t6g7Held{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t6g7Held{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t6g7Held{
			Ctx:      ctx,
			Progress: t6g7OpsProgress{env: env},
			Finish: func(runErr error) error {
				h.Release()
				return runErr
			},
		}, nil
	}
}

// t6g7OpsProgress is operations.ProgressReporter over the env: the sink a
// delegated server-side pass takes.
type t6g7OpsProgress struct{ env t6g7Env }

func (r t6g7OpsProgress) UpdateProgress(current, total int, message string) error {
	r.env.Progress(current, total, message)
	return nil
}

func (r t6g7OpsProgress) Log(level, message string, details *string) error {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	if details != nil {
		message += ": " + *details
	}
	r.env.Log(lvl, message)
	return nil
}

func (r t6g7OpsProgress) IsCanceled() bool { return r.env.Canceled != nil && r.env.Canceled() }

// t6g7HeadStandDown is HEAD's stand-down (acquireScanStandDownForApply over the
// plugin's scan controller).
func t6g7HeadStandDown(scan ScanController, rep sdk.Reporter) func(ctx context.Context, reason string) (t6g7Hold, error) {
	return func(ctx context.Context, reason string) (t6g7Hold, error) {
		holderID, held, release, err := acquireScanStandDownForApply(ctx, scan, rep, reason)
		if err != nil {
			return t6g7Hold{}, err
		}
		return t6g7Hold{
			Lost:    func() bool { return scanStandDownLostForApply(scan, holderID, held) },
			Release: release,
		}, nil
	}
}

// t6g7KindFor is the kind name rule R3 gives a HEAD def ID.
func t6g7KindFor(headID string) tasks.Kind { return tasks.Kind(t6g7KindPrefix + headID) }

// t6g7Common is the Common block every definition shares; the caller fills the
// per-definition fields (Title, Help, Effects, Resume, Priority, Timeout...).
func t6g7Common(kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Perms: tasks.Permissions{Start: t6g7StartRequirement()},
		Ready: []tasks.ReadySignal{t6g7SignalMemDB},
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// t6g7LiveOnly is the refusal a live-only writer gives a Preview run: HEAD has
// no preview for it, and a native Preview must not write.
func t6g7LiveOnly(rc *tasks.RC, kind tasks.Kind) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// t6g7ReporterSink is the sdk.Reporter a native shell hands the shared helpers
// that take one (sdk.NewProgress, the batch writer's liveness stamps and
// pre-write heartbeat). Every method is backed by the run's own hooks or, where
// the run has none, is a plain no-op or a refusal, so a helper that reaches a
// method the sink does not carry fails loudly instead of dereferencing a nil
// embedded Reporter. It implements registry.LivenessToucher: the executor stamps
// liveness from a progress report, a Touch and a Tick alike.
type t6g7ReporterSink struct {
	progress   func(current, total int, msg string)
	touch      func()
	log        func(level slog.Level, msg string, attrs ...slog.Attr)
	logger     *slog.Logger
	canceled   func() bool
	checkpoint func(state any) error
}

var (
	_ sdk.Reporter                = t6g7ReporterSink{}
	_ opsregistry.LivenessToucher = t6g7ReporterSink{}
)

func (s t6g7ReporterSink) UpdateProgress(current, total int, msg string) error {
	if s.progress != nil {
		s.progress(current, total, msg)
	}
	return nil
}

func (s t6g7ReporterSink) Log(level slog.Level, msg string, attrs ...slog.Attr) error {
	if s.log != nil {
		s.log(level, msg, attrs...)
	}
	return nil
}

func (s t6g7ReporterSink) Logger() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	// Only a sink built without a logger gets here (a test's); a native run
	// always sets one. Discard, not the global logger: slog_guard_test bars a
	// direct slog.Default from this package.
	return slog.New(slog.DiscardHandler)
}

func (s t6g7ReporterSink) Checkpoint(state any) error {
	if s.checkpoint == nil {
		return errors.New("checkpoint is not available in this run")
	}
	return s.checkpoint(state)
}

func (s t6g7ReporterSink) IsCanceled() bool { return s.canceled != nil && s.canceled() }

func (s t6g7ReporterSink) RunPhase(ctx context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, s)
}

func (s t6g7ReporterSink) Trigger(context.Context, string, any) error {
	return errors.New("triggering an event is not available in this run")
}

func (s t6g7ReporterSink) SetCurrentItem(string) {}

// TouchLiveness stamps the run's liveness clock (rc.Progress().Tick) without
// reporting progress.
func (s t6g7ReporterSink) TouchLiveness() {
	if s.touch != nil {
		s.touch()
	}
}

// t6g7ModeKeys are the param keys that would carry the run's mode: dry_run,
// dryRun and apply. The mode comes from the start request (05-tasks 5T.3.7),
// never from params, so a native writing shell refuses them.
var t6g7ModeKeys = []string{"dry_run", "dryRun", "apply"}

// t6g7DecodeParams is the native shells' params step: a mode key is refused
// (case-insensitively, as encoding/json matches field names), then the rest is
// decoded into v. Empty and null params decode to the zero value; params that
// are not a JSON object are refused.
func t6g7DecodeParams(kind tasks.Kind, raw json.RawMessage, v any) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range t6g7ModeKeys {
			if strings.EqualFold(key, banned) {
				return fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	if err := json.Unmarshal(t, v); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return nil
}

// t6g7DecodeNative is t6g7DecodeParams into a fresh P, for the native shells that
// take their params raw (so a mode key can be refused before decoding).
func t6g7DecodeNative[P any](kind tasks.Kind, raw json.RawMessage) (P, error) {
	var v P
	err := t6g7DecodeParams(kind, raw, &v)
	return v, err
}

// t6g7RefuseModeKeys is t6g7DecodeParams for a shell whose body takes no params
// of its own; any other key is ignored, as HEAD ignored the params of these defs.
func t6g7RefuseModeKeys(kind tasks.Kind, raw json.RawMessage) error {
	return t6g7DecodeParams(kind, raw, &struct{}{})
}

// t6g7NeedDep refuses a native run whose owner left a dependency unset, before
// the core dereferences it.
func t6g7NeedDep(kind tasks.Kind, name string, set bool) error {
	if !set {
		return fmt.Errorf("%s: t6g7Deps.%s is not set", kind, name)
	}
	return nil
}

// t6g7NewWriter builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned nothing).
func t6g7NewWriter(kind tasks.Kind, deps t6g7Deps, rc *tasks.RC) (t6g7Writer, error) {
	if err := t6g7NeedDep(kind, "NewWriter", deps.NewWriter != nil); err != nil {
		return nil, err
	}
	w := deps.NewWriter(rc.Writer())
	if w == nil {
		return nil, fmt.Errorf("%s: t6g7Deps.NewWriter returned no writer", kind)
	}
	return w, nil
}

// t6g7Res is the Effects refs of a row of t6WriterResources, in module "books".
func t6g7Res(rs ...ops.Resource) []tasks.Resource {
	out := make([]tasks.Resource, len(rs))
	for i, r := range rs {
		out[i] = tasks.Res("books", string(r))
	}
	return out
}

// t6g7Store is every read the 15 definitions make, composed from each
// definition's own narrow interface. A method two definitions share has one
// signature, so the embeds overlap cleanly. Nothing here writes.
type t6g7Store interface {
	reviewStatusIndexRepairStore
	unknownAuthorAuditStore
	versionGroupPrimaryReportStore
	reportsPruneStore
	titleBackfillStore
	titleRepairStore
}

// t6g7Writer is every write the 15 definitions make, composed from each
// definition's own narrow interface. The native shells build it with
// t6g7Deps.NewWriter over the run's kernel-gated tasks.Writer, so every write
// goes through the gate (and is recorded, not executed, in a Preview run).
type t6g7Writer interface {
	reviewStatusIndexRepairWriter
	titleBackfillWriter
	titleRepairWriter
	bulkWriteBackWriter
}

// t6g7Deps is what the native kinds are built from. Every field is set once, by
// the F5-18 registration; nothing here is read from a plugin receiver.
type t6g7Deps struct {
	// Store is the read side of the library store.
	Store t6g7Store
	// NewWriter builds the write side over the run's gated writer. Per run: the
	// writer it wraps belongs to that run's executor, which is why this is a
	// constructor and not a field of the store (conversion rule 3).
	NewWriter func(tasks.Writer) t6g7Writer
	// RootDir is the library root.
	RootDir func() string
	// AgreedChapterTitle is the CONS-17b all-chapters-agree read of a directory's
	// tags: metadata.AgreedChapterTitle over the configured supported extensions.
	// F5-18 sets it.
	AgreedChapterTitle func(dir string) (agreed string, multi bool)
	// StandDownKey is the concurrency key of the library scan kinds, which a live
	// apply parks while it writes (rc.StandDown). Empty refuses a live apply that
	// needs the interlock. F5-18 sets it.
	StandDownKey tasks.Key
}

// t6g7Kinds returns the 15 native definitions of group 7 in file order. sched
// hands a kind its schedule (nil, or a nil result, means none): the schedule of
// a kind is the registration's to give, not this file's (R5).
func (p *Plugin) t6g7Kinds(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return t6g7KindList(p, deps, sched)
}

// t6g7KindList is t6g7Kinds as a package-level function: tasklint's
// unregistered-kind rule counts package-level list functions only, and not a
// method.
func t6g7KindList(p *Plugin, deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.reviewStatusIndexRepairNative(deps, sched),
		p.unknownAuthorAuditNative(deps, sched),
		p.versionGroupPrimaryReportNative(deps, sched),
		p.reportsPruneNative(deps, sched),
		p.titleBackfillNative(deps, sched),
		p.titleRepairNative(deps, sched),
		p.bulkWriteBackNative(deps, sched),
	}
}

// t6g7KindsRoot names t6g7KindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root (a list function nothing
// references registers nothing, and the analyzer does not load test files).
// F5-18 deletes it with the HEAD shells.
var _ = t6g7KindList

// The brief's R9 line, kept so the method stays referenced.
var _ = (*Plugin).t6g7Kinds
