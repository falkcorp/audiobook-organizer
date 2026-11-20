// file: internal/plugins/maintenance/deps_t6g6.go
// version: 0.1.0
// guid: 2f6b9d14-7c53-4e80-a1d9-0b3e58c47a96
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g6BootHost is what the server serves that is not a store row: the two activity
// compactions.
type t6g6BootHost interface {
	CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (database.CompactResult, error)
	RecompactActivityDigests(ctx context.Context) (database.RecompactResult, error)
}

// newT6G6Deps is the books module's t6g6Deps over the ops store src (the
// interface HEAD's definitions already read and write through), with every read
// and write the ops store does not carry resolved per call through
// database.AsCapability. As in the other builders, a missing input leaves the
// field that needs it unset: a nil src leaves Store and NewWriter unset, a nil
// host leaves NewWriter unset (the activity compactions have nowhere to go).
func newT6G6Deps(src OpsStore, host t6g6BootHost, activityFullDetailDays func() int, standDownKey tasks.Key, rootDir func() string, itunesRoots func() ([]string, error)) t6g6Deps {
	if src == nil {
		return t6g6Deps{}
	}
	d := t6g6Deps{
		Store:                     &booksT6G6Store{src: src},
		ActivityLogFullDetailDays: activityFullDetailDays,
		StandDownKey:              standDownKey,
		RootDir:                   rootDir,
		ITunesRoots:               itunesRoots,
	}
	if rootDir != nil {
		d.ResolveReportPath = reportPathOver(rootDir)
	}
	if host != nil {
		rows := groupRows(map[string]string{"SetRaw": "SetRaw/user_state"}, reflect.TypeFor[t6g6Writer]())
		d.NewWriter = func(w tasks.Writer) t6g6Writer {
			return &booksT6G6Writer{src: src, host: host, gate: newBooksGate(w, rows)}
		}
	}
	return d
}

// booksT6G6Store is the group's reads. The ops store's own reads are promoted;
// the rest are resolved per call.
type booksT6G6Store struct {
	src OpsStore
}

func (s *booksT6G6Store) GetBookByID(id string) (*database.Book, error) { return s.src.GetBookByID(id) }
func (s *booksT6G6Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.src.GetBookFiles(bookID)
}

func (s *booksT6G6Store) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	return s.src.GetBookFileByID(bookID, fileID)
}

func (s *booksT6G6Store) GetBookFileByPath(path string) (*database.BookFile, error) {
	return s.src.GetBookFileByPath(path)
}

func (s *booksT6G6Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.src.GetAllBookFilesCore()
}

func (s *booksT6G6Store) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCoreComplete(limit, offset)
}

func (s *booksT6G6Store) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	return s.src.GetBooksByVersionGroup(groupID)
}

func (s *booksT6G6Store) ListBookIDs() ([]string, error) { return s.src.ListBookIDs() }
func (s *booksT6G6Store) ListReviewItems(f database.ReviewFilter) ([]database.ReviewItem, int, error) {
	return s.src.ListReviewItems(f)
}

func (s *booksT6G6Store) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
	return s.src.ListSoftDeletedBooks(limit, offset, olderThan)
}

func (s *booksT6G6Store) LiveBookIDsAtPath(path string) ([]string, error) {
	return s.src.LiveBookIDsAtPath(path)
}

func (s *booksT6G6Store) ScanPrefix(prefix string) ([]database.KVPair, error) {
	return s.src.ScanPrefix(prefix)
}

func (s *booksT6G6Store) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.src)
}

func (s *booksT6G6Store) BookAtPathIndexBuilt() (bool, error) {
	f, err := srcCap[interface{ BookAtPathIndexBuilt() (bool, error) }](s.src, "book-at-path index")
	if err != nil {
		return false, err
	}
	return f.BookAtPathIndexBuilt()
}

