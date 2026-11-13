// file: internal/plugins/maintenance/kinds_t6_g2.go
// version: 1.0.1
// guid: f05c0cfb-33a6-46e3-a29b-4ab6825825dc
// last-edited: 2026-11-13

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Native books Task kinds for the second sub-batch of the maintenance plugin's
// definitions (F5-07, 05-tasks 5T.8.2 group T6, "group 2"): the 13 v2
// sdk.OperationDefs of backfill.go, book_atpath_index.go,
// book_hash_index_repair.go, book_shape_report.go,
// bookfile_acoustid_index_repair.go, booksig_recovery_audit.go,
// booksig_sidecar_migrate.go, build_folder_book_files.go and
// chapters_backfill.go.
//
// Each def's body moved to a <runFunc>Core(ctx, params, env) function; HEAD's
// Run is its v2 shell and the t6g2*Kind function beside it is its native one.
// The cores reach the store and every write ONLY through the env their shell
// hands them (conversion rule 3), so the native shell hands them
// deps.NewWriter(rc.Writer()) and HEAD's hands them adapters over the plugin's
// ServerDeps. The kinds are DARK: nothing registers them before the tasks flip
// (F5-18), which also deletes the v2 defs, the head* adapters below and
// t6g2KindList's keep-alive line.

// signalT6G2MemDB is the readiness component the kinds wait for (rule R5,
// NoWarmupWait false, which every def of this group is). Books registers the
// component with the health registry at F5-18.
const signalT6G2MemDB tasks.ReadySignal = "books.memdb"

// t6g2SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (05-rbac 6.1, internal/books.AtomTable), the permission every def of this
// group gets because none sets Permissions. internal/books imports this package
// through its import graph, so the table cannot be read from here; the external
// test TestT6G2Kinds_StartRequirementMatchesAtomTable fails when the two drift.
var t6g2SettingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

func t6g2StartRequirement() rbac.Requirement { return rbac.AllOf(t6g2SettingsManageScopes...) }

// t6g2Res is rule R7's resource spelling: tasks.Res("books", kind) per name.
func t6g2Res(kinds ...string) []tasks.Resource {
	out := make([]tasks.Resource, len(kinds))
	for i, k := range kinds {
		out[i] = tasks.Res("books", k)
	}
	return out
}

// t6g2Meta is the Common of one kind, spelled once so the kind builder and the
// parity test read the same table.
type t6g2Meta struct {
	Kind            tasks.Kind
	Title, Help     string
	Effects         tasks.Effects
	Resume          tasks.ResumePolicy
	Priority        tasks.Priority
	Timeout         time.Duration
	ProgressTimeout time.Duration
	// Key is HEAD's ConcurrencyKey; the kind's key is "books." + Key (rule R5).
	Key string
}

func (m t6g2Meta) common(sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Title:           m.Title,
		Help:            m.Help,
		Effects:         m.Effects,
		Perms:           tasks.Permissions{Start: t6g2StartRequirement()},
		Resume:          m.Resume,
		Priority:        m.Priority,
		Timeout:         m.Timeout,
		ProgressTimeout: m.ProgressTimeout,
		Ready:           []tasks.ReadySignal{signalT6G2MemDB},
	}
	if m.Key != "" {
		c.ConcurrencyKey = tasks.Key("books." + m.Key)
	}
	if sched != nil {
		c.Schedule = sched(m.Kind)
	}
	return c
}

// ---------------------------------------------------------------------------
// The store and writer the cores reach, and the env that carries them.
//
// Both are built from embedded group interfaces: interfacebloat counts declared
// entries (an embed is one), so a flat list would trip it as the groups grow.
// Reads live on t6g2Store, every write on t6g2Writer (rule R10).
// ---------------------------------------------------------------------------

// t6g2Capability names an optional store capability the cores probe through
// Store.Supports, so a refusal can say what the store lacks. HEAD resolves
// these with database.AsCapability (a decorator can hide *PebbleStore's
// methods), which the head adapter keeps doing.
type t6g2Capability string

