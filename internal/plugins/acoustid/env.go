// file: internal/plugins/acoustid/env.go
// version: 1.0.0
// guid: 12973561-d1ec-48c5-a942-90c2e29ae2de
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-05: the engine-neutral half of the acoustid plugin's dark conversion. Every
// definition has one shared core taking (ctx, params, env) and two shells over
// it: HEAD's v2 Run (this file's headEnv) and the native books kind
// (kinds*.go, nativeEnv). The core reaches the run only through the env it is
// handed. This file imports the sdk (HEAD's Reporter) so that no kinds*.go file
// has to: the T5 gate is that the native shells do not import pkg/plugin/sdk
// (05-tasks 5T.8.2). Modelled on internal/plugins/dedup/env.go (F5-04); the
// helpers are duplicated per package on purpose.

// kindEnv is what every core takes from its shell besides the stores, the
// writer and the services (each definition's own env struct embeds it and adds
// those, rule R10).
type kindEnv struct {
	// Live is false for a dry run (HEAD) or a Preview run (native).
	Live bool
	// Logger is the run's logger: HEAD's Reporter.Logger(), or rc.Log().
	Logger *slog.Logger
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// ItemProgress is the status a hand-rolled pool's items and heartbeat report
	// from inside the loop (fingerprint-rescan, fingerprint-duration-repair):
	// HEAD's UpdateProgress, which those loops called directly. Natively it is a
	// status note only, because the executor's own pool reports the completion
	// count and a second writer of the count would race it.
	ItemProgress func(current, total int, msg string)
	// Canceled reports whether the run was canceled (HEAD: Reporter.IsCanceled).
	Canceled func() bool
	// Items runs a per-item loop on the shell's pool (HEAD: registry.RunItems).
	Items kindItemsFunc
	// Pool runs a loop HEAD hand-rolled with a semaphore and a WaitGroup
	// (fingerprint-rescan, fingerprint-duration-repair). HEAD's shell keeps
	// that exact pool; the native shell hands the core the executor's.
	Pool kindItemsFunc
	// Reporter is the sdk.Reporter the shared helpers that take one
	// (sdk.NewProgress) are handed: HEAD's own Reporter, or a sink over Progress
	// for a native run.
	Reporter sdk.Reporter
	// Checkpoint saves state for the run's resume: HEAD's Reporter.Checkpoint,
	// or rc.Checkpoint (HEAD's checkpoint struct types unchanged, so a native
	// Restore decodes the bytes HEAD wrote).
	Checkpoint func(state any) error
	// Restore loads the state Checkpoint saved into v and reports whether there
	// was any (rc.Restore). Nil in a HEAD shell, whose params ARE its checkpoint
	// (ResumeRestart re-dispatches with the last checkpoint as the params).
	Restore func(v any) (bool, error)
}

// kindItemsOptions are the per-loop settings a core hands its pool.
type kindItemsOptions struct {
	// ProgressBase and ProgressTotal place a slice of a larger set on the
	// progress bar.
	ProgressBase, ProgressTotal int
	// PureLabel is HEAD's per-item progress label when it reads only the item
	// index and the total. Both pools carry it. A label that reads a running
	// counter cannot be native (the kernel's label is pure): set Label instead,
	// which only HEAD's pool carries (a seam recorded in seams.d). Label is called
	// inside HEAD's worker goroutines, so it reads its counters atomically.
	PureLabel func(i, total int) string
	Label     func(i, total int) string
	// CheckpointEvery and CheckpointState are registry.RunItemsOptions'
	// CheckpointEvery and CheckpointStateFn: CheckpointState is called with the
	// ABSOLUTE contiguous-completion watermark, serialized, throttled to one
	// call per CheckpointEvery completed items, and only for a successful item.
	// The native pool has no hook of that shape (tasks.ItemsOptions.Watermark
	// writes the kernel's own named checkpoint, not HEAD's struct), so
	// nativeItems ports the same rule.
	CheckpointEvery int
	CheckpointState func(ctx context.Context, mark int) error
}

