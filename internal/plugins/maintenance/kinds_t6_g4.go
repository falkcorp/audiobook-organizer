// file: internal/plugins/maintenance/kinds_t6_g4.go
// version: 1.0.0
// guid: a6f18f0f-9e00-4b3a-a135-2ea70c56209f
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"runtime"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Native books kinds for T6 group 4 (F5-09, 05-tasks 5T.8.2 conversion group T6,
// sub-batch 4 of 7): the maintenance-plugin definitions of the 14 files named in
// docs/agent-tasks/platform-core/F5-09-*.md. They land DARK: t6g4Kinds is called
// by nothing in production, and HEAD's v2 definitions keep running unchanged
// until the tasks flip (F5-18) registers these and deletes HEAD's engine.
//
// Each definition's body lives once, in an unexported <run>Core function in its
// own file that takes the decoded params and a per-definition env. HEAD's Run is
// the v2 shell around the core and the func in this file's Kind builders is the
// native shell around the same core (rule R2). The core reaches its writer and
// its stores only through the env its shell hands it (rule R10): nothing in a
// core reads p.deps, a receiver field or a package variable for a store.

// t6g4KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
const t6g4KindPrefix = "books."

// t6g4SignalMemDB is the readiness component the kinds wait for (rule R5,
// NoWarmupWait false, which every definition of this group is). Books registers
// the component with the health registry at F5-18.
const t6g4SignalMemDB tasks.ReadySignal = "books.memdb"

// t6g4SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (05-rbac 6.1, internal/books.AtomTable), which HEAD's empty Permissions
// list and file-integrity-check's explicit PermSettingsManage both mean. This
// package cannot import internal/books (books imports it), so the scopes are
// copied here and kinds_t6_g4_atoms_test.go (package maintenance_test) fails
// when the two drift.
var t6g4SettingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// t6g4StartRequirement is rule R6 for a definition with no Permissions of its own
// (or exactly settings.manage): all of the atom's scopes.
func t6g4StartRequirement() rbac.Requirement {
	return rbac.AllOf(t6g4SettingsManageScopes...)
}

// t6g4Res is rule R7's res(s) = tasks.Res("books", s).
func t6g4Res(kinds ...string) []tasks.Resource {
	out := make([]tasks.Resource, len(kinds))
	for i, k := range kinds {
		out[i] = tasks.Res("books", k)
	}
	return out
}

// ---- the group's two interfaces and its dependency value (rule R10) ----

// t6g4Store holds exactly the READ methods this group's cores call, with HEAD's
// signatures; a method that writes is never on it. It is composed of small
// groups (the interface-width ratchet counts declared entries) and grows by one
// group per definition.
type t6g4Store interface {
	t6g4BookFileReads
	t6g4IdentReads
	t6g4AIDedupReads
	t6g4TriageReads
	t6g4WAVReads
	t6g4ProvReads
	t6g4BookCoreReads
	t6g4OpQueueReads
	t6g4FileIntroReads
	t6g4RegroupReads
	t6g4DurationReads
}

// t6g4Writer holds exactly the WRITE methods this group's cores call. Where HEAD
// wrote through a store method, the method here has that method's name and
// signature. Nothing passes a real implementation in production before F5-18,
// which wires internal/books/writer behind t6g4Deps.NewWriter.
type t6g4Writer interface {
	t6g4IdentWrites
	t6g4AIDedupWrites
	t6g4TriageWrites
	t6g4WAVWrites
	t6g4ProvWrites
	t6g4BookFileWrites
	t6g4DedupeRowsWrites
	t6g4FileIntroWrites
	t6g4BookModifyWrites
	t6g4BookFilesBatchWrites
	t6g4RegroupWrites
	t6g4DurationWrites
}

// t6g4BookFileReads is the book_file read the file-integrity scan makes.
type t6g4BookFileReads interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

// t6g4IdentReads is the read of ident-index-rebuild's preview pass.
type t6g4IdentReads interface {
	VerifyIdentIndex(ctx context.Context) (database.IdentIndexReport, error)
}

// t6g4IdentWrites is the one write of ident-index-rebuild: the rebuild of the
// ident: rows (never a book or cache row).
type t6g4IdentWrites interface {
	RebuildIdentIndex(ctx context.Context) (database.IdentRebuildStats, error)
}

// t6g4AIDedupReads is what ai-dedup-batch reads: whether AI parsing is on (a
// read of the server's configuration, so it arrives through the store the shell
// hands the core), the in-flight batch listing, and the authors it samples.
type t6g4AIDedupReads interface {
	HasAIParsing() bool
	ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]database.AIJob, error)
	GetAllAuthors() ([]database.Author, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// t6g4AIDedupWrites is ai-dedup-batch's one write: submitting the author dedup
// batch, which writes the ai_jobs rows. The implementation owns the OpenAI
// parser (HEAD builds it from config in the shell).
type t6g4AIDedupWrites interface {
	SubmitAuthorDedupJob(ctx context.Context, sourceID string, inputs []ai.AuthorDiscoveryInput) (string, error)
}

// t6g4TriageReads is dedup-exact-triage's read side: whether the dedup engine is
// up, and the classify-only scan a preview runs (apply=false, which dismisses
// nothing).
type t6g4TriageReads interface {
	HasDedupEngine() bool
	DedupTriagePreview(ctx context.Context) (*TriageReport, error)
}

// t6g4TriageWrites is the applying scan: it classifies and dismisses every
// purgeable candidate (a status update on dedup candidate rows).
type t6g4TriageWrites interface {
	DedupTriageApply(ctx context.Context) (*TriageReport, error)
}

// t6g4PathReads is the library root every run that writes under the library
// reads (a read of the server's configuration, not a store row).
type t6g4PathReads interface {
	RootDir() string
}

// t6g4WAVReads is what extract-wav-clips reads: the book ids and books it
// walks, each book's files, and the library root.
type t6g4WAVReads interface {
	t6g4PathReads
	ListBookIDs() ([]string, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// t6g4WAVWrites is extract-wav-clips' one write: the content-addressed file
// hash on a book_files row that has none (when-missing only).
type t6g4WAVWrites interface {
	SetBookFileHash(id, hash string) error
}

// t6g4ProvReads is what the two provenance kinds read from the ledger.
// FileProvenanceAvailable is false where no layer of the store implements the
// file_prov keyspace (HEAD's FileProvenanceStore() returning nil).
type t6g4ProvReads interface {
	FileProvenanceAvailable() bool
	FindFileEventsByHash(hash string) ([]database.FileEvent, error)
	GetFileProvExportCursor() (uint64, error)
	MaxFileEventSeq() (uint64, error)
	ScanFileEventsBySeq(afterSeq uint64, limit int) ([]database.FileEventSeqRow, error)
	GetFileHistory(bookFileID string) ([]database.FileEvent, error)
}

// t6g4ProvWrites is the provenance kinds' writes: an orphan observation appended
// by capture, and the durable export cursor advanced by export.
type t6g4ProvWrites interface {
	AppendFileEvent(e database.FileEvent) error
	SetFileProvExportCursor(seq uint64) error
}

// t6g4BookCoreReads is the whole-library book listing the filepath collision
// report reads in one snapshot.
type t6g4BookCoreReads interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

// t6g4BookFileWrites is the whole-row book_file write-back
// (UpdateBookFile) the intro migration and the file-row dedup share.
type t6g4BookFileWrites interface {
	UpdateBookFile(id string, file *database.BookFile) error
}

// t6g4OpQueueReads is the active-operation listing the file-row dedup consults
// before it applies (a library.scan rewrites the rows it deletes).
// HasOperationQueue is false where HEAD's OperationQueueStore() returned nil.
type t6g4OpQueueReads interface {
	HasOperationQueue() bool
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

// t6g4DedupeRowsWrites are the file-row dedup's writes beyond UpdateBookFile: the
// fingerprint-window carry-over, the undo-ledger row, the batched delete and the
// aggregate recompute.
type t6g4DedupeRowsWrites interface {
	CarryOverFingerprintWindows(from []database.FingerprintWindowRef, to database.FingerprintWindowRef) (int, error)
	CreateOperationChange(change *database.OperationChange) error
	DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error)
	RecomputeBookAggregates(bookID string) error
}

// t6g4FileIntroReads is what the per-file intro transcription reads beyond the
// shared groups: a file row and a book row by id (donor reuse, the rollback
// preview), and whether the store can write per-file intro results at all (HEAD's
// database.AsCapability probe, which a core cannot make on a store it only reads).
type t6g4FileIntroReads interface {
	FileIntroWritesAvailable() bool
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookByID(id string) (*database.Book, error)
}

// t6g4FileIntroWrites are the per-file intro writes: a result, a rollback, the
// timeout strikes, and the run's persisted counters.
type t6g4FileIntroWrites interface {
	SetBookFileIntro(bookID, fileID string, r database.BookFileIntroResult) (database.BookFileIntroWrite, error)
	ClearBookFileIntro(bookID, fileID, runOpID string) (before *database.BookFile, cleared bool, err error)
	RecordIntroTimeout(bookID, fileID string, s database.IntroTimeoutStrike) (database.IntroTimeoutOutcome, error)
	ClearIntroTimeouts(bookID, fileID, sourceHash string) (cleared bool, err error)
	PutFileIntroStats(*database.FileIntroStats) error
}

// t6g4BookModifyWrites is ModifyBook: a write of the book row's own columns
// under its lock (a book-level transcript fill or its revert).
type t6g4BookModifyWrites interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// t6g4RegroupReads is what fs-regroup-xml reads beyond the shared groups: the
// planner's whole-library snapshot, the apply's per-group re-reads, and the
// reads of the helpers it hands the store to (merge, versionprimary,
// itunesguard, the field-lock loader).
type t6g4RegroupReads interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	GetUserPreference(key string) (*database.UserPreference, error)
	LiveBookIDsAtPath(path string) ([]string, error)
}

// t6g4BookFilesBatchWrites is the batched book_file write-back that recomputes
// each touched book once (UpdateBookFiles).
type t6g4BookFilesBatchWrites interface {
	UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error)
}

// t6g4RegroupWrites are fs-regroup-xml's writes beyond the shared groups: a row
// created in a gap, rows moved to the survivor, a soft-delete row's void, and
// the retired shell's external ids moved one at a time.
type t6g4RegroupWrites interface {
	CreateBookFile(file *database.BookFile) error
	MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error
	MarkOperationChangesReverted(operationID string, changeIDs []string) error
	ReassignExternalID(source, externalID, newBookID string) error
}

// t6g4DurationReads is what duration-backfill reads beyond the shared groups: the
// library's book count (the progress denominator), and whether the store can
// defer aggregates on a row write (HEAD's database.AsCapability probe).
type t6g4DurationReads interface {
	CountAllBooks() (int, error)
	DeferAggregatesAvailable() bool
}

// t6g4DurationWrites is the per-row book_file write that defers the book's
// aggregate recompute (a probe-failure record, or an unknownRows measurement).
type t6g4DurationWrites interface {
	ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// t6g4View is the read-write store a core hands to a helper that needs both
// (a recompute that follows its own row writes, a ledger helper): the env's
// reads and the env's writes, nothing else.
type t6g4View struct {
	t6g4Store
	t6g4Writer
}

// t6g4ReportPath is the report-path resolution Plugin.resolveReportPath makes,
// over the library root a core reads from its store rather than from the
// plugin's deps: the caller's explicit path when given, else
// {RootDir}/.reports/<fileName>, with its directory proven writable.
func t6g4ReportPath(store t6g4PathReads, explicit, fileName string) (string, error) {
	return library.ReportPathNotify(store.RootDir(), explicit, fileName, func(dir string, was fs.FileMode) {
		logger.New("maintenance").Warn("removed world write from the report directory, its mode was %o dir=%s", was, logger.SanitizeLogValue(dir))
	})
}

// t6g4Deps is what the owner (books, from F5-18) declares for the group's kinds
// (rule R10 item 4; 05-tasks 5T.8.4 store-capture).
type t6g4Deps struct {
	Store t6g4Store
	// NewWriter wraps one run's tasks.Writer in the typed writer the cores call
	// (F5-18: internal/books/writer over rc.Writer()). The shell of a ReadOnly
	// kind never calls it.
	NewWriter func(tasks.Writer) t6g4Writer
	// Scan is the scan stand-down service fs-regroup-xml's apply takes around
	// its writes. Nil means none (HEAD's degraded caller, which runs unchanged
	// where there is no scanner).
	Scan ScanController
}

// ---- the env every core receives (rule R2, widened by the notes in seams.d/F5-09.md) ----

// t6g4Base is the part of every per-definition env that does not vary: what the
// engine around the core supplies. Its shape is rule R2's list, with Log taking
// slog attributes (HEAD's reporter.Log does, and a core's log lines carry
// them), and Logger, SetItem, Canceled and OpID added because the bodies call
// reporter.Logger(), SetCurrentItem, IsCanceled and ReporterOpID and the
// helpers beneath them take an sdk.Reporter that tests pin (rule R2 item 4).
type t6g4Base struct {
	Live     bool
	OpID     string
	Log      func(level slog.Level, msg string, attrs ...slog.Attr)
	Logger   func() *slog.Logger
	Progress func(done, total int, msg string)
	SetItem  func(label string)
	Canceled func() bool
	// Touch tells the engine's liveness watchdog the run is alive without
	// moving the progress bar (HEAD: registry.TouchLiveness; native:
	// rc.Progress().Tick()).
	Touch          func()
	SaveCheckpoint func(v any) error
	LoadCheckpoint func(v any) (bool, error)
	Writer         t6g4Writer // rule R10: the only write path
	Store          t6g4Store  // rule R10: the only read path
	// Each is the loop runner the core calls where HEAD called
	// registry.RunItems: n items addressed by index (the core closes over its
	// slice), at the concurrency in o. HEAD's shell backs it with
	// registry.RunItems, the native shell with tasks.RunItems (rule R4).
	Each func(ctx context.Context, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error
}

// t6g4CPU is t6g4EachOpts.Concurrency for a loop HEAD sizes at runtime.NumCPU():
// the native shell declares it tasks.CPU() (rule R4).
const t6g4CPU = -1

// t6g4EachOpts is the part of HEAD's registry.RunItemsOptions a group 4 loop
// sets, in the terms both runners understand.
type t6g4EachOpts struct {
	// Concurrency: 0 or 1 is HEAD's sequential default (tasks.Sequential), n>1
	// is tasks.Fixed(n), t6g4CPU is tasks.CPU().
	Concurrency int
	// Label names item i of total; it runs inside the worker goroutine, so any
	// counter it reads needs the caller's mutex or atomic.
	Label func(i, total int) string
	// ItemTimeout is HEAD's PerItemTimeout.
	ItemTimeout time.Duration
	// Collect is HEAD's ErrModeCollect (tasks.Collect): run every item, join
	// the errors.
	Collect bool
	// ProgressTotal is the absolute denominator (0 = n).
	ProgressTotal int
}

// t6g4Par returns the tasks.Parallelism a loop declares for o.
func t6g4Par(o t6g4EachOpts, reason string) tasks.Parallelism {
	switch {
	case o.Concurrency == t6g4CPU:
		return tasks.CPU()
	case o.Concurrency > 1:
		return tasks.Fixed(o.Concurrency, reason)
	default:
		return tasks.Sequential(reason)
	}
}

// t6g4EachFn is the type of t6g4Base.Each, for the helpers that take a loop runner.
type t6g4EachFn = func(ctx context.Context, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error

// t6g4RunEach is the shared native loop: tasks.RunItems over the indexes.
func t6g4RunEach(rc *tasks.RC, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	opts := tasks.ItemsOptions[int]{ItemTimeout: o.ItemTimeout, ProgressTotal: o.ProgressTotal}
	if o.Collect {
		opts.OnError = tasks.Collect
	}
	if o.Label != nil {
		opts.Label = func(i int) string { return o.Label(i, n) }
	}
	_, err := tasks.RunItems(rc, t6g4Par(o, "a T6 group 4 loop at HEAD's registry.RunItems concurrency"), idx,
		func(ic *tasks.ItemRC, i int) error { return fn(ic.Context(), i) }, opts)
	return err
}

// t6g4RunEachHEAD is HEAD's loop runner: registry.RunItems with HEAD's options.
func t6g4RunEachHEAD(reporter sdk.Reporter) func(ctx context.Context, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error {
	return func(ctx context.Context, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		conc := o.Concurrency
		if conc == t6g4CPU {
			conc = runtime.NumCPU()
		}
		opts := registry.RunItemsOptions{
			Concurrency:    conc,
			PerItemTimeout: o.ItemTimeout,
			Label:          o.Label,
			ProgressTotal:  o.ProgressTotal,
		}
		if o.Collect {
			opts.ErrMode = registry.ErrModeCollect
		}
		return registry.RunItems(ctx, reporter, idx, func(ctx context.Context, i int) error { return fn(ctx, i) }, opts)
	}
}

// reporter is an sdk.Reporter over the env, for the helpers beneath a core that
// take one. It adds no behaviour: every method calls the env function the
// shell wired to the engine's own.
func (b t6g4Base) reporter(ctx context.Context) sdk.Reporter { return t6g4Reporter{ctx: ctx, b: b} }

type t6g4Reporter struct {
	ctx context.Context
	b   t6g4Base
}

func (r t6g4Reporter) UpdateProgress(current, total int, message string) error {
	if r.b.Progress != nil {
		r.b.Progress(current, total, message)
	}
	return nil
}

func (r t6g4Reporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	if r.b.Log != nil {
		r.b.Log(level, message, attrs...)
	}
	return nil
}

func (r t6g4Reporter) Logger() *slog.Logger {
	if r.b.Logger != nil {
		return r.b.Logger()
	}
	return slog.Default()
}

func (r t6g4Reporter) Checkpoint(state any) error {
	if r.b.SaveCheckpoint == nil {
		return nil
	}
	return r.b.SaveCheckpoint(state)
}

func (r t6g4Reporter) IsCanceled() bool {
	if r.b.Canceled != nil {
		return r.b.Canceled()
	}
	return r.ctx.Err() != nil
}

func (r t6g4Reporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

func (r t6g4Reporter) Trigger(context.Context, string, any) error {
	return fmt.Errorf("maintenance: a T6 group 4 core fires no triggers")
}

func (r t6g4Reporter) SetCurrentItem(label string) {
	if r.b.SetItem != nil {
		r.b.SetItem(label)
	}
}

// TouchLiveness makes registry.TouchLiveness reach the env's Touch.
func (r t6g4Reporter) TouchLiveness() {
	if r.b.Touch != nil {
		r.b.Touch()
	}
}

// OpID makes registry.ReporterOpID answer with the run's id.
func (r t6g4Reporter) OpID() string { return r.b.OpID }

// t6g4HeadBase is HEAD's shell filling the part of the env every core shares from
// the reporter (rule R2 item 2). store is what HEAD's body read; writer is the
// adapter over the HEAD writer (rule R10 item 5).
func t6g4HeadBase(reporter sdk.Reporter, live bool, store t6g4Store, writer t6g4Writer) t6g4Base {
	return t6g4Base{
		Live:     live,
		OpID:     registry.ReporterOpID(reporter),
		Log:      func(level slog.Level, msg string, attrs ...slog.Attr) { _ = reporter.Log(level, msg, attrs...) },
		Logger:   reporter.Logger,
		Progress: func(done, total int, msg string) { _ = reporter.UpdateProgress(done, total, msg) },
		SetItem:  reporter.SetCurrentItem,
		Canceled: reporter.IsCanceled,
		Touch:    func() { registry.TouchLiveness(reporter) },
		SaveCheckpoint: func(v any) error {
			return reporter.Checkpoint(v)
		},
		Writer: writer,
		Store:  store,
		Each:   t6g4RunEachHEAD(reporter),
	}
}

// t6g4NativeBase is the native shell filling the same part from rc (rule R2 item
// 3): Live is the run's mode, and the writer is deps.NewWriter(rc.Writer()).
// needsWriter is false for a ReadOnly kind, which gets none (core hands a
// ReadOnly run no writer, and its core writes nothing).
func t6g4NativeBase(rc *tasks.RC, deps t6g4Deps, needsWriter bool) (t6g4Base, error) {
	if deps.Store == nil {
		return t6g4Base{}, fmt.Errorf("%s: t6g4Deps.Store is not set", rc.Kind())
	}
	var w t6g4Writer
	if needsWriter {
		if deps.NewWriter == nil {
			return t6g4Base{}, fmt.Errorf("%s: t6g4Deps.NewWriter is not set", rc.Kind())
		}
		if w = deps.NewWriter(rc.Writer()); w == nil {
			return t6g4Base{}, fmt.Errorf("%s: t6g4Deps.NewWriter returned no writer", rc.Kind())
		}
	}
	ctx := rc.Context()
	return t6g4Base{
		Live: rc.Mode() == tasks.ModeLive,
		OpID: string(rc.RunID()),
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			rc.Log().LogAttrs(ctx, level, msg, attrs...)
		},
		Logger: rc.Log,
		Progress: func(done, total int, msg string) {
			t := tasks.Unknown()
			if total > 0 {
				t = tasks.Known(int64(total))
			}
			rc.Progress().Set(int64(done), t)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		SetItem:        func(label string) { rc.Progress().Note(label) },
		Canceled:       rc.Canceled,
		Touch:          func() { rc.Progress().Tick() },
		SaveCheckpoint: rc.Checkpoint,
		LoadCheckpoint: rc.Restore,
		Writer:         w,
		Store:          deps.Store,
		Each: func(_ context.Context, n int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error {
			return t6g4RunEach(rc, n, o, fn)
		},
	}, nil
}

// ---- HEAD's adapters (rule R10 item 5) ----

// headT6G4Store is the t6g4Store HEAD's shells hand a core: the store HEAD's body
// read (every method of t6g4Store is one of OpsStore's) plus the non-store
// reads the plugin's deps answer.
type headT6G4Store struct {
	OpsStore
	deps ServerDeps
	// ident is the identification-index capability database.AsCapability
	// resolved off the store (ident-index-rebuild only).
	ident identIndexRebuilder
	// aiJobs is the ai_jobs capability resolved off the store (ai-dedup-batch).
	aiJobs database.AIJobsStore
	// prov is the file_prov keyspace resolved off the deps (nil when absent).
	prov database.FileProvenanceStore
}

func (h headT6G4Store) FileProvenanceAvailable() bool { return h.prov != nil }

// errT6G4StoreLacks is the error of an adapter method whose store method is not
// part of OpsStore (HEAD's fs-regroup-xml reaches it by a type assertion, and
// refuses to run when the store lacks it).
func errT6G4StoreLacks(method string) error {
	return fmt.Errorf("store lacks %s; refusing to run", method)
}

func (h headT6G4Store) BookFilesAtPath(path string) ([]database.BookFile, error) {
	r, ok := any(h.OpsStore).(interface {
		BookFilesAtPath(string) ([]database.BookFile, error)
	})
	if !ok {
		return nil, errT6G4StoreLacks("BookFilesAtPath")
	}
	return r.BookFilesAtPath(path)
}

func (h headT6G4Store) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	r, ok := any(h.OpsStore).(interface {
		GetChaptersForBook(string) ([]database.Chapter, error)
	})
	if !ok {
		return nil, errT6G4StoreLacks("GetChaptersForBook")
	}
	return r.GetChaptersForBook(bookID)
}

func (h headT6G4Store) DeferAggregatesAvailable() bool {
	_, ok := database.AsCapability[segmentFailureWriter](h.OpsStore)
	return ok
}

func (h headT6G4Store) FileIntroWritesAvailable() bool {
	_, ok := database.AsCapability[database.BookFileIntroStore](h.OpsStore)
	return ok
}

func (h headT6G4Store) FindFileEventsByHash(hash string) ([]database.FileEvent, error) {
	return h.prov.FindFileEventsByHash(hash)
}

func (h headT6G4Store) GetFileProvExportCursor() (uint64, error) {
	return h.prov.GetFileProvExportCursor()
}

func (h headT6G4Store) MaxFileEventSeq() (uint64, error) { return h.prov.MaxFileEventSeq() }

func (h headT6G4Store) ScanFileEventsBySeq(afterSeq uint64, limit int) ([]database.FileEventSeqRow, error) {
	return h.prov.ScanFileEventsBySeq(afterSeq, limit)
}

func (h headT6G4Store) GetFileHistory(bookFileID string) ([]database.FileEvent, error) {
	return h.prov.GetFileHistory(bookFileID)
}

func (h headT6G4Store) HasOperationQueue() bool { return h.deps.OperationQueueStore() != nil }

func (h headT6G4Store) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return h.deps.OperationQueueStore().ListActiveOperationsV2()
}

func (h headT6G4Store) RootDir() string { return h.deps.RootDir() }

func (h headT6G4Store) HasDedupEngine() bool { return h.deps.HasDedupEngine() }

func (h headT6G4Store) DedupTriagePreview(ctx context.Context) (*TriageReport, error) {
	return h.deps.DedupTriageExactPending(ctx, false)
}

func (h headT6G4Store) HasAIParsing() bool { return h.deps.HasAIParsing() }

func (h headT6G4Store) ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]database.AIJob, error) {
	return h.aiJobs.ListAIJobs(typeFilter, statusFilter, limit, offset)
}

func (h headT6G4Store) VerifyIdentIndex(ctx context.Context) (database.IdentIndexReport, error) {
	return h.ident.VerifyIdentIndex(ctx)
}

// headT6G4Writer is the t6g4Writer HEAD's shells hand a core: each method calls
// exactly what HEAD's body called, in the same order with the same arguments.
type headT6G4Writer struct {
	store OpsStore
	ident identIndexRebuilder
	// aiParser and aiJobs are what HEAD's body built to submit the batch.
	aiParser *ai.OpenAIParser
	aiJobs   database.AIJobsStore
	// deps is the server dependency set whose service dedup-exact-triage calls.
	deps ServerDeps
	// prov is the file_prov keyspace resolved off the deps (nil when absent).
	prov database.FileProvenanceStore
}

func (h headT6G4Writer) MarkOperationChangesReverted(operationID string, changeIDs []string) error {
	r, ok := any(h.store).(interface {
		MarkOperationChangesReverted(string, []string) error
	})
	if !ok {
		return errT6G4StoreLacks("MarkOperationChangesReverted")
	}
	return r.MarkOperationChangesReverted(operationID, changeIDs)
}

func (h headT6G4Writer) CreateBookFile(file *database.BookFile) error {
	return h.store.CreateBookFile(file)
}

func (h headT6G4Writer) MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error {
	return h.store.MoveBookFilesToBookBulk(moves, targetBookID)
}

func (h headT6G4Writer) ReassignExternalID(source, externalID, newBookID string) error {
	return h.store.ReassignExternalID(source, externalID, newBookID)
}

func (h headT6G4Writer) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error) {
	return h.store.UpdateBookFiles(ctx, files, afterRow)
}

func (h headT6G4Writer) ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	w, ok := database.AsCapability[segmentFailureWriter](h.store)
	if !ok {
		return nil, fmt.Errorf("the store cannot defer aggregates")
	}
	return w.ModifyBookFileDeferAggregates(bookID, fileID, fn)
}

func (h headT6G4Writer) intros() (database.BookFileIntroStore, error) {
	intros, ok := database.AsCapability[database.BookFileIntroStore](h.store)
	if !ok {
		return nil, fmt.Errorf("the store does not support per-file intro writes")
	}
	return intros, nil
}

func (h headT6G4Writer) SetBookFileIntro(bookID, fileID string, r database.BookFileIntroResult) (database.BookFileIntroWrite, error) {
	intros, err := h.intros()
	if err != nil {
		return 0, err
	}
	return intros.SetBookFileIntro(bookID, fileID, r)
}

func (h headT6G4Writer) ClearBookFileIntro(bookID, fileID, runOpID string) (*database.BookFile, bool, error) {
	intros, err := h.intros()
	if err != nil {
		return nil, false, err
	}
	return intros.ClearBookFileIntro(bookID, fileID, runOpID)
}

func (h headT6G4Writer) RecordIntroTimeout(bookID, fileID string, s database.IntroTimeoutStrike) (database.IntroTimeoutOutcome, error) {
	intros, err := h.intros()
	if err != nil {
		return database.IntroTimeoutOutcome{}, err
	}
	return intros.RecordIntroTimeout(bookID, fileID, s)
}

func (h headT6G4Writer) ClearIntroTimeouts(bookID, fileID, sourceHash string) (bool, error) {
	intros, err := h.intros()
	if err != nil {
		return false, err
	}
	return intros.ClearIntroTimeouts(bookID, fileID, sourceHash)
}

// PutFileIntroStats is best effort, as HEAD's sink is: a store without the
// capability simply persists no counters.
func (h headT6G4Writer) PutFileIntroStats(st *database.FileIntroStats) error {
	sink, ok := database.AsCapability[database.FileIntroStatsStore](h.store)
	if !ok {
		return nil
	}
	return sink.PutFileIntroStats(st)
}

func (h headT6G4Writer) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return h.store.ModifyBook(id, fn)
}

