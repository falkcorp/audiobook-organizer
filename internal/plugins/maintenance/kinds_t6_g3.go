// file: internal/plugins/maintenance/kinds_t6_g3.go
// version: 1.0.1
// guid: 29eb0771-6105-406d-b32a-2a11e3535809
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
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	pathutil "github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Native books Task kinds for the maintenance plugin's file group 3 (F5-08,
// 05-tasks 5T.8.2 group T6, sub-batch 3 of 7). Each definition's body is a
// shared core in its own file; HEAD's v2 Run is the core's v2 shell and the
// kind built here is its native one. The kinds are DARK: nothing registers
// them before the tasks flip (F5-18).
//
// Rule R10: a core reaches its writer and its stores only through the env its
// shell hands it (Writer and Store below). The HEAD shell hands adapters over
// p.deps (headT6g3Writer, headT6g3Store); the native shell hands what the
// owner declared in t6g3Deps, with the writer built over rc.Writer().

// --- writer: the write methods the group's cores call ---

// t6g3PurgeWriter is the retention and reclamation writes.
type t6g3PurgeWriter interface {
	RunAutoPurgeSoftDeleted(ctx context.Context, opID string) error
	ActivityFlushOp(opID string)
	ResolveTombstoneChains() (int, error)
	CleanupTrashedVersions() (versions.CleanupResult, error)
}

// t6g3FileWriter is the writes that remove files.
type t6g3FileWriter interface {
	CleanupOrphanedTempFiles(rootDir string, opID string) int
	CleanupStaleTempFiles(root string, app pathutil.AppDirs, maxAge time.Duration, dryRun bool) int
	RemoveOldBackupFiles(ctx context.Context, root string, app pathutil.AppDirs,
		match func(name string) (time.Time, bool), maxAge time.Duration,
		log func(level slog.Level, msg string, kv ...any)) (int, error)
}

// t6g3ActivityWriter is the activity-log writes.
type t6g3ActivityWriter interface {
	CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error)
	MaintainActivityLog(ctx context.Context, changeDays, debugDays int) (summarized int, pruned int, indexOrphansRemoved int64, err error)
}

// t6g3PreferenceWriter is the user-preference write.
type t6g3PreferenceWriter interface {
	SetUserPreferenceForUser(userID, key, value string) error
}

// t6g3CoverWriter is the cover ops' writes: a book row, and a raw-key record
// (the cover-text store's records and per-book index).
type t6g3CoverWriter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	SetRaw(key string, value []byte) error
}

// t6g3CreditsWriter is the credits backfill's book and narrator writes.
type t6g3CreditsWriter interface {
	ModifyBookCredits(bookID string, fn func(book *database.Book, credits *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error)
	CreateNarrator(name string) (*database.Narrator, error)
	DeleteNarrator(id int) error
	CreateOperationChange(change *database.OperationChange) error
	InvalidateAuthorsCache()
}

// t6g3OpRowWriter is the operation-row writes: the class id lists beside a
// result, and the judged markers on other operations.
type t6g3OpRowWriter interface {
	ReplaceOpRowsPrefix(opID, rowPrefix string, n int, at func(i int) (database.OpRow, error)) error
	SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error)
	ClearClassLists(opID string) error
}

// t6g3Writer holds exactly the write methods this group's cores call. Methods
// are only added, never re-signed (rule R10 item 2).
type t6g3Writer interface {
	t6g3PurgeWriter
	t6g3FileWriter
	t6g3ActivityWriter
	t6g3PreferenceWriter
	t6g3CoverWriter
	t6g3CreditsWriter
	t6g3OpRowWriter
}

// --- store: the read methods the group's cores call ---

// t6g3ConfigReader is the configuration reads.
type t6g3ConfigReader interface {
	RootDir() string
	BackupRetentionDays() int
	ActivityLogNightlyCompactionEnabled() bool
	ActivityLogCompactionDays() int
	ActivityLogRetentionChangeDays() int
	ActivityLogRetentionDebugDays() int
}

// t6g3ImportPathReader is the import-path read.
type t6g3ImportPathReader interface {
	GetAllImportPaths() ([]database.ImportPath, error)
}

// t6g3PreferenceReader is the user-preference read.
type t6g3PreferenceReader interface {
	GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error)
}

