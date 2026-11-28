// file: internal/plugins/maintenance/deps_t6g5.go
// version: 0.1.0
// guid: 3e9a6c17-84d2-4f5b-b1a0-d57c2e8f9143
// last-edited: 2026-11-28

package maintenance

import (
	"context"
	"errors"
	"os"
	"reflect"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// G5ReadHost is what the host serves group 5's reads that is not an ops-store
// row: the library root, the metadata refresh scan, the operation queue, the
// clone plan, the iTunes path mapping, and the stores whose accessors the host
// holds (tags, chapters, playlists).
type G5ReadHost interface {
	RootDir() string
	RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error
	OperationQueueStore() OpQueueReader
	PlanLibraryClone(book *database.Book, files []database.BookFile) ([]string, error)
	LibraryITunesPath(path string) string
	BookTagReader() BookTagReader
	VersionPrimaryStore() VersionPrimaryStore
	PlaylistStore() database.UserPlaylistStore
}

// G5WriteHost is what the host serves group 5's writes that is not an ops-store
// method: the book merge and clone services, the merge user-state store, the
// metadata cache and the authors cache. Every write through it is a gated call.
type G5WriteHost interface {
	MergeBooks(opID string, bookIDs []string, primaryID string) (mergedCount int, err error)
	CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error)
	MergeUserStateStore() merge.UserStateRepairStore
	MetadataCacheStore() database.MetadataCacheStore
	InvalidateAuthorsCache()
}

// G5Host is both halves.
type G5Host interface {
	G5ReadHost
	G5WriteHost
}

// newT6G5Deps is the books module's t6g5Deps over the ops store src and the host.
// scanKey is the concurrency key of the library scan kinds, which an apply stands
// down. A nil src or host leaves Store and NewWriter unset: t6g5Kinds refuses a
// group with no store at the first run, and BooksTaskLists refuses them by name.
func newT6G5Deps(src OpsStore, host G5Host, scanKey tasks.Key) t6g5Deps {
	if src == nil || host == nil {
		return t6g5Deps{ScanKey: scanKey}
	}
	rows := groupRows(nil, reflect.TypeFor[t6g5Writer]())
	return t6g5Deps{
		Store: &booksT6G5Store{src: src, host: host},
		NewWriter: func(w tasks.Writer) t6g5Writer {
			return &booksT6G5Writer{src: src, host: host, gate: newBooksGate(w, rows)}
		},
		ScanKey: scanKey,
	}
}

// booksT6G5Store is the group's reads: exactly the methods of t6g5Store, none of
// which writes.
type booksT6G5Store struct {
	src  OpsStore
	host G5Host
}

// retired is the store's retired-row reader capability, or the refusal HEAD's
// regroup gave when the store cannot list retired rows.
func (s *booksT6G5Store) retired() (database.RetiredBookFileReader, error) {
	r, ok := database.AsCapability[database.RetiredBookFileReader](s.src)
	if !ok {
		return nil, errors.New("the store cannot list book_file rows including retired ones")
	}
	return r, nil
}

func (s *booksT6G5Store) BookFileRowsAtPathStrict(path string) ([]database.BookFile, error) {
	lk, ok := database.AsCapability[database.BookFilePathLookup](s.src)
	if !ok {
		return nil, errors.New("the store cannot look up which rows name a path")
	}
	return database.BookFileRowsAtPathStrict(lk, path)
}

func (s *booksT6G5Store) BooksLinkingNarrators(ids map[int]bool) (map[string][]database.BookNarrator, error) {
	return database.BooksLinkingNarrators(s.src, ids)
}

func (s *booksT6G5Store) CountSmartPlaylists() (int, error) {
	_, total, err := s.src.ListUserPlaylists("smart", 1, 0)
	return total, err
}

func (s *booksT6G5Store) GetAllAuthors() ([]database.Author, error) {
	return s.src.GetAllAuthors()
}

func (s *booksT6G5Store) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.src)
}

func (s *booksT6G5Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.src.GetAllBookFilesCore()
}

func (s *booksT6G5Store) GetAllBooksCoreComplete(limit int, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCoreComplete(limit, offset)
}

