// file: internal/plugins/metafetch/env.go
// version: 1.0.0
// guid: c4bd21d4-6171-4d06-9fe8-6701e962a591
// last-edited: 2026-11-19

package metafetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/tasks"
	"golang.org/x/sync/errgroup"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-05: the engine-neutral half of the metafetch plugin's dark conversion
// (same shape as dedup's env.go, F5-04). Every definition has one shared core
// taking (ctx, env) and two shells over it: HEAD's v2 Run (headEnv) and the
// native books kind (kinds.go, nativeEnv). The core reaches the run only
// through the env it is handed. This file imports the sdk (HEAD's Reporter) so
// that kinds.go does not have to: the T5 gate is that the native shells do not
// import pkg/plugin/sdk (05-tasks 5T.8.2).

// kindEnv is what every core takes from its shell besides the stores and the
// writer (each definition's own env struct embeds it and adds those, rule R10).
type kindEnv struct {
	// Live is false for a dry run (HEAD) or a Preview run (native).
	Live bool
	// Logger is the run's logger: HEAD's Reporter.Logger(), or rc.Log().
	Logger *slog.Logger
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// Canceled reports whether the run was canceled (HEAD: Reporter.IsCanceled).
	Canceled func() bool
	// Items runs a per-item loop on the shell's pool.
	Items kindItemsFunc
	// Reporter is the sdk.Reporter the shared helpers that take one
	// (sdk.NewProgress) are handed: HEAD's own Reporter, or a sink over Progress
	// for a native run.
	Reporter sdk.Reporter
	// Checkpoint saves state for the run's resume: HEAD's Reporter.Checkpoint, or
	// rc.Checkpoint.
	Checkpoint func(state any) error
	// Restore loads the state Checkpoint saved into v and reports whether there
	// was any (rc.Restore). Nil in a HEAD shell, where the registry merges the
	// checkpoint into the params before Run.
	Restore func(v any) (bool, error)
}

// kindItemsOptions are the per-loop settings a core hands its pool.
type kindItemsOptions struct {
	// ProgressBase and ProgressTotal place a slice of a larger set on the
	// progress bar.
	ProgressBase, ProgressTotal int
	// PureLabel is HEAD's per-item progress label when it reads only the item
	// index and the total. Both pools carry it: HEAD's as RunItemsOptions.Label,
	// the native as the item-only tasks.ItemsOptions.Label. The label is called
	// inside HEAD's worker goroutines.
	PureLabel func(i, total int) string
	// Label is a label that reads a running counter. It cannot be native (the
	// kernel's label is pure), so only HEAD's pool carries it (a seam recorded in
	// seams.d). It reads its counters atomically: HEAD calls it inside the workers.
	Label func(i, total int) string
}

// kindItemsFunc runs fn over the indexes 0..n-1 on the shell's pool with the
// parallelism par (the core's own, rule R4). The first error stops dispatch and
// is returned.
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
		lbl := o.Label
		if lbl == nil {
			lbl = o.PureLabel
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, opsregistry.RunItemsOptions{
			Concurrency:    par.Workers(),
			ProgressOffset: o.ProgressBase,
			ProgressTotal:  o.ProgressTotal,
			Label:          lbl,
		})
	}
}

// headErrgroupItems is the plain errgroup pool pcache-sweep's pointer pages ran
// on at HEAD: a bounded, silent set of goroutines that reports no progress
// (registry.RunItems would add frames HEAD never emitted). The group's context
// is not handed to fn, as at HEAD.
func headErrgroupItems() kindItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, _ kindItemsOptions, fn func(ctx context.Context, i int) error) error {
		g, _ := errgroup.WithContext(ctx)
		g.SetLimit(par.Workers())
		for i := range n {
			g.Go(func() error { return fn(ctx, i) })
		}
		return g.Wait()
	}
}

// nativeItems is the native pool: tasks.RunItems over the indexes. A failed
// item's own error comes back, as at HEAD, not the ItemsError around it. A
// counter Label is dropped natively (the kernel's label is pure).
func nativeItems(rc *tasks.RC) kindItemsFunc {
	return func(_ context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
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
		}, tasks.ItemsOptions[int]{OnError: tasks.FailFast, ProgressBase: o.ProgressBase, ProgressTotal: o.ProgressTotal, Label: label})
		var ie *tasks.ItemsError
		if errors.As(err, &ie) && ie.First != nil {
			return ie.First
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
		Canceled:   rep.IsCanceled,
		Logger:     rep.Logger(),
		Items:      headItems(rep),
		Reporter:   rep,
		Checkpoint: rep.Checkpoint,
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
		Canceled:   rc.Canceled,
		Logger:     rc.Log(),
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
// registry.LivenessToucher.
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

// errNeedWriter is the refusal for a NewWriter factory that returned nothing.
func errNeedWriter(kind tasks.Kind) error {
	return fmt.Errorf("%s: the writer factory returned no writer", kind)
}