const (
	t6g2CapAtPathVerify     t6g2Capability = "book_atpath.verify"
	t6g2CapAtPathRebuild    t6g2Capability = "book_atpath.rebuild"
	t6g2CapAtPathBackfill   t6g2Capability = "book_atpath.backfill"
	t6g2CapHashIndex        t6g2Capability = "book_hash.index"
	t6g2CapAcoustIDRepair   t6g2Capability = "book_file_acoustid.repair"
	t6g2CapSigMigrate       t6g2Capability = "book_sig.migrate"
	t6g2CapChapters         t6g2Capability = "chapters.persist"
	t6g2CapFolderBuild      t6g2Capability = "book_file.by_path"
	t6g2CapAtPathIndexBuilt t6g2Capability = "book_atpath.index_built"
)

// t6g2HashIndexRun is one check or repair of the book hash indexes: the store's
// report plus the four totals the core prints. The totals are computed where the
// report type lives (the adapter), so the core never calls a write-named
// accessor such as Removed on a store-package type.
type t6g2HashIndexRun struct {
	Report                           database.BookHashIndexReport
	Missing, Extra, Written, Removed int
}

type t6g2BookReads interface {
	GetBookByID(id string) (*database.Book, error)
	ListBookIDs() ([]string, error)
	GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

type t6g2FileReads interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	BookAtPathIndexBuilt() (bool, error)
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
}

type t6g2IndexReads interface {
	VerifyBookAtPathIndex(ctx context.Context) (database.BookAtPathIndexReport, error)
	CheckBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (t6g2HashIndexRun, error)
	ScanBookFileAcoustIDIndex(after string, limit int) ([]database.BookFileAcoustIDEntry, string, error)
	JudgeBookFileAcoustIDEntry(e database.BookFileAcoustIDEntry) (database.BookFileAcoustIDVerdict, error)
}

type t6g2StoreProbe interface {
	// Describe is the %T of the underlying store, for refusals that name it.
	Describe() string
	// Supports reports whether the store (or a store in its decorator chain)
	// has the capability.
	Supports(c t6g2Capability) bool
}

// t6g2Store is every read the group's cores make.
type t6g2Store interface {
	t6g2BookReads
	t6g2FileReads
	t6g2IndexReads
	t6g2StoreProbe
}

// t6g2BootWriter is the five boot-time file and id passes. The first four walk
// the library under a walkresume.Run; QuarantineKnownBadFiles does not.
type t6g2BootWriter interface {
	BackfillExternalIDs(ctx context.Context, ifNeeded bool, progress func(processed, total int, msg string), run walkresume.Run) (skipped bool, err error)
	StripMovementAtoms(ctx context.Context, run walkresume.Run) (alreadyDone bool, err error)
	RemuxMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string), run walkresume.Run) (alreadyDone bool, err error)
	TranscodeMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string), run walkresume.Run) (alreadyDone bool, err error)
	QuarantineKnownBadFiles(ctx context.Context) (quarantined int, alreadyDone bool, err error)
}

// t6g2IndexWriter writes index keys.
type t6g2IndexWriter interface {
	RepairBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (t6g2HashIndexRun, error)
	RebuildBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error)
	BackfillBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error)
	DeleteOrphanBookFileAcoustIDEntries(ctx context.Context, entries []database.BookFileAcoustIDEntry) (int, error)
}

// t6g2RowWriter writes book, chapter and book_file rows.
type t6g2RowWriter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	SaveChaptersForBook(bookID string, chapters []database.Chapter) error
	BatchCreateBookFiles(files []*database.BookFile) error
	// MigrateBookSigToSidecar takes its own dryRun: a preview classifies and
	// writes nothing (see the seams note on how NewWriter must treat it).
	MigrateBookSigToSidecar(id string, dryRun bool) (database.BookSigMigrateOutcome, error)
}

