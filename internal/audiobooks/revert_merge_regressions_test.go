// file: internal/audiobooks/revert_merge_regressions_test.go
// version: 1.1.0
// guid: 2d9b7f41-6e05-4c38-a1f2-8b3c5e9d0a74
// last-edited: 2026-10-18

package audiobooks

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// Regressions from the #4064 review: reverting a merge through POST
// /operations/:id/revert must put back exactly what the merge did, and must
// never write over a book row the merge did not retire.

// rowState renders the book columns a combine undo could rewrite.
func rowState(t *testing.T, store database.Store, id string) string {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b, "book %s", id)
	s := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	pv := "<nil>"
	if b.IsPrimaryVersion != nil {
		pv = fmt.Sprint(*b.IsPrimaryVersion)
	}
	return fmt.Sprintf("path=%q group=%s primary=%s state=%s softdel=%v", b.FilePath, s(b.VersionGroupID), pv, s(b.LibraryState), b.IsSoftDeleted())
}

func setState(t *testing.T, store database.Store, state string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, err := store.ModifyBook(id, func(b *database.Book) error { b.LibraryState = &state; return nil })
		require.NoError(t, err)
	}
}

// editRow links id into another version group as its primary and renames it:
// what an organize or a later link does to a live book.
func editRow(t *testing.T, store database.Store, id string) {
	t.Helper()
	g, tr := "LATER-GROUP", true
	_, err := store.ModifyBook(id, func(b *database.Book) error {
		b.VersionGroupID, b.IsPrimaryVersion, b.FilePath = &g, &tr, "/tmp/revert-paths/edited-"+id
		return nil
	})
	require.NoError(t, err)
}

// failMoveFor fails MoveBookFilesToBook for one source book (an ordinary,
// atomic error: nothing moved).
type failMoveFor struct {
	*database.PebbleStore
	src string
}

func (s failMoveFor) MoveBookFilesToBook(ids []string, src, dst string) error {
	if src == s.src {
		return errors.New("injected move failure")
	}
	return s.PebbleStore.MoveBookFilesToBook(ids, src, dst)
}

// crashSplitMergeAfterPointer runs a split-book merge that journals its
// pointer under opID and then "crashes" before its first mutation.
func crashSplitMergeAfterPointer(t *testing.T, store database.Store, f combineFixture, opID string) {
	t.Helper()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_, _ = dedup.MergeSplitBookClusterWithOptions(store, f.keep, []string{f.a, f.b}, "", dedup.SplitMergeOptions{
			OnJournal: func(id string) error {
				if _, err := merge.RecordJournalPointerIn(store, opID, f.keep, merge.CombineJournalPointerType, id); err != nil {
					return err
				}
				panic("crash")
			},
		})
	}()
	require.True(t, panicked)
	require.Len(t, pointerRows(t, store, opID, undo.ChangeTypeMergeCombineJournal), 1)
}

// B1: a split-book merge leaves a source live when its file move fails. The
// book is later linked elsewhere and renamed. Reverting the merge brings the
// other source back and leaves the live one's row exactly as edited.
func TestRevert_SplitMergeLeftLiveBook_EditedSince_KeepsItsRow(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	setState(t, store, "organized", f.a)
	setState(t, store, "imported", f.b)
	bBefore := rowState(t, store, f.b)

	res, err := dedup.MergeSplitBookClusterWithOptions(failMoveFor{store, f.a}, f.keep, []string{f.a, f.b}, "", dedup.SplitMergeOptions{OperationID: "op-leftlive"})
	require.NoError(t, err)
	j, err := merge.NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, merge.CombineJournalApplied, j.Status)
	require.True(t, j.Absorbed[0].LeftLive, "a was left live")

	editRow(t, store, f.a)
	aEdited := rowState(t, store, f.a)

	rv, err := NewRevertService(store).RevertOperation("op-leftlive")
	require.NoError(t, err)
	require.Equal(t, 1, rv.RestoredTypes[undo.ChangeTypeMergeCombineJournal])
	require.Equal(t, aEdited, rowState(t, store, f.a), "the live book's row is not rewritten: group, primary, path and state as edited")
	require.Equal(t, bBefore, rowState(t, store, f.b), "the retired source is restored, its state kept")
	files, err := store.GetBookFiles(f.b)
	require.NoError(t, err)
	require.Len(t, files, 1, "b's file is back")
}