func (s *booksT6G5Store) GetAllBooksCore(limit int, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCore(limit, offset)
}

func (s *booksT6G5Store) GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error) {
	return s.src.GetAllBooksFullFrom(afterID, limit)
}

func (s *booksT6G5Store) GetAllPreferencesForUser(userID string) ([]database.UserPreferenceKV, error) {
	return s.src.GetAllPreferencesForUser(userID)
}

func (s *booksT6G5Store) GetAllSeries() ([]database.Series, error) {
	return s.src.GetAllSeries()
}

func (s *booksT6G5Store) GetAuthorByID(id int) (*database.Author, error) {
	return s.src.GetAuthorByID(id)
}

func (s *booksT6G5Store) GetAuthorByName(name string) (*database.Author, error) {
	return s.src.GetAuthorByName(name)
}

func (s *booksT6G5Store) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	return s.src.GetBookAuthors(bookID)
}

func (s *booksT6G5Store) GetBookByID(id string) (*database.Book, error) {
	return s.src.GetBookByID(id)
}

func (s *booksT6G5Store) GetBookFileByID(bookID string, fileID string) (*database.BookFile, error) {
	return s.src.GetBookFileByID(bookID, fileID)
}

func (s *booksT6G5Store) GetBookFileByPIDIncludingRetired(itunesPID string) (*database.BookFile, error) {
	r, err := s.retired()
	if err != nil {
		return nil, err
	}
	return r.GetBookFileByPIDIncludingRetired(itunesPID)
}

func (s *booksT6G5Store) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	r, err := s.retired()
	if err != nil {
		return nil, err
	}
	return r.GetBookFileByPathIncludingRetired(path)
}

func (s *booksT6G5Store) GetBookFilesIncludingRetired(bookID string) ([]database.BookFile, error) {
	r, err := s.retired()
	if err != nil {
		return nil, err
	}
	return r.GetBookFilesIncludingRetired(bookID)
}

func (s *booksT6G5Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.src.GetBookFiles(bookID)
}

func (s *booksT6G5Store) GetBookNarrators(bookID string) ([]database.BookNarrator, error) {
	return s.src.GetBookNarrators(bookID)
}

func (s *booksT6G5Store) GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error) {
	tr := s.host.BookTagReader()
	if tr == nil {
		return nil, errT6g5NoStore
	}
	return tr.GetBookTagsByBookIDs(bookIDs)
}

func (s *booksT6G5Store) GetBookTagsDetailed(bookID string) ([]database.BookTag, error) {
	tr := s.host.BookTagReader()
	if tr == nil {
		return nil, errT6g5NoStore
	}
	return tr.GetBookTagsDetailed(bookID)
}

func (s *booksT6G5Store) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	return s.src.GetBooksByVersionGroup(groupID)
}

func (s *booksT6G5Store) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	vps := s.host.VersionPrimaryStore()
	if vps == nil {
		return nil, errT6g5NoStore
	}
	return vps.GetChaptersForBook(bookID)
}

func (s *booksT6G5Store) GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error) {
	return s.src.GetExternalIDsForBook(bookID)
}

func (s *booksT6G5Store) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.src.GetMetadataFieldStates(bookID)
}

func (s *booksT6G5Store) GetRetiredBookFiles(holderID string) ([]database.BookFile, error) {
	r, err := s.retired()
	if err != nil {
		return nil, err
	}
	return r.GetRetiredBookFiles(holderID)
}

func (s *booksT6G5Store) GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error) {
	r, err := s.retired()
	if err != nil {
		return nil, err
	}
	return r.GetRetiredVersionsFromBook(shellID)
}

func (s *booksT6G5Store) GetSeriesByID(id int) (*database.Series, error) {
	return s.src.GetSeriesByID(id)
}

func (s *booksT6G5Store) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.src.GetUserPreference(key)
}

func (s *booksT6G5Store) LibraryITunesPath(path string) string {
	return s.host.LibraryITunesPath(path)
}

func (s *booksT6G5Store) LibraryReadPath() string {
	return config.Snapshot().ITunes.LibraryReadPath
}

func (s *booksT6G5Store) ListMetadataCacheKeys() ([]database.MetadataCacheSummary, error) {
	c := s.host.MetadataCacheStore()
	if c == nil {
		return nil, errT6g5NoStore
	}
	return c.ListMetadataCacheKeys()
}