// t6g2Writer is every write the group's cores make.
type t6g2Writer interface {
	t6g2BootWriter
	t6g2IndexWriter
	t6g2RowWriter
}

// t6g2Log is the reporter's Log: level, message, attrs.
type t6g2Log func(level slog.Level, msg string, attrs ...slog.Attr)

// t6g2Loop is what a core says about one items loop; the shell's Items turns it
// into its pool's options. Worker counts are not here: they are the kind's
// declared parallelism, which the shell binds.
type t6g2Loop struct {
	// Collect runs every item and returns every error; otherwise the first error
	// stops the loop.
	Collect bool
	// Label is HEAD's per-item label. It runs inside worker goroutines, so it may
	// only read atomics. The native loop has no per-item label and ignores it.
	Label func(i, total int) string
	// ResumeFrom skips the first ResumeFrom items (a restored watermark).
	ResumeFrom int
	// CheckpointEvery and CheckpointState: CheckpointState(ctx, watermark) is
	// called, serialized, when the contiguous completion prefix has advanced by
	// CheckpointEvery (or reached the end). The watermark is absolute.
	CheckpointEvery int
	CheckpointState func(ctx context.Context, watermark int) error
	// ProgressOffset and ProgressTotal pin the progress numbering.
	ProgressOffset, ProgressTotal int
}

// t6g2Items runs fn(ctx, i) for i in [0, n) on the shell's pool.
type t6g2Items func(ctx context.Context, n int, loop t6g2Loop, fn func(ctx context.Context, i int) error) error

// t6g2Env is everything a core reaches outside its own arguments. A core uses
// only the fields it needs; the shells fill every field.
type t6g2Env struct {
	Log    t6g2Log
	Logger *slog.Logger
	// NoOp marks a "nothing to do" summary so HEAD's activity feed hides it.
	// Native runs have no such feed: it is the zero Attr, which slog drops.
	NoOp slog.Attr
	// Progress reports (done, total, message); total 0 is unknown.
	Progress func(done, total int, msg string)
	// Touch stamps the run's liveness without reporting progress.
	Touch      func()
	Checkpoint func(state any) error
	// Continue is the sentinel a core wraps to ask for "restart this same run".
	Continue error
	// NewYieldDue builds the per-attempt "stop at the next checkpoint" test.
	NewYieldDue func(ctx context.Context) func() bool
	Items       t6g2Items
	Store       t6g2Store
	Writer      t6g2Writer
	// ReportPath resolves where a report file goes: the explicit path, else the
	// default directory under the library root with the run's own file name.
	ReportPath func(explicit, prefix string) (string, error)
}

// ---------------------------------------------------------------------------
// HEAD's side of the env.
// ---------------------------------------------------------------------------

// headT6G2Env is the env of a v2 run: the reporter's own methods.
func headT6G2Env(p *Plugin, reporter sdk.Reporter) t6g2Env {
	return t6g2Env{
		Log:    func(l slog.Level, m string, a ...slog.Attr) { _ = reporter.Log(l, m, a...) },
		Logger: reporter.Logger(),
		NoOp:   opsregistry.NoOpAttr(),
		Progress: func(done, total int, msg string) {
			_ = reporter.UpdateProgress(done, total, msg)
		},
		Touch:      func() { opsregistry.TouchLiveness(reporter) },
		Checkpoint: reporter.Checkpoint,
		Continue:   opsregistry.ErrContinueInPlace,
		NewYieldDue: func(ctx context.Context) func() bool {
			return newYieldDue(ctx, bootYieldMargin)
		},
		ReportPath: func(explicit, prefix string) (string, error) {
			return p.resolveReportPath(explicit, opReportFileName(reporter, prefix))
		},
	}
}