// B1: a split-book merge stops right after its pointer (a crash before any
// mutation). A source is then edited. The revert leaves that edit alone and
// changes nothing else.
func TestRevert_SplitMergeStoppedBeforeFirstWrite_EditedSince_KeepsItsRow(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	crashSplitMergeAfterPointer(t, store, f, "op-crash")
	editRow(t, store, f.a)
	want := map[string]string{f.keep: rowState(t, store, f.keep), f.a: rowState(t, store, f.a), f.b: rowState(t, store, f.b)}
	files := snapLibrary(t, store, f).Files

	_, err := NewRevertService(store).RevertOperation("op-crash")
	require.NoError(t, err)
	for id, w := range want {
		require.Equal(t, w, rowState(t, store, id), "book %s untouched", id)
	}
	require.Equal(t, files, snapLibrary(t, store, f).Files)
}

// B1: the same stopped merge with no edit at all: the revert must not relabel
// the untouched books (organized stayed organized; it used to turn into
// imported).
func TestRevert_SplitMergeStoppedBeforeFirstWrite_NoRelabel(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	setState(t, store, "organized", f.a, f.b)
	before := snapLibrary(t, store, f)
	rows := map[string]string{f.a: rowState(t, store, f.a), f.b: rowState(t, store, f.b)}
	crashSplitMergeAfterPointer(t, store, f, "op-crash2")
	require.Equal(t, before, snapLibrary(t, store, f), "nothing moved")

	_, err := NewRevertService(store).RevertOperation("op-crash2")
	require.NoError(t, err)
	require.Equal(t, before, snapLibrary(t, store, f))
	for id, w := range rows {
		require.Equal(t, w, rowState(t, store, id), "book %s keeps its state", id)
	}
}

// failFirstMove fails the first MoveBookFilesToBook (the undo's first file
// move back), after step 1 has restored the absorbed rows.
type failFirstMove struct {
	*database.PebbleStore
	failed *bool
}

func (s failFirstMove) MoveBookFilesToBook(ids []string, src, dst string) error {
	if !*s.failed {
		*s.failed = true
		return errors.New("injected move failure")
	}
	return s.PebbleStore.MoveBookFilesToBook(ids, src, dst)
}

// B1: an undo that failed part-way already restored the absorbed rows. One of
// them is then edited. The retry (the operation revert) moves the files back
// but leaves the edited row as it is.
func TestRevert_CombineUndoRetry_EditedSince_KeepsItsRow(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	res, err := merge.NewService(store).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: "op-retry"})
	require.NoError(t, err)

	failed := false
	_, err = merge.NewService(failFirstMove{store, &failed}).UndoCombine(res.JournalID)
	require.Error(t, err)
	j, err := merge.NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, merge.CombineJournalUndoFailed, j.Status)
	b, err := store.GetBookByID(f.a)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "the failed attempt restored a")

	editRow(t, store, f.a)
	aEdited := rowState(t, store, f.a)

	rv, err := NewRevertService(store).RevertOperation("op-retry")
	require.NoError(t, err)
	require.Equal(t, 1, rv.RestoredTypes[undo.ChangeTypeMergeCombineJournal])
	require.Equal(t, aEdited, rowState(t, store, f.a), "the retry does not write over the edit")
	files, err := store.GetBookFiles(f.a)
	require.NoError(t, err)
	require.Len(t, files, 2, "a's files are back on it")
	owner, err := store.GetBookByExternalID("itunes", f.pid)
	require.NoError(t, err)
	require.Equal(t, f.a, owner)
}

// crashStore panics at one write of a combine: crash reports whether the
// call named by method and arg is the one to stop at. With post unset it
// stops BEFORE the write; with post set it stops right AFTER the write lands
// (the store call returned nil), which is the window a journal written after
// its step would leave unrecorded.
type crashStore struct {
	*database.PebbleStore
	crash func(method, arg string) bool
	post  bool
}

func (s crashStore) before(method, arg string) {
	if !s.post && s.crash(method, arg) {
		panic("crash before " + method + " " + arg)
	}
}

func (s crashStore) after(method, arg string, err error) {
	if s.post && err == nil && s.crash(method, arg) {
		panic("crash after " + method + " " + arg)
	}
}

func (s crashStore) CreateBookFile(bf *database.BookFile) error {
	s.before("CreateBookFile", bf.FilePath)
	err := s.PebbleStore.CreateBookFile(bf)
	s.after("CreateBookFile", bf.FilePath, err)
	return err
}