func (h headT6G4Writer) AppendFileEvent(e database.FileEvent) error { return h.prov.AppendFileEvent(e) }

func (h headT6G4Writer) SetFileProvExportCursor(seq uint64) error {
	return h.prov.SetFileProvExportCursor(seq)
}

func (h headT6G4Writer) CarryOverFingerprintWindows(from []database.FingerprintWindowRef, to database.FingerprintWindowRef) (int, error) {
	return h.store.CarryOverFingerprintWindows(from, to)
}

func (h headT6G4Writer) CreateOperationChange(change *database.OperationChange) error {
	return h.store.CreateOperationChange(change)
}

func (h headT6G4Writer) DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error) {
	return h.store.DeleteBookFilesByIDs(refs)
}

func (h headT6G4Writer) RecomputeBookAggregates(bookID string) error {
	return h.store.RecomputeBookAggregates(bookID)
}

func (h headT6G4Writer) UpdateBookFile(id string, file *database.BookFile) error {
	return h.store.UpdateBookFile(id, file)
}

func (h headT6G4Writer) SetBookFileHash(id, hash string) error {
	return h.store.SetBookFileHash(id, hash)
}

func (h headT6G4Writer) DedupTriageApply(ctx context.Context) (*TriageReport, error) {
	return h.deps.DedupTriageExactPending(ctx, true)
}