// t6g3RawReader is the raw-key read (the combine journal's).
type t6g3RawReader interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

// t6g3CoverReader is the cover ops' reads: the books they examine, a book's
// files and metadata locks (foldercover's reads), and the operation queue the
// live folder-cover run checks for a scan.
type t6g3CoverReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit int, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	database.MetadataFieldStateReader
	OpQueueReader
}

// t6g3Capability names a read a store may lack: the core asks Can before it
// relies on one, and the optional read answers errT6g3NoCapability without it.
type t6g3Capability int

const (
	// t6g3CapNameIndex is GetNarratorByName (a store without it is answered by
	// the run's frozen index).
	t6g3CapNameIndex t6g3Capability = iota + 1
	// t6g3CapOpsLister is ListOperationsV2Since.
	t6g3CapOpsLister
	// t6g3CapOpRows is GetOpRows and ScanOpRowIndex (and ReplaceOpRowsPrefix on
	// the writer): the judged markers.
	t6g3CapOpRows
	// t6g3CapClassLists is the row store the class id lists are kept in.
	t6g3CapClassLists
)

// t6g3CreditsReader is the credits backfill's reads beyond the census's.
type t6g3CreditsReader interface {
	repairs.GuardReader
	GetAllSeries() ([]database.Series, error)
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// t6g3OptionalReader is the reads a store may lack (database.NarratorRefStore's
// three methods are the narrator link reads, database.OpRowStore's reads are the
// marker reads). Each answers errT6g3NoCapability when the store lacks it; Can
// says so without a call.
type t6g3OptionalReader interface {
	Can(c t6g3Capability) bool
	GetNarratorByName(name string) (*database.Narrator, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
	GetOpRows(opID string, rowIDs []string) ([][]byte, error)
	ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) error
	database.NarratorRefStore
}

// t6g3Store holds exactly the read methods this group's cores call; none of
// them writes (rule R10 item 3).
type t6g3Store interface {
	creditCensusStore
	t6g3ConfigReader
	t6g3ImportPathReader
	t6g3PreferenceReader
	t6g3RawReader
	t6g3CoverReader
	t6g3CreditsReader
	t6g3OptionalReader
}

// t6g3Deps is what the owner (books, from F5-18) declares for the group's
// kinds (rule R10 item 4).
type t6g3Deps struct {
	Store t6g3Store
	// GuardTags is the credits backfill's franchise-tag reader (nil: the tag
	// check is skipped, as repairs.GuardOwnerManualBooks documents).
	GuardTags repairs.GuardTagReader
	// NewWriter returns the writer for one run, its write methods going through
	// w (F5-18: internal/books/writer over rc.Writer()). w is nil for a
	// ReadOnly kind, whose core writes nothing.
	NewWriter func(w tasks.Writer) t6g3Writer
}

// --- the HEAD adapters (rule R10 item 5) ---

// headT6g3Writer is t6g3Writer over HEAD's dependencies: each method calls what
// HEAD's body called, in the same order with the same arguments. Deps are read
// lazily so a test double with no store behind it is only touched by the
// method a test reaches.
type headT6g3Writer struct{ p *Plugin }

func (w headT6g3Writer) RunAutoPurgeSoftDeleted(ctx context.Context, opID string) error {
	return w.p.deps.RunAutoPurgeSoftDeleted(ctx, opID)
}

func (w headT6g3Writer) ActivityFlushOp(opID string) { w.p.deps.ActivityFlushOp(opID) }

func (w headT6g3Writer) ResolveTombstoneChains() (int, error) {
	store := w.p.deps.OpsStore()
	if store == nil {
		return 0, errT6g3NoStore
	}
	return store.ResolveTombstoneChains()
}

func (w headT6g3Writer) CleanupTrashedVersions() (versions.CleanupResult, error) {
	return w.p.deps.CleanupTrashedVersions()
}

func (w headT6g3Writer) CleanupOrphanedTempFiles(rootDir string, opID string) int {
	return w.p.deps.CleanupOrphanedTempFiles(rootDir, opID)
}

func (w headT6g3Writer) CleanupStaleTempFiles(root string, app pathutil.AppDirs, maxAge time.Duration, dryRun bool) int {
	return transcode.CleanupStaleTempFiles(root, app, maxAge, dryRun)
}

func (w headT6g3Writer) RemoveOldBackupFiles(ctx context.Context, root string, app pathutil.AppDirs,
	match func(name string) (time.Time, bool), maxAge time.Duration,
	log func(level slog.Level, msg string, kv ...any)) (int, error) {
	return sweep.RemoveOldBackupFiles(ctx, root, app, match, maxAge, log)
}

func (w headT6g3Writer) CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error) {
	return w.p.deps.CompactActivityEntries(ctx, cutoff, progress)
}