func (s *booksT6G5Store) ListNarrators() ([]database.Narrator, error) {
	return s.src.ListNarrators()
}

func (s *booksT6G5Store) NarratorLinkCount(narratorID int) (int, error) {
	return database.NarratorLinkCount(s.src, narratorID)
}

func (s *booksT6G5Store) NarratorRefCounts() (database.NarratorRefs, error) {
	return database.NarratorRefCounts(s.src)
}

func (s *booksT6G5Store) PlanLibraryClone(book *database.Book, files []database.BookFile) ([]string, error) {
	return s.host.PlanLibraryClone(book, files)
}

func (s *booksT6G5Store) ProbeSmartPlaylists(lib *itunes.ITLLibrary, ownerUserID string) itunesservice.PlaylistImportResult {
	return itunesservice.NewPlaylistImporter(s.host.PlaylistStore()).MigrateSmartPlaylists(lib,
		itunesservice.PlaylistImportOptions{OwnerUserID: ownerUserID, DryRun: true})
}

func (s *booksT6G5Store) RefuseWhileLibraryScanActive(opName string) error {
	return refuseWhileLibraryScanActive(s.host.OperationQueueStore(), opName)
}

func (s *booksT6G5Store) RootDir() string {
	return s.host.RootDir()
}

func (s *booksT6G5Store) RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error {
	return s.host.RunMetadataRefreshScan(ctx, progress)
}

// booksT6G5Writer is one run's gated writes: exactly the methods of t6g5Writer.
// Each is ONE gated call stamped with the method's whole row of the shared table
// (groupRows). The three methods with an empty row (HardDeleteWithoutUserState,
// NewUserStateProbe, InvalidateAuthorsCache) write nothing themselves: the first
// runs a delete the CALLER makes through this writer, and that delete is gated.
type booksT6G5Writer struct {
	src  OpsStore
	host G5Host
	gate booksGate
}

// userState is the host's merge user-state store, or HEAD's "database not
// initialized".
func (w *booksT6G5Writer) userState() (merge.UserProgressMerger, error) {
	um := w.host.MergeUserStateStore()
	if um == nil {
		return nil, errT6g5NoStore
	}
	return um, nil
}

func (w *booksT6G5Writer) CreateBook(book *database.Book) (*database.Book, error) {
	return gatedValue(w.gate, "CreateBook", kindRef(ops.ResBooks), func() (*database.Book, error) {
		return w.src.CreateBook(book)
	})
}

func (w *booksT6G5Writer) CreateNarrator(name string) (*database.Narrator, error) {
	return gatedValue(w.gate, "CreateNarrator", kindRef(ops.ResBooks), func() (*database.Narrator, error) {
		return w.src.CreateNarrator(name)
	})
}

func (w *booksT6G5Writer) DeleteNarrator(id int) error {
	return w.gate.do(runless(), "DeleteNarrator", kindRef(ops.ResBooks), func(context.Context) error {
		return w.src.DeleteNarrator(id)
	})
}

func (w *booksT6G5Writer) DeleteBook(id string) error {
	return w.gate.do(runless(), "DeleteBook", bookRef(id), func(context.Context) error {
		return w.src.DeleteBook(id)
	})
}

func (w *booksT6G5Writer) DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error) {
	return gatedValue(w.gate, "DeleteBookFilesByIDs", kindRef(ops.ResBookFiles), func() ([]database.BookFile, error) {
		return w.src.DeleteBookFilesByIDs(refs)
	})
}

func (w *booksT6G5Writer) DeleteMetadataCache(bookID string) error {
	return w.gate.do(runless(), "DeleteMetadataCache", bookRef(bookID), func(context.Context) error {
		c := w.host.MetadataCacheStore()
		if c == nil {
			return errT6g5NoStore
		}
		return c.DeleteMetadataCache(bookID)
	})
}

func (w *booksT6G5Writer) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return gatedValue(w.gate, "ModifyBook", bookRef(id), func() (*database.Book, error) {
		return w.src.ModifyBook(id, fn)
	})
}