func (h headT6G4Writer) SubmitAuthorDedupJob(ctx context.Context, sourceID string, inputs []ai.AuthorDiscoveryInput) (string, error) {
	return h.aiParser.SubmitAuthorDedupJob(ctx, h.aiJobs, sourceID, inputs)
}

func (h headT6G4Writer) RebuildIdentIndex(ctx context.Context) (database.IdentRebuildStats, error) {
	return h.ident.RebuildIdentIndex(ctx)
}

// t6g4RequireLive is rule R7's "Live-only writer": HEAD runs such a definition only
// in an explicit live mode, and so does its native kind. A requeue or resume
// reaches here with the mode the run was started with.
func t6g4RequireLive(rc *tasks.RC) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", rc.Kind())
	}
	return nil
}

// t6g4Kinds returns every native kind of this group, in file order, each with
// Schedule: sched(kind) (rule R9; a nil sched means none). Nothing calls it in
// production: F5-18 passes the list to Registrar.Tasks.
func (p *Plugin) t6g4Kinds(deps t6g4Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	at := func(k tasks.Kind) *tasks.Schedule {
		if sched == nil {
			return nil
		}
		return sched(k)
	}
	return []tasks.Definition{
		p.integrityCheckKind(deps, at(integrityCheckKind)),
		p.identIndexKind(deps, at(identIndexKind)),
		p.aiDedupBatchKind(deps, at(aiDedupBatchKind)),
		p.dedupExactTriageKind(deps, at(dedupExactTriageKind)),
		p.extractWAVClipsKind(deps, at(extractWAVClipsKind)),
		p.fileProvenanceCaptureKind(deps, at(fileProvenanceCaptureKind)),
		p.fileProvenanceExportKind(deps, at(fileProvenanceExportKind)),
		p.filePathCollisionReportKind(deps, at(filePathCollisionReportKind)),
		p.introMigrateSingleFileKind(deps, at(introMigrateSingleFileKind)),
		p.dedupeBookFileRowsKind(deps, at(dedupeBookFileRowsKind)),
		p.fileIntroTranscribeKind(deps, at(fileIntroKind)),
		p.fsRegroupXMLKind(deps, at(fsRegroupKind)),
		p.durationBackfillKind(deps, at(durationBackfillKind)),
	}
}

// t6g4Parallelism is rule R4's Parallelism of each converted in-body loop, by
// kind: the value the kind's native shell passes to tasks.RunItems and the
// parity test compares with HEAD's RunItemsOptions. A kind with no in-body loop
// has no entry.
var t6g4Parallelism = map[tasks.Kind]tasks.Parallelism{}

// t6g4KindsRoot names t6g4Kinds from non-test code so tasklint's unregistered-kind
// rule counts it a registration root until F5-18's Registrar.Tasks call replaces
// this line. F5-18 deletes it.
var _ = (*Plugin).t6g4Kinds
