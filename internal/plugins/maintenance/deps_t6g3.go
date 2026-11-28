// file: internal/plugins/maintenance/deps_t6g3.go
// version: 0.1.0
// guid: 5a7d3e91-0c48-4f26-b8d1-e2a69f04c357
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	pathutil "github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g3Source is the store the group-3 builder reads and writes through. The
// optional capabilities (the narrator name index, the operations lister, the
// operation row store, the raw-key reads, the credits modifier) are resolved
// per call with database.AsCapability, as HEAD does.
type t6g3Source interface {
	creditCensusStore
	t6g3ImportPathReader
	t6g3PreferenceReader
	t6g3CoverSourceReads
	t6g3CreditsReader
	t6g3PreferenceWriter
	t6g3CoverWriter
	t6g3SourceOps
}

// t6g3SourceOps is the source's own writes that no narrower group carries.
type t6g3SourceOps interface {
	ResolveTombstoneChains() (int, error)
	CreateNarrator(name string) (*database.Narrator, error)
	DeleteNarrator(id int) error
	CreateOperationChange(change *database.OperationChange) error
}

// t6g3CoverSourceReads is t6g3CoverReader without the operation queue, which
// the host serves.
type t6g3CoverSourceReads interface {
	GetAllBooksCore(limit int, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	database.MetadataFieldStateReader
}

// t6g3BootHost is what the server serves that is not a store row: the
// configuration, the operation queue, and the passes that purge, flush and
// compact.
type t6g3BootHost interface {
	t6g3ConfigReader
	OperationQueueStore() OpQueueReader
	t6g3HostPurge
	t6g3HostActivity
}

type t6g3HostPurge interface {
	RunAutoPurgeSoftDeleted(ctx context.Context, opID string) error
	CleanupOrphanedTempFiles(rootDir string, opID string) int
	CleanupTrashedVersions() (versions.CleanupResult, error)
}

type t6g3HostActivity interface {
	ActivityFlushOp(opID string)
	MaintainActivityLog(ctx context.Context, changeDays, debugDays int) (summarized int, pruned int, indexOrphansRemoved int64, err error)
	CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error)
	InvalidateAuthorsCache()
}

// newT6G3Deps is the books module's t6g3Deps. As in newT6G2Deps, a missing
// input leaves the field that needs it unset so the group's own refusals fire.
//
// runs is the tasks run store (t6g3Deps.Runs), passed through as it is: a nil
// runs leaves the credits backfill refused while the other kinds run.
func newT6G3Deps(src t6g3Source, host t6g3BootHost, guardTags repairs.GuardTagReader, standDownKey tasks.Key, runs cbRunStore) t6g3Deps {
	if src == nil || host == nil {
		return t6g3Deps{}
	}
	rows := groupRows(map[string]string{"SetRaw": "SetRaw/cover_text"}, reflect.TypeFor[t6g3Writer]())
	return t6g3Deps{
		Store:        &booksT6G3Store{src: src, host: host},
		GuardTags:    guardTags,
		StandDownKey: standDownKey,
		Runs:         runs,
		NewWriter: func(w tasks.Writer) t6g3Writer {
			return &booksT6G3Writer{src: src, host: host, gate: newBooksGate(w, rows)}
		},
	}
}

// booksT6G3Store is the group's reads. src is embedded for the plain reads;
// the configuration reads, the operation queue and the optional capabilities are
// explicit.
type booksT6G3Store struct {
	src  t6g3Source
	host t6g3BootHost
}

func (s *booksT6G3Store) ListBookIDs() ([]string, error) { return s.src.ListBookIDs() }
func (s *booksT6G3Store) GetBookByID(id string) (*database.Book, error) {
	return s.src.GetBookByID(id)
}

func (s *booksT6G3Store) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	return s.src.GetBookAuthors(bookID)
}

func (s *booksT6G3Store) GetBookNarrators(bookID string) ([]database.BookNarrator, error) {
	return s.src.GetBookNarrators(bookID)
}
func (s *booksT6G3Store) GetAllAuthors() ([]database.Author, error) { return s.src.GetAllAuthors() }
func (s *booksT6G3Store) GetAuthorByID(id int) (*database.Author, error) {
	return s.src.GetAuthorByID(id)
}
func (s *booksT6G3Store) ListNarrators() ([]database.Narrator, error) { return s.src.ListNarrators() }
func (s *booksT6G3Store) GetNarratorByID(id int) (*database.Narrator, error) {
	return s.src.GetNarratorByID(id)
}

func (s *booksT6G3Store) GetAllImportPaths() ([]database.ImportPath, error) {
	return s.src.GetAllImportPaths()
}

func (s *booksT6G3Store) GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error) {
	return s.src.GetAllPreferencesForUser(userID)
}