func (w headT6g3Writer) MaintainActivityLog(ctx context.Context, changeDays, debugDays int) (int, int, int64, error) {
	return w.p.deps.MaintainActivityLog(ctx, changeDays, debugDays)
}

func (w headT6g3Writer) SetUserPreferenceForUser(userID, key, value string) error {
	store := w.p.deps.OpsStore()
	if store == nil {
		return errT6g3NoStore
	}
	return store.SetUserPreferenceForUser(userID, key, value)
}

func (w headT6g3Writer) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	store := w.p.deps.OpsStore()
	if store == nil {
		return nil, errT6g3NoStore
	}
	return store.ModifyBook(id, fn)
}

func (w headT6g3Writer) SetRaw(key string, value []byte) error {
	store := w.p.deps.OpsStore()
	if store == nil {
		return errT6g3NoStore
	}
	return store.SetRaw(key, value)
}

func (w headT6g3Writer) ModifyBookCredits(bookID string, fn func(book *database.Book, credits *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	store := w.p.deps.OpsStore()
	if store == nil {
		return nil, database.BookCreditsEdit{}, errT6g3NoStore
	}
	m, ok := database.AsCapability[database.BookCreditsModifier](store)
	if !ok {
		return nil, database.BookCreditsEdit{}, fmt.Errorf("%w: store is %T, which cannot write a book's credits and row together (ModifyBookCredits)", errT6g3NoCapability, store)
	}
	return m.ModifyBookCredits(bookID, fn)
}

func (w headT6g3Writer) CreateNarrator(name string) (*database.Narrator, error) {
	store := w.p.deps.OpsStore()
	if store == nil {
		return nil, errT6g3NoStore
	}
	return store.CreateNarrator(name)
}

func (w headT6g3Writer) DeleteNarrator(id int) error {
	store := w.p.deps.OpsStore()
	if store == nil {
		return errT6g3NoStore
	}
	return store.DeleteNarrator(id)
}

func (w headT6g3Writer) CreateOperationChange(change *database.OperationChange) error {
	store := w.p.deps.OpsStore()
	if store == nil {
		return errT6g3NoStore
	}
	return store.CreateOperationChange(change)
}

// CreateOperationChange forwards the row to the writer. It sits here, with the
// other forwarder of the journal method, so the journal-writer guard
// (internal/arch) allowlists this file alone: credits_backfill.go calls
// database.RecordOperationChange, which checks the row's type before it reaches
// this method.
func (v cbStoreView) CreateOperationChange(change *database.OperationChange) error {
	return v.w.CreateOperationChange(change)
}

func (w headT6g3Writer) InvalidateAuthorsCache() { w.p.deps.InvalidateAuthorsCache() }

// ReplaceOpRowsPrefix writes through the ops store's row store, as the judged
// markers always did.
func (w headT6g3Writer) ReplaceOpRowsPrefix(opID, rowPrefix string, n int, at func(i int) (database.OpRow, error)) error {
	rows, ok := database.AsCapability[database.OpRowStore](w.p.deps.OpsStore())
	if !ok {
		return fmt.Errorf("%w: no operation row store", errT6g3NoCapability)
	}
	return rows.ReplaceOpRowsPrefix(opID, rowPrefix, n, at)
}

// SaveClassLists and ClearClassLists write through the operation queue's row
// store, as the result's class lists always did.
func (w headT6g3Writer) SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error) {
	rows, ok := database.AsCapability[database.OpRowStore](w.p.deps.OperationQueueStore())
	if !ok {
		return nil, fmt.Errorf("%w: the operation store %T cannot keep the class id lists (database.OpRowStore)", errT6g3NoCapability, w.p.deps.OperationQueueStore())
	}
	return database.SaveClassLists(rows, opID, lists)
}

