// file: internal/plugins/maintenance/deps_t6g7.go
// version: 0.1.0
// guid: 91c4e7a2-3d58-4b60-a1f9-5e08c27d6b43
// last-edited: 2026-11-22

package maintenance

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own t6g7Deps (F5-18 A6), dark: nothing calls the builder
// until the flip hands its result to t6g7Kinds. It follows the group-2..4
// builders (deps_t6g2.go..deps_t6g4.go): a Store whose method set is exactly the
// group's read interface, a NewWriter whose every method is ONE writer.Gate.Do
// over the run's tasks.Writer stamped with the method's whole row, and the plain
// dependencies the shells need.

// G7BookReads is the group's reads of books, their files and paths.
type G7BookReads interface {
	BookAtPathIndexBuilt() (bool, error)
	BookFilesAtPath(string) ([]database.BookFile, error)
	GetBookByID(string) (*database.Book, error)
	GetBookFileByID(string, string) (*database.BookFile, error)
	GetBookFiles(string) ([]database.BookFile, error)
	GetBooksByVersionGroup(string) ([]database.Book, error)
	ListSoftDeletedBooks(int, int, *time.Time) ([]database.Book, error)
	LiveBookIDsAtPath(string) ([]string, error)
}

// G7CatalogReads is the group's whole-library reads of books, files, authors and series.
type G7CatalogReads interface {
	CountPrimaryBooks() (int, error)
	GetAllAuthors() ([]database.Author, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksCore(int, int) ([]database.BookCore, error)
	GetAllBooksCoreComplete(int, int) ([]database.BookCore, error)
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
}

// G7AuxReads is the group's reads of single series, chapters, field states, preferences and operations.
type G7AuxReads interface {
	GetAllPreferencesForUser(string) ([]database.UserPreferenceKV, error)
	GetChaptersForBook(string) ([]database.Chapter, error)
	GetMetadataFieldStates(string) ([]database.MetadataFieldState, error)
	GetSeriesByID(int) (*database.Series, error)
	GetSeriesByName(string, *int) (*database.Series, error)
	GetUserPreference(string) (*database.UserPreference, error)
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

// G7Reads is the library store's reads the group's cores make that every store
// carries (database.Store satisfies it; the books module passes it as is). Four
// more reads are optional capabilities, resolved from the same value through its
// decorator chain with database.AsCapability, as HEAD resolves them:
// GetAllBookFilesCoreIncludingRetired (RetiredBookFileReader),
// GetAllSeriesBookRefCounts (SeriesBookRefStore), GetSeriesBookRefCount
// (SeriesBookRefCounter) and GetBooksBySeriesIDsAllVersions
// (SeriesMembershipStore). A store without one refuses at the call.
//
// Split into the 3 interfaces above on 2026-11-22. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type G7Reads interface {
	G7BookReads
	G7CatalogReads
	G7AuxReads
}

// G7BookWrites is the group's writes of books, their files and series.
type G7BookWrites interface {
	BatchUpsertBookFiles([]*database.BookFile) error
	CreateSeries(string, *int) (*database.Series, error)
	ModifyBook(string, func(*database.Book) error) (*database.Book, error)
	ModifyBookFile(string, string, func(*database.BookFile) error) (*database.BookFile, error)
	UpdateBookFiles(context.Context, []*database.BookFile, func(int, bool)) (int, error)
}

// G7RecordWrites is the group's writes of change records and user preferences.
type G7RecordWrites interface {
	CreateOperationChange(*database.OperationChange) error
	RecordMetadataChange(*database.MetadataChangeRecord) error
	RecordPathChange(*database.BookPathChange) error
	SetUserPreferenceForUser(string, string, string) error
}

// G7Writes is the library store's writes the group's cores make that every store
// carries, each of which the builder stamps with its row before it reaches the
// store. Two more are optional capabilities resolved from the same value:
// DeleteSeriesIfUnreferenced (SeriesUnreferencedDeleter) and
// RebuildReviewStatusIndex (ReviewStatusIndexRepairer).
//
// Split into the 2 interfaces above on 2026-11-22. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type G7Writes interface {
	G7BookWrites
	G7RecordWrites
}

