// file: internal/plugins/maintenance/deps_jobs.go
// version: 0.1.0
// guid: 3e8a51d7-c924-4f60-b1a8-72d0e9f5a436
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's jobs.KindDeps (F5-18c): the store view one maintenance job
// run gets.
//
// The value NewStore returns satisfies jobs.Store, and every write on it is one
// writer.Gate.Do over the run's tasks.Writer. It deliberately has no Unwrap and
// no embedded store: database.AsCapability and method promotion would both hand
// the job the raw store, and every write it then made through that would
// bypass the stamp. What the jobs reach by runtime assertion (seams.d/F5-01.md,
// "What F5-18 must do" item 3) is therefore implemented on the wrapper itself,
// each method one more gated call or a read.
//
// The capabilities are resolved once, when the deps are built, and a source that
// lacks one gets no NewStore at all (the run fails before the job starts, which
// is what a nil NewStore does). A wrapper that answered every assertion with yes
// and failed later would turn the jobs that branch on the assertion's result
// (recompute-book-aggregates keeps a fallback loop, backfill-sync-ids skips when
// sync identity is absent) into a different job.

// jobsReads is every method of jobs.Store that writes nothing: no method here
// has a row in the shared method-to-resource table, which
// TestBooksJobsStore_ReadsHaveNoRowAndWritesAreGated checks, so the embedded
// value cannot be a way to write.
type jobsReads interface {
	BookFilesAtPath(path string) ([]database.BookFile, error)
	CountPrefix(prefix string) (int64, error)
	GetAllAuthors() ([]database.Author, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
	GetBookUserTags(bookID string) ([]string, error)
	GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	GetOperationByID(id string) (*database.Operation, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
	GetOperationState(opID string) ([]byte, error)
	GetOperationV2(id string) (*database.OperationV2Row, error)
	GetRaw(key string) ([]byte, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetSetting(key string) (*database.Setting, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	GetUserPreference(key string) (*database.UserPreference, error)
	ListBookIDs() ([]string, error)
	ListOperations(limit, offset int) ([]database.Operation, int, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	ListUsers() ([]database.User, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
	ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error)
}

// jobsDeferredAggregates, jobsAggregatesMarker: the shape of the jobs' private
// capability interfaces (hbDeferredStore, aggregatesBackfillMarker), which a
// package this one cannot import declares.
type jobsDeferredAggregates interface {
	ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

type jobsAggregatesMarker interface {
	IsBookAggregatesBackfillDone() bool
	MarkBookAggregatesBackfillDone() error
}

// jobsCaps is the optional store capabilities the jobs reach, resolved through
// the source's decorator chain once.
type jobsCaps struct {
	sync        database.SyncIdentityStore
	syncFile    database.SyncFileStore
	bookmarks   database.BookmarkStore
	deferred    jobsDeferredAggregates
	aggregates  jobsAggregatesMarker
	seriesDel   database.SeriesUnreferencedDeleter
	seriesReady database.SeriesDeleteReadiness
	planner     database.SnapshotPrunePlanner
	hashes      database.BookFileHashUpdater
}

// resolveJobsCaps resolves every capability or names every one src lacks.
func resolveJobsCaps(src any) (jobsCaps, error) {
	var c jobsCaps
	var missing []string
	need := func(name string, ok bool) {
		if !ok {
			missing = append(missing, name)
		}
	}
	var ok bool
	c.sync, ok = database.AsCapability[database.SyncIdentityStore](src)
	need("SyncIdentityStore", ok)
	c.syncFile, ok = database.AsCapability[database.SyncFileStore](src)
	need("SyncFileStore", ok)
	c.bookmarks, ok = database.AsCapability[database.BookmarkStore](src)
	need("BookmarkStore", ok)
	c.deferred, ok = database.AsCapability[jobsDeferredAggregates](src)
	need("ModifyBookFileDeferAggregates", ok)
	c.aggregates, ok = database.AsCapability[jobsAggregatesMarker](src)
	need("aggregates backfill sentinel", ok)
	c.seriesDel, ok = database.AsCapability[database.SeriesUnreferencedDeleter](src)
	need("SeriesUnreferencedDeleter", ok)
	c.seriesReady, ok = database.AsCapability[database.SeriesDeleteReadiness](src)
	need("SeriesDeleteReadiness", ok)
	c.planner, ok = database.AsCapability[database.SnapshotPrunePlanner](src)
	need("SnapshotPrunePlanner", ok)
	c.hashes, ok = database.AsCapability[database.BookFileHashUpdater](src)
	need("BookFileHashUpdater", ok)
	if len(missing) > 0 {
		return jobsCaps{}, fmt.Errorf("maintenance jobs: store %T lacks %s; no job store is built, so every maintenance run fails before it starts",
			src, strings.Join(missing, ", "))
	}
	return c, nil
}

// jobsWriteLabels is every gated write of the wrapper, as the label the gate
// looks its row up by: the method name, or "SetRaw/<family>" for the two raw
// writes, whose row depends on the key.
var jobsWriteLabels = []string{
	"AddBookUserTag", "BatchCreateBookFiles", "CreateAuthor", "CreateBookFile", "CreateOperationChange",
	"CreateOperationResult", "DeleteBook", "DeleteOperationState", "DeleteOperationWithLogs", "DeleteRawBatch",
	"ModifyBook", "ModifyBookAuthors", "ModifyBookFile", "MoveBookFilesToBook", "PruneBookSnapshots",
	"ReassignExternalIDs", "RecomputeBookAggregates", "SaveOperationState", "SaveOperationSummaryLog",
	"SetBookFileHash", "SetSetting", "SetUserBookState", "SetUserPosition", "UpdateBook", "UpdateBookFile",
	// raw writes, by key family
	"SetRaw/merge_journal", "SetRaw/group_redirect", "SetRaw/user_state",
	"DeleteRaw/merge_journal", "DeleteRaw/group_redirect", "DeleteRaw/user_state", "DeleteRaw/dead_book_index",
	// capability writes (rows the table has; the rest of each capability's writes have none and refuse)
	"MintOrGetSyncID", "RecordSyncMerge", "MintOrGetSyncFileID", "RepointSyncFileToBook", "CreateBookmark",
	"ModifyBookFileDeferAggregates", "MarkBookAggregatesBackfillDone", "DeleteSeriesIfUnreferenced", "UpdateBookFileHashes",
}

func jobsRows() map[string][]ops.Resource {
	rows := make(map[string][]ops.Resource, len(jobsWriteLabels))
	for _, l := range jobsWriteLabels {
		if r, ok := t6WriterResources[l]; ok {
			rows[l] = r
		}
	}
	return rows
}

// jobsRawFamilies maps a raw key's prefix to the family the shared table names
// its row by. The prefixes are the merge package's key families
// (internal/merge: combine_journal.go, group_redirect.go, pending_repair.go) and
// the dead-index prefixes retention-and-hygiene sweeps. A key in none of them
// has no family, so its write is refused: the same SetRaw is a different row on
// every key family and a caller that cannot say which gets none.
var jobsRawFamilies = []struct{ prefix, family string }{
	{"merge:combine-journal:", "merge_journal"},
	{"merge:combine-undo-applied:", "merge_journal"},
	{"merge:group-redirect:", "group_redirect"},
	{"merge_user_state_pending:", "user_state"},
	{"merge:survivor-reconcile:", "user_state"},
	{"book:series:", "dead_book_index"},
	{"book:author:", "dead_book_index"},
}

// jobsRawLabel is the gate label of a raw write on key: verb/family, or
// verb/unclassified (no row) when the key is in no family.
func jobsRawLabel(verb, key string) string {
	for _, f := range jobsRawFamilies {
		if strings.HasPrefix(key, f.prefix) {
			return verb + "/" + f.family
		}
	}
	return verb + "/unclassified"
}

// newJobsDeps is the books module's jobs.KindDeps over src, the store the jobs
// ran against before (jobs.Store). A nil src, or one that lacks a capability a
// job reaches, gets no NewStore and the error says which.
func newJobsDeps(src jobs.Store) (jobs.KindDeps, error) {
	if src == nil {
		return jobs.KindDeps{}, errors.New("maintenance jobs: no store")
	}
	caps, err := resolveJobsCaps(src)
	if err != nil {
		return jobs.KindDeps{}, err
	}
	rows := jobsRows()
	return jobs.KindDeps{NewStore: func(w tasks.Writer) jobs.Store {
		return &booksJobsStore{jobsReads: src, src: src, caps: caps, gate: newBooksGate(w, rows)}
	}}, nil
}

// booksJobsStore is one run's store view. jobsReads is the embedded read
// interface (no writes by construction); src is only ever called inside a gated
// write, and is never returned or promoted.
type booksJobsStore struct {
	jobsReads
	src  jobs.Store
	caps jobsCaps
	gate booksGate
}

var _ jobs.Store = (*booksJobsStore)(nil)

func (w *booksJobsStore) SetRaw(key string, value []byte) error {
	return w.gate.do(runless(), jobsRawLabel("SetRaw", key), kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.SetRaw(key, value)
	})
}

func (w *booksJobsStore) DeleteRaw(key string) error {
	return w.gate.do(runless(), jobsRawLabel("DeleteRaw", key), kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.DeleteRaw(key)
	})
}

// The capabilities. Each is the jobs' own assertion target, implemented here so
// it holds on the wrapper.

func (w *booksJobsStore) MintOrGetSyncID(bookID string) (id string, err error) {
	err = w.gate.do(runless(), "MintOrGetSyncID", bookRef(bookID), func(context.Context) error {
		var e error
		id, e = w.caps.sync.MintOrGetSyncID(bookID)
		return e
	})
	return id, err
}

func (w *booksJobsStore) GetSyncIDForBook(bookID string) (string, bool, error) {
	return w.caps.sync.GetSyncIDForBook(bookID)
}

func (w *booksJobsStore) ResolveSyncItem(syncID string) (*database.SyncItem, error) {
	return w.caps.sync.ResolveSyncItem(syncID)
}

// RepointSyncItem has no row in the shared table (no job writes it), so it is
// refused: a job that starts to need it must declare it first.
func (w *booksJobsStore) RepointSyncItem(oldBookID, newBookID string) error {
	return w.gate.do(runless(), "RepointSyncItem", bookRef(newBookID), func(context.Context) error {
		return w.caps.sync.RepointSyncItem(oldBookID, newBookID)
	})
}

func (w *booksJobsStore) RecordSyncMerge(loserBookID, winnerBookID string) error {
	return w.gate.do(runless(), "RecordSyncMerge", bookRef(winnerBookID), func(context.Context) error {
		return w.caps.sync.RecordSyncMerge(loserBookID, winnerBookID)
	})
}

func (w *booksJobsStore) ListSyncAliases(syncID string) ([]string, error) {
	return w.caps.sync.ListSyncAliases(syncID)
}

func (w *booksJobsStore) MintOrGetSyncFileID(bookID, fileID string) (id string, err error) {
	err = w.gate.do(runless(), "MintOrGetSyncFileID", bookRef(bookID), func(context.Context) error {
		var e error
		id, e = w.caps.syncFile.MintOrGetSyncFileID(bookID, fileID)
		return e
	})
	return id, err
}

func (w *booksJobsStore) MintOrGetSyncFileIDs(bookID string, fileIDs []string) (ids map[string]string, err error) {
	err = w.gate.do(runless(), "MintOrGetSyncFileIDs", bookRef(bookID), func(context.Context) error {
		var e error
		ids, e = w.caps.syncFile.MintOrGetSyncFileIDs(bookID, fileIDs)
		return e
	})
	return ids, err
}

func (w *booksJobsStore) GetSyncFileID(bookID, fileID string) (string, bool, error) {
	return w.caps.syncFile.GetSyncFileID(bookID, fileID)
}

func (w *booksJobsStore) ListSyncFilesForBook(bookID string) ([]database.SyncFile, error) {
	return w.caps.syncFile.ListSyncFilesForBook(bookID)
}

func (w *booksJobsStore) RepointSyncFile(bookID, oldFileID, newFileID string) error {
	return w.gate.do(runless(), "RepointSyncFile", bookRef(bookID), func(context.Context) error {
		return w.caps.syncFile.RepointSyncFile(bookID, oldFileID, newFileID)
	})
}

func (w *booksJobsStore) RepointSyncFileToBook(oldBookID, newBookID, fileID string) error {
	return w.gate.do(runless(), "RepointSyncFileToBook", bookRef(newBookID), func(context.Context) error {
		return w.caps.syncFile.RepointSyncFileToBook(oldBookID, newBookID, fileID)
	})
}

func (w *booksJobsStore) CreateBookmark(b progress.Bookmark) error {
	return w.gate.do(runless(), "CreateBookmark", kindRef(ops.ResUserState), func(context.Context) error {
		return w.caps.bookmarks.CreateBookmark(b)
	})
}

func (w *booksJobsStore) ListBookmarks(userID, itemID string) ([]progress.Bookmark, error) {
	return w.caps.bookmarks.ListBookmarks(userID, itemID)
}

func (w *booksJobsStore) ListBookmarksForUser(userID string) ([]progress.Bookmark, error) {
	return w.caps.bookmarks.ListBookmarksForUser(userID)
}

func (w *booksJobsStore) UpdateBookmarkTitle(userID, itemID string, timeSec float64, newTitle string) error {
	return w.gate.do(runless(), "UpdateBookmarkTitle", kindRef(ops.ResUserState), func(context.Context) error {
		return w.caps.bookmarks.UpdateBookmarkTitle(userID, itemID, timeSec, newTitle)
	})
}

func (w *booksJobsStore) DeleteBookmark(userID, itemID string, timeSec float64) error {
	return w.gate.do(runless(), "DeleteBookmark", kindRef(ops.ResUserState), func(context.Context) error {
		return w.caps.bookmarks.DeleteBookmark(userID, itemID, timeSec)
	})
}

func (w *booksJobsStore) ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (out *database.BookFile, err error) {
	err = w.gate.do(runless(), "ModifyBookFileDeferAggregates", bookRef(bookID), func(context.Context) error {
		var e error
		out, e = w.caps.deferred.ModifyBookFileDeferAggregates(bookID, fileID, fn)
		return e
	})
	return out, err
}

func (w *booksJobsStore) IsBookAggregatesBackfillDone() bool {
	return w.caps.aggregates.IsBookAggregatesBackfillDone()
}

// MarkBookAggregatesBackfillDone has an empty row (the sentinel is not a
// resource a kind declares), so it runs unfenced and the job owns the mode: it
// marks only after a live run, as it did.
func (w *booksJobsStore) MarkBookAggregatesBackfillDone() error {
	return w.gate.do(runless(), "MarkBookAggregatesBackfillDone", kindRef(ops.ResBooks), func(context.Context) error {
		return w.caps.aggregates.MarkBookAggregatesBackfillDone()
	})
}

func (w *booksJobsStore) DeleteSeriesIfUnreferenced(id int, expectName string, expectAuthorID *int) (deleted bool, err error) {
	err = w.gate.do(runless(), "DeleteSeriesIfUnreferenced", kindRef(ops.ResSeries), func(context.Context) error {
		var e error
		deleted, e = w.caps.seriesDel.DeleteSeriesIfUnreferenced(id, expectName, expectAuthorID)
		return e
	})
	return deleted, err
}

func (w *booksJobsStore) SeriesDeleteReady() error { return w.caps.seriesReady.SeriesDeleteReady() }

func (w *booksJobsStore) PlanBookSnapshotPrune(id string, keepCount int) (database.BookSnapshotPrunePlan, error) {
	return w.caps.planner.PlanBookSnapshotPrune(id, keepCount)
}

func (w *booksJobsStore) AuditMergeJournalSnapshots(idCap int) (database.MergeJournalSnapshotAudit, error) {
	return w.caps.planner.AuditMergeJournalSnapshots(idCap)
}

func (w *booksJobsStore) CheckSnapshotPins() error { return w.caps.planner.CheckSnapshotPins() }

func (w *booksJobsStore) UpdateBookFileHashes(fileID, originalHash, postHash, fileHash string) error {
	return w.gate.do(runless(), "UpdateBookFileHashes", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.caps.hashes.UpdateBookFileHashes(fileID, originalHash, postHash, fileHash)
	})
}

func (w *booksJobsStore) AddBookUserTag(bookID, tag string) error {
	return w.gate.do(runless(), "AddBookUserTag", bookRef(bookID), func(context.Context) error {
		return w.src.AddBookUserTag(bookID, tag)
	})
}

func (w *booksJobsStore) BatchCreateBookFiles(files []*database.BookFile) error {
	return w.gate.do(runless(), "BatchCreateBookFiles", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.BatchCreateBookFiles(files)
	})
}

func (w *booksJobsStore) CreateAuthor(name string) (out *database.Author, err error) {
	err = w.gate.do(runless(), "CreateAuthor", kindRef(ops.ResAuthors), func(context.Context) error {
		var e error
		out, e = w.src.CreateAuthor(name)
		return e
	})
	return out, err
}

func (w *booksJobsStore) CreateBookFile(file *database.BookFile) error {
	return w.gate.do(runless(), "CreateBookFile", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.CreateBookFile(file)
	})
}

func (w *booksJobsStore) CreateOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "CreateOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

func (w *booksJobsStore) CreateOperationResult(result *database.OperationResult) error {
	return w.gate.do(runless(), "CreateOperationResult", kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.CreateOperationResult(result)
	})
}