func (w headT6g3Writer) ClearClassLists(opID string) error {
	rows, ok := database.AsCapability[database.OpRowStore](w.p.deps.OperationQueueStore())
	if !ok {
		return fmt.Errorf("%w: no operation row store", errT6g3NoCapability)
	}
	return database.ClearClassLists(rows, opID)
}

// errT6g3NoStore is HEAD's "database not initialized".
var errT6g3NoStore = errors.New("database not initialized")

// errT6g3NoCapability marks a store that lacks an optional capability a core
// can work without or must refuse on. A core tells it from a read that failed.
var errT6g3NoCapability = errors.New("store lacks the capability")

// headT6g3Store is t6g3Store over HEAD's dependencies. The embedded OpsStore is
// HEAD's own store, answering the reads t6g3Store names; it is nil when HEAD
// has none, and a read then fails the way HEAD's own call on it did.
type headT6g3Store struct {
	OpsStore
	p *Plugin
}

func (s headT6g3Store) RootDir() string          { return s.p.deps.RootDir() }
func (s headT6g3Store) BackupRetentionDays() int { return s.p.deps.BackupRetentionDays() }
func (s headT6g3Store) ActivityLogNightlyCompactionEnabled() bool {
	return s.p.deps.ActivityLogNightlyCompactionEnabled()
}
func (s headT6g3Store) ActivityLogCompactionDays() int { return s.p.deps.ActivityLogCompactionDays() }
func (s headT6g3Store) ActivityLogRetentionChangeDays() int {
	return s.p.deps.ActivityLogRetentionChangeDays()
}
func (s headT6g3Store) ActivityLogRetentionDebugDays() int {
	return s.p.deps.ActivityLogRetentionDebugDays()
}

// ListActiveOperationsV2 reads HEAD's operation queue. HEAD may have none
// (OperationQueueStore() is nil), which the cover op's scan check treats as
// "cannot verify": errT6g3NoCapability says so.
func (s headT6g3Store) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	queue := s.p.deps.OperationQueueStore()
	if queue == nil {
		return nil, fmt.Errorf("%w: no operation queue", errT6g3NoCapability)
	}
	return queue.ListActiveOperationsV2()
}

// Can answers from HEAD's stores: the optional reads resolve through
// database.AsCapability, as the credits backfill always resolved them.
func (s headT6g3Store) Can(c t6g3Capability) bool {
	switch c {
	case t6g3CapNameIndex:
		_, ok := database.AsCapability[narratorByNameReader](s.p.deps.OpsStore())
		return ok
	case t6g3CapOpsLister:
		_, ok := database.AsCapability[database.OperationsV2SinceLister](s.p.deps.OpsStore())
		return ok
	case t6g3CapOpRows:
		_, ok := database.AsCapability[database.OpRowStore](s.p.deps.OpsStore())
		return ok
	case t6g3CapClassLists:
		_, ok := database.AsCapability[database.OpRowStore](s.p.deps.OperationQueueStore())
		return ok
	}
	return false
}

func headCap[T any](s headT6g3Store, what string) (T, error) {
	store := s.p.deps.OpsStore()
	if store == nil {
		var zero T
		return zero, errT6g3NoStore
	}
	v, ok := database.AsCapability[T](store)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: store is %T, which has no %s", errT6g3NoCapability, store, what)
	}
	return v, nil
}

func (s headT6g3Store) GetNarratorByName(name string) (*database.Narrator, error) {
	r, err := headCap[narratorByNameReader](s, "narrator name index")
	if err != nil {
		return nil, err
	}
	return r.GetNarratorByName(name)
}

func (s headT6g3Store) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	r, err := headCap[database.OperationsV2SinceLister](s, "operation lister (OperationsV2SinceLister)")
	if err != nil {
		return nil, err
	}
	return r.ListOperationsV2Since(since, limit)
}

func (s headT6g3Store) GetOpRows(opID string, rowIDs []string) ([][]byte, error) {
	r, err := headCap[database.OpRowStore](s, "operation row store")
	if err != nil {
		return nil, err
	}
	return r.GetOpRows(opID, rowIDs)
}

func (s headT6g3Store) ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) error {
	r, err := headCap[database.OpRowStore](s, "operation row store")
	if err != nil {
		return err
	}
	return r.ScanOpRowIndex(opID, fn)
}

