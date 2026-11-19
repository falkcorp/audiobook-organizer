// file: internal/plugins/deluge/env.go
// version: 1.0.2
// guid: ef303b12-eeca-4ad8-bd87-b34265d15f21
// last-edited: 2026-11-19

package deluge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// F5-05: the engine-neutral half of the Deluge plugin's dark conversion. Every
// definition has one shared core taking (ctx, params, env) and two shells over
// it: HEAD's v2 Run (this file's headEnv and the head* adapters) and the native
// books kind (kinds*.go, nativeEnv). The core reaches the run only through the
// env it is handed. This file imports the sdk (HEAD's Reporter) so that no
// kinds*.go file has to: the T5 gate is that the native shells do not import
// pkg/plugin/sdk (05-tasks 5T.8.2).

// kindEnv is what every core takes from its shell besides the stores, the
// writer and the Deluge client (each definition's own env struct embeds it and
// adds those, rule R10).
type kindEnv struct {
	// Live is false for a dry run (HEAD) or a Preview run (native).
	Live bool
	// Logger is the run's logger: HEAD's Reporter.Logger(), or rc.Log().
	Logger *slog.Logger
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// Log writes one line to the run's log.
	Log func(level slog.Level, msg string)
	// Canceled reports whether the run was canceled (HEAD: Reporter.IsCanceled).
	Canceled func() bool
	// Items runs a per-item loop on the shell's pool.
	Items kindItemsFunc
	// SetResult persists a result document: HEAD's ReporterSetResult. Nil in a
	// native shell, whose Task returns the result instead.
	SetResult func(v any) error
	// Reporter is the sdk.Reporter the shared helpers that take one
	// (sdk.NewProgress) are handed: HEAD's own Reporter, or a sink over Progress
	// for a native run.
	Reporter sdk.Reporter
	// Checkpoint saves state for the run's liveness record (HEAD:
	// Reporter.Checkpoint, native: rc.Checkpoint).
	Checkpoint func(state any) error
	// Touch stamps the run's liveness clock without reporting progress (HEAD:
	// registry.TouchLiveness, native: rc.Progress().Tick).
	Touch func()
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
	// index (and, for the total, nothing the pool does not already know). Both
	// pools carry it: HEAD's as RunItemsOptions.Label, the native as the
	// item-only tasks.ItemsOptions.Label. A label that reads a running counter
	// cannot be native (the kernel's label is pure): set Label instead, which
	// only HEAD's pool carries (a seam recorded in seams.d). Label is called
	// inside HEAD's worker goroutines, so it reads its counters atomically.
	PureLabel func(i, total int) string
	Label     func(i, total int) string
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

// headEnv fills the env from HEAD's Reporter, Checkpoint included. Only a def
// whose core calls env.Checkpoint uses it.
func headEnv(rep opsregistry.Reporter, live bool) kindEnv {
	e := headEnvBase(rep, live)
	e.Checkpoint = rep.Checkpoint
	return e
}

// headEnvBase is headEnv without a Checkpoint: for a def whose core never
// checkpoints (link-backfill resumes through its start_after cursor). Leaving
// the hook unbound keeps that visible to TestResumeRestartDefsCheckpoint, which
// would otherwise read a shared binding as a checkpoint the run never writes.
func headEnvBase(rep opsregistry.Reporter, live bool) kindEnv {
	return kindEnv{
		Live: live,
		Progress: func(current, total int, msg string) {
			_ = rep.UpdateProgress(current, total, msg)
		},
		Log:       func(level slog.Level, msg string) { _ = rep.Log(level, msg) },
		Canceled:  rep.IsCanceled,
		Logger:    rep.Logger(),
		Items:     headItems(rep),
		SetResult: func(v any) error { return opsregistry.ReporterSetResult(rep, v) },
		Reporter:  rep,
		Touch:     func() { opsregistry.TouchLiveness(rep) },
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
			rc.Progress().Set(int64(current), t)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		Log: func(level slog.Level, msg string) {
			rc.Log().Log(rc.Context(), level, msg)
		},
		Canceled:   rc.Canceled,
		Logger:     rc.Log(),
		Items:      nativeItems(rc),
		Checkpoint: rc.Checkpoint,
		Touch:      rc.Progress().Tick,
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

// errNeedWriter is the refusal for a NewWriter factory that returned nothing.
func errNeedWriter(kind tasks.Kind) error {
	return fmt.Errorf("%s: the writer factory returned no writer", kind)
}

// headKindStore is HEAD's store for the cores: the plugin's pluginStore, which
// the HEAD shells read through. Every method forwards to the store; nothing here
// writes. The retired-reader methods resolve database.RetiredBookFileReader
// through any decorator chain (AsCapability), and fail closed with
// database.ErrRetiredReaderUnavailable when no layer has it, exactly as
// database.BookFileByPathIncludingRetired did when ImportToLibraryWith handed it
// p.store.
type headKindStore struct{ s pluginStore }

var _ kindStore = headKindStore{}

func (h headKindStore) GetBookByID(id string) (*database.Book, error) { return h.s.GetBookByID(id) }
func (h headKindStore) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	return h.s.GetBookFileByID(bookID, fileID)
}
func (h headKindStore) GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error) {
	return h.s.GetBookFilesNeedingDelugeImportCore()
}
func (h headKindStore) GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error) {
	return h.s.GetBookVersionsByBookID(bookID)
}
func (h headKindStore) GetBookFileByPath(filePath string) (*database.BookFile, error) {
	return h.s.GetBookFileByPath(filePath)
}
func (h headKindStore) GetAllBookFilesCoreComplete() ([]database.BookFileCore, error) {
	return h.s.GetAllBookFilesCoreComplete()
}
func (h headKindStore) BookFilesWithHash(hash string) ([]database.BookFile, error) {
	return h.s.BookFilesWithHash(hash)
}
func (h headKindStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	return h.s.BookFilesAtPath(path)
}
func (h headKindStore) GetBooksByIDs(ids []string) ([]database.Book, error) {
	return h.s.GetBooksByIDs(ids)
}