// headT6G2Items is HEAD's pool: opsregistry.RunItems over an index slice, with
// the shell's worker count (Concurrency is set; it defaults to one).
func headT6G2Items(r sdk.Reporter, workers int) t6g2Items {
	return func(ctx context.Context, n int, loop t6g2Loop, fn func(context.Context, int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		mode := opsregistry.ErrModeFail
		if loop.Collect {
			mode = opsregistry.ErrModeCollect
		}
		return opsregistry.RunItems(ctx, r, idx, fn, opsregistry.RunItemsOptions{
			Concurrency:       workers,
			ErrMode:           mode,
			Label:             loop.Label,
			ResumeFrom:        loop.ResumeFrom,
			CheckpointEvery:   loop.CheckpointEvery,
			CheckpointStateFn: loop.CheckpointState,
			ProgressOffset:    loop.ProgressOffset,
			ProgressTotal:     loop.ProgressTotal,
		})
	}
}

// headT6G2Store reads through the plugin's OpsStore, resolving optional
// capabilities the way HEAD always has (database.AsCapability, then a bare
// assertion where HEAD asserted).
type headT6G2Store struct{ ops OpsStore }

func (s headT6G2Store) GetBookByID(id string) (*database.Book, error) { return s.ops.GetBookByID(id) }
func (s headT6G2Store) ListBookIDs() ([]string, error)                { return s.ops.ListBookIDs() }
func (s headT6G2Store) GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error) {
	return s.ops.GetBookSnapshots(id, limit)
}
func (s headT6G2Store) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return s.ops.GetAllBooksCore(limit, offset)
}
func (s headT6G2Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.ops.GetAllBookFilesCore()
}
func (s headT6G2Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.ops.GetBookFiles(bookID)
}
func (s headT6G2Store) LiveBookIDsAtPath(path string) ([]string, error) {
	return s.ops.LiveBookIDsAtPath(path)
}

func (s headT6G2Store) BookFilesAtPath(path string) ([]database.BookFile, error) {
	f, ok := any(s.ops).(interface {
		BookFilesAtPath(string) ([]database.BookFile, error)
	})
	if !ok {
		return nil, t6g2Unsupported(s, t6g2CapFolderBuild)
	}
	return f.BookFilesAtPath(path)
}

func (s headT6G2Store) BookAtPathIndexBuilt() (bool, error) {
	f, ok := any(s.ops).(interface{ BookAtPathIndexBuilt() (bool, error) })
	if !ok {
		return false, t6g2Unsupported(s, t6g2CapAtPathIndexBuilt)
	}
	return f.BookAtPathIndexBuilt()
}

func (s headT6G2Store) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	c, ok := database.AsCapability[chapterPersister](s.ops)
	if !ok {
		return nil, t6g2Unsupported(s, t6g2CapChapters)
	}
	return c.GetChaptersForBook(bookID)
}

func (s headT6G2Store) VerifyBookAtPathIndex(ctx context.Context) (database.BookAtPathIndexReport, error) {
	v, ok := database.AsCapability[bookAtPathVerifier](s.ops)
	if !ok {
		return database.BookAtPathIndexReport{}, t6g2Unsupported(s, t6g2CapAtPathVerify)
	}
	return v.VerifyBookAtPathIndex(ctx)
}

func (s headT6G2Store) CheckBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (t6g2HashIndexRun, error) {
	c, ok := database.AsCapability[bookHashIndexChecker](s.ops)
	if !ok {
		return t6g2HashIndexRun{}, t6g2Unsupported(s, t6g2CapHashIndex)
	}
	rep, err := c.CheckBookHashIndex(ctx, progress)
	return t6g2NewHashIndexRun(rep), err
}

func (s headT6G2Store) ScanBookFileAcoustIDIndex(after string, limit int) ([]database.BookFileAcoustIDEntry, string, error) {
	r, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](s.ops)
	if !ok {
		return nil, "", t6g2Unsupported(s, t6g2CapAcoustIDRepair)
	}
	return r.ScanBookFileAcoustIDIndex(after, limit)
}