// G7UserMerge is the user-merge service (*usermerge.Service): the reads of a
// plan and a journal and the three writes.
type G7UserMerge interface {
	ActorIsAdmin(id string) (bool, error)
	BuildPlan(ctx context.Context, req usermerge.PlanRequest) (*usermerge.Plan, error)
	LoadJournal(id string) (*usermerge.Journal, error)
	Apply(ctx context.Context, req usermerge.ApplyRequest, prog usermerge.Progress) (*usermerge.Journal, error)
	Resume(ctx context.Context, journalID string, prog usermerge.Progress) (*usermerge.Journal, error)
	Undo(ctx context.Context, req usermerge.UndoRequest, prog usermerge.Progress) (*usermerge.Journal, error)
}

// BulkWriteBackFunc runs the library's bulk tag write-back (the server's
// runBulkWriteBack).
type BulkWriteBackFunc func(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter) error

// G7Inputs is what newT6G7Deps builds the group's deps from.
type G7Inputs struct {
	Reads  G7Reads
	Writes G7Writes
	// UserMerge is nil on a store that cannot merge users: the kind then refuses
	// at run time with HEAD's "this store cannot merge users".
	UserMerge G7UserMerge
	// BulkWriteBack is nil where the server has no write-back: the kind refuses.
	BulkWriteBack BulkWriteBackFunc
	// RootDir is the library root; the report path is confined under it.
	RootDir func() string
	// StandDownKey is the concurrency key of the library scan kinds.
	StandDownKey tasks.Key
	// InvalidateSeriesCache drops the cached series list and counts.
	InvalidateSeriesCache func()
	// AgreedChapterTitle and ChapterProber are HEAD's tag and ffprobe reads. Nil
	// means HEAD's: metadata.AgreedChapterTitle over the configured extensions,
	// and versionprimary.FFprobeChapterCounter.
	AgreedChapterTitle func(dir string) (agreed string, multi bool)
	ChapterProber      func() (versionprimary.ChapterProber, error)
}

// errT6g7NoUserMerge is HEAD's refusal of a user-merge run on a store that has no
// merge service.
var errT6g7NoUserMerge = errors.New("user-merge: this store cannot merge users")

// errT6g7NoWriteBack is the refusal of a bulk write-back with no runner wired.
var errT6g7NoWriteBack = errors.New("bulk write-back: no write-back runner is wired")

// newT6G7Deps is the books module's t6g7Deps. A nil Reads or Writes leaves the
// field that needs it unset so the group's own refusals fire, as newT6G2Deps
// does; a nil RootDir leaves RootDir and ResolveReportPath unset.
func newT6G7Deps(in G7Inputs) t6g7Deps {
	d := t6g7Deps{
		RootDir:               in.RootDir,
		StandDownKey:          in.StandDownKey,
		InvalidateSeriesCache: in.InvalidateSeriesCache,
		AgreedChapterTitle:    in.AgreedChapterTitle,
		ChapterProber:         in.ChapterProber,
	}
	if d.AgreedChapterTitle == nil {
		d.AgreedChapterTitle = func(dir string) (string, bool) {
			return metadata.AgreedChapterTitle(dir, config.Snapshot().SupportedExtensions)
		}
	}
	if d.ChapterProber == nil {
		d.ChapterProber = versionprimary.FFprobeChapterCounter
	}
	if in.RootDir != nil {
		d.ResolveReportPath = reportPathOver(in.RootDir)
	}
	if !isSet(in.Reads) || !isSet(in.Writes) {
		return d
	}
	// A nil *usermerge.Service held in the interface is not == nil: it would pass
	// every nil check below and panic at the first call. Normalize it to the
	// untyped nil those checks (and errT6g7NoUserMerge) are written for.
	merge := in.UserMerge
	if !isSet(merge) {
		merge = nil
	}
	rows := groupRows(nil, reflect.TypeFor[t6g7Writer]())
	d.Store = &booksT6G7Store{reads: in.Reads, writes: in.Writes, merge: merge}
	d.NewWriter = func(w tasks.Writer) t6g7Writer {
		return &booksT6G7Writer{src: in.Writes, merge: merge, writeBack: in.BulkWriteBack, gate: newBooksGate(w, rows)}
	}
	return d
}