func (s crashStore) MoveBookFilesToBookBulk(moves []database.BookFileMove, dst string) error {
	s.before("MoveBookFilesToBookBulk", dst)
	err := s.PebbleStore.MoveBookFilesToBookBulk(moves, dst)
	s.after("MoveBookFilesToBookBulk", dst, err)
	return err
}

func (s crashStore) ReassignExternalIDs(oldID, newID string) error {
	s.before("ReassignExternalIDs", oldID)
	err := s.PebbleStore.ReassignExternalIDs(oldID, newID)
	s.after("ReassignExternalIDs", oldID, err)
	return err
}

func (s crashStore) SetRaw(key string, value []byte) error {
	s.before("SetRaw", key)
	err := s.PebbleStore.SetRaw(key, value)
	s.after("SetRaw", key, err)
	return err
}

func (s crashStore) DeleteRaw(key string) error {
	s.before("DeleteRaw", key)
	err := s.PebbleStore.DeleteRaw(key)
	s.after("DeleteRaw", key, err)
	return err
}

func (s crashStore) SetUserBookState(st *database.UserBookState) error {
	s.before("SetUserBookState", st.BookID)
	err := s.PebbleStore.SetUserBookState(st)
	s.after("SetUserBookState", st.BookID, err)
	return err
}

func (s crashStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.before("ModifyBook", id)
	b, err := s.PebbleStore.ModifyBook(id, fn)
	s.after("ModifyBook", id, err)
	return b, err
}

func (s crashStore) MoveBookFilesToBook(ids []string, src, dst string) error {
	s.before("MoveBookFilesToBook", src)
	err := s.PebbleStore.MoveBookFilesToBook(ids, src, dst)
	s.after("MoveBookFilesToBook", src, err)
	return err
}

func (s crashStore) UpdateBookFile(id string, f *database.BookFile) error {
	s.before("UpdateBookFile", f.BookID)
	err := s.PebbleStore.UpdateBookFile(id, f)
	s.after("UpdateBookFile", f.BookID, err)
	return err
}

func (s crashStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	s.before("UpdateBook", id)
	out, err := s.PebbleStore.UpdateBook(id, b)
	s.after("UpdateBook", id, err)
	return out, err
}

func (s crashStore) ReassignExternalID(source, externalID, newBookID string) error {
	s.before("ReassignExternalID", externalID)
	err := s.PebbleStore.ReassignExternalID(source, externalID, newBookID)
	s.after("ReassignExternalID", externalID, err)
	return err
}

func (s crashStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	s.before("SetBookAuthors", bookID)
	err := s.PebbleStore.SetBookAuthors(bookID, authors)
	s.after("SetBookAuthors", bookID, err)
	return err
}

func (s crashStore) CreateAuthor(name string) (*database.Author, error) {
	s.before("CreateAuthor", name)
	a, err := s.PebbleStore.CreateAuthor(name)
	s.after("CreateAuthor", name, err)
	return a, err
}

func (s crashStore) UpsertMetadataFieldState(st *database.MetadataFieldState) error {
	s.before("UpsertMetadataFieldState", st.BookID+"/"+st.Field)
	err := s.PebbleStore.UpsertMetadataFieldState(st)
	s.after("UpsertMetadataFieldState", st.BookID+"/"+st.Field, err)
	return err
}

func (s crashStore) DeleteMetadataFieldState(bookID, field string) error {
	s.before("DeleteMetadataFieldState", bookID+"/"+field)
	err := s.PebbleStore.DeleteMetadataFieldState(bookID, field)
	s.after("DeleteMetadataFieldState", bookID+"/"+field, err)
	return err
}

func (s crashStore) SetUserPosition(userID, bookID, segmentID string, pos float64) error {
	s.before("SetUserPosition", bookID)
	err := s.PebbleStore.SetUserPosition(userID, bookID, segmentID, pos)
	s.after("SetUserPosition", bookID, err)
	return err
}

func (s crashStore) SetUserPositionAt(userID, bookID, segmentID string, pos float64, at time.Time) error {
	s.before("SetUserPositionAt", bookID)
	err := s.PebbleStore.SetUserPositionAt(userID, bookID, segmentID, pos, at)
	s.after("SetUserPositionAt", bookID, err)
	return err
}

func (s crashStore) ClearUserPositions(userID, bookID string) error {
	s.before("ClearUserPositions", bookID)
	err := s.PebbleStore.ClearUserPositions(userID, bookID)
	s.after("ClearUserPositions", bookID, err)
	return err
}