func (s *booksT6G6Store) BookFilesAtPath(path string) ([]database.BookFile, error) {
	f, err := srcCap[interface {
		BookFilesAtPath(string) ([]database.BookFile, error)
	}](s.src, "BookFilesAtPath")
	if err != nil {
		return nil, err
	}
	return f.BookFilesAtPath(path)
}

func (s *booksT6G6Store) GetBookFileByPID(pid string) (*database.BookFile, error) {
	f, err := srcCap[interface {
		GetBookFileByPID(string) (*database.BookFile, error)
	}](s.src, "GetBookFileByPID")
	if err != nil {
		return nil, err
	}
	return f.GetBookFileByPID(pid)
}

func (s *booksT6G6Store) GetBookTombstone(id string) (*database.Book, error) {
	f, err := srcCap[interface {
		GetBookTombstone(string) (*database.Book, error)
	}](s.src, "GetBookTombstone")
	if err != nil {
		return nil, err
	}
	return f.GetBookTombstone(id)
}

func (s *booksT6G6Store) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	f, err := srcCap[interface {
		GetUserBookState(string, string) (*database.UserBookState, error)
	}](s.src, "user book state reads")
	if err != nil {
		return nil, err
	}
	return f.GetUserBookState(userID, bookID)
}

func (s *booksT6G6Store) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	f, err := srcCap[interface {
		ListUserPositionsForBook(string, string) ([]database.UserPosition, error)
	}](s.src, "user position reads")
	if err != nil {
		return nil, err
	}
	return f.ListUserPositionsForBook(userID, bookID)
}

func (s *booksT6G6Store) ListUserPositionsSince(userID string, t time.Time) ([]database.UserPosition, error) {
	f, err := srcCap[interface {
		ListUserPositionsSince(string, time.Time) ([]database.UserPosition, error)
	}](s.src, "user position reads")
	if err != nil {
		return nil, err
	}
	return f.ListUserPositionsSince(userID, t)
}

func (s *booksT6G6Store) ListUsers() ([]database.User, error) {
	f, err := srcCap[interface {
		ListUsers() ([]database.User, error)
	}](s.src, "ListUsers")
	if err != nil {
		return nil, err
	}
	return f.ListUsers()
}

// OpChangeIndexVerifier resolves the index's verify surface through the
// decorator chain. The verify and the build halves are ONE capability, as at
// HEAD: a decorator that carries only one half answers "no index". The value
// handed back is a forwarder with the verify alone, not the capability: the
// capability also rebuilds the index, and a caller could assert it back.
func (s *booksT6G6Store) OpChangeIndexVerifier() (t6g6OpChangeIndexVerifier, string, bool) {
	v, ok := database.AsCapability[t6g6OpChangeIndexRebuilder](s.src)
	if !ok {
		return nil, fmt.Sprintf("%T", s.src), false
	}
	return forwardedOpChangeVerifier{v: v}, fmt.Sprintf("%T", s.src), true
}

// UserStateSyncReads and UserStateBookmarkReads hand back forwarders of the
// read methods only, for the same reason: SyncIdentityStore and BookmarkStore
// carry writes.
func (s *booksT6G6Store) UserStateSyncReads() (t6g6SyncReads, bool) {
	r := database.AsSyncIdentityStore(s.src)
	if r == nil {
		return nil, false
	}
	return forwardedSyncReads{s: r}, true
}

func (s *booksT6G6Store) UserStateBookmarkReads() (t6g6BookmarkReads, bool) {
	r := database.AsBookmarkStore(s.src)
	if r == nil {
		return nil, false
	}
	return forwardedBookmarkReads{s: r}, true
}

type forwardedOpChangeVerifier struct{ v t6g6OpChangeIndexVerifier }

func (f forwardedOpChangeVerifier) VerifyOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (database.OpChangeByBookIndexReport, error) {
	return f.v.VerifyOpChangeByBookIndex(ctx, progress)
}

type forwardedSyncReads struct{ s t6g6SyncReads }