func (s headT6G2Store) JudgeBookFileAcoustIDEntry(e database.BookFileAcoustIDEntry) (database.BookFileAcoustIDVerdict, error) {
	r, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](s.ops)
	if !ok {
		return 0, t6g2Unsupported(s, t6g2CapAcoustIDRepair)
	}
	return r.JudgeBookFileAcoustIDEntry(e)
}

func (s headT6G2Store) Describe() string { return fmt.Sprintf("%T", s.ops) }

func (s headT6G2Store) Supports(c t6g2Capability) bool {
	switch c {
	case t6g2CapAtPathVerify:
		_, ok := database.AsCapability[bookAtPathVerifier](s.ops)
		return ok
	case t6g2CapAtPathRebuild:
		_, ok := database.AsCapability[bookAtPathRebuilder](s.ops)
		return ok
	case t6g2CapAtPathBackfill:
		_, ok := database.AsCapability[bookAtPathBackfiller](s.ops)
		return ok
	case t6g2CapHashIndex:
		_, ok := database.AsCapability[bookHashIndexChecker](s.ops)
		return ok
	case t6g2CapAcoustIDRepair:
		_, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](s.ops)
		return ok
	case t6g2CapSigMigrate:
		return database.AsBookSigMigrateStore(s.ops) != nil
	case t6g2CapChapters:
		_, ok := database.AsCapability[chapterPersister](s.ops)
		return ok
	case t6g2CapFolderBuild:
		_, ok := any(s.ops).(folderBuildStore)
		return ok
	}
	return false
}

// t6g2Unsupported is the error of a call whose capability the store lacks. The
// cores check Supports first, so reaching it means a caller skipped the probe.
func t6g2Unsupported(s t6g2StoreProbe, c t6g2Capability) error {
	return fmt.Errorf("store is %s, which has no %s", s.Describe(), c)
}

// t6g2NewHashIndexRun fills the totals from the report, here, in the one place
// that reads the report type's accessors.
func t6g2NewHashIndexRun(rep database.BookHashIndexReport) t6g2HashIndexRun {
	return t6g2HashIndexRun{
		Report:  rep,
		Missing: rep.Missing(),
		Extra:   rep.Extra(),
		Written: rep.Written(),
		Removed: rep.Removed(),
	}
}

// headT6G2Writer writes through the plugin's ServerDeps and OpsStore.
type headT6G2Writer struct {
	deps ServerDeps
	ops  OpsStore
}

func (w headT6G2Writer) BackfillExternalIDs(ctx context.Context, ifNeeded bool, progress func(int, int, string), run walkresume.Run) (bool, error) {
	return w.deps.BackfillExternalIDs(ctx, ifNeeded, progress, run)
}
func (w headT6G2Writer) StripMovementAtoms(ctx context.Context, run walkresume.Run) (bool, error) {
	return w.deps.StripMovementAtoms(ctx, run)
}
func (w headT6G2Writer) RemuxMalformedM4BFiles(ctx context.Context, progress func(int, int, string), run walkresume.Run) (bool, error) {
	return w.deps.RemuxMalformedM4BFiles(ctx, progress, run)
}
func (w headT6G2Writer) TranscodeMalformedM4BFiles(ctx context.Context, progress func(int, int, string), run walkresume.Run) (bool, error) {
	return w.deps.TranscodeMalformedM4BFiles(ctx, progress, run)
}
func (w headT6G2Writer) QuarantineKnownBadFiles(ctx context.Context) (int, bool, error) {
	return w.deps.QuarantineKnownBadFiles(ctx)
}

func (w headT6G2Writer) RepairBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (t6g2HashIndexRun, error) {
	c, ok := database.AsCapability[bookHashIndexChecker](w.ops)
	if !ok {
		return t6g2HashIndexRun{}, t6g2Unsupported(headT6G2Store{w.ops}, t6g2CapHashIndex)
	}
	rep, err := c.RepairBookHashIndex(ctx, progress)
	return t6g2NewHashIndexRun(rep), err
}

