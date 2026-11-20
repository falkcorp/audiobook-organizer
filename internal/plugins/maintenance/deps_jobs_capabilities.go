// file: internal/plugins/maintenance/deps_jobs_capabilities.go
// version: 0.1.0
// guid: c41f8e63-a9d2-4b7e-8f05-6e2b9d1a3c78
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The capabilities of the jobs' store beyond jobs.Store (F5-18c, round 2).
//
// Three jobs hand their store to code that resolves optional capabilities from
// it (merge.Service, the dedup engine's store interface, authorcredit, the
// retired-version helpers), and a capability this value lacks is, to that code,
// a store without it. The set is not written down by hand: the scan in
// deps_jobs_capscan_test.go finds every resolution site the jobs reach and
// TestEveryCapabilityTheJobsResolveIsClassified compares each interface's
// method set, from the source, with this type's.
//
// Every method here is one of three things:
//
//   - a read, forwarded;
//   - a write with a row in the shared table (jobsWriteLabels), one gated call;
//   - a write with no row (jobsRefusedLabels), which the gate refuses.

// jobsStoreOps is the methods of database.Store that the merge package and the
// dedup engine reach and that jobs.Store does not carry. Every one is a method
// of database.Store, so every layer of a decorator chain has them and one
// resolution is enough (see jobsCaps). It is six groups of at most eight, and
// composed of them, so that no declaration is wider than the interface-width
// gate allows (.golangci.yml interfacebloat.max: 8).
type jobsStoreOps interface {
	jobsBookReads
	jobsDuplicateReads
	jobsAuthorReads
	jobsMergeReads
	jobsBookWrites
	jobsSeriesWrites
}

type jobsBookReads interface {
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
	GetBookAtVersion(id string, ts time.Time) (*database.Book, error)
	GetBookFileByAcoustID(fingerprint string) (*database.BookFile, error)
	GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error)
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
}

type jobsDuplicateReads interface {
	GetDuplicateBooks() ([][]database.Book, error)
	GetDuplicateBooksByMetadataCore(threshold float64) ([][]database.BookCore, error)
	GetFolderDuplicatesCore() ([][]database.BookCore, error)
}

type jobsAuthorReads interface {
	FindAuthorByAlias(aliasName string) (*database.Author, error)
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

type jobsMergeReads interface {
	GetUserByID(id string) (*database.User, error)
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
	ListUserPlaylists(playlistType string, limit, offset int) ([]database.UserPlaylist, int, error)
	LiveBookPathsUnderDir(dir string) (map[string]string, error)
}

type jobsBookWrites interface {
	AddBookTagWithSource(bookID, tag, source string) error
	ClearUserPositions(userID, bookID string) error
	RemoveBookTagsByPrefix(bookID, prefix, source string) error
	RevertBookToVersion(id string, ts time.Time) (*database.Book, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

type jobsSeriesWrites interface {
	DeleteSeries(id int) error
	UpdateSeriesName(id int, name string) error
}

// The shapes of the merge package's and the store package's private
// capability interfaces, which this package cannot name. The classification
// test compares the real interface, from the source, against this type's method
// set, so a drifted copy here shows up as a capability the wrapper does not
// satisfy.
type (
	jobsSyncMergeClearer interface {
		ClearSyncMerge(loserBookID, winnerBookID string) error
	}
	jobsSyncRedirectClearer interface {
		ClearSyncRedirect(loserBookID string) (winnerBookID string, cleared bool, err error)
	}
	jobsOpChangeScanner interface {
		ScanOperationChanges(fn func(*database.OperationChange) error) error
	}
	jobsRetiredProbe interface{ HasRetiredVersions() (bool, error) }
)

var (
	_ authorcredit.TitleSource       = (*booksJobsStore)(nil)
	_ authorcredit.AliasFinder       = (*booksJobsStore)(nil)
	_ authorcredit.AuthorBooksSource = (*booksJobsStore)(nil)
	_ authorcredit.TitleCacheKeyer   = (*booksJobsStore)(nil)
	_ database.RetiredBookFileReader = (*booksJobsStore)(nil)
	_ database.BookCreditsModifier   = (*booksJobsStore)(nil)
)

// --- reads forwarded from database.Store ---------------------------------

func (w *booksJobsStore) FindAuthorByAlias(aliasName string) (*database.Author, error) {
	return w.caps.store.FindAuthorByAlias(aliasName)
}

func (w *booksJobsStore) GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error) {
	return w.caps.store.GetBookAlternativeTitles(bookID)
}

func (w *booksJobsStore) GetBookAtVersion(id string, ts time.Time) (*database.Book, error) {
	return w.caps.store.GetBookAtVersion(id, ts)
}

func (w *booksJobsStore) GetBookFileByAcoustID(fingerprint string) (*database.BookFile, error) {
	return w.caps.store.GetBookFileByAcoustID(fingerprint)
}

func (w *booksJobsStore) GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error) {
	return w.caps.store.GetBookSnapshots(id, limit)
}

func (w *booksJobsStore) GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error) {
	return w.caps.store.GetBooksByAuthorIDCore(authorID)
}