func (s crashStore) CreateOperationChange(c *database.OperationChange) error {
	s.before("CreateOperationChange", c.ChangeType)
	err := s.PebbleStore.CreateOperationChange(c)
	s.after("CreateOperationChange", c.ChangeType, err)
	return err
}

func (s crashStore) RecomputeBookAggregates(id string) error {
	s.before("RecomputeBookAggregates", id)
	err := s.PebbleStore.RecomputeBookAggregates(id)
	s.after("RecomputeBookAggregates", id, err)
	return err
}

// crashFixture: keep and b are VIRTUAL single-file books (the combine creates
// their file rows), a has two files, an iTunes id and a reader's progress.
type crashFixture struct {
	keep, a, b, pid, user string
	paths                 map[string]string // file path -> the book that must own it
}

func seedCrashFixture(t *testing.T, store *database.PebbleStore) crashFixture {
	t.Helper()
	f := crashFixture{keep: "k" + ulid.Make().String(), a: "a" + ulid.Make().String(), b: "b" + ulid.Make().String(), pid: "PID-" + ulid.Make().String()}
	f.paths = map[string]string{
		"/tmp/crash/keep.mp3": f.keep, "/tmp/crash/b.mp3": f.b,
		"/tmp/crash/a/01.mp3": f.a, "/tmp/crash/a/02.mp3": f.a,
	}
	for id, p := range map[string]string{f.keep: "/tmp/crash/keep.mp3", f.a: "/tmp/crash/a", f.b: "/tmp/crash/b.mp3"} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "T " + id[:1], Format: "mp3", FilePath: p})
		require.NoError(t, err)
	}
	for i, p := range []string{"/tmp/crash/a/01.mp3", "/tmp/crash/a/02.mp3"} {
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: f.a, FilePath: p, Format: "mp3", TrackNumber: i + 1, Duration: 60}))
	}
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: f.pid, BookID: f.a}))
	// An explicit state: the restore rule for a retired row reads a nil state
	// as organized and turns organized outside the library root into
	// imported, which would show up as a difference no combine made.
	setState(t, store, "imported", f.keep, f.a, f.b)
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	f.user = u.ID
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: f.a, Status: database.UserBookStatusInProgress, ProgressPct: 40,
		LastActivityAt: time.Now().UTC().Truncate(time.Second),
	}))
	require.NoError(t, store.SetUserPosition(u.ID, f.a, "seg", 40))
	return f
}

// crashSnap is what a combine changes: each book's row, each file path's
// owner, the iTunes id's owner and the reader's progress on a and keep.
type crashSnap struct {
	Rows      map[string]string
	Owner     map[string]string // path -> owner
	PIDOwner  string
	AProgress string
	KProgress string
}

func progressOf(t *testing.T, store database.Store, user, book string) string {
	t.Helper()
	st, err := store.GetUserBookState(user, book)
	require.NoError(t, err)
	pos, err := store.ListUserPositionsForBook(user, book)
	require.NoError(t, err)
	out := "none"
	// An empty state row (no status, 0%) holds no progress. The undo of a
	// whole combine leaves one on the survivor (it existed before this
	// review; reported separately), so it reads as none here.
	if st != nil && (st.Status != "" || st.ProgressPct != 0) {
		out = fmt.Sprintf("%s/%d", st.Status, st.ProgressPct)
	}
	for _, p := range pos {
		out += fmt.Sprintf(" %s@%.0f", p.SegmentID, p.PositionSeconds)
	}
	return out
}

func snapCrash(t *testing.T, store database.Store, f crashFixture) crashSnap {
	t.Helper()
	s := crashSnap{Rows: map[string]string{}, Owner: map[string]string{}}
	for _, id := range []string{f.keep, f.a, f.b} {
		s.Rows[id] = rowState(t, store, id)
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		for _, fl := range files {
			s.Owner[fl.FilePath] = fl.BookID
		}
	}
	owner, err := store.GetBookByExternalID("itunes", f.pid)
	require.NoError(t, err)
	s.PIDOwner = owner
	s.AProgress = progressOf(t, store, f.user, f.a)
	s.KProgress = progressOf(t, store, f.user, f.keep)
	return s
}