func (s headT6g3Store) GetAllNarratorRefs() (database.NarratorRefs, error) {
	r, err := headCap[database.NarratorRefStore](s, "narrator reference reads")
	if err != nil {
		return database.NarratorRefs{}, err
	}
	return r.GetAllNarratorRefs()
}

func (s headT6g3Store) CountNarratorBookLinks(narratorID int) (int, error) {
	r, err := headCap[database.NarratorRefStore](s, "narrator reference reads")
	if err != nil {
		return 0, err
	}
	return r.CountNarratorBookLinks(narratorID)
}

func (s headT6g3Store) BookNarratorsLinking(narratorIDs map[int]bool) (map[string][]database.BookNarrator, error) {
	r, err := headCap[database.NarratorRefStore](s, "narrator reference reads")
	if err != nil {
		return nil, err
	}
	return r.BookNarratorsLinking(narratorIDs)
}

// GetRaw and ScanPrefix resolve the raw-key surface through
// database.AsCapability, because OpsStore() can be a decorator that hides the
// concrete store's methods (the same resolution the combine-journal check has
// always used). A store without it answers errT6g3NoCapability.
func (s headT6g3Store) GetRaw(key string) ([]byte, error) {
	kv, err := s.rawKV()
	if err != nil {
		return nil, err
	}
	return kv.GetRaw(key)
}

func (s headT6g3Store) ScanPrefix(prefix string) ([]database.KVPair, error) {
	kv, err := s.rawKV()
	if err != nil {
		return nil, err
	}
	return kv.ScanPrefix(prefix)
}

func (s headT6g3Store) rawKV() (combineJournalKV, error) {
	store := s.p.deps.OpsStore()
	if store == nil {
		return nil, errT6g3NoStore
	}
	kv, ok := database.AsCapability[combineJournalKV](store)
	if !ok {
		return nil, fmt.Errorf("%w: store is %T, which has no raw-key reads", errT6g3NoCapability, store)
	}
	return kv, nil
}

// --- the env every core takes ---

// t6g3Base is the part of every core's env that both shells fill the same way.
// A def's own env embeds it and adds only what that body needs beyond it.
type t6g3Base struct {
	// Live is false for a preview (HEAD: dry run).
	Live bool
	// Log writes one line to the run's log.
	Log func(level slog.Level, msg string)
	// Progress reports done of total with a message; a total of 0 is unknown.
	Progress func(done, total int, msg string)
	// OpID is the run's operation ID (HEAD: ctxOpID(ctx); native: rc.RunID()).
	OpID string
	// Writer is the only write path (rule R10).
	Writer t6g3Writer
	// Store is the only read path (rule R10).
	Store t6g3Store
}

// t6g3ItemsOpts is what one in-body loop asks of its engine beyond the items:
// the options HEAD's registry.RunItems call carries, in the engine-neutral form
// both shells map (HEAD to registry.RunItemsOptions, native to
// tasks.ItemsOptions).
type t6g3ItemsOpts struct {
	// Label is the progress label of item i of total. Called from worker
	// goroutines: whatever it reads needs a mutex or atomic.
	Label func(i, total int) string
	// Collect runs every item and joins the errors (HEAD's ErrModeCollect);
	// otherwise the first error stops the loop.
	Collect bool
	// ItemTimeout bounds each item's context when > 0.
	ItemTimeout time.Duration
}

// t6g3ItemsFunc runs fn over n items on the engine's worker pool; i indexes the
// caller's own slice. The pool size is the engine's: rule R4 fixes it per def.
type t6g3ItemsFunc func(n int, opts t6g3ItemsOpts, fn func(ctx context.Context, i int) error) error

// t6g3HeadItems is HEAD's loop: registry.RunItems over the indexes with the
// def's own worker count (workers 0 or 1 is sequential, as there).
func t6g3HeadItems(ctx context.Context, reporter sdk.Reporter, workers int) t6g3ItemsFunc {
	return func(n int, o t6g3ItemsOpts, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		opts := opsregistry.RunItemsOptions{Concurrency: workers, PerItemTimeout: o.ItemTimeout, Label: o.Label}
		if o.Collect {
			opts.ErrMode = opsregistry.ErrModeCollect
		}
		return opsregistry.RunItems(ctx, reporter, idx, func(ctx context.Context, i int) error { return fn(ctx, i) }, opts)
	}
}

