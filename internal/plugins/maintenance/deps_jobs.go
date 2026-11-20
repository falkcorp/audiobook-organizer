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

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
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
	jobsReadBooks
	jobsReadBookDetail
	jobsReadBookFiles
	jobsReadContributors
	jobsReadUserState
	jobsReadOperations
	jobsReadKV
	jobsReadLinked
}

// The read groups follow jobs.Store's store*Store grouping, each at most 8
// declared entries (the interface-width gate, .golangci.yml interfacebloat.max).

// jobsReadBooks is the book reads (jobs.Store's storeBookReader).
type jobsReadBooks interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	ListBookIDs() ([]string, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// jobsReadBookDetail is the per-book detail reads (storeManualOnlyReader and the metadata field states).
type jobsReadBookDetail interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
	GetSeriesByID(id int) (*database.Series, error)
}

// jobsReadBookFiles is the book-file reads (storeBookFileReader).
type jobsReadBookFiles interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
}

// jobsReadContributors is the author and series reads (storeContributorStore's reads).
type jobsReadContributors interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
}

// jobsReadUserState is the user-state reads (storeUserStateStore's reads).
type jobsReadUserState interface {
	ListUsers() ([]database.User, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	GetBookUserTags(bookID string) ([]string, error)
}

// jobsReadOperations is the operation record, state and undo-ledger reads (storeOperationStore's reads).
type jobsReadOperations interface {
	GetOperationByID(id string) (*database.Operation, error)
	GetOperationV2(id string) (*database.OperationV2Row, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
	ListOperations(limit, offset int) ([]database.Operation, int, error)
	GetOperationState(opID string) ([]byte, error)
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// jobsReadKV is the settings and raw key reads (storeKVStore's reads).
type jobsReadKV interface {
	GetSetting(key string) (*database.Setting, error)
	GetUserPreference(key string) (*database.UserPreference, error)
	GetRaw(key string) ([]byte, error)
	CountPrefix(prefix string) (int64, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
	ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error)
}

// jobsReadLinked is the external-id and chapter reads.
type jobsReadLinked interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
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
//
// One field per capability, never one wide interface for several: AsCapability
// answers with the first layer of the chain that has the WHOLE interface, so a
// combined one is satisfied by no layer when its methods live on two (the Pebble
// store holds most of these, a decorator forwards only the ones it knows).
// store is the exception on purpose: every method in jobsStoreOps is a method of
// database.Store, which every layer has.
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

	store          jobsStoreOps
	bookmarkCopier database.BookmarkCopier
	syncMerge      jobsSyncMergeClearer
	syncRedirect   jobsSyncRedirectClearer
	strictUsers    database.StrictUserLister
	replacer       database.UserPositionReplacer
	stateDeleter   database.UserBookStateDeleter
	positionAt     database.UserPositionTimestampWriter
	opScan         jobsOpChangeScanner
	retired        database.RetiredBookFileReader
	credits        database.BookCreditsModifier
	retiredProbe   jobsRetiredProbe
	seriesRefs     database.SeriesBookRefStore
	seriesMembers  database.SeriesMembershipStore
	provenance     database.FileProvenanceRecorder
	identDirty     database.IdentDirtyMarker
	// metrics is the Pebble metrics store the sweep deletes through. It is a
	// concrete type because that is what internal/books/store builds from a
	// store; the wrapper's SweepExpiredMetrics is the only call made on it.
	metrics *database.PebbleMetricsStore
	// titleKey is the identity authorcredit caches its title index under. It is
	// opaque (no method reads the store back out of it), so handing it out is not
	// a way to reach the store.
	titleKey authorcredit.TitleKey
}

// capOf resolves one capability, noting its name when src lacks it.
func capOf[T any](src any, name string, missing *[]string) T {
	c, ok := database.AsCapability[T](src)
	if !ok {
		*missing = append(*missing, name)
	}
	return c
}

// resolveJobsCaps resolves every capability or names every one src lacks.
func resolveJobsCaps(src any) (jobsCaps, error) {
	var c jobsCaps
	var missing []string
	c.sync = capOf[database.SyncIdentityStore](src, "SyncIdentityStore", &missing)
	c.syncFile = capOf[database.SyncFileStore](src, "SyncFileStore", &missing)
	c.bookmarks = capOf[database.BookmarkStore](src, "BookmarkStore", &missing)
	c.deferred = capOf[jobsDeferredAggregates](src, "ModifyBookFileDeferAggregates", &missing)
	c.aggregates = capOf[jobsAggregatesMarker](src, "aggregates backfill sentinel", &missing)
	c.credits = capOf[database.BookCreditsModifier](src, "BookCreditsModifier", &missing)
	c.seriesDel = capOf[database.SeriesUnreferencedDeleter](src, "SeriesUnreferencedDeleter", &missing)
	c.seriesReady = capOf[database.SeriesDeleteReadiness](src, "SeriesDeleteReadiness", &missing)
	c.planner = capOf[database.SnapshotPrunePlanner](src, "SnapshotPrunePlanner", &missing)
	c.hashes = capOf[database.BookFileHashUpdater](src, "BookFileHashUpdater", &missing)

	c.store = capOf[jobsStoreOps](src, "the database.Store methods merge and dedup reach", &missing)
	c.bookmarkCopier = capOf[database.BookmarkCopier](src, "BookmarkCopier", &missing)
	c.syncMerge = capOf[jobsSyncMergeClearer](src, "ClearSyncMerge", &missing)
	c.syncRedirect = capOf[jobsSyncRedirectClearer](src, "ClearSyncRedirect", &missing)
	c.strictUsers = capOf[database.StrictUserLister](src, "StrictUserLister", &missing)
	c.replacer = capOf[database.UserPositionReplacer](src, "UserPositionReplacer", &missing)
	c.stateDeleter = capOf[database.UserBookStateDeleter](src, "UserBookStateDeleter", &missing)
	c.positionAt = capOf[database.UserPositionTimestampWriter](src, "UserPositionTimestampWriter", &missing)
	c.opScan = capOf[jobsOpChangeScanner](src, "ScanOperationChanges", &missing)
	c.retired = capOf[database.RetiredBookFileReader](src, "RetiredBookFileReader", &missing)
	c.retiredProbe = capOf[jobsRetiredProbe](src, "HasRetiredVersions", &missing)
	c.seriesRefs = capOf[database.SeriesBookRefStore](src, "SeriesBookRefStore", &missing)
	c.seriesMembers = capOf[database.SeriesMembershipStore](src, "SeriesMembershipStore", &missing)
	c.provenance = capOf[database.FileProvenanceRecorder](src, "FileProvenanceRecorder", &missing)
	c.identDirty = capOf[database.IdentDirtyMarker](src, "IdentDirtyMarker", &missing)
	if c.metrics = database.NewPebbleMetricsStoreFromStore(src); c.metrics == nil {
		missing = append(missing, "the Pebble metrics store")
	}
	c.titleKey = titleKeyOf(src)

	if len(missing) > 0 {
		return jobsCaps{}, fmt.Errorf("maintenance jobs: store %T lacks %s; no job store is built, so every maintenance run fails before it starts",
			src, strings.Join(missing, ", "))
	}
	return c, nil
}

// titleKeyOf is the key authorcredit caches src's title index under: the key of
// the layer a bare store resolves authorcredit.TitleSource at (that layer's own
// key when it names one, else that layer). Cached under the job store's own
// value, a store built for every run would leave one library-sized title index
// behind per run, and nothing evicts it.
func titleKeyOf(src any) authorcredit.TitleKey {
	layer := src
	if ts, ok := database.AsCapability[authorcredit.TitleSource](src); ok {
		layer = ts
	}
	if k, ok := layer.(authorcredit.TitleCacheKeyer); ok {
		return k.TitleCacheKey()
	}
	return authorcredit.TitleKeyOf(layer)
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
	// capability writes with a row in the shared table
	"MintOrGetSyncID", "RecordSyncMerge", "MintOrGetSyncFileID", "RepointSyncFileToBook", "CreateBookmark",
	"ModifyBookFileDeferAggregates", "MarkBookAggregatesBackfillDone", "DeleteSeriesIfUnreferenced", "UpdateBookFileHashes",
	"AppendFileEvent", "ClearUserPositions", "CopyBookmarkIfAbsent", "DeleteSeries", "SetBookAuthors", "SetUserPositionAt",
	"SweepExpiredMetrics", "ModifyBookCredits",
}

// jobsRefusedLabels is every write the wrapper carries that has NO row in the
// shared table, so the gate refuses it (ErrNoRow, wrapping ErrOutsideEffects)
// and the store is never called. Each is a method of a capability a job reaches
// (the merge package resolves most of them at runtime), implemented so that the
// reach is a refusal naming the method instead of a capability that quietly is
// not there (a consumer that tests for the capability takes its own fallback,
// and the fallback is not always the same operation). A row is added to
// resources.Table() (and rowsPinned) by the row that declares the writer a
// job's Effects must admit; this list shrinks as that happens, and TestBooksJobsStore_RefusedLabelsHaveNoRowAndNeverReachTheStore
// fails on a label that gains a row while still listed.
var jobsRefusedLabels = []string{
	"AddBookTagWithSource", "ClearSyncMerge", "ClearSyncRedirect", "DeleteBookmark", "DeleteUserBookState",
	"MarkIdentDirty", "MintOrGetSyncFileIDs", "RemoveBookTagsByPrefix", "RepointSyncFile", "RepointSyncItem",
	"ModifyBookNarrators", "ReplaceUserPositions", "RevertBookToVersion", "UpdateBookmarkTitle", "UpdateSeriesName",
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
// (internal/merge: combine_journal.go, retire_link.go, sibling_journal.go,
// group_redirect.go, pending_repair.go; TestJobsRawFamiliesCoverEveryMergePrefix
// fails when the package gains one this list lacks) and
// the dead-index prefixes retention-and-hygiene sweeps. A key in none of them
// has no family, so its write is refused: the same SetRaw is a different row on
// every key family and a caller that cannot say which gets none.
var jobsRawFamilies = []struct{ prefix, family string }{
	{"merge:combine-journal:", "merge_journal"},
	{"merge:combine-undo-applied:", "merge_journal"},
	{"merge:combine-retire:", "merge_journal"},
	{"merge:sibling-journal:", "merge_journal"},
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