func (w *booksJobsStore) DeleteBook(id string) error {
	return w.gate.do(runless(), "DeleteBook", bookRef(id), func(context.Context) error {
		return w.src.DeleteBook(id)
	})
}

func (w *booksJobsStore) DeleteOperationState(opID string) error {
	return w.gate.do(runless(), "DeleteOperationState", kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.DeleteOperationState(opID)
	})
}

func (w *booksJobsStore) DeleteOperationWithLogs(id string) error {
	return w.gate.do(runless(), "DeleteOperationWithLogs", kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.DeleteOperationWithLogs(id)
	})
}

func (w *booksJobsStore) DeleteRawBatch(keys []string) error {
	return w.gate.do(runless(), "DeleteRawBatch", kindRef(ops.ResAuthorityLists), func(context.Context) error {
		return w.src.DeleteRawBatch(keys)
	})
}

func (w *booksJobsStore) ModifyBook(id string, fn func(*database.Book) error) (out *database.Book, err error) {
	err = w.gate.do(runless(), "ModifyBook", bookRef(id), func(context.Context) error {
		var e error
		out, e = w.src.ModifyBook(id, fn)
		return e
	})
	return out, err
}

func (w *booksJobsStore) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) (out []database.BookAuthor, err error) {
	err = w.gate.do(runless(), "ModifyBookAuthors", bookRef(bookID), func(context.Context) error {
		var e error
		out, e = w.src.ModifyBookAuthors(bookID, fn)
		return e
	})
	return out, err
}