func (w *booksJobsStore) GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) {
	return w.caps.store.GetBooksByAuthorIDWithRoleCore(authorID)
}

func (w *booksJobsStore) GetBooksByMetadataSourceHash(hash string) ([]database.Book, error) {
	return w.caps.store.GetBooksByMetadataSourceHash(hash)
}

func (w *booksJobsStore) GetDuplicateBooks() ([][]database.Book, error) {
	return w.caps.store.GetDuplicateBooks()
}

func (w *booksJobsStore) GetDuplicateBooksByMetadataCore(threshold float64) ([][]database.BookCore, error) {
	return w.caps.store.GetDuplicateBooksByMetadataCore(threshold)
}

func (w *booksJobsStore) GetFolderDuplicatesCore() ([][]database.BookCore, error) {
	return w.caps.store.GetFolderDuplicatesCore()
}

func (w *booksJobsStore) GetUserByID(id string) (*database.User, error) {
	return w.caps.store.GetUserByID(id)
}

func (w *booksJobsStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return w.caps.store.ListActiveOperationsV2()
}

func (w *booksJobsStore) ListUserPlaylists(playlistType string, limit, offset int) ([]database.UserPlaylist, int, error) {
	return w.caps.store.ListUserPlaylists(playlistType, limit, offset)
}

func (w *booksJobsStore) LiveBookPathsUnderDir(dir string) (map[string]string, error) {
	return w.caps.store.LiveBookPathsUnderDir(dir)
}

// --- reads forwarded from the other capabilities -------------------------

func (w *booksJobsStore) ListUsersStrict() ([]database.User, error) {
	return w.caps.strictUsers.ListUsersStrict()
}

func (w *booksJobsStore) ScanOperationChanges(fn func(*database.OperationChange) error) error {
	return w.caps.opScan.ScanOperationChanges(fn)
}

func (w *booksJobsStore) HasRetiredVersions() (bool, error) {
	return w.caps.retiredProbe.HasRetiredVersions()
}

func (w *booksJobsStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	return w.caps.seriesRefs.GetAllSeriesBookRefCounts()
}

func (w *booksJobsStore) GetBooksBySeriesIDsAllVersions(seriesIDs []int) (map[int][]database.BookCore, error) {
	return w.caps.seriesMembers.GetBooksBySeriesIDsAllVersions(seriesIDs)
}

func (w *booksJobsStore) GetBookFilesIncludingRetired(bookID string) ([]database.BookFile, error) {
	return w.caps.retired.GetBookFilesIncludingRetired(bookID)
}

func (w *booksJobsStore) GetRetiredBookFiles(holderID string) ([]database.BookFile, error) {
	return w.caps.retired.GetRetiredBookFiles(holderID)
}

func (w *booksJobsStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return w.caps.retired.GetAllBookFilesCoreIncludingRetired()
}

func (w *booksJobsStore) GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error) {
	return w.caps.retired.GetRetiredVersionsFromBook(shellID)
}

func (w *booksJobsStore) GetBookFileByPIDIncludingRetired(itunesPID string) (*database.BookFile, error) {
	return w.caps.retired.GetBookFileByPIDIncludingRetired(itunesPID)
}

func (w *booksJobsStore) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	return w.caps.retired.GetBookFileByPathIncludingRetired(path)
}

// TitleCacheKey is the identity authorcredit caches the title index under: the
// key a bare store would be cached under, fixed when the deps were built. It is
// not a store capability; see titleKeyOf.
func (w *booksJobsStore) TitleCacheKey() authorcredit.TitleKey { return w.caps.titleKey }

// --- writes with a row in the shared table -------------------------------

func (w *booksJobsStore) AppendFileEvent(e database.FileEvent) error {
	return w.gate.do(runless(), "AppendFileEvent", kindRef(ops.ResFileProvenance), func(context.Context) error {
		return w.caps.provenance.AppendFileEvent(e)
	})
}

func (w *booksJobsStore) ClearUserPositions(userID, bookID string) error {
	return w.gate.do(runless(), "ClearUserPositions", bookRef(bookID), func(context.Context) error {
		return w.caps.store.ClearUserPositions(userID, bookID)
	})
}

func (w *booksJobsStore) CopyBookmarkIfAbsent(b progress.Bookmark) (copied bool, err error) {
	err = w.gate.do(runless(), "CopyBookmarkIfAbsent", kindRef(ops.ResUserState), func(context.Context) error {
		var e error
		copied, e = w.caps.bookmarkCopier.CopyBookmarkIfAbsent(b)
		return e
	})
	return copied, err
}

func (w *booksJobsStore) DeleteSeries(id int) error {
	return w.gate.do(runless(), "DeleteSeries", kindRef(ops.ResSeries), func(context.Context) error {
		return w.caps.store.DeleteSeries(id)
	})
}

func (w *booksJobsStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	return w.gate.do(runless(), "SetBookAuthors", bookRef(bookID), func(context.Context) error {
		return w.caps.store.SetBookAuthors(bookID, authors)
	})
}