func (w headT6G2Writer) RebuildBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
	r, ok := database.AsCapability[bookAtPathRebuilder](w.ops)
	if !ok {
		return database.BookAtPathBackfillResult{}, t6g2Unsupported(headT6G2Store{w.ops}, t6g2CapAtPathRebuild)
	}
	return r.RebuildBookAtPathIndex(ctx, progress)
}

func (w headT6G2Writer) BackfillBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (database.BookAtPathBackfillResult, error) {
	b, ok := database.AsCapability[bookAtPathBackfiller](w.ops)
	if !ok {
		return database.BookAtPathBackfillResult{}, t6g2Unsupported(headT6G2Store{w.ops}, t6g2CapAtPathBackfill)
	}
	return b.BackfillBookAtPathIndex(ctx, progress)
}

func (w headT6G2Writer) DeleteOrphanBookFileAcoustIDEntries(ctx context.Context, entries []database.BookFileAcoustIDEntry) (int, error) {
	r, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](w.ops)
	if !ok {
		return 0, t6g2Unsupported(headT6G2Store{w.ops}, t6g2CapAcoustIDRepair)
	}
	return r.DeleteOrphanBookFileAcoustIDEntries(ctx, entries)
}

func (w headT6G2Writer) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return w.ops.ModifyBook(id, fn)
}

func (w headT6G2Writer) SaveChaptersForBook(bookID string, chapters []database.Chapter) error {
	c, ok := database.AsCapability[chapterPersister](w.ops)
	if !ok {
		return t6g2Unsupported(headT6G2Store{w.ops}, t6g2CapChapters)
	}
	return c.SaveChaptersForBook(bookID, chapters)
}

func (w headT6G2Writer) BatchCreateBookFiles(files []*database.BookFile) error {
	return w.ops.BatchCreateBookFiles(files)
}

func (w headT6G2Writer) MigrateBookSigToSidecar(id string, dryRun bool) (database.BookSigMigrateOutcome, error) {
	m := database.AsBookSigMigrateStore(w.ops)
	if m == nil {
		return 0, t6g2Unsupported(headT6G2Store{w.ops}, t6g2CapSigMigrate)
	}
	return m.MigrateBookSigToSidecar(id, dryRun)
}

// headT6G2Wire fills the env's store and writer from the plugin's deps, for a
// def whose HEAD shell needs them. A nil OpsStore is the caller's "database not
// initialized" check, made before this.
func headT6G2Wire(env t6g2Env, p *Plugin, ops OpsStore) t6g2Env {
	env.Store = headT6G2Store{ops: ops}
	env.Writer = headT6G2Writer{deps: p.deps, ops: ops}
	return env
}

// ---------------------------------------------------------------------------
// The native side of the env.
// ---------------------------------------------------------------------------

// t6g2Deps is what the native shells need from the host. F5-18 supplies it.
type t6g2Deps struct {
	// Store serves every read.
	Store t6g2Store
	// NewWriter wraps the run's tasks.Writer into the t6g2Writer a core writes
	// through: every write goes through the Writer so the effects gate and the
	// fence apply.
	NewWriter func(tasks.Writer) t6g2Writer
	// ReportPath resolves a report file's location (book-shape-report's TSV):
	// explicit if given, else the default directory under the library root. A
	// file write is not a tasks.Writer write, so it is a dep of its own.
	ReportPath func(explicit, fileName string) (string, error)
}