func (w *booksT6G5Writer) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return gatedValue(w.gate, "ModifyBookFile", bookRef(bookID), func() (*database.BookFile, error) {
		return w.src.ModifyBookFile(bookID, fileID, fn)
	})
}

func (w *booksT6G5Writer) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	return gatedValue(w.gate, "UpdateBook", bookRef(id), func() (*database.Book, error) {
		return w.src.UpdateBook(id, book)
	})
}

func (w *booksT6G5Writer) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (n int, err error) {
	err = w.gate.do(ctx, "UpdateBookFiles", kindRef(ops.ResBookFiles), func(ctx context.Context) error {
		var e error
		n, e = w.src.UpdateBookFiles(ctx, files, afterRow)
		return e
	})
	return n, err
}

func (w *booksT6G5Writer) RecomputeBookAggregates(bookID string) error {
	return w.gate.do(runless(), "RecomputeBookAggregates", bookRef(bookID), func(context.Context) error {
		return w.src.RecomputeBookAggregates(bookID)
	})
}

// SetLibraryStateOrganized is setLibraryStateOrganized over the store's
// ModifyBook, the one write of a compare-and-set, as HEAD's adapter is.
func (w *booksT6G5Writer) SetLibraryStateOrganized(id, from string) (found, applied bool, err error) {
	err = w.gate.do(runless(), "SetLibraryStateOrganized", bookRef(id), func(context.Context) error {
		var e error
		found, applied, e = setLibraryStateOrganized(w.src.ModifyBook, id, from)
		return e
	})
	return found, applied, err
}

func (w *booksT6G5Writer) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	return w.gate.do(runless(), "MoveBookFilesToBook", bookRef(targetBookID), func(context.Context) error {
		return w.src.MoveBookFilesToBook(fileIDs, sourceBookID, targetBookID)
	})
}

func (w *booksT6G5Writer) MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error {
	return w.gate.do(runless(), "MoveBookFilesToBookBulk", bookRef(targetBookID), func(context.Context) error {
		return w.src.MoveBookFilesToBookBulk(moves, targetBookID)
	})
}

func (w *booksT6G5Writer) ReassignExternalID(source, externalID, newBookID string) error {
	return w.gate.do(runless(), "ReassignExternalID", bookRef(newBookID), func(context.Context) error {
		return w.src.ReassignExternalID(source, externalID, newBookID)
	})
}

func (w *booksT6G5Writer) RecordOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "RecordOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

// SaveChaptersForBook refuses a store that cannot write chapter tables.
func (w *booksT6G5Writer) SaveChaptersForBook(bookID string, chapters []database.Chapter) error {
	return w.gate.do(runless(), "SaveChaptersForBook", bookRef(bookID), func(context.Context) error {
		cp, ok := database.AsCapability[chapterPersister](w.src)
		if !ok {
			return errors.New("store cannot write chapter tables")
		}
		return cp.SaveChaptersForBook(bookID, chapters)
	})
}

func (w *booksT6G5Writer) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	return w.gate.do(runless(), "SetBookAuthors", bookRef(bookID), func(context.Context) error {
		return w.src.SetBookAuthors(bookID, authors)
	})
}

func (w *booksT6G5Writer) SetBookNarrators(bookID string, narrators []database.BookNarrator) error {
	return w.gate.do(runless(), "SetBookNarrators", bookRef(bookID), func(context.Context) error {
		return w.src.SetBookNarrators(bookID, narrators)
	})
}

func (w *booksT6G5Writer) SetUserPreferenceForUser(userID, key, value string) error {
	return w.gate.do(runless(), "SetUserPreferenceForUser", kindRef(ops.ResUserState), func(context.Context) error {
		return w.src.SetUserPreferenceForUser(userID, key, value)
	})
}

// MergeBooks is the host's book merge service: it merges the books and carries
// their state and external ids, which is the table's whole row.
func (w *booksT6G5Writer) MergeBooks(opID string, bookIDs []string, primaryID string) (int, error) {
	return gatedValue(w.gate, "MergeBooks", bookRef(primaryID), func() (int, error) {
		return w.host.MergeBooks(opID, bookIDs, primaryID)
	})
}