// isSet reports whether v is a usable interface value: not nil, and not a typed
// nil (a nil pointer, map, slice, func or chan held in a non-nil interface).
func isSet(v any) bool {
	if v == nil {
		return false
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return !rv.IsNil()
	}
	return true
}

// booksT6G7Store is the group's reads: the store's own, the user-merge service's,
// and the two the group derives (the review-status index report, and whether the
// store can take an undoable series apply).
type booksT6G7Store struct {
	reads  G7Reads
	writes G7Writes
	merge  G7UserMerge
}

func (s *booksT6G7Store) BookAtPathIndexBuilt() (bool, error) { return s.reads.BookAtPathIndexBuilt() }
func (s *booksT6G7Store) BookFilesAtPath(p string) ([]database.BookFile, error) {
	return s.reads.BookFilesAtPath(p)
}
func (s *booksT6G7Store) CountPrimaryBooks() (int, error)           { return s.reads.CountPrimaryBooks() }
func (s *booksT6G7Store) GetAllAuthors() ([]database.Author, error) { return s.reads.GetAllAuthors() }
func (s *booksT6G7Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.reads.GetAllBookFilesCore()
}

func (s *booksT6G7Store) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	r, err := srcCap[database.RetiredBookFileReader](s.reads, "retired book_file reader")
	if err != nil {
		return nil, err
	}
	return r.GetAllBookFilesCoreIncludingRetired()
}

func (s *booksT6G7Store) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return s.reads.GetAllBooksCore(limit, offset)
}

func (s *booksT6G7Store) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	return s.reads.GetAllBooksCoreComplete(limit, offset)
}

func (s *booksT6G7Store) GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error) {
	return s.reads.GetAllPreferencesForUser(userID)
}
func (s *booksT6G7Store) GetAllSeries() ([]database.Series, error) { return s.reads.GetAllSeries() }
func (s *booksT6G7Store) GetAllSeriesBookCounts() (map[int]int, error) {
	return s.reads.GetAllSeriesBookCounts()
}

func (s *booksT6G7Store) GetAllSeriesBookRefCounts() (map[int]int, error) {
	r, err := srcCap[database.SeriesBookRefStore](s.reads, "series reference-count reader")
	if err != nil {
		return nil, err
	}
	return r.GetAllSeriesBookRefCounts()
}

func (s *booksT6G7Store) GetBookByID(id string) (*database.Book, error) {
	return s.reads.GetBookByID(id)
}

func (s *booksT6G7Store) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	return s.reads.GetBookFileByID(bookID, fileID)
}

func (s *booksT6G7Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.reads.GetBookFiles(bookID)
}

func (s *booksT6G7Store) GetBooksBySeriesIDsAllVersions(ids []int) (map[int][]database.BookCore, error) {
	r, err := srcCap[database.SeriesMembershipStore](s.reads, "series membership reader")
	if err != nil {
		return nil, err
	}
	return r.GetBooksBySeriesIDsAllVersions(ids)
}

func (s *booksT6G7Store) GetBooksByVersionGroup(id string) ([]database.Book, error) {
	return s.reads.GetBooksByVersionGroup(id)
}

func (s *booksT6G7Store) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	return s.reads.GetChaptersForBook(bookID)
}

func (s *booksT6G7Store) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.reads.GetMetadataFieldStates(bookID)
}
func (s *booksT6G7Store) GetSeriesBookRefCount(id int) (int, error) {
	r, err := srcCap[database.SeriesBookRefCounter](s.reads, "series reference counter")
	if err != nil {
		return 0, err
	}
	return r.GetSeriesBookRefCount(id)
}

func (s *booksT6G7Store) GetSeriesByID(id int) (*database.Series, error) {
	return s.reads.GetSeriesByID(id)
}