func (f forwardedSyncReads) GetSyncIDForBook(bookID string) (string, bool, error) {
	return f.s.GetSyncIDForBook(bookID)
}
func (f forwardedSyncReads) ResolveSyncItem(syncID string) (*database.SyncItem, error) {
	return f.s.ResolveSyncItem(syncID)
}
func (f forwardedSyncReads) ListSyncAliases(syncID string) ([]string, error) {
	return f.s.ListSyncAliases(syncID)
}

type forwardedBookmarkReads struct{ s t6g6BookmarkReads }

func (f forwardedBookmarkReads) ListBookmarks(userID, itemID string) ([]progress.Bookmark, error) {
	return f.s.ListBookmarks(userID, itemID)
}
func (f forwardedBookmarkReads) ListBookmarksForUser(userID string) ([]progress.Bookmark, error) {
	return f.s.ListBookmarksForUser(userID)
}

// booksT6G6Writer is the group's writes: every method is one gated call (no
// method of this group has an empty row), and the capability getters hand out
// writers that gate the same way.
type booksT6G6Writer struct {
	src  OpsStore
	host t6g6BootHost
	gate booksGate
}

func (w *booksT6G6Writer) BatchCreateBookFiles(files []*database.BookFile) error {
	return w.gate.do(runless(), "BatchCreateBookFiles", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.BatchCreateBookFiles(files)
	})
}

func (w *booksT6G6Writer) CreateBookFile(file *database.BookFile) error {
	return w.gate.do(runless(), "CreateBookFile", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.CreateBookFile(file)
	})
}

func (w *booksT6G6Writer) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (n int, err error) {
	err = w.gate.do(ctx, "UpdateBookFiles", kindRef(ops.ResBookFiles), func(ctx context.Context) error {
		var e error
		n, e = w.src.UpdateBookFiles(ctx, files, afterRow)
		return e
	})
	return n, err
}

func (w *booksT6G6Writer) ModifyBook(id string, fn func(*database.Book) error) (b *database.Book, err error) {
	err = w.gate.do(runless(), "ModifyBook", bookRef(id), func(context.Context) error {
		var e error
		b, e = w.src.ModifyBook(id, fn)
		return e
	})
	return b, err
}

func (w *booksT6G6Writer) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (f *database.BookFile, err error) {
	err = w.gate.do(runless(), "ModifyBookFile", bookRef(bookID), func(context.Context) error {
		var e error
		f, e = w.src.ModifyBookFile(bookID, fileID, fn)
		return e
	})
	return f, err
}

func (w *booksT6G6Writer) RecomputeBookAggregates(bookID string) error {
	return w.gate.do(runless(), "RecomputeBookAggregates", bookRef(bookID), func(context.Context) error {
		return w.src.RecomputeBookAggregates(bookID)
	})
}

// CreateOperationChange goes through the classification check, as HEAD's body
// did: an unclassified change type is refused before the store sees it.
func (w *booksT6G6Writer) CreateOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "CreateOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

func (w *booksT6G6Writer) SetRaw(key string, value []byte) error {
	return w.gate.do(runless(), "SetRaw", kindRef(ops.ResUserState), func(context.Context) error {
		return w.src.SetRaw(key, value)
	})
}

func (w *booksT6G6Writer) DeleteRaw(key string) error {
	return w.gate.do(runless(), "DeleteRaw", kindRef(ops.ResUserState), func(context.Context) error {
		return w.src.DeleteRaw(key)
	})
}

func (w *booksT6G6Writer) UpsertReviewItem(item database.ReviewItem) (out database.ReviewItem, err error) {
	err = w.gate.do(runless(), "UpsertReviewItem", kindRef(ops.ResReviewItems), func(context.Context) error {
		var e error
		out, e = w.src.UpsertReviewItem(item)
		return e
	})
	return out, err
}