// t6g3NativeItems is the native loop: tasks.RunItems over the indexes with the
// def's parallelism (rule R4).
func t6g3NativeItems(rc *tasks.RC, par tasks.Parallelism) t6g3ItemsFunc {
	return func(n int, o t6g3ItemsOpts, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		opts := tasks.ItemsOptions[int]{ItemTimeout: o.ItemTimeout}
		if o.Label != nil {
			opts.Label = func(i int) string { return o.Label(i, n) }
		}
		if o.Collect {
			opts.OnError = tasks.Collect
		}
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error { return fn(ic.Context(), i) }, opts)
		return err
	}
}

// t6g3HeadBase fills the base the way HEAD's shells do: from the reporter and
// the plugin's dependencies.
func (p *Plugin) t6g3HeadBase(ctx context.Context, reporter sdk.Reporter, live bool) t6g3Base {
	return t6g3Base{
		Live:     live,
		Log:      func(level slog.Level, msg string) { _ = reporter.Log(level, msg) },
		Progress: func(done, total int, msg string) { _ = reporter.UpdateProgress(done, total, msg) },
		OpID:     ctxOpID(ctx),
		Writer:   headT6g3Writer{p: p},
		Store:    headT6g3Store{OpsStore: p.deps.OpsStore(), p: p},
	}
}