func (s *booksT6G3Store) GetAllBooksCore(limit int, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCore(limit, offset)
}

func (s *booksT6G3Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.src.GetBookFiles(bookID)
}

func (s *booksT6G3Store) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.src.GetMetadataFieldStates(bookID)
}

func (s *booksT6G3Store) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.src.GetUserPreference(key)
}
func (s *booksT6G3Store) GetAllSeries() ([]database.Series, error) { return s.src.GetAllSeries() }
func (s *booksT6G3Store) GetOperationChanges(operationID string) ([]*database.OperationChange, error) {
	return s.src.GetOperationChanges(operationID)
}

func (s *booksT6G3Store) RootDir() string          { return s.host.RootDir() }
func (s *booksT6G3Store) BackupRetentionDays() int { return s.host.BackupRetentionDays() }
func (s *booksT6G3Store) ActivityLogNightlyCompactionEnabled() bool {
	return s.host.ActivityLogNightlyCompactionEnabled()
}
func (s *booksT6G3Store) ActivityLogCompactionDays() int { return s.host.ActivityLogCompactionDays() }
func (s *booksT6G3Store) ActivityLogRetentionChangeDays() int {
	return s.host.ActivityLogRetentionChangeDays()
}

func (s *booksT6G3Store) ActivityLogRetentionDebugDays() int {
	return s.host.ActivityLogRetentionDebugDays()
}

// ListActiveOperationsV2 reads the host's operation queue; a host without one
// answers errT6g3NoCapability, which the cover op's scan check reads as "cannot
// verify".
func (s *booksT6G3Store) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	queue := s.host.OperationQueueStore()
	if queue == nil {
		return nil, fmt.Errorf("%w: no operation queue", errT6g3NoCapability)
	}
	return queue.ListActiveOperationsV2()
}

func (s *booksT6G3Store) Can(c t6g3Capability) bool {
	switch c {
	case t6g3CapNameIndex:
		_, ok := database.AsCapability[narratorByNameReader](s.src)
		return ok
	case t6g3CapOpsLister:
		_, ok := database.AsCapability[database.OperationsV2SinceLister](s.src)
		return ok
	case t6g3CapOpRows:
		_, ok := database.AsCapability[database.OpRowStore](s.src)
		return ok
	}
	return false
}

// g3Cap resolves one optional capability of the source, or says which one the
// store lacks.
func g3Cap[T any](src any, what string) (T, error) {
	v, ok := database.AsCapability[T](src)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: store is %T, which has no %s", errT6g3NoCapability, src, what)
	}
	return v, nil
}

func (s *booksT6G3Store) GetNarratorByName(name string) (*database.Narrator, error) {
	r, err := g3Cap[narratorByNameReader](s.src, "narrator name index")
	if err != nil {
		return nil, err
	}
	return r.GetNarratorByName(name)
}

func (s *booksT6G3Store) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	r, err := g3Cap[database.OperationsV2SinceLister](s.src, "operation lister (OperationsV2SinceLister)")
	if err != nil {
		return nil, err
	}
	return r.ListOperationsV2Since(since, limit)
}

func (s *booksT6G3Store) GetOpRows(opID string, rowIDs []string) ([][]byte, error) {
	r, err := g3Cap[database.OpRowStore](s.src, "operation row store")
	if err != nil {
		return nil, err
	}
	return r.GetOpRows(opID, rowIDs)
}

func (s *booksT6G3Store) ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) error {
	r, err := g3Cap[database.OpRowStore](s.src, "operation row store")
	if err != nil {
		return err
	}
	return r.ScanOpRowIndex(opID, fn)
}

func (s *booksT6G3Store) GetAllNarratorRefs() (database.NarratorRefs, error) {
	r, err := g3Cap[database.NarratorRefStore](s.src, "narrator reference reads")
	if err != nil {
		return database.NarratorRefs{}, err
	}
	return r.GetAllNarratorRefs()
}

func (s *booksT6G3Store) CountNarratorBookLinks(narratorID int) (int, error) {
	r, err := g3Cap[database.NarratorRefStore](s.src, "narrator reference reads")
	if err != nil {
		return 0, err
	}
	return r.CountNarratorBookLinks(narratorID)
}

func (s *booksT6G3Store) BookNarratorsLinking(narratorIDs map[int]bool) (map[string][]database.BookNarrator, error) {
	r, err := g3Cap[database.NarratorRefStore](s.src, "narrator reference reads")
	if err != nil {
		return nil, err
	}
	return r.BookNarratorsLinking(narratorIDs)
}

func (s *booksT6G3Store) GetRaw(key string) ([]byte, error) {
	kv, err := g3Cap[combineJournalKV](s.src, "raw-key reads")
	if err != nil {
		return nil, err
	}
	return kv.GetRaw(key)
}