func (w *booksT6G6Writer) DeleteReviewItemIfPending(id string) (deleted bool, err error) {
	err = w.gate.do(runless(), "DeleteReviewItemIfPending", kindRef(ops.ResReviewItems), func(context.Context) error {
		d, e := srcCap[database.ReviewItemPendingDeleter](w.src, "DeleteReviewItemIfPending")
		if e != nil {
			return e
		}
		deleted, e = d.DeleteReviewItemIfPending(id)
		return e
	})
	return deleted, err
}

func (w *booksT6G6Writer) SetUserBookState(state *database.UserBookState) error {
	return w.gate.do(runless(), "SetUserBookState", kindRef(ops.ResUserState), func(context.Context) error {
		f, err := srcCap[interface {
			SetUserBookState(*database.UserBookState) error
		}](w.src, "SetUserBookState")
		if err != nil {
			return err
		}
		return f.SetUserBookState(state)
	})
}

func (w *booksT6G6Writer) SetUserPosition(userID, bookID, segmentID string, seconds float64) error {
	return w.gate.do(runless(), "SetUserPosition", kindRef(ops.ResUserState), func(context.Context) error {
		f, err := srcCap[interface {
			SetUserPosition(string, string, string, float64) error
		}](w.src, "SetUserPosition")
		if err != nil {
			return err
		}
		return f.SetUserPosition(userID, bookID, segmentID, seconds)
	})
}

func (w *booksT6G6Writer) ClearUserPositions(userID, bookID string) error {
	return w.gate.do(runless(), "ClearUserPositions", kindRef(ops.ResUserState), func(context.Context) error {
		f, err := srcCap[interface {
			ClearUserPositions(string, string) error
		}](w.src, "ClearUserPositions")
		if err != nil {
			return err
		}
		return f.ClearUserPositions(userID, bookID)
	})
}

func (w *booksT6G6Writer) CompactActivityEntries(ctx context.Context, cutoff time.Time, progress database.CompactProgress) (res database.CompactResult, err error) {
	err = w.gate.do(ctx, "CompactActivityEntries", kindRef(ops.ResActivity), func(ctx context.Context) error {
		var e error
		res, e = w.host.CompactActivityEntries(ctx, cutoff, progress)
		return e
	})
	return res, err
}

func (w *booksT6G6Writer) RecompactActivityDigests(ctx context.Context) (res database.RecompactResult, err error) {
	err = w.gate.do(ctx, "RecompactActivityDigests", kindRef(ops.ResActivity), func(ctx context.Context) error {
		var e error
		res, e = w.host.RecompactActivityDigests(ctx)
		return e
	})
	return res, err
}

func (w *booksT6G6Writer) RebuildOpChangeByBookIndex(ctx context.Context, progress database.OpChangeIndexProgress) (res database.OpChangeByBookBackfillResult, err error) {
	err = w.gate.do(ctx, "RebuildOpChangeByBookIndex", kindRef(ops.ResOperations), func(ctx context.Context) error {
		b, e := srcCap[t6g6OpChangeIndexBuilder](w.src, "opchange_by_book index")
		if e != nil {
			return e
		}
		res, e = b.RebuildOpChangeByBookIndex(ctx, progress)
		return e
	})
	return res, err
}

func (w *booksT6G6Writer) EnsureOpChangeByBookIndexWith(ctx context.Context, opts database.OpChangeEnsureOptions) (res database.OpChangeByBookEnsureResult, err error) {
	err = w.gate.do(ctx, "EnsureOpChangeByBookIndexWith", kindRef(ops.ResOperations), func(ctx context.Context) error {
		b, e := srcCap[t6g6OpChangeIndexBuilder](w.src, "opchange_by_book index")
		if e != nil {
			return e
		}
		res, e = b.EnsureOpChangeByBookIndexWith(ctx, opts)
		return e
	})
	return res, err
}

// The iTunes heal's three writes are the reconcile package's own (the duplicate
// collapse and the reflink are unexported there), each behind one gated call.
func (w *booksT6G6Writer) heal() (reconcile.HealWriter, error) {
	rs, err := srcCap[ReconcileStore](w.src, "reconcile surface (ReconcileStore)")
	if err != nil {
		return nil, err
	}
	return reconcile.NewHeadHealWriter(rs), nil
}