// S3: CombineBooks journals every step, so a combine stopped at any write --
// just before it, or just after it landed -- is undone by reverting its
// operation: every book row, file, id and reader's progress goes back. Rows
// the combine created stay, on the book whose audio they name (book_file rows
// are never deleted).
func TestRevert_CombineStoppedPartWay_EveryCrashPoint(t *testing.T) {
	type crashFn func(f crashFixture) func(method, arg string) bool
	cases := []struct {
		name  string
		crash crashFn
		// mayKeepCopy: the follow drained a's progress onto keep and the
		// combine stopped before the after-snapshot. With only the
		// before-snapshot journaled, the undo puts a's progress back and
		// leaves keep's as is (it cannot tell the follow's write from one the
		// reader made since): keep may hold a copy, nothing is lost.
		mayKeepCopy bool
	}{
		{"creating the survivor's own file row", func(crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "CreateBookFile" && arg == "/tmp/crash/keep.mp3" }
		}, false},
		{"the bulk file move", func(crashFixture) func(string, string) bool {
			return func(m, _ string) bool { return m == "MoveBookFilesToBookBulk" }
		}, false},
		{"reassigning the external ids", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "ReassignExternalIDs" && arg == f.a }
		}, false},
		{"recording a's pending repair", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool {
				return m == "SetRaw" && strings.HasPrefix(arg, merge.PendingUserStateRepairPrefix+f.a+":")
			}
		}, true},
		{"draining a's progress", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "SetUserBookState" && arg == f.a }
		}, true},
		{"dropping a's pending repair after the drain", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool {
				return m == "DeleteRaw" && strings.HasPrefix(arg, merge.PendingUserStateRepairPrefix+f.a+":")
			}
		}, true},
		{"soft-deleting a", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "ModifyBook" && arg == f.a }
		}, false},
		{"creating b's file row", func(crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "CreateBookFile" && arg == "/tmp/crash/b.mp3" }
		}, false},
		{"soft-deleting b after a was retired", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "ModifyBook" && arg == f.b }
		}, false},
		{"recomputing the survivor once the body is done", func(f crashFixture) func(string, string) bool {
			return func(m, arg string) bool { return m == "RecomputeBookAggregates" && arg == f.keep }
		}, false},
	}
	run := func(t *testing.T, crash func(f crashFixture) func(string, string) bool, post, wantCrash, mayKeepCopy bool) {
		store := dbtest.NewStore(t)
		f := seedCrashFixture(t, store)
		before := snapCrash(t, store, f)
		opID := "op-" + ulid.Make().String()

		crashed := false
		func() {
			defer func() { crashed = recover() != nil }()
			_, err := merge.NewService(crashStore{store, crash(f), post}).CombineBooksWithOptions(
				[]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: opID})
			require.NoError(t, err)
		}()
		require.Equal(t, wantCrash, crashed)
		require.Len(t, pointerRows(t, store, opID, undo.ChangeTypeMergeCombineJournal), 1)
		// Every row the stopped combine left, created ones included, must
		// still be there after the revert, on the book whose audio it is.
		mid := snapCrash(t, store, f)

		pf, err := undo.PreflightUndoConflicts(store, opID)
		require.NoError(t, err)
		require.Equal(t, 1, pf.Safe, "%+v", pf)

		rv, err := NewRevertService(store).RevertOperation(opID)
		require.NoError(t, err)
		require.Equal(t, 1, rv.RestoredTypes[undo.ChangeTypeMergeCombineJournal], "%+v", rv)

		after := snapCrash(t, store, f)
		require.Equal(t, before.Rows, after.Rows, "every book row as before")
		require.Equal(t, before.PIDOwner, after.PIDOwner)
		require.Equal(t, before.AProgress, after.AProgress, "a's reader progress back")
		if mayKeepCopy {
			require.Contains(t, []string{before.KProgress, before.AProgress}, after.KProgress, "keep has its own progress or a copy of a's")
		} else {
			require.Equal(t, before.KProgress, after.KProgress)
		}
		for path, owner := range after.Owner {
			require.Equal(t, f.paths[path], owner, "file %s on the book whose audio it is", path)
		}
		for path, owner := range before.Owner {
			require.Equal(t, owner, after.Owner[path], "file %s still there, on its book", path)
		}
		for path := range mid.Owner {
			require.Equal(t, f.paths[path], after.Owner[path], "row for %s (the combine left it) is on its owner after the revert", path)
		}
	}
	t.Run("no crash", func(t *testing.T) {
		run(t, func(crashFixture) func(string, string) bool {
			return func(string, string) bool { return false }
		}, false, false, false)
	})
	for _, tc := range cases {
		for _, post := range []bool{false, true} {
			name := tc.name + map[bool]string{false: " (before the write)", true: " (after the write)"}[post]
			t.Run(name, func(t *testing.T) { run(t, tc.crash, post, true, tc.mayKeepCopy) })
		}
	}
}