func (w *booksJobsStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (out *database.BookFile, err error) {
	err = w.gate.do(runless(), "ModifyBookFile", bookRef(bookID), func(context.Context) error {
		var e error
		out, e = w.src.ModifyBookFile(bookID, fileID, fn)
		return e
	})
	return out, err
}

func (w *booksJobsStore) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	return w.gate.do(runless(), "MoveBookFilesToBook", bookRef(targetBookID), func(context.Context) error {
		return w.src.MoveBookFilesToBook(fileIDs, sourceBookID, targetBookID)
	})
}

func (w *booksJobsStore) PruneBookSnapshots(id string, keepCount int) (out int, err error) {
	err = w.gate.do(runless(), "PruneBookSnapshots", bookRef(id), func(context.Context) error {
		var e error
		out, e = w.src.PruneBookSnapshots(id, keepCount)
		return e
	})
	return out, err
}

func (w *booksJobsStore) ReassignExternalIDs(oldBookID, newBookID string) error {
	return w.gate.do(runless(), "ReassignExternalIDs", bookRef(newBookID), func(context.Context) error {
		return w.src.ReassignExternalIDs(oldBookID, newBookID)
	})
}

func (w *booksJobsStore) RecomputeBookAggregates(bookID string) error {
	return w.gate.do(runless(), "RecomputeBookAggregates", bookRef(bookID), func(context.Context) error {
		return w.src.RecomputeBookAggregates(bookID)
	})
}

