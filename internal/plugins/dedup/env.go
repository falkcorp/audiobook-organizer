// file: internal/plugins/dedup/env.go
// version: 1.1.1
// guid: 64bede01-c260-409b-9b20-f7e73920ae55
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/tasks"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-04: the engine-neutral half of the dedup plugin's dark conversion. Every
// definition has one shared core taking (ctx, params, env) and two shells over
// it: HEAD's v2 Run (this file's headEnv) and the native books kind
// (kinds*.go, nativeEnv). The core reaches the run only through the env it is
// handed, so the same body serves both engines. This file imports the sdk (HEAD's
// Reporter) so that no kinds*.go file has to: the T5 gate is that the native
// shells do not import pkg/plugin/sdk (05-tasks 5T.8.2).

// kindEnv is what every core takes from its shell besides the stores, the
// writer and the engine (each definition's own env struct embeds it and adds
// those, rule R10).
type kindEnv struct {
	// Live is false for a dry run (HEAD) or a Preview run (native).
	Live bool
	// Logger is the run's logger: HEAD's Reporter.Logger(), or rc.Log().
	Logger *slog.Logger
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// Log writes one line to the run's log: HEAD's Reporter.Log (the operation's
	// own log, which the Operations page shows), or the run's logger natively.
	Log func(level slog.Level, msg string)
	// Canceled reports whether the run was canceled (HEAD: Reporter.IsCanceled).
	Canceled func() bool
	// OpID is the id of the run: HEAD's operation id ("" when the reporter cannot
	// say) or the native RunID.
	OpID string
	// Items runs a per-item loop on the shell's pool.
	Items kindItemsFunc
	// SetResult persists a result document: HEAD's ReporterSetResult. Nil in a
	// native shell, whose Task returns the result instead.
	SetResult func(v any) error
	// Reporter is the sdk.Reporter the shared helpers that take one
	// (sdk.NewProgress) are handed: HEAD's own Reporter, or a sink over Progress
	// for a native run.
	Reporter sdk.Reporter
	// Checkpoint saves state for the run's resume (rc.Checkpoint). Nil in a HEAD
	// shell, whose cores that checkpoint do so through their own Reporter.
	Checkpoint func(state any) error
	// Restore loads the state Checkpoint saved into v and reports whether there
	// was any (rc.Restore). Nil in a HEAD shell.
	Restore func(v any) (bool, error)
}

// kindItemsOptions are the per-loop settings a core hands its pool.
type kindItemsOptions struct {
	// Collect runs every item and joins the item errors (HEAD ErrModeCollect);
	// otherwise the first error stops dispatch and is returned.
	Collect bool
	// ProgressBase and ProgressTotal place a slice of a larger set on the
	// progress bar.
	ProgressBase, ProgressTotal int
	// PureLabel is HEAD's per-item progress label when it reads only the item
	// index and the total. Both pools carry it: HEAD's as RunItemsOptions.Label,
	// the native as the item-only tasks.ItemsOptions.Label. A label that reads a
	// running counter cannot be native (the kernel's label is pure): set Label
	// instead, which only HEAD's pool carries (a seam recorded in seams.d). Label
	// is called inside HEAD's worker goroutines, so it reads its counters
	// atomically.
	PureLabel func(i, total int) string
	Label     func(i, total int) string
	// HeadWorkers is HEAD's own worker count for a pool whose Parallelism cannot
	// carry it: tasks.Fixed clamps to [1, MaxFixed], and a count HEAD passed to
	// RunItems unbounded must not be narrowed in HEAD's shell. HEAD's pool uses it
	// when it is above zero; the native pool ignores it and runs the declared
	// Parallelism (with the kernel's ceiling).
	HeadWorkers int
}

// kindItemsFunc runs fn over the indexes 0..n-1 on the shell's pool with the
// parallelism par (the core's own, rule R4).
type kindItemsFunc func(ctx context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error

// headItems is HEAD's pool: registry.RunItems over the indexes with the worker
// count the parallelism names (RunItems clamps below 1 up to 1, so a Sequential
// parallelism is the plain loop it was).
func headItems(rep sdk.Reporter) kindItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error {
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
		workers := par.Workers()
		if o.HeadWorkers > 0 {
			workers = o.HeadWorkers
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, opsregistry.RunItemsOptions{
			Concurrency:    workers,
			ErrMode:        mode,
			ProgressOffset: o.ProgressBase,
			ProgressTotal:  o.ProgressTotal,
			Label:          lbl,
		})
	}
}