// retired resolves the capability, or a reader that refuses every call.
func (h headKindStore) retired() database.RetiredBookFileReader {
	if r, ok := database.AsCapability[database.RetiredBookFileReader](h.s); ok {
		return r
	}
	return retiredUnavailable{}
}

func (h headKindStore) GetBookFilesIncludingRetired(bookID string) ([]database.BookFile, error) {
	return h.retired().GetBookFilesIncludingRetired(bookID)
}
func (h headKindStore) GetRetiredBookFiles(holderID string) ([]database.BookFile, error) {
	return h.retired().GetRetiredBookFiles(holderID)
}
func (h headKindStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return h.retired().GetAllBookFilesCoreIncludingRetired()
}
func (h headKindStore) GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error) {
	return h.retired().GetRetiredVersionsFromBook(shellID)
}
func (h headKindStore) GetBookFileByPIDIncludingRetired(itunesPID string) (*database.BookFile, error) {
	return h.retired().GetBookFileByPIDIncludingRetired(itunesPID)
}
func (h headKindStore) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	return h.retired().GetBookFileByPathIncludingRetired(path)
}

// retiredUnavailable is the retired reader of a store with none: every call
// fails closed with the error database.BookFileByPathIncludingRetired gives.
type retiredUnavailable struct{}

func (retiredUnavailable) GetBookFilesIncludingRetired(bookID string) ([]database.BookFile, error) {
	return nil, fmt.Errorf("book_files of %q: %w", bookID, database.ErrRetiredReaderUnavailable)
}
func (retiredUnavailable) GetRetiredBookFiles(holderID string) ([]database.BookFile, error) {
	return nil, fmt.Errorf("retired book_files of %q: %w", holderID, database.ErrRetiredReaderUnavailable)
}
func (retiredUnavailable) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return nil, fmt.Errorf("all book_files: %w", database.ErrRetiredReaderUnavailable)
}
func (retiredUnavailable) GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error) {
	return nil, fmt.Errorf("retired versions of %q: %w", shellID, database.ErrRetiredReaderUnavailable)
}
func (retiredUnavailable) GetBookFileByPIDIncludingRetired(itunesPID string) (*database.BookFile, error) {
	return nil, fmt.Errorf("book_file at pid %q: %w", itunesPID, database.ErrRetiredReaderUnavailable)
}
func (retiredUnavailable) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	// The text database.BookFileByPathIncludingRetired wraps its sentinel in.
	return nil, fmt.Errorf("book_file at %q: %w", path, database.ErrRetiredReaderUnavailable)
}

// headKindWriter is HEAD's writer for the cores: each method makes the very
// store call HEAD's body made, with the same arguments (the Deluge ops wrote
// through the plugin's store directly, never through a v3 writer).
type headKindWriter struct{ s pluginStore }

var _ kindWriter = headKindWriter{}

func (h headKindWriter) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return h.s.ModifyBookFile(bookID, fileID, fn)
}
func (h headKindWriter) UpdateBookFile(id string, file *database.BookFile) error {
	return h.s.UpdateBookFile(id, file)
}

// headDelugeClient is HEAD's Deluge client for the cores: the plugin's own
// *delugeclient.Client, called exactly as HEAD's bodies called it. The three
// narrow interfaces below are the only parts of the client the cores reach (the
// Deluge client is not a store write and stays in the core, F5-05 brief).
type headDelugeClient struct{ c *delugeclient.Client }

var _ delugeClient = headDelugeClient{}

func (h headDelugeClient) ListTorrentDetailsByLabel(label string) ([]delugeclient.TorrentDetail, error) {
	return h.c.ListTorrentDetailsByLabel(label)
}
func (h headDelugeClient) MoveStorage(torrentIDs []string, destPath string) error {
	return h.c.MoveStorage(torrentIDs, destPath)
}
func (h headDelugeClient) ImportToLibrary(cfg *config.Config, store delugeclient.Store, bookFile *database.BookFile, opts delugeclient.ImportOptions) (string, error) {
	return delugeclient.ImportToLibraryWith(cfg, h.c, store, bookFile, opts)
}

// headClient is the env's client: nil (an untyped nil interface, so the cores'
// "is Deluge configured" checks stay true to HEAD's p.client == nil) when the
// plugin has none.
func (p *Plugin) headClient() delugeClient {
	if p.client == nil {
		return nil
	}
	return headDelugeClient{c: p.client}
}

// headImporter is the importer centralize hands ImportToLibraryWith: HEAD passed
// a possibly-nil *Client (ImportToLibraryWith treats it as "never move
// storage"), so the adapter is always present.
func (p *Plugin) headImporter() libraryImporter { return headDelugeClient{c: p.client} }

// headStore is the env's store: nil when the plugin has none (HEAD's
// p.store == nil check), else the forwarding adapter.
func (p *Plugin) headStore() kindStore {
	if p.store == nil {
		return nil
	}
	return headKindStore{s: p.store}
}

// headWriter is the env's writer over the plugin's store.
func (p *Plugin) headWriter() kindWriter { return headKindWriter{s: p.store} }