func (w *booksT6G5Writer) CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error) {
	return gatedValue(w.gate, "CloneBookIntoLibrary", kindRef(ops.ResBooks), func() (string, error) {
		return w.host.CloneBookIntoLibrary(book, files, opID)
	})
}

// ImportSmartPlaylists has no error to return (HEAD's signature, which a group
// may not re-sign), so a refused write is the zero result and a log line: the
// kind re-reads the playlist count after an apply and reports that, not this.
func (w *booksT6G5Writer) ImportSmartPlaylists(lib *itunes.ITLLibrary, ownerUserID string) itunesservice.PlaylistImportResult {
	res, err := gatedValue(w.gate, "ImportSmartPlaylists", kindRef(ops.ResUserState), func() (itunesservice.PlaylistImportResult, error) {
		store := w.host.PlaylistStore()
		if store == nil {
			return itunesservice.PlaylistImportResult{}, errT6g5NoStore
		}
		return itunesservice.NewPlaylistImporter(store).MigrateSmartPlaylists(lib,
			itunesservice.PlaylistImportOptions{OwnerUserID: ownerUserID, DryRun: false}), nil
	})
	if err != nil {
		logger.New("maintenance").Warn("itunes-playlist-import: the playlist write was refused: %v", err)
	}
	return res
}

func (w *booksT6G5Writer) Reflink(src, dst string) error {
	return w.gate.do(runless(), "Reflink", kindRef(ops.ResFiles), func(context.Context) error {
		return fileops.Reflink(src, dst)
	})
}

func (w *booksT6G5Writer) RemoveClonePath(path string) error {
	return w.gate.do(runless(), "RemoveClonePath", kindRef(ops.ResFiles), func(context.Context) error {
		return os.Remove(path)
	})
}

func (w *booksT6G5Writer) CarryStateBeforeHardDelete(keepID, doomedID string) error {
	return w.gate.do(runless(), "CarryStateBeforeHardDelete", bookRef(keepID), func(context.Context) error {
		um, err := w.userState()
		if err != nil {
			return err
		}
		return merge.CarryStateBeforeHardDelete(um, keepID, doomedID)
	})
}

func (w *booksT6G5Writer) CarryStateBetweenLiveBooks(toID, fromID string) error {
	return w.gate.do(runless(), "CarryStateBetweenLiveBooks", bookRef(toID), func(context.Context) error {
		um, err := w.userState()
		if err != nil {
			return err
		}
		return merge.CarryStateBetweenLiveBooks(um, toID, fromID)
	})
}

// CarryStateThenHardDelete is one gated call: the carry, the precheck and the
// delete run inside it, and the table's row for it includes the delete's.
func (w *booksT6G5Writer) CarryStateThenHardDelete(keepID, doomedID string, precheck, del func() error) error {
	return w.gate.do(runless(), "CarryStateThenHardDelete", bookRef(keepID), func(context.Context) error {
		um, err := w.userState()
		if err != nil {
			return err
		}
		return merge.CarryStateThenHardDelete(um, keepID, doomedID, precheck, del)
	})
}

// HardDeleteWithoutUserState runs del under the merge lock. Its row is empty:
// it writes nothing itself, and del is a delete the caller makes through this
// writer, which is gated.
func (w *booksT6G5Writer) HardDeleteWithoutUserState(bookID string, del func() error) error {
	return w.gate.do(runless(), "HardDeleteWithoutUserState", bookRef(bookID), func(context.Context) error {
		um, err := w.userState()
		if err != nil {
			return err
		}
		return merge.HardDeleteWithoutUserState(um, bookID, del)
	})
}

// NewUserStateProbe reads only (empty row).
func (w *booksT6G5Writer) NewUserStateProbe() (t6g5RegroupStateProbe, error) {
	um, err := w.userState()
	if err != nil {
		return nil, err
	}
	// Not `return merge.NewUserStateProbe(um)`: on error that wraps a nil
	// *UserStateProbe in a non-nil interface.
	p, err := merge.NewUserStateProbe(um)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// InvalidateAuthorsCache drops the derived authors cache; it writes no row.
func (w *booksT6G5Writer) InvalidateAuthorsCache() { w.host.InvalidateAuthorsCache() }