func (w *booksJobsStore) SaveOperationState(opID string, state []byte) error {
	return w.gate.do(runless(), "SaveOperationState", kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.SaveOperationState(opID, state)
	})
}

func (w *booksJobsStore) SaveOperationSummaryLog(op *database.OperationSummaryLog) error {
	return w.gate.do(runless(), "SaveOperationSummaryLog", kindRef(ops.ResOperations), func(context.Context) error {
		return w.src.SaveOperationSummaryLog(op)
	})
}

func (w *booksJobsStore) SetBookFileHash(id, hash string) error {
	return w.gate.do(runless(), "SetBookFileHash", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.SetBookFileHash(id, hash)
	})
}

func (w *booksJobsStore) SetSetting(key, value, typ string, isSecret bool) error {
	return w.gate.do(runless(), "SetSetting", kindRef(ops.ResSettings), func(context.Context) error {
		return w.src.SetSetting(key, value, typ, isSecret)
	})
}

func (w *booksJobsStore) SetUserBookState(state *database.UserBookState) error {
	return w.gate.do(runless(), "SetUserBookState", kindRef(ops.ResUserState), func(context.Context) error {
		return w.src.SetUserBookState(state)
	})
}

func (w *booksJobsStore) SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error {
	return w.gate.do(runless(), "SetUserPosition", bookRef(bookID), func(context.Context) error {
		return w.src.SetUserPosition(userID, bookID, segmentID, positionSeconds)
	})
}

func (w *booksJobsStore) UpdateBook(id string, book *database.Book) (out *database.Book, err error) {
	err = w.gate.do(runless(), "UpdateBook", bookRef(id), func(context.Context) error {
		var e error
		out, e = w.src.UpdateBook(id, book)
		return e
	})
	return out, err
}

func (w *booksJobsStore) UpdateBookFile(id string, file *database.BookFile) error {
	return w.gate.do(runless(), "UpdateBookFile", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.UpdateBookFile(id, file)
	})
}