// kindItemsFunc runs fn over the indexes 0..n-1 on the shell's pool with the
// parallelism par (the core's own, rule R4).
type kindItemsFunc func(ctx context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error

// registryItemsOptions is the one place a kindItemsOptions becomes HEAD's
// RunItemsOptions, so a test of a core's options (backfillRunOptions,
// windowRunOptions) runs the very conversion HEAD's pool does.
func registryItemsOptions(par tasks.Parallelism, o kindItemsOptions) opsregistry.RunItemsOptions {
	lbl := o.Label
	if lbl == nil {
		lbl = o.PureLabel
	}
	return opsregistry.RunItemsOptions{
		Concurrency:       par.Workers(),
		ProgressOffset:    o.ProgressBase,
		ProgressTotal:     o.ProgressTotal,
		Label:             lbl,
		CheckpointEvery:   o.CheckpointEvery,
		CheckpointStateFn: o.CheckpointState,
	}
}

// headItems is HEAD's pool: registry.RunItems over the indexes with the worker
// count the parallelism names (RunItems clamps below 1 up to 1, so a Sequential
// parallelism is the plain loop it was).
func headItems(rep sdk.Reporter) kindItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, registryItemsOptions(par, o))
	}
}

// headPool is HEAD's hand-rolled pool, exactly as fingerprint-rescan and
// fingerprint-duration-repair wrote it: a buffered-channel semaphore of the
// worker count, one goroutine per item (sync.WaitGroup.Go), a ctx check before
// every dispatch, and on cancel a wait for the in-flight items and ctx.Err().
// fn's error is not part of this shape (those bodies return nothing from an
// item); a non-nil one is dropped, as HEAD had nothing to return it to.
func headPool() kindItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, _ kindItemsOptions, fn func(ctx context.Context, i int) error) error {
		sem := make(chan struct{}, par.Workers())
		var wg sync.WaitGroup
		for i := range n {
			select {
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			default:
			}
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				_ = fn(ctx, i)
			})
		}
		wg.Wait()
		return nil
	}
}

// kindWatermark is registry.completionTracker: the contiguous completed prefix
// of the items.
type kindWatermark struct {
	mu        sync.Mutex
	done      []bool
	watermark int
}

func newKindWatermark(n int) *kindWatermark { return &kindWatermark{done: make([]bool, n)} }

func (c *kindWatermark) complete(i int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= 0 && i < len(c.done) {
		c.done[i] = true
	}
	for c.watermark < len(c.done) && c.done[c.watermark] {
		c.watermark++
	}
	return c.watermark
}

// nativeItems is the native pool: tasks.RunItems over the indexes. It differs
// from HEAD's registry.RunItems in four places, each kept equal on purpose:
//
//   - the ctx argument is observed: an item's ctx is also canceled when the
//     caller's ctx is (window's remote-only tier passes a WithCancelCause ctx a
//     heartbeat cancels), and an item dispatched after that returns its error;
//   - a failed item's own error comes back (the first one), not the ItemsError
//     around it, and a run canceled with no item error returns ctx.Err(), as
//     registry.RunItems does;
//   - like registry.RunItems' parallel path, a pool of more than one worker
//     returns ctx.Err() when ctx ended during the run, even if every item ran;
//   - CheckpointState is served by an in-pool watermark (see kindItemsOptions).
//
// A counter Label is dropped natively (the kernel's label is pure).
func nativeItems(rc *tasks.RC) kindItemsFunc {
	return func(ctx context.Context, par tasks.Parallelism, n int, o kindItemsOptions, fn func(ctx context.Context, i int) error) error {
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
		var (
			tracker  *kindWatermark
			ckptMu   sync.Mutex
			lastCkpt int
		)
		if o.CheckpointState != nil {
			tracker = newKindWatermark(n)
		}
		maybeCheckpoint := func(ctx context.Context, i int) {
			if tracker == nil {
				return
			}
			mark := tracker.complete(i)
			every := max(o.CheckpointEvery, 1)
			ckptMu.Lock()
			defer ckptMu.Unlock()
			if mark <= lastCkpt || mark-lastCkpt < every && mark < n {
				return
			}
			lastCkpt = mark
			_ = o.CheckpointState(ctx, mark)
		}
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			ictx, cancel := context.WithCancelCause(ic.Context())
			defer cancel(nil)
			stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
			defer stop()
			err := fn(ictx, i)
			if err == nil {
				maybeCheckpoint(ictx, i)
			}
			return err
		}, tasks.ItemsOptions[int]{OnError: tasks.FailFast, ProgressBase: o.ProgressBase, ProgressTotal: o.ProgressTotal, Label: label})
		var ie *tasks.ItemsError
		if errors.As(err, &ie) {
			if ie.First != nil {
				return ie.First
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if cerr := rc.Context().Err(); cerr != nil {
				return cerr
			}
			return err
		}
		if err == nil && par.Workers() > 1 {
			return ctx.Err()
		}
		return err
	}
}