func (w *booksJobsStore) SetUserPositionAt(userID, bookID, segmentID string, positionSeconds float64, updatedAt time.Time) error {
	return w.gate.do(runless(), "SetUserPositionAt", bookRef(bookID), func(context.Context) error {
		return w.caps.positionAt.SetUserPositionAt(userID, bookID, segmentID, positionSeconds, updatedAt)
	})
}

func (w *booksJobsStore) SweepExpiredMetrics() (deleted int64, err error) {
	err = w.gate.do(runless(), "SweepExpiredMetrics", kindRef(ops.ResMetrics), func(context.Context) error {
		var e error
		deleted, e = w.caps.metrics.SweepExpiredMetrics()
		return e
	})
	return deleted, err
}

// --- writes with no row: refused ------------------------------------------
//
// Each is the call a capability would make and the gate stops: see
// jobsRefusedLabels.

func (w *booksJobsStore) AddBookTagWithSource(bookID, tag, source string) error {
	return w.gate.do(runless(), "AddBookTagWithSource", bookRef(bookID), func(context.Context) error {
		return w.caps.store.AddBookTagWithSource(bookID, tag, source)
	})
}

func (w *booksJobsStore) ClearSyncMerge(loserBookID, winnerBookID string) error {
	return w.gate.do(runless(), "ClearSyncMerge", bookRef(winnerBookID), func(context.Context) error {
		return w.caps.syncMerge.ClearSyncMerge(loserBookID, winnerBookID)
	})
}

func (w *booksJobsStore) ClearSyncRedirect(loserBookID string) (winnerBookID string, cleared bool, err error) {
	err = w.gate.do(runless(), "ClearSyncRedirect", bookRef(loserBookID), func(context.Context) error {
		var e error
		winnerBookID, cleared, e = w.caps.syncRedirect.ClearSyncRedirect(loserBookID)
		return e
	})
	return winnerBookID, cleared, err
}

func (w *booksJobsStore) DeleteUserBookState(userID, bookID string) error {
	return w.gate.do(runless(), "DeleteUserBookState", bookRef(bookID), func(context.Context) error {
		return w.caps.stateDeleter.DeleteUserBookState(userID, bookID)
	})
}

func (w *booksJobsStore) MarkIdentDirty(bookID string) (seq uint64, err error) {
	err = w.gate.do(runless(), "MarkIdentDirty", bookRef(bookID), func(context.Context) error {
		var e error
		seq, e = w.caps.identDirty.MarkIdentDirty(bookID)
		return e
	})
	return seq, err
}

func (w *booksJobsStore) RemoveBookTagsByPrefix(bookID, prefix, source string) error {
	return w.gate.do(runless(), "RemoveBookTagsByPrefix", bookRef(bookID), func(context.Context) error {
		return w.caps.store.RemoveBookTagsByPrefix(bookID, prefix, source)
	})
}

func (w *booksJobsStore) ReplaceUserPositions(userID, bookID string, positions []database.UserPosition) error {
	return w.gate.do(runless(), "ReplaceUserPositions", bookRef(bookID), func(context.Context) error {
		return w.caps.replacer.ReplaceUserPositions(userID, bookID, positions)
	})
}

func (w *booksJobsStore) RevertBookToVersion(id string, ts time.Time) (out *database.Book, err error) {
	err = w.gate.do(runless(), "RevertBookToVersion", bookRef(id), func(context.Context) error {
		var e error
		out, e = w.caps.store.RevertBookToVersion(id, ts)
		return e
	})
	return out, err
}

func (w *booksJobsStore) UpdateSeriesName(id int, name string) error {
	return w.gate.do(runless(), "UpdateSeriesName", kindRef(ops.ResSeries), func(context.Context) error {
		return w.caps.store.UpdateSeriesName(id, name)
	})
}

// ModifyBookCredits is the one commit of a book's row and its credit lists
// (database.BookCreditsModifier, which refetch-missing-authors resolves). It is
// gated under the shared table's row for it.
func (w *booksJobsStore) ModifyBookCredits(bookID string, fn func(book *database.Book, credits *database.BookCreditsEdit) error) (b *database.Book, edit database.BookCreditsEdit, err error) {
	err = w.gate.do(runless(), "ModifyBookCredits", bookRef(bookID), func(context.Context) error {
		var e error
		b, edit, e = w.caps.credits.ModifyBookCredits(bookID, fn)
		return e
	})
	return b, edit, err
}

// ModifyBookNarrators is the other method of the same capability. It has no row
// in the shared table, so it is refused.
func (w *booksJobsStore) ModifyBookNarrators(bookID string, fn func([]database.BookNarrator) ([]database.BookNarrator, error)) (out []database.BookNarrator, err error) {
	err = w.gate.do(runless(), "ModifyBookNarrators", bookRef(bookID), func(context.Context) error {
		var e error
		out, e = w.caps.credits.ModifyBookNarrators(bookID, fn)
		return e
	})
	return out, err
}