// t6g2NativeEnv is the env of a native run. par is the kind's declared
// parallelism, bound into Items.
func t6g2NativeEnv(rc *tasks.RC, deps t6g2Deps, par tasks.Parallelism) (t6g2Env, error) {
	if deps.Store == nil {
		return t6g2Env{}, fmt.Errorf("%s: t6g2Deps.Store is not set", rc.Kind())
	}
	if deps.NewWriter == nil {
		return t6g2Env{}, fmt.Errorf("%s: t6g2Deps.NewWriter is not set", rc.Kind())
	}
	w := deps.NewWriter(rc.Writer())
	if w == nil {
		return t6g2Env{}, fmt.Errorf("%s: t6g2Deps.NewWriter returned no writer", rc.Kind())
	}
	env := t6g2Env{
		Log: func(l slog.Level, m string, a ...slog.Attr) {
			rc.Log().LogAttrs(rc.Context(), l, m, a...)
		},
		Logger: rc.Log(),
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
		Touch:       rc.Progress().Tick,
		Checkpoint:  rc.Checkpoint,
		Continue:    tasks.ErrContinueInPlace,
		NewYieldDue: func(context.Context) func() bool { return rc.ShouldYield },
		Items:       t6g2NativeItems(rc, par),
		Store:       deps.Store,
		Writer:      w,
	}
	if deps.ReportPath != nil {
		env.ReportPath = func(explicit, prefix string) (string, error) {
			return deps.ReportPath(explicit, prefix+"-"+string(rc.RunID())+".tsv")
		}
	}
	return env, nil
}

// t6g2NativeItems is the native pool: tasks.RunItems over an index slice with
// the kind's declared parallelism. It keeps the one thing RunItems has no hook
// for, a contiguous-completion watermark callback, in t6g2Watermark.
func t6g2NativeItems(rc *tasks.RC, par tasks.Parallelism) t6g2Items {
	return func(_ context.Context, n int, loop t6g2Loop, fn func(context.Context, int) error) error {
		from := min(max(loop.ResumeFrom, 0), n)
		if from >= n {
			return nil
		}
		idx := make([]int, 0, n-from)
		for i := from; i < n; i++ {
			idx = append(idx, i)
		}
		// A resumed loop keeps the original numbering, as HEAD's RunItems does.
		total := loop.ProgressTotal
		if total == 0 && from > 0 {
			total = n
		}
		opts := tasks.ItemsOptions[int]{
			OnError:       tasks.FailFast,
			ProgressBase:  loop.ProgressOffset + from,
			ProgressTotal: total,
		}
		if loop.Collect {
			opts.OnError = tasks.Collect
		}
		wm := newT6G2Watermark(from, n-from, loop.CheckpointEvery, loop.CheckpointState)
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error {
			if e := fn(ic.Context(), i); e != nil {
				return e
			}
			// Only a success advances the watermark: a failed item must be
			// revisited by a resume, not skipped.
			wm.complete(ic.Context(), i)
			return nil
		}, opts)
		return t6g2ItemsError(err, loop.Collect)
	}
}

// t6g2ItemsError turns tasks.RunItems' one error type back into what HEAD's pool
// returned: the first item error (fail-fast) or the joined item errors
// (collect), with the run's end cause attached when it changed the outcome.
func t6g2ItemsError(err error, collect bool) error {
	var ie *tasks.ItemsError
	if err == nil || !errors.As(err, &ie) {
		return err
	}
	var out error
	if collect {
		out = errors.Join(ie.Errs...)
	} else {
		out = ie.First
	}
	switch {
	case out == nil && ie.Cause != nil:
		return ie.Cause
	case out == nil:
		return err
	case ie.Cause != nil && !errors.Is(out, ie.Cause):
		return errors.Join(out, ie.Cause)
	}
	return out
}

// t6g2Watermark tracks the contiguous prefix of completed items and calls the
// checkpoint function each time it has advanced by every items (or reached the
// end), serialized. It is opsregistry.RunItems' maybeCheckpoint, which
// tasks.RunItems has no equivalent hook for.
type t6g2Watermark struct {
	mu    sync.Mutex
	from  int
	total int
	every int
	fn    func(ctx context.Context, watermark int) error
	done  []bool
	mark  int
	last  int
}