func (s *booksT6G7Store) GetSeriesByName(name string, authorID *int) (*database.Series, error) {
	return s.reads.GetSeriesByName(name, authorID)
}

func (s *booksT6G7Store) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.reads.GetUserPreference(key)
}

func (s *booksT6G7Store) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return s.reads.ListActiveOperationsV2()
}

func (s *booksT6G7Store) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
	return s.reads.ListSoftDeletedBooks(limit, offset, olderThan)
}

func (s *booksT6G7Store) LiveBookIDsAtPath(p string) ([]string, error) {
	return s.reads.LiveBookIDsAtPath(p)
}

// ReviewStatusIndexReport is HEAD's read: the repairer with apply false, which
// scans the index against the records and writes nothing.
func (s *booksT6G7Store) ReviewStatusIndexReport() (database.ReviewStatusIndexRepair, error) {
	r, err := srcCap[database.ReviewStatusIndexRepairer](s.writes, "review-status index repairer")
	if err != nil {
		return database.ReviewStatusIndexRepair{}, err
	}
	return r.RebuildReviewStatusIndex(false)
}

// SeriesApplyReady is HEAD's check that the store takes an apply that can be
// undone (seriesDenumberStoreReady).
func (s *booksT6G7Store) SeriesApplyReady() error { return seriesDenumberStoreReady(s.writes) }

func (s *booksT6G7Store) ActorIsAdmin(id string) (bool, error) {
	if s.merge == nil {
		return false, errT6g7NoUserMerge
	}
	return s.merge.ActorIsAdmin(id)
}

func (s *booksT6G7Store) BuildPlan(ctx context.Context, req usermerge.PlanRequest) (*usermerge.Plan, error) {
	if s.merge == nil {
		return nil, errT6g7NoUserMerge
	}
	return s.merge.BuildPlan(ctx, req)
}

func (s *booksT6G7Store) LoadJournal(id string) (*usermerge.Journal, error) {
	if s.merge == nil {
		return nil, errT6g7NoUserMerge
	}
	return s.merge.LoadJournal(id)
}

// booksT6G7Writer is one run's gated writes: each method is one gate.do stamped
// with its row of the shared table.
type booksT6G7Writer struct {
	src       G7Writes
	merge     G7UserMerge
	writeBack BulkWriteBackFunc
	gate      booksGate
}

func (w *booksT6G7Writer) BatchUpsertBookFiles(files []*database.BookFile) error {
	return w.gate.do(runless(), "BatchUpsertBookFiles", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.BatchUpsertBookFiles(files)
	})
}

func (w *booksT6G7Writer) CreateOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "CreateOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

func (w *booksT6G7Writer) CreateSeries(name string, authorID *int) (s *database.Series, err error) {
	err = w.gate.do(runless(), "CreateSeries", kindRef(ops.ResSeries), func(context.Context) error {
		var e error
		s, e = w.src.CreateSeries(name, authorID)
		return e
	})
	return s, err
}

func (w *booksT6G7Writer) DeleteSeriesIfUnreferenced(id int, expectName string, expectAuthorID *int) (deleted bool, err error) {
	err = w.gate.do(runless(), "DeleteSeriesIfUnreferenced", kindRef(ops.ResSeries), func(context.Context) error {
		d, e := srcCap[database.SeriesUnreferencedDeleter](w.src, "check-and-delete series writer")
		if e != nil {
			return e
		}
		deleted, e = d.DeleteSeriesIfUnreferenced(id, expectName, expectAuthorID)
		return e
	})
	return deleted, err
}

func (w *booksT6G7Writer) ModifyBook(id string, fn func(*database.Book) error) (b *database.Book, err error) {
	err = w.gate.do(runless(), "ModifyBook", bookRef(id), func(context.Context) error {
		var e error
		b, e = w.src.ModifyBook(id, fn)
		return e
	})
	return b, err
}

func (w *booksT6G7Writer) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (f *database.BookFile, err error) {
	err = w.gate.do(runless(), "ModifyBookFile", bookRef(bookID), func(context.Context) error {
		var e error
		f, e = w.src.ModifyBookFile(bookID, fileID, fn)
		return e
	})
	return f, err
}