// headEnv fills the env from HEAD's Reporter.
//
// A nil reporter gives an env with no hooks set: a HEAD run that refuses before
// it reports anything (the server-decode guard is tested with a nil reporter)
// never reaches one.
func headEnv(rep opsregistry.Reporter, live bool) kindEnv {
	if rep == nil {
		return kindEnv{Live: live}
	}
	progress := func(current, total int, msg string) {
		_ = rep.UpdateProgress(current, total, msg)
	}
	return kindEnv{
		Live:         live,
		Progress:     progress,
		ItemProgress: progress,
		Canceled:     rep.IsCanceled,
		Logger:       rep.Logger(),
		Items:        headItems(rep),
		Pool:         headPool(),
		Reporter:     rep,
		Checkpoint:   rep.Checkpoint,
	}
}

// nativeEnv fills the env from the native run. Pool is the same executor pool
// as Items: the native shell has one.
func nativeEnv(rc *tasks.RC) kindEnv {
	e := kindEnv{
		Live: rc.Mode() == tasks.ModeLive,
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
		ItemProgress: func(_, _ int, msg string) {
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		Canceled:   rc.Canceled,
		Logger:     rc.Log(),
		Items:      nativeItems(rc),
		Pool:       nativeItems(rc),
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

// newProgress is sdk.NewProgress over the env's reporter: the same frames a
// HEAD run reports, replicated natively through the sink's Progress hook.
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

// FingerprintDeps is the host access of the fpcalc-running kinds: the process
// facts HEAD's bodies read from globals (the server-decode switch, the fpcalc
// probe, the worker-count knob, the reference-tool setting) and the one place
// the rescan writes outside the store (its report file). A nil field is HEAD's
// own behaviour; tests set fakes. It is a field of KindDeps so the cores read
// none of it from a global (rule R10).
type FingerprintDeps struct {
	// ServerDecodeCheck is serverdecode.Check: refuses a decode the server must
	// not run (acoustid.fingerprint-rescan).
	ServerDecodeCheck func(op string) error
	// FingerprintAvailable is fingerprint.Available: an fpcalc or ffmpeg backend
	// exists.
	FingerprintAvailable func() bool
	// Workers is the fpcalc worker-pool size (fpRescanWorkers for the rescan and
	// the duration repair, backfillWorkers for the backfill: the same knob).
	Workers func() int
	// ReferenceToolsSet reports whether a reference tool pair is configured, in
	// which case the server must not decode audio.
	ReferenceToolsSet func() bool
	// WriteIneligibleReport writes the rescan's JSONL report and returns its
	// path (writeIneligibleReport).
	WriteIneligibleReport func(opID string, entries []ineligibleEntry) (string, error)
}

// hostHooks is FingerprintDeps with every nil field filled in with HEAD's own
// function, so a core calls its hooks without a nil check.
type hostHooks struct {
	ServerDecodeCheck     func(op string) error
	FingerprintAvailable  func() bool
	Workers               func() int
	ReferenceToolsSet     func() bool
	WriteIneligibleReport func(opID string, entries []ineligibleEntry) (string, error)
}

// headHostHooks is HEAD's own: the package functions the bodies called.
func headHostHooks() hostHooks { return FingerprintDeps{}.hooks() }

func (d FingerprintDeps) hooks() hostHooks {
	h := hostHooks(d)
	if h.ServerDecodeCheck == nil {
		h.ServerDecodeCheck = serverdecode.Check
	}
	if h.FingerprintAvailable == nil {
		h.FingerprintAvailable = fingerprint.Available
	}
	if h.Workers == nil {
		h.Workers = fpRescanWorkers
	}
	if h.ReferenceToolsSet == nil {
		h.ReferenceToolsSet = func() bool {
			return strings.TrimSpace(config.AppConfig.FingerprintWindowReferenceTools) != ""
		}
	}
	if h.WriteIneligibleReport == nil {
		h.WriteIneligibleReport = writeIneligibleReport
	}
	return h
}

// kindStore is the reads every core takes (rule R10): the nine read methods of
// pluginStore, measured by splitting that interface. Nothing in it writes.
type kindStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	CountAllBooks() (int, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetFilesWithZeroDurationFingerprint(limit, offset int) ([]database.BookFile, int64, error)
	GetFingerprintWindows(ref database.FingerprintWindowRef) ([]database.FingerprintWindow, error)
	GetFingerprintWindowFailure(ref database.FingerprintWindowRef) (*database.FingerprintWindowFailure, error)
}

// kindWriter is the writes every core makes (rule R10): the five write methods
// of pluginStore, plus the two writes the reset op makes that pluginStore does
// not carry. The native shell builds it over the run's gated tasks.Writer
// (KindDeps.NewWriter); HEAD's shell builds headKindWriter over its own store.
type kindWriter interface {
	UpdateBookFile(id string, file *database.BookFile) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	ClearBookSignature(id string) error
	ReplaceFingerprintWindows(ref database.FingerprintWindowRef, ws []database.FingerprintWindow) error
	RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error
	// DeleteCandidates drops acoustid-layer dedup candidates (reset-all).
	DeleteCandidates(ids []int64) (int, error)
	// FingerprintResetter is the batched whole-library wipe (reset-all), nil on
	// a backend without it. It is a write found by capability, so it comes from
	// the writer and never from the read store.
	FingerprintResetter() fingerprintResetter
}

// kindView joins a core's read store and its writer into the pluginStore the
// shared helpers (doFingerprintFile, synthesizeBookSignatureForBook, the window
// planner) take. The nine reads come from the store and the five writes from the
// writer, so a write through a view reaches env.Writer and nothing else.
type kindView struct {
	kindStore
	kindWriter
}

var _ pluginStore = kindView{}

// candidateReads is the candidate list the scan's summary and the reset pass
// read: the one read of the embedding store they take.
type candidateReads interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
}

// headKindWriter is HEAD's kindWriter: each method calls exactly what HEAD's
// body called, on HEAD's own store (rule R10 item 5). cands is the candidate
// store the reset pass deletes from (nil when the plugin has none).
type headKindWriter struct {
	store pluginStore
	cands resetCandidateStore
}

var _ kindWriter = headKindWriter{}

func (w headKindWriter) UpdateBookFile(id string, f *database.BookFile) error {
	return w.store.UpdateBookFile(id, f)
}

func (w headKindWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return w.store.ModifyBook(id, fn)
}

func (w headKindWriter) ClearBookSignature(id string) error { return w.store.ClearBookSignature(id) }

func (w headKindWriter) ReplaceFingerprintWindows(ref database.FingerprintWindowRef, ws []database.FingerprintWindow) error {
	return w.store.ReplaceFingerprintWindows(ref, ws)
}

func (w headKindWriter) RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error {
	return w.store.RecordFingerprintWindowFailure(f)
}

func (w headKindWriter) DeleteCandidates(ids []int64) (int, error) {
	return w.cands.DeleteCandidates(ids)
}

// FingerprintResetter is the batched wipe resolved through the decorator walk
// HEAD used (resolveFingerprintResetter), nil on a backend without it.
func (w headKindWriter) FingerprintResetter() fingerprintResetter {
	return resolveFingerprintResetter(w.store)
}