// t6g3NativeBase fills the base the way the native shells do: from rc and the
// owner's deps. writes is false for a ReadOnly kind, which gets no writer.
func t6g3NativeBase(kind tasks.Kind, rc *tasks.RC, deps t6g3Deps, writes bool) (t6g3Base, error) {
	b := t6g3Base{
		Live: rc.Mode() == tasks.ModeLive,
		Log: func(level slog.Level, msg string) {
			rc.Log().Log(rc.Context(), level, msg)
		},
		Progress: func(done, total int, msg string) {
			n := tasks.Unknown()
			if total > 0 {
				n = tasks.Known(int64(total))
			}
			rc.Progress().Set(int64(done), n)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		OpID:  string(rc.RunID()),
		Store: deps.Store,
	}
	if writes {
		if deps.NewWriter == nil {
			return t6g3Base{}, fmt.Errorf("%s: t6g3Deps.NewWriter is not set", kind)
		}
		b.Writer = deps.NewWriter(rc.Writer())
		if b.Writer == nil {
			return t6g3Base{}, fmt.Errorf("%s: t6g3Deps.NewWriter returned no writer", kind)
		}
	}
	return b, nil
}

// t6g3NeedStore fails a native run whose owner declared no store, before the
// core reads one.
func t6g3NeedStore(kind tasks.Kind, b t6g3Base) error {
	if b.Store == nil {
		return fmt.Errorf("%s: t6g3Deps.Store is not set", kind)
	}
	return nil
}

// t6g3DecodeParams is the native shells' params step (05-tasks 5T.3.7 mode
// contract): a key that carries the run's mode (keys: dry_run, apply) is refused
// because the mode comes from the start request, not params, then the rest is
// decoded into v. Keys compare case-insensitively, as encoding/json matches
// field names. Empty and null params decode to the zero value.
func t6g3DecodeParams(kind tasks.Kind, raw json.RawMessage, v any, modeKeys ...string) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range modeKeys {
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

// t6g3LiveOnly is rule R7's "Live-only writer": HEAD runs such a def only in an
// explicit live mode, so its native shell refuses a preview.
func t6g3LiveOnly(kind tasks.Kind, rc *tasks.RC) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// t6g3ProgressStart and t6g3ProgressDone are sdk.NewProgress(reporter, 0)'s
// Start and Done frames: (0, 2) and (2, 2) with a "(pct%)" suffix.
func t6g3ProgressStart(env t6g3Base, msg string) { env.Progress(0, 2, msg+" (0%)") }
func t6g3ProgressDone(env t6g3Base, msg string)  { env.Progress(2, 2, msg+" (100%)") }

// --- HEAD field mapping (rules R5, R6) ---

const (
	// t6g3KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	t6g3KindPrefix = "books."

	// t6g3SignalMemDB is the readiness component the kinds wait for (rule R5,
	// NoWarmupWait false). F5-18 registers it with the health registry.
	t6g3SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t6g3SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (05-rbac 6.1, internal/books.AtomTable). internal/books imports this
// package, so the table cannot be read from here;
// TestT6G3_StartRequirementMatchesAtomTable (an external test) fails when
// the two drift.
var t6g3SettingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// t6g3Resume is rule R5's resume column. An unspecified policy stays zero,
// which registration refuses.
func t6g3Resume(p sdk.ResumePolicy) tasks.ResumePolicy {
	switch p {
	case sdk.ResumeRestart:
		return tasks.ResumeContinue
	case sdk.ResumeRequeue:
		return tasks.ResumeFromZero
	case sdk.ResumeDrop:
		return tasks.ResumeDrop
	case sdk.ResumeAsk:
		return tasks.ResumeAsk
	}
	return 0
}

// t6g3Priority is rule R5's priority column (HEAD 0/1/2 to Low/Normal/High).
func t6g3Priority(p sdk.Priority) tasks.Priority {
	switch p {
	case sdk.PriorityLow:
		return tasks.PriorityLow
	case sdk.PriorityHigh:
		return tasks.PriorityHigh
	}
	return tasks.PriorityNormal
}

// t6g3StartRequirement is rule R6: HEAD's empty permission list is its default
// settings.manage, and so is an explicit settings.manage. Any other atom has no
// row here and yields the zero requirement, which registration refuses (fail
// closed) rather than starting on a guessed scope.
func t6g3StartRequirement(def sdk.OperationDef) rbac.Requirement {
	for _, perm := range def.Permissions {
		if perm != auth.PermSettingsManage {
			return rbac.Requirement{}
		}
	}
	return rbac.AllOf(t6g3SettingsManageScopes...)
}

// t6g3Common maps HEAD's definition onto tasks.Common (rules R5 and R6). The
// effects are each kind's own (rule R7); the schedule comes from sched (rule
// R9) and is never a HEAD cron string.
func t6g3Common(def sdk.OperationDef, kind tasks.Kind, effects tasks.Effects, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Title:           def.DisplayName,
		Help:            def.Description,
		Effects:         effects,
		Perms:           tasks.Permissions{Start: t6g3StartRequirement(def)},
		Resume:          t6g3Resume(def.ResumePolicy),
		Priority:        t6g3Priority(def.DefaultPriority),
		Timeout:         def.Timeout,
		ProgressTimeout: def.ProgressTimeout,
	}
	if !def.NoWarmupWait {
		c.Ready = []tasks.ReadySignal{t6g3SignalMemDB}
	}
	if def.ConcurrencyKey != "" {
		c.ConcurrencyKey = tasks.Key(t6g3KindPrefix + def.ConcurrencyKey)
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// --- the group's list function ---

// t6g3Kinds returns every native kind of file group 3, in file order, each with
// Schedule: sched(kind) (rule R9; nil means none). Nothing calls it in
// production; F5-18 does.
func (p *Plugin) t6g3Kinds(deps t6g3Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.purgeDeletedKind(deps, sched),
		p.tombstoneCleanupKind(deps, sched),
		p.tempFileCleanupKind(deps, sched),
		p.transcodeTempCleanupKind(deps, sched),
		p.cleanupActivityLogKind(deps, sched),
		p.cleanupOldBackupsKind(deps, sched),
		p.trashCleanupKind(deps, sched),
		p.clearApplyRenameFailuresKind(deps, sched),
		p.combineJournalCheckKind(deps, sched),
		p.compactActivityLogKind(deps, sched),
		p.folderCoverBackfillKind(deps, sched),
		p.coverTextReadKind(deps, sched),
		p.creditCensusKind(deps, sched),
		p.creditsBackfillKind(deps, sched),
	}
}

// t6g3KindList names t6g3Kinds from a package-level function so tasklint's
// unregistered-kind rule counts the group a registration root until F5-18's
// Registrar.Tasks call replaces it (a method is not a list function, and the
// analyzer does not load test files). F5-18 deletes both lines.
func t6g3KindList(p *Plugin, deps t6g3Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return p.t6g3Kinds(deps, sched)
}

var _ = t6g3KindList