func (w *booksT6G6Writer) CollapseHealDuplicates(opID, keepID string, dupIDs []string) error {
	return w.gate.do(runless(), "CollapseHealDuplicates", kindRef(ops.ResBooks), func(context.Context) error {
		h, err := w.heal()
		if err != nil {
			return err
		}
		return h.CollapseHealDuplicates(opID, keepID, dupIDs)
	})
}

func (w *booksT6G6Writer) HealTrack(dst, src string) error {
	return w.gate.do(runless(), "HealTrack", kindRef(ops.ResFiles), func(context.Context) error {
		h, err := w.heal()
		if err != nil {
			return err
		}
		return h.HealTrack(dst, src)
	})
}

// HealWhisperJournal answers nil, which the heal reads as "run without the
// journal", when the call is refused. The journal it hands back has its appends
// and removals stamped too (gatedResultJournal).
func (w *booksT6G6Writer) HealWhisperJournal() (j transcribe.ResultJournal) {
	gateQuiet(w.gate, "HealWhisperJournal", kindRef(ops.ResAIJournal), func() {
		if h, err := w.heal(); err == nil {
			if hj := h.HealWhisperJournal(); hj != nil {
				j = gatedResultJournal{j: hj, gate: w.gate, method: "HealWhisperJournal"}
			}
		}
	})
	return j
}

func (w *booksT6G6Writer) UserStateSyncWrites() (t6g6SyncWrites, bool) {
	s := database.AsSyncIdentityStore(w.src)
	if s == nil {
		return nil, false
	}
	return gatedSyncWrites{s: s, gate: w.gate}, true
}

func (w *booksT6G6Writer) UserStateBookmarkWrites() (t6g6BookmarkWrites, bool) {
	s := database.AsBookmarkCopier(w.src)
	if s == nil {
		return nil, false
	}
	return gatedBookmarkWrites{s: s, gate: w.gate}, true
}

func (w *booksT6G6Writer) UserStateTimestampWriter() (database.UserPositionTimestampWriter, bool) {
	s, ok := database.AsCapability[database.UserPositionTimestampWriter](w.src)
	if !ok {
		return nil, false
	}
	return gatedStampWriter{s: s, gate: w.gate}, true
}

type gatedSyncWrites struct {
	s    t6g6SyncWrites
	gate booksGate
}

func (g gatedSyncWrites) MintOrGetSyncID(bookID string) (id string, err error) {
	err = g.gate.do(runless(), "MintOrGetSyncID", bookRef(bookID), func(context.Context) error {
		var e error
		id, e = g.s.MintOrGetSyncID(bookID)
		return e
	})
	return id, err
}

func (g gatedSyncWrites) RecordSyncMerge(loser, winner string) error {
	return g.gate.do(runless(), "RecordSyncMerge", bookRef(winner), func(context.Context) error {
		return g.s.RecordSyncMerge(loser, winner)
	})
}

type gatedBookmarkWrites struct {
	s    t6g6BookmarkWrites
	gate booksGate
}

func (g gatedBookmarkWrites) CopyBookmarkIfAbsent(b progress.Bookmark) (copied bool, err error) {
	err = g.gate.do(runless(), "CopyBookmarkIfAbsent", kindRef(ops.ResUserState), func(context.Context) error {
		var e error
		copied, e = g.s.CopyBookmarkIfAbsent(b)
		return e
	})
	return copied, err
}

type gatedStampWriter struct {
	s    database.UserPositionTimestampWriter
	gate booksGate
}

func (g gatedStampWriter) SetUserPositionAt(userID, bookID, segmentID string, seconds float64, at time.Time) error {
	return g.gate.do(runless(), "SetUserPositionAt", kindRef(ops.ResUserState), func(context.Context) error {
		return g.s.SetUserPositionAt(userID, bookID, segmentID, seconds, at)
	})
}