// S2: the survivor of a sibling-moving merge is later combined away and
// purged. Reverting the merge still puts the sibling back: the undo needs
// only the journal, not the survivor row.
func TestRevert_SiblingMove_SurvivorPurgedSince(t *testing.T) {
	f, k, l, ls := siblingFixture(t)
	// A reader's progress on the loser (the merge follows it onto the
	// survivor) and on the sibling the merge moves: the revert must not
	// remove listening data from any live book.
	u, err := f.S.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.S.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: l, Status: database.UserBookStatusInProgress, ProgressPct: 60, LastActivityAt: time.Now().UTC().Truncate(time.Second)}))
	require.NoError(t, f.S.SetUserPosition(u.ID, l, "seg-1", 1234))
	require.NoError(t, f.S.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: ls, Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: time.Now().UTC().Truncate(time.Second)}))
	require.NoError(t, f.S.SetUserPosition(u.ID, ls, "seg-2", 77))
	res, err := merge.NewService(f.S).MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	require.Equal(t, "H", f.GroupOf(t, ls))
	other := f.Book(t, vptest.Spec{ID: "m", Group: "M", Primary: "true"})
	kf, err := f.S.GetBookFiles(k)
	require.NoError(t, err)
	var ids []string
	for _, x := range kf {
		ids = append(ids, x.ID)
	}
	if len(ids) > 0 {
		require.NoError(t, f.S.MoveBookFilesToBook(ids, k, other))
	}
	require.NoError(t, f.S.DeleteBook(k))
	progress := func() map[string]string {
		out := map[string]string{}
		for _, id := range []string{l, ls, other} {
			out[id] = progressOf(t, f.S, u.ID, id)
		}
		return out
	}
	progressBefore := progress()
	require.Contains(t, progressBefore[ls], "/30", "the sibling's own progress went with it")

	pf, err := undo.PreflightUndoConflicts(f.S, res.SiblingOperationID)
	require.NoError(t, err)
	require.Equal(t, 1, pf.Safe, "the preflight does not refuse over the purged survivor: %+v", pf)
	require.Empty(t, pf.BookMissing)

	rv, err := NewRevertService(f.S).RevertOperation(res.SiblingOperationID)
	require.NoError(t, err)
	require.Equal(t, 1, rv.RestoredTypes[undo.ChangeTypeMergeSiblingJournal])
	require.Equal(t, "G", f.GroupOf(t, ls), "the sibling is back in its group")
	require.Equal(t, progressBefore, progress(), "no live book's listening data changed")
}

// A sibling journal whose merge stopped at its first membership write (after
// the pointer) reverts to nothing changed.
type panicModifyStore struct {
	*database.PebbleStore
	armed *bool
}