func (s *booksT6G3Store) ScanPrefix(prefix string) ([]database.KVPair, error) {
	kv, err := g3Cap[combineJournalKV](s.src, "raw-key reads")
	if err != nil {
		return nil, err
	}
	return kv.ScanPrefix(prefix)
}

// booksT6G3Writer is the group's writes. Every method whose row names a
// resource is one gated call. The methods with an empty row are decided here:
//
//   - ReplaceOpRowsPrefix, SaveClassLists and ClearClassLists write op rows
//     beside a v2 operation (ledger rows that belong to no Effects resource).
//     The native credits backfill no longer calls them (F5-18d): its class id
//     lists are its own run rows (env.ClassLists) and its judgements ride in
//     its result (env.Ledger), both in the tasks run store. They stay on the
//     writer, with no stamp (the gate refuses them for a stopped run, see
//     writer.Gate.Do), because t6g3Writer only gains methods (rule R10 item 2);
//     removing them is F5-18's call (seams.d/F5-18d.md).
//     TestBooksT6G3Writer_EmptyRowMethodsStayUnfenced pins that they carry no
//     stamp; the stopped-run refusal is writer's TestGate_EmptyRowCancelTimeoutLease.
//   - InvalidateAuthorsCache drops an in-memory cache: no stamp, as before, but
//     not past a stopped run.
type booksT6G3Writer struct {
	src  t6g3Source
	host t6g3BootHost
	gate booksGate
}

func (w *booksT6G3Writer) RunAutoPurgeSoftDeleted(ctx context.Context, opID string) error {
	return w.gate.do(ctx, "RunAutoPurgeSoftDeleted", kindRef(ops.ResBooks), func(ctx context.Context) error {
		return w.host.RunAutoPurgeSoftDeleted(ctx, opID)
	})
}

// ActivityFlushOp has no error result: a refusal is logged and nothing is
// flushed (see gateQuiet).
func (w *booksT6G3Writer) ActivityFlushOp(opID string) {
	gateQuiet(w.gate, "ActivityFlushOp", kindRef(ops.ResActivity), func() { w.host.ActivityFlushOp(opID) })
}

func (w *booksT6G3Writer) ResolveTombstoneChains() (n int, err error) {
	err = w.gate.do(runless(), "ResolveTombstoneChains", kindRef(ops.ResAuthors), func(context.Context) error {
		var e error
		n, e = w.src.ResolveTombstoneChains()
		return e
	})
	return n, err
}

func (w *booksT6G3Writer) CleanupTrashedVersions() (res versions.CleanupResult, err error) {
	err = w.gate.do(runless(), "CleanupTrashedVersions", kindRef(ops.ResFiles), func(context.Context) error {
		var e error
		res, e = w.host.CleanupTrashedVersions()
		return e
	})
	return res, err
}

func (w *booksT6G3Writer) CleanupOrphanedTempFiles(rootDir string, opID string) (n int) {
	gateQuiet(w.gate, "CleanupOrphanedTempFiles", kindRef(ops.ResFiles), func() { n = w.host.CleanupOrphanedTempFiles(rootDir, opID) })
	return n
}

func (w *booksT6G3Writer) CleanupStaleTempFiles(root string, app pathutil.AppDirs, maxAge time.Duration) (n int) {
	gateQuiet(w.gate, "CleanupStaleTempFiles", kindRef(ops.ResFiles), func() {
		n = transcode.CleanupStaleTempFiles(root, app, maxAge, false)
	})
	return n
}

func (w *booksT6G3Writer) RemoveOldBackupFiles(ctx context.Context, root string, app pathutil.AppDirs,
	match func(name string) (time.Time, bool), maxAge time.Duration,
	log func(level slog.Level, msg string, kv ...any),
) (n int, err error) {
	err = w.gate.do(ctx, "RemoveOldBackupFiles", kindRef(ops.ResFiles), func(ctx context.Context) error {
		var e error
		n, e = sweep.RemoveOldBackupFiles(ctx, root, app, match, maxAge, log)
		return e
	})
	return n, err
}

func (w *booksT6G3Writer) CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (res database.CompactResult, err error) {
	err = w.gate.do(ctx, "CompactActivityEntries", kindRef(ops.ResActivity), func(ctx context.Context) error {
		var e error
		res, e = w.host.CompactActivityEntries(ctx, cutoff, progress)
		return e
	})
	return res, err
}

func (w *booksT6G3Writer) MaintainActivityLog(ctx context.Context, changeDays, debugDays int) (summarized, pruned int, orphans int64, err error) {
	err = w.gate.do(ctx, "MaintainActivityLog", kindRef(ops.ResActivity), func(ctx context.Context) error {
		var e error
		summarized, pruned, orphans, e = w.host.MaintainActivityLog(ctx, changeDays, debugDays)
		return e
	})
	return summarized, pruned, orphans, err
}