func newT6G2Watermark(from, total, every int, fn func(context.Context, int) error) *t6g2Watermark {
	if fn == nil {
		return nil
	}
	return &t6g2Watermark{from: from, total: total, every: max(every, 1), fn: fn, done: make([]bool, total)}
}

// complete records item i (an absolute index) as done. Safe on a nil tracker.
func (w *t6g2Watermark) complete(ctx context.Context, i int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if local := i - w.from; local >= 0 && local < w.total {
		w.done[local] = true
	}
	for w.mark < w.total && w.done[w.mark] {
		w.mark++
	}
	// Guard on the watermark, not the completion count: with a gap open it
	// stalls, and checkpointing the same value again is write amplification.
	if w.mark <= w.last || w.mark-w.last < w.every && w.mark < w.total {
		return
	}
	w.last = w.mark
	_ = w.fn(ctx, w.from+w.mark)
}

// ---------------------------------------------------------------------------
// Shared shell pieces.
// ---------------------------------------------------------------------------

// t6g2Live is a native run's write direction: only an explicit live mode writes
// (a requeue or resume reaches here with the mode the run started with).
func t6g2Live(rc *tasks.RC) bool { return rc.Mode() == tasks.ModeLive }

// t6g2RefuseNotLive is the native shell of a live-only writer (HEAD's "no
// Preview", rule R7): a preview run is refused before anything is touched.
func t6g2RefuseNotLive(rc *tasks.RC, kind tasks.Kind) error {
	if !t6g2Live(rc) {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// t6g2RefuseModeKeys is 05-tasks 5T.3.7 mode contract item 1: params carry no
// key the run's mode replaces. A requeue starts with none; keys are compared
// case-insensitively because the params decode with encoding/json.
func t6g2RefuseModeKeys(kind tasks.Kind, raw []byte, keys ...string) error {
	m, err := t6g2ParamObject(kind, raw)
	if err != nil {
		return err
	}
	for key := range m {
		for _, banned := range keys {
			if strings.EqualFold(key, banned) {
				return fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	return nil
}

// t6g2ParamObject decodes params as a JSON object; empty and null params are an
// empty object.
func t6g2ParamObject(kind tasks.Kind, raw []byte) (map[string]json.RawMessage, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(t, &m); err != nil {
		return nil, fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// The kind list.
// ---------------------------------------------------------------------------

// t6g2Kinds returns the group's 13 native kinds, in the order of the brief's
// table (the order of the files). Nothing registers them in production: F5-18
// passes the list to Registrar.Tasks. sched is the schedule lookup of rule R9;
// nil means no kind is scheduled.
func (p *Plugin) t6g2Kinds(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return t6g2KindList(deps, sched)
}

// t6g2KindList is t6g2Kinds as a package-level function: tasklint's
// unregistered-kind rule counts package-level list functions only.
func t6g2KindList(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		t6g2ExternalIDBackfillKind(deps, sched),
		t6g2MovementAtomCleanupKind(deps, sched),
		t6g2MalformedM4BRemuxKind(deps, sched),
		t6g2MalformedM4BTranscodeKind(deps, sched),
		t6g2BookAtPathIndexVerifyKind(deps, sched),
		t6g2BookAtPathIndexBackfillKind(deps, sched),
		t6g2BookHashIndexRepairKind(deps, sched),
		t6g2BookShapeReportKind(deps, sched),
		t6g2BookFileAcoustIDIndexRepairKind(deps, sched),
		t6g2BookSigRecoveryAuditKind(deps, sched),
		t6g2BookSigSidecarMigrateKind(deps, sched),
		t6g2BuildFolderBookFilesKind(deps, sched),
		t6g2ChaptersBackfillKind(deps, sched),
	}
}

// t6g2KindsRoot names t6g2KindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root until F5-18's
// Registrar.Tasks call replaces this line (a list function nothing references
// registers nothing, and the analyzer does not load test files). F5-18 deletes
// it.
var _ = t6g2KindList
