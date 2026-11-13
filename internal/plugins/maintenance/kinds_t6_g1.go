// file: internal/plugins/maintenance/kinds_t6_g1.go
// version: 1.0.0
// guid: 4494039c-1a79-4b3b-9872-ecea118f76ad
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// Native books Task kinds for T6 group 1 of the maintenance plugin (F5-06,
// 05-tasks 5T.8.2 group T2): 14 definitions that live in 13 files. They are
// DARK: nothing registers them before the tasks flip (F5-18), which also
// removes the HEAD v2 shells.
//
// Every definition has two shells over one core. HEAD's Run (the v2 shell) keeps
// its param decoding and its Reporter and calls <name>Core; the native shell is
// a tasks.Task and calls the same core. A core reaches the store only through
// the env it is handed: reads through env.Store, writes through env.Writer, and
// everything else (logging, progress, checkpoints, per-item pools) through the
// hooks on env. The HEAD shell fills the env from the plugin's deps; the native
// shell fills it from the run's context, with the writer the kernel gates
// (deps.NewWriter(rc.Writer())).

const (
	// t6g1SignalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false). It is the same component F5-01 declares in package
	// jobs (unexported there).
	t6g1SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t6g1SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (internal/books/atoms.go). internal/books imports this package through
// its import graph, so the table cannot be read from here; it is the same copy
// internal/maintenance/jobs/kinds.go keeps, and drifts the same way.
var t6g1SettingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// t6g1StartRequirement is rule R6 for all 14 definitions (every one is gated on
// settings.manage at HEAD, directly or through an empty Permissions list that
// the registry defaults to it).
func t6g1StartRequirement() rbac.Requirement {
	return rbac.AllOf(t6g1SettingsManageScopes...)
}

// t6g1Env is what every core takes from its shell besides the stores.
type t6g1Env struct {
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
}

// t6g1Held is a scan stand-down held for a write phase, with everything the
// hold rebinds: HEAD's registry.HoldScanStandDown hands back a context that is
// canceled when the lease is lost and a reporter whose progress stamps renew it.
type t6g1Held struct {
	// Ctx is the context the write phase runs under.
	Ctx context.Context
	// Env is the env for the write phase (progress renews the lease at HEAD).
	Env t6g1Env
	// Items is the pool bound to the held env.
	Items t6g1ItemsFunc
	// Checkpoint is the per-step lease check: nil to go on, else the cause.
	Checkpoint func() error
	// Finish releases the hold and folds a lost lease into the run's error.
	Finish func(runErr error) error
}

// t6g1HeadHold is HEAD's hold (registry.HoldScanStandDown over the plugin's
// scan controller).
func (p *Plugin) t6g1HeadHold(rep sdk.Reporter) func(ctx context.Context, reason string) (t6g1Held, error) {
	return func(ctx context.Context, reason string) (t6g1Held, error) {
		h, err := opsregistry.HoldScanStandDown(ctx, p.deps, rep, reason)
		if err != nil {
			return t6g1Held{}, err
		}
		hrep := h.Reporter()
		return t6g1Held{
			Ctx:        h.Context(),
			Env:        t6g1HeadEnv(hrep, true),
			Items:      t6g1HeadItems(hrep),
			Checkpoint: h.Checkpoint,
			Finish:     h.Finish,
		}, nil
	}
}

// t6g1NativeHold is the native hold: rc.StandDown. A lost lease fences the
// run's writer, which fails the next write with tasks.ErrFenced, and cancels
// the run's context; the check below reports the cause.
func t6g1NativeHold(rc *tasks.RC, env t6g1Env, items t6g1ItemsFunc, key tasks.Key) func(ctx context.Context, reason string) (t6g1Held, error) {
	return func(ctx context.Context, reason string) (t6g1Held, error) {
		if key == "" {
			return t6g1Held{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t6g1Held{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t6g1Held{
			Ctx:   ctx,
			Env:   env,
			Items: items,
			Checkpoint: func() error {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				return nil
			},
			Finish: func(runErr error) error {
				h.Release()
				return runErr
			},
		}, nil
	}
}

// t6g1ItemsOptions are the per-loop settings a core hands its pool.
type t6g1ItemsOptions struct {
	// Concurrency is the worker count: n > 0 is a fixed pool, anything else
	// every CPU. HEAD's registry.RunItems clamps below 1 up to 1, so a caller
	// that wants a pool sets it.
	Concurrency int
	// ProgressBase and ProgressTotal place a slice of a larger set on the
	// progress bar.
	ProgressBase, ProgressTotal int
	// ResumeFrom skips the first N items, a resumed run's finished prefix.
	ResumeFrom int
	// CheckpointEvery throttles CheckpointState to once per N completed items
	// (0 is every item).
	CheckpointEvery int
	// CheckpointState receives the absolute contiguous-completion watermark
	// (items from the start that are all finished, ResumeFrom included). Calls
	// are serialized. A failed item never advances it.
	CheckpointState func(ctx context.Context, watermark int) error
	// Label is HEAD's per-item progress label. Called inside the worker
	// goroutines, so it reads counters atomically. The native pool labels
	// nothing from it.
	Label func(i, total int) string
}

// t6g1ItemsFunc runs fn over the indexes 0..n-1 on the shell's pool. The first
// item error stops dispatch and is returned.
type t6g1ItemsFunc func(ctx context.Context, n int, o t6g1ItemsOptions, fn func(ctx context.Context, i int) error) error

// t6g1HeadItems is HEAD's pool: registry.RunItems over the indexes.
func t6g1HeadItems(rep sdk.Reporter) t6g1ItemsFunc {
	return func(ctx context.Context, n int, o t6g1ItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, opsregistry.RunItemsOptions{
			Concurrency:       o.Concurrency,
			ProgressOffset:    o.ProgressBase,
			ProgressTotal:     o.ProgressTotal,
			ResumeFrom:        o.ResumeFrom,
			CheckpointEvery:   o.CheckpointEvery,
			CheckpointStateFn: o.CheckpointState,
			Label:             o.Label,
		})
	}
}

// t6g1NativeItems is the native pool: tasks.RunItems over the indexes. A failed
// item's own error comes back, as at HEAD, not the ItemsError around it.
//
// The checkpoint is HEAD's, not tasks.ItemsOptions.Watermark: the cores keep
// their own checkpoint struct (it carries more than a position) and
// rc.Checkpoint/rc.Restore store it, so the pool tracks the contiguous
// watermark itself, with the same meaning as registry.RunItems's.
func t6g1NativeItems(rc *tasks.RC) t6g1ItemsFunc {
	return func(_ context.Context, n int, o t6g1ItemsOptions, fn func(ctx context.Context, i int) error) error {
		par := tasks.CPU()
		if o.Concurrency > 0 {
			par = tasks.Fixed(o.Concurrency, "the run's concurrency param")
		}
		from := min(max(o.ResumeFrom, 0), n)
		idx := make([]int, 0, n-from)
		for i := from; i < n; i++ {
			idx = append(idx, i)
		}
		wm := newT6g1Watermark(from, n, o.CheckpointEvery, o.CheckpointState)
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error {
			if err := fn(ic.Context(), i); err != nil {
				return err
			}
			if cerr := wm.complete(ic.Context(), i); cerr != nil {
				rc.Log().Warn("checkpoint write failed", "err", cerr)
			}
			return nil
		}, tasks.ItemsOptions[int]{ProgressBase: o.ProgressBase + from, ProgressTotal: o.ProgressTotal})
		var ie *tasks.ItemsError
		if errors.As(err, &ie) && ie.First != nil {
			return ie.First
		}
		return err
	}
}

// t6g1Watermark tracks the contiguous prefix of finished items out of n and
// hands it to a callback every `every` completions, serialized.
type t6g1Watermark struct {
	mu    sync.Mutex
	done  []bool
	next  int
	count int
	every int
	fn    func(ctx context.Context, watermark int) error
}

func newT6g1Watermark(from, n, every int, fn func(ctx context.Context, watermark int) error) *t6g1Watermark {
	return &t6g1Watermark{done: make([]bool, n), next: from, every: max(every, 1), fn: fn}
}

// complete records item i as finished and reports the watermark when due.
func (w *t6g1Watermark) complete(ctx context.Context, i int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done[i] = true
	for w.next < len(w.done) && w.done[w.next] {
		w.next++
	}
	w.count++
	if w.fn == nil || w.count%w.every != 0 {
		return nil
	}
	return w.fn(ctx, w.next)
}

// t6g1Hold is a held scan stand-down.
type t6g1Hold struct {
	// Lost renews the lease and reports whether it is gone (the caller aborts its
	// remaining writes). Always false when the hold is not held or the shell
	// cannot renew in the loop (the native writer's fence fails the write
	// instead, with tasks.ErrFenced).
	Lost func() bool
	// Release is always safe to call.
	Release func()
}

// t6g1ProgressSink adapts an env.Progress hook to the sdk.Reporter that
// sdk.NewProgress takes; NewProgress only calls UpdateProgress.
type t6g1ProgressSink struct {
	sdk.Reporter
	fn func(current, total int, msg string)
}

func (s t6g1ProgressSink) UpdateProgress(current, total int, msg string) error {
	s.fn(current, total, msg)
	return nil
}

// t6g1HeadEnv fills the env from HEAD's Reporter.
func t6g1HeadEnv(rep opsregistry.Reporter, live bool) t6g1Env {
	return t6g1Env{
		Live: live,
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			_ = rep.Log(level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			_ = rep.UpdateProgress(current, total, msg)
		},
		Canceled: rep.IsCanceled,
		Logger:   rep.Logger(),
		OpID:     opsregistry.ReporterOpID(rep),
	}
}

// t6g1NativeEnv fills the env from the native run.
func t6g1NativeEnv(rc *tasks.RC) t6g1Env {
	return t6g1Env{
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
	}
}

// t6g1Common is the Common block every definition shares; the caller fills the
// per-definition fields (Title, Help, Effects, Resume, Priority, Timeout...).
func t6g1Common(kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Perms: tasks.Permissions{Start: t6g1StartRequirement()},
		Ready: []tasks.ReadySignal{t6g1SignalMemDB},
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// t6g1Writer is every write the 14 cores make, as one set of narrow groups, one
// per definition (declared next to its core). A core takes only its own group.
type t6g1Writer interface {
	pruneAIJournalWriter
	purgeEmptyAuthorsWriter
	authorityBuildWriter
	activityFilterWriter
	authorSplitWriter
	t6g1MergeWriter
	authorIDRepairWriter
	authorPathLinkWriter
	authorStripMergeWriter
}

// t6g1Store is every read the 14 cores make, grouped like t6g1Writer. Reads and
// writes are disjoint, so a helper that needs both takes a struct of the two.
type t6g1Store interface {
	authorTitleFragmentStore
	authorWhitespaceCollisionStore
	purgeEmptyAuthorsStore
	authorityBuildStore
	activityFilterStore
	authorSplitStore
	resolveProductionStore
	t6g1MergeReader
	authorConjunctionRepairStore
	authorDuplicateMergeStore
	authorIDRepairStore
	authorPathLinkReader
	authorStripMergeStore
	autoMatchTranscribedStore
}

// t6g1Deps is what the native shells need from the plugin: the stores and the
// services the cores call. The kernel builds it at F5-18.
type t6g1Deps struct {
	// Store reads the library.
	Store t6g1Store
	// NewWriter wraps the run's gated writer: every write a core makes goes
	// through the value it returns (conversion rule 3).
	NewWriter func(tasks.Writer) t6g1Writer
	// AIJournalRetentionDays is the configured ai_journal_retention_days.
	AIJournalRetentionDays func() int
	// ActivityFilter reads the Pebble activity store's filter-index state; nil
	// when no Pebble activity store is wired. Its writes go through NewWriter.
	ActivityFilter activityFilterStore
	// PlanReader reads the stored dry run an authority-build apply carries out.
	PlanReader authorityPlanReader
	// StandDownKey is the concurrency key of the library scan kinds, which a live
	// apply parks while it writes (rc.StandDown). Empty refuses a live apply that
	// needs the stand-down: it never runs without it. F5-18 sets it.
	StandDownKey tasks.Key
	// InvalidateDedupCache drops the duplicate-author cache after a write.
	InvalidateDedupCache func()
	// NewTranscriptionRunners builds the transcription search and apply over the
	// run's gated writer, so the apply's writes do not go around it. Nil means no
	// metadata fetch service is wired.
	NewTranscriptionRunners func(tasks.Writer) TranscriptionRunners
	// ManualOnly are the owner-manual check's readers (applygate.BookManualOnly).
	ManualOnly applygate.ManualOnlyReaders
	// InvalidateAuthorsCache drops the cached author list after a write.
	InvalidateAuthorsCache func()
	// NewMetadataFetcher builds the per-book metadata fetcher over the run's
	// gated writer, so the fetch's writes do not go around it. Nil means no fetch
	// service is wired.
	NewMetadataFetcher func(tasks.Writer) t6g1MetadataFetcher
}

// t6g1HeadFetcher is HEAD's metadata fetcher: the plugin deps' service.
type t6g1HeadFetcher struct{ deps ServerDeps }

func (f t6g1HeadFetcher) HasService() bool { return f.deps.HasMetadataFetchService() }
func (f t6g1HeadFetcher) FetchByTitle(ctx context.Context, bookID string) (*int, error) {
	return f.deps.FetchMetadataByTitle(ctx, bookID)
}

// t6g1NoFetcher stands in when no fetch service is wired.
type t6g1NoFetcher struct{}

func (t6g1NoFetcher) HasService() bool { return false }
func (t6g1NoFetcher) FetchByTitle(context.Context, string) (*int, error) {
	return nil, fmt.Errorf("metadata fetch service not wired")
}

// t6g1NativeStandDown takes the run's stand-down for a native live apply.
func t6g1NativeStandDown(rc *tasks.RC, key tasks.Key) func(ctx context.Context, reason string) (t6g1Hold, error) {
	return func(_ context.Context, reason string) (t6g1Hold, error) {
		if key == "" {
			return t6g1Hold{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t6g1Hold{}, err
		}
		return t6g1Hold{Lost: func() bool { return false }, Release: h.Release}, nil
	}
}

// t6g1HeadStandDown is HEAD's stand-down (acquireScanStandDownForApply over the
// plugin's scan controller).
func (p *Plugin) t6g1HeadStandDown(rep sdk.Reporter) func(ctx context.Context, reason string) (t6g1Hold, error) {
	return func(ctx context.Context, reason string) (t6g1Hold, error) {
		holderID, held, release, err := acquireScanStandDownForApply(ctx, p.deps, rep, reason)
		if err != nil {
			return t6g1Hold{}, err
		}
		return t6g1Hold{
			Lost:    func() bool { return scanStandDownLostForApply(p.deps, holderID, held) },
			Release: release,
		}, nil
	}
}

// t6g1HeadStore is HEAD's t6g1Store: the plugin's OpsStore plus the reads that
// are a helper over it rather than a method of it.
type t6g1HeadStore struct{ OpsStore }

// AuthorRefCounts is the unfiltered per-author reference count, through the
// decorator chain.
func (s t6g1HeadStore) AuthorRefCounts() (map[int]int, error) {
	return database.AuthorRefCounts(s.OpsStore)
}

// AuthorFileRefCounts is the unfiltered per-author file count over every book
// that references the author by any route, in any state.
func (s t6g1HeadStore) AuthorFileRefCounts() (map[int]int, error) {
	return database.AuthorFileRefCounts(s.OpsStore)
}

// t6g1HeadActivity is HEAD's activity-store adapter, both its reads
// (activityFilterStore) and its writes (activityFilterWriter).
type t6g1HeadActivity struct {
	s database.ActivityFilterIndexBackfiller
}

func (a t6g1HeadActivity) FilterIndexBackfillDone() bool { return a.s.FilterIndexBackfillDone() }
func (a t6g1HeadActivity) FilterIndexEarliestNanos(ctx context.Context) (int64, bool, error) {
	return a.s.FilterIndexEarliestNanos(ctx)
}
func (a t6g1HeadActivity) ClearFilterIndexBackfillDone() error {
	return a.s.ClearFilterIndexBackfillDone()
}
func (a t6g1HeadActivity) MarkFilterIndexBackfillDone() error {
	return a.s.MarkFilterIndexBackfillDone()
}
func (a t6g1HeadActivity) BackfillFilterIndexWindow(ctx context.Context, from, to int64, openEnded bool) (int, error) {
	return a.s.BackfillFilterIndexWindow(ctx, from, to, openEnded)
}

// ReconcileSupport is nil when the store has ReconcileFilterIndexCoverage.
func (a t6g1HeadActivity) ReconcileSupport() error {
	if _, ok := a.s.(activityFilterIndexReconciler); !ok {
		return fmt.Errorf("activity-filter-index-backfill: cannot reconcile: the activity store is %T, which has no "+
			"ReconcileFilterIndexCoverage", a.s)
	}
	return nil
}

func (a t6g1HeadActivity) ReconcileFilterIndexCoverage(ctx context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
	rec, ok := a.s.(activityFilterIndexReconciler)
	if !ok {
		return database.FilterIndexReconcileResult{}, a.ReconcileSupport()
	}
	return rec.ReconcileFilterIndexCoverage(ctx, progress)
}

// t6g1HeadWriter is HEAD's t6g1Writer: the writes go straight to the plugin's
// store, as they did before the cores existed.
type t6g1HeadWriter struct{ OpsStore }

func (w t6g1HeadWriter) AIJournalPruner(kind string) (t6g1JournalPruner, error) {
	j, err := resultjournal.New(w.OpsStore, kind)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// RecordOperationChange journals one change on the undo ledger.
func (w t6g1HeadWriter) RecordOperationChange(change *database.OperationChange) error {
	return database.RecordOperationChange(w.OpsStore, change)
}

// t6g1MergeReader is the reads an author merge makes beyond the library lists.
type t6g1MergeReader interface {
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	// CanMergeDelete reports whether the store merge-deletes an author (moving its
	// aliases and writing the redirect). A merge refuses without it.
	CanMergeDelete() bool
	// VerifyAuthorUnlinked errors while a book still links the author
	// (database.VerifyAuthorUnlinked).
	VerifyAuthorUnlinked(authorID int) error
}

// t6g1MergeWriter is the composite writes an author merge makes. Each journals
// an undo row BEFORE its write and voids it when the write is refused
// (internal/authormerge), so the composite is one gated write here: splitting it
// into its journal row and its write would let a fence land between them.
type t6g1MergeWriter interface {
	// MoveCredits moves one book's credits off the merged author.
	MoveCredits(opID string, m authormerge.CreditMove) (authormerge.MoveResult, error)
	// MergeDeleteAuthor deletes the merged author, moving its aliases.
	MergeDeleteAuthor(opID string, from, into database.Author) (authormerge.DeleteResult, error)
	// RenameAuthorJournaled renames an author in place under the name check.
	RenameAuthorJournaled(opID string, author database.Author, name string) error
}

// AuthorRefBucketCounts is the per-author reference count split into live,
// trashed and dangling references.
func (s t6g1HeadStore) AuthorRefBucketCounts() (map[int]database.AuthorRefBuckets, error) {
	return database.AuthorRefBucketCounts(s.OpsStore)
}

// CanMergeDelete is whether the plugin's store merge-deletes authors.
func (s t6g1HeadStore) CanMergeDelete() bool {
	_, ok := database.AsCapability[database.AuthorMergeDeleter](s.OpsStore)
	return ok
}

// VerifyAuthorUnlinked is the shared pre-delete link check.
func (s t6g1HeadStore) VerifyAuthorUnlinked(authorID int) error {
	return database.VerifyAuthorUnlinked(s.OpsStore, authorID)
}

// MoveCredits is authormerge.MoveCredits over the plugin's store.
func (w t6g1HeadWriter) MoveCredits(opID string, m authormerge.CreditMove) (authormerge.MoveResult, error) {
	return authormerge.MoveCredits(w.OpsStore, opID, m)
}

// MergeDeleteAuthor is authormerge.MergeDelete over the plugin's store.
func (w t6g1HeadWriter) MergeDeleteAuthor(opID string, from, into database.Author) (authormerge.DeleteResult, error) {
	return authormerge.MergeDelete(w.OpsStore, opID, from, into)
}

// RenameAuthorJournaled is renameAuthorJournaled over the plugin's store.
func (w t6g1HeadWriter) RenameAuthorJournaled(opID string, author database.Author, name string) error {
	return renameAuthorJournaled(w.OpsStore, opID, author, name)
}

// t6g1Kinds returns every native kind of group 1 in file order (author.go's two
// in the order it declares them). sched is rule
// R9's schedule lookup; nil means no kind is scheduled.
func (p *Plugin) t6g1Kinds(deps t6g1Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.activityFilterIndexBackfillNative(deps, sched),
		p.pruneAIJournalNative(deps, sched),
		p.authorSplitScanNative(deps, sched),
		p.resolveProductionAuthorsNative(deps, sched),
		p.authorConjunctionRepairNative(deps, sched),
		p.authorDuplicateMergeNative(deps, sched),
		p.authorIDRepairNative(deps, sched),
		p.authorPathLinkNative(deps, sched),
		p.purgeEmptyAuthorsNative(deps, sched),
		p.authorStripMergeNative(deps, sched),
		p.authorTitleFragmentNative(deps, sched),
		p.authorWhitespaceCollisionNative(deps, sched),
		p.authorityBuildNative(deps, sched),
		p.autoMatchTranscribedNative(deps, sched),
	}
}

// t6g1KindsRoot names t6g1Kinds from non-test code so tasklint's
// unregistered-kind rule counts it a registration root until F5-18's
// Registrar.Tasks call replaces this line (the analyzer does not load test
// files). F5-18 deletes it.
var _ = (*Plugin).t6g1Kinds