func (w *booksT6G3Writer) SetUserPreferenceForUser(userID, key, value string) error {
	return w.gate.do(runless(), "SetUserPreferenceForUser", kindRef(ops.ResUserState), func(context.Context) error {
		return w.src.SetUserPreferenceForUser(userID, key, value)
	})
}

func (w *booksT6G3Writer) ModifyBook(id string, fn func(*database.Book) error) (b *database.Book, err error) {
	err = w.gate.do(runless(), "ModifyBook", bookRef(id), func(context.Context) error {
		var e error
		b, e = w.src.ModifyBook(id, fn)
		return e
	})
	return b, err
}

func (w *booksT6G3Writer) SetRaw(key string, value []byte) error {
	return w.gate.do(runless(), "SetRaw", kindRef(ops.ResCoverText), func(context.Context) error {
		return w.src.SetRaw(key, value)
	})
}

func (w *booksT6G3Writer) ModifyBookCredits(bookID string, fn func(book *database.Book, credits *database.BookCreditsEdit) error) (b *database.Book, edit database.BookCreditsEdit, err error) {
	err = w.gate.do(runless(), "ModifyBookCredits", bookRef(bookID), func(context.Context) error {
		m, e := g3Cap[database.BookCreditsModifier](w.src, "ModifyBookCredits (a book's credits and row written together)")
		if e != nil {
			return e
		}
		b, edit, e = m.ModifyBookCredits(bookID, fn)
		return e
	})
	return b, edit, err
}

func (w *booksT6G3Writer) CreateNarrator(name string) (n *database.Narrator, err error) {
	err = w.gate.do(runless(), "CreateNarrator", kindRef(ops.ResAuthors), func(context.Context) error {
		var e error
		n, e = w.src.CreateNarrator(name)
		return e
	})
	return n, err
}

func (w *booksT6G3Writer) DeleteNarrator(id int) error {
	return w.gate.do(runless(), "DeleteNarrator", kindRef(ops.ResAuthors), func(context.Context) error {
		return w.src.DeleteNarrator(id)
	})
}

func (w *booksT6G3Writer) CreateOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "CreateOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

func (w *booksT6G3Writer) InvalidateAuthorsCache() {
	gateQuiet(w.gate, "InvalidateAuthorsCache", kindRef(ops.ResAuthors), func() { w.host.InvalidateAuthorsCache() })
}

func (w *booksT6G3Writer) ReplaceOpRowsPrefix(opID, rowPrefix string, n int, at func(i int) (database.OpRow, error)) error {
	return w.gate.do(runless(), "ReplaceOpRowsPrefix", kindRef(ops.ResOperations), func(context.Context) error {
		rows, err := g3Cap[database.OpRowStore](w.src, "operation row store")
		if err != nil {
			return err
		}
		return rows.ReplaceOpRowsPrefix(opID, rowPrefix, n, at)
	})
}

func (w *booksT6G3Writer) SaveClassLists(opID string, lists map[string][]string) (sum *database.ClassListSummary, err error) {
	err = w.gate.do(runless(), "SaveClassLists", kindRef(ops.ResOperations), func(context.Context) error {
		rows, e := g3Cap[database.OpRowStore](w.host.OperationQueueStore(), "operation row store (database.OpRowStore) to keep the class id lists")
		if e != nil {
			return e
		}
		sum, e = database.SaveClassLists(rows, opID, lists)
		return e
	})
	return sum, err
}

func (w *booksT6G3Writer) ClearClassLists(opID string) error {
	return w.gate.do(runless(), "ClearClassLists", kindRef(ops.ResOperations), func(context.Context) error {
		rows, err := g3Cap[database.OpRowStore](w.host.OperationQueueStore(), "operation row store")
		if err != nil {
			return err
		}
		return database.ClearClassLists(rows, opID)
	})
}

// gateQuiet is a gated call of a method that has no error result (the
// interface signatures are fixed: rule R10 item 2 forbids re-signing them). A
// refusal cannot reach the core, so it is logged and the method does nothing and
// returns the zero value. A stopped run (ErrFenced) and a preview are expected
// and logged as warnings; a write outside the declared effects is a wiring
// fault and is logged as an error.
func gateQuiet(b booksGate, method string, subject entity.Ref, call func()) {
	err := b.do(runless(), method, subject, func(context.Context) error { call(); return nil })
	if err == nil {
		return
	}
	log := logger.New("maintenance")
	if errors.Is(err, tasks.ErrOutsideEffects) {
		log.Error("%s refused, nothing done: %v", method, err)
		return
	}
	log.Warn("%s not run: %v", method, err)
}