func (s panicModifyStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if *s.armed {
		panic("crash")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func TestRevert_SiblingMergeStoppedAtFirstMembershipWrite(t *testing.T) {
	f, k, l, ls := siblingFixture(t)
	armed := true
	func() {
		defer func() { _ = recover() }()
		_, _ = merge.NewService(panicModifyStore{f.S, &armed}).MergeBooksWithOptions([]string{l, k}, k, merge.MergeOptions{OperationID: "op-scrash"})
	}()
	armed = false
	require.Equal(t, "G", f.GroupOf(t, ls))
	require.Equal(t, "G", f.GroupOf(t, l))
	require.Len(t, pointerRows(t, f.S, "op-scrash", undo.ChangeTypeMergeSiblingJournal), 1)

	_, err := NewRevertService(f.S).RevertOperation("op-scrash")
	require.NoError(t, err)
	require.Equal(t, "G", f.GroupOf(t, ls))
	require.Equal(t, "G", f.GroupOf(t, l))
	f.RequireSinglePrimary(t, "G", l)
}

// B1 for the split-book merge: stopped at each store write it makes --
// before the write, and right after it lands -- the merge is undone by
// reverting its operation: every book row (title included), file owner and
// number, the iTunes id and the reader's progress go back. The write list
// comes from a traced run, so a write added to the merge later is crashed
// too.
func TestRevert_SplitMergeStoppedPartWay_EveryCrashPoint(t *testing.T) {
	type snap struct {
		Lib       librarySnap
		Duration  string
		AProgress string
		KProgress string
	}
	seed := func(t *testing.T) (*database.PebbleStore, combineFixture, string) {
		store := dbtest.NewStore(t)
		f := seedCombineFixture(t, store)
		u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
		require.NoError(t, err)
		require.NoError(t, store.SetUserBookState(&database.UserBookState{
			UserID: u.ID, BookID: f.a, Status: database.UserBookStatusInProgress, ProgressPct: 40,
			LastActivityAt: time.Now().UTC().Truncate(time.Second),
		}))
		require.NoError(t, store.SetUserPosition(u.ID, f.a, "seg", 40))
		return store, f, u.ID
	}
	snapOf := func(t *testing.T, store database.Store, f combineFixture, user string) snap {
		k, err := store.GetBookByID(f.keep)
		require.NoError(t, err)
		d := "nil"
		if k.Duration != nil {
			d = fmt.Sprint(*k.Duration)
		}
		return snap{Lib: snapLibrary(t, store, f), Duration: d,
			AProgress: progressOf(t, store, user, f.a), KProgress: progressOf(t, store, user, f.keep)}
	}
	merge := func(db database.Store, f combineFixture, opID string) {
		_, _ = dedup.MergeSplitBookClusterWithOptions(db, f.keep, []string{f.a, f.b},
			"Merged Title", dedup.SplitMergeOptions{OperationID: opID, OrderByBooks: true})
	}

	var writes []string
	var mergedK string // keep's progress after a whole merge: a's, mapped into keep's timeline
	{
		store, f, user := seed(t)
		merge(crashStore{store, func(m, arg string) bool { writes = append(writes, m+" "+arg); return false }, false}, f, "op-trace")
		mergedK = progressOf(t, store, user, f.keep)
	}
	// copyPieces: a stop inside the follow, before its after-snapshot is
	// journaled, leaves keep holding (part of) the copy of a's progress; the
	// undo cannot tell that write from the reader's own, so it is kept.
	// Nothing is removed (a's progress is back in full), and keep holds
	// nothing that is not a piece of that copy.
	copyPieces := func(got string) bool {
		allowed := map[string]bool{"none": true}
		for _, tok := range strings.Fields(mergedK) {
			allowed[tok] = true
		}
		for _, tok := range strings.Fields(got) {
			if !allowed[tok] {
				return false
			}
		}
		return true
	}
	for _, want := range []string{"MoveBookFilesToBook", "ReassignExternalIDs", "SetUserBookState", "UpdateBookFile", "ModifyBook"} {
		found := false
		for _, w := range writes {
			found = found || strings.HasPrefix(w, want+" ")
		}
		require.True(t, found, "the trace covers %s: %v", want, writes)
	}

	for i := 1; i <= len(writes); i++ {
		for _, post := range []bool{false, true} {
			name := fmt.Sprintf("%02d %s %s", i, strings.Fields(writes[i-1])[0], map[bool]string{false: "before", true: "after"}[post])
			t.Run(name, func(t *testing.T) {
				store, f, user := seed(t)
				before := snapOf(t, store, f, user)
				opID := "op-" + ulid.Make().String()
				n := 0
				crashed := false
				func() {
					defer func() { crashed = recover() != nil }()
					merge(crashStore{store, func(string, string) bool { n++; return n == i }, post}, f, opID)
				}()
				require.True(t, crashed, "write %d was reached", i)
				if len(pointerRows(t, store, opID, undo.ChangeTypeMergeCombineJournal)) == 0 {
					require.Equal(t, before, snapOf(t, store, f, user), "stopped before its undo pointer: nothing changed")
					return
				}

				rv, err := NewRevertService(store).RevertOperation(opID)
				require.NoError(t, err, "%+v", rv)
				after := snapOf(t, store, f, user)
				require.Equal(t, before.Lib, after.Lib, "every book, file row and id back")
				require.Equal(t, before.Duration, after.Duration, "keep's duration back")
				require.Equal(t, before.AProgress, after.AProgress, "a's reader progress back")
				require.True(t, after.KProgress == before.KProgress || copyPieces(after.KProgress),
					"keep has its own progress (%q) or pieces of the merged copy (%q), got %q", before.KProgress, mergedK, after.KProgress)
			})
		}
	}
}