// nativeItems is the native pool: tasks.RunItems over the indexes. A failed
// item's own error comes back, as at HEAD (the first one, or every one joined
// in Collect mode), not the ItemsError around it. A counter Label is dropped
// natively (the kernel's label is pure).
func nativeItems(rc *tasks.RC) kindItemsFunc {
	return func(_ context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error {
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
		if ie, ok := errors.AsType[*tasks.ItemsError](err); ok {
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

// headEnv fills the env from HEAD's Reporter.
func headEnv(rep opsregistry.Reporter, live bool) kindEnv {
	return kindEnv{
		Live: live,
		Progress: func(current, total int, msg string) {
			_ = rep.UpdateProgress(current, total, msg)
		},
		Log:       func(level slog.Level, msg string) { _ = rep.Log(level, msg) },
		Canceled:  rep.IsCanceled,
		Logger:    rep.Logger(),
		OpID:      opsregistry.ReporterOpID(rep),
		Items:     headItems(rep),
		SetResult: func(v any) error { return opsregistry.ReporterSetResult(rep, v) },
		Reporter:  rep,
	}
}

// nativeEnv fills the env from the native run.
func nativeEnv(rc *tasks.RC) kindEnv {
	e := kindEnv{
		Live: rc.Mode() == tasks.ModeLive,
		Progress: func(current, total int, msg string) {
			t := tasks.Unknown()
			if total > 0 {
				t = tasks.Known(int64(total))
			}
			rc.Progress().Update(int64(current), t, msg)
		},
		Log: func(level slog.Level, msg string) {
			rc.Log().Log(rc.Context(), level, msg)
		},
		Canceled:   rc.Canceled,
		Logger:     rc.Log(),
		OpID:       string(rc.RunID()),
		Items:      nativeItems(rc),
		Checkpoint: rc.Checkpoint,
		Restore:    rc.Restore,
	}
	e.Reporter = reporterSink{
		progress:   e.Progress,
		touch:      rc.Progress().Tick,
		logger:     e.Logger,
		canceled:   e.Canceled,
		checkpoint: e.Checkpoint,
	}
	return e
}

// reporterSink is the sdk.Reporter a native shell hands the shared helpers
// that take one (sdk.NewProgress). Every method is backed by the run's own
// hooks or, where the run has none, is a plain no-op or a refusal, so a helper
// that reaches a method the sink does not carry fails loudly instead of
// dereferencing a nil embedded Reporter. It implements
// registry.LivenessToucher: the executor stamps liveness from a progress
// report, a Touch and a Tick alike.
type reporterSink struct {
	progress   func(current, total int, msg string)
	touch      func()
	logger     *slog.Logger
	canceled   func() bool
	checkpoint func(state any) error
}

var (
	_ sdk.Reporter                = reporterSink{}
	_ opsregistry.LivenessToucher = reporterSink{}
)

func (s reporterSink) UpdateProgress(current, total int, msg string) error {
	if s.progress != nil {
		s.progress(current, total, msg)
	}
	return nil
}

func (s reporterSink) Log(level slog.Level, msg string, attrs ...slog.Attr) error {
	s.Logger().LogAttrs(context.Background(), level, msg, attrs...)
	return nil
}

func (s reporterSink) Logger() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	// Only a sink built without a logger gets here (a test's); a native run
	// always sets one. Discard, not the global logger: slog_guard_test bars a
	// direct slog.Default from this package.
	return slog.New(slog.DiscardHandler)
}

func (s reporterSink) Checkpoint(state any) error {
	if s.checkpoint == nil {
		return errors.New("checkpoint is not available in this run")
	}
	return s.checkpoint(state)
}

func (s reporterSink) IsCanceled() bool { return s.canceled != nil && s.canceled() }

func (s reporterSink) RunPhase(ctx context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, s)
}

func (s reporterSink) Trigger(context.Context, string, any) error {
	return errors.New("triggering an event is not available in this run")
}

func (s reporterSink) SetCurrentItem(string) {}

// TouchLiveness stamps the run's liveness clock (rc.Progress().Tick) without
// reporting progress.
func (s reporterSink) TouchLiveness() {
	if s.touch != nil {
		s.touch()
	}
}

// newProgress is sdk.NewProgress over the env's reporter.
func newProgress(env kindEnv, n int) *sdk.Progress {
	return sdk.NewProgress(env.Reporter, n)
}

// errNeed refuses a run whose owner left a dependency unset, before the core
// dereferences it.
func errNeed(kind tasks.Kind, name string, set bool) error {
	if !set {
		return fmt.Errorf("%s: KindDeps.%s is not set", kind, name)
	}
	return nil
}

// errNeedEngine is the refusal for a NewEngine factory that returned nothing.
func errNeedEngine(kind tasks.Kind) error {
	return fmt.Errorf("%s: the engine factory returned no engine", kind)
}

// errNeedWriter is the refusal for a NewWriter factory that returned nothing.
func errNeedWriter(kind tasks.Kind) error {
	return fmt.Errorf("%s: the writer factory returned no writer", kind)
}