func (w *booksT6G7Writer) RebuildReviewStatusIndex(apply bool) (rep database.ReviewStatusIndexRepair, err error) {
	err = w.gate.do(runless(), "RebuildReviewStatusIndex", kindRef(ops.ResReviewItems), func(context.Context) error {
		r, e := srcCap[database.ReviewStatusIndexRepairer](w.src, "review-status index repairer")
		if e != nil {
			return e
		}
		rep, e = r.RebuildReviewStatusIndex(apply)
		return e
	})
	return rep, err
}

func (w *booksT6G7Writer) RecordMetadataChange(rec *database.MetadataChangeRecord) error {
	return w.gate.do(runless(), "RecordMetadataChange", kindRef(ops.ResBooks), func(context.Context) error {
		return w.src.RecordMetadataChange(rec)
	})
}

func (w *booksT6G7Writer) RecordPathChange(change *database.BookPathChange) error {
	return w.gate.do(runless(), "RecordPathChange", kindRef(ops.ResBooks), func(context.Context) error {
		return w.src.RecordPathChange(change)
	})
}

func (w *booksT6G7Writer) SetUserPreferenceForUser(userID, key, value string) error {
	return w.gate.do(runless(), "SetUserPreferenceForUser", kindRef(ops.ResUserState), func(context.Context) error {
		return w.src.SetUserPreferenceForUser(userID, key, value)
	})
}

func (w *booksT6G7Writer) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (n int, err error) {
	err = w.gate.do(ctx, "UpdateBookFiles", kindRef(ops.ResBookFiles), func(ctx context.Context) error {
		var e error
		n, e = w.src.UpdateBookFiles(ctx, files, afterRow)
		return e
	})
	return n, err
}

func (w *booksT6G7Writer) RunBulkWriteBack(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter) error {
	return w.gate.do(ctx, "RunBulkWriteBack", kindRef(ops.ResBooks), func(ctx context.Context) error {
		if w.writeBack == nil {
			return errT6g7NoWriteBack
		}
		return w.writeBack(ctx, opID, bookIDs, doRename, startIdx, progress)
	})
}

func (w *booksT6G7Writer) ApplyUserMerge(ctx context.Context, req usermerge.ApplyRequest, prog usermerge.Progress) (j *usermerge.Journal, err error) {
	err = w.gate.do(ctx, "ApplyUserMerge", kindRef(ops.ResUserState), func(ctx context.Context) error {
		if w.merge == nil {
			return errT6g7NoUserMerge
		}
		var e error
		j, e = w.merge.Apply(ctx, req, prog)
		return e
	})
	return j, err
}

func (w *booksT6G7Writer) ResumeUserMerge(ctx context.Context, journalID string, prog usermerge.Progress) (j *usermerge.Journal, err error) {
	err = w.gate.do(ctx, "ResumeUserMerge", kindRef(ops.ResUserState), func(ctx context.Context) error {
		if w.merge == nil {
			return errT6g7NoUserMerge
		}
		var e error
		j, e = w.merge.Resume(ctx, journalID, prog)
		return e
	})
	return j, err
}

func (w *booksT6G7Writer) UndoUserMerge(ctx context.Context, req usermerge.UndoRequest, prog usermerge.Progress) (j *usermerge.Journal, err error) {
	err = w.gate.do(ctx, "UndoUserMerge", kindRef(ops.ResUserState), func(ctx context.Context) error {
		if w.merge == nil {
			return errT6g7NoUserMerge
		}
		var e error
		j, e = w.merge.Undo(ctx, req, prog)
		return e
	})
	return j, err
}

var (
	_ t6g7Store   = (*booksT6G7Store)(nil)
	_ t6g7Writer  = (*booksT6G7Writer)(nil)
	_ G7UserMerge = (*usermerge.Service)(nil)
	_ G7Reads     = database.Store(nil)
	_ G7Writes    = database.Store(nil)
)
