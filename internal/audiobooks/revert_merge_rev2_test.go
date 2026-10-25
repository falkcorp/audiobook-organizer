// file: internal/audiobooks/revert_merge_rev2_test.go
// version: 1.1.2
// guid: 5d0c7a52-8e7b-4f1e-9c36-2a4b6f1d8e90
// last-edited: 2026-10-25

package audiobooks

// Regression tests from the second review of the combine/sibling undo work
// (#4064). Each one started as a reviewer probe that showed a revert losing
// or clobbering something; the assertions pin the fixed behaviour.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// B1: a combine that stops right after a's soft-delete landed (before any
// later journal write) is undone exactly: the stamp was journaled before the
// write, so the revert recognises the shell it retired.
func TestRevert_CombineStoppedAfterSoftDelete_JournaledStampMatches(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCrashFixture(t, store)
	before := snapCrash(t, store, f)
	opID := "op-" + ulid.Make().String()
	crashed := false
	func() {
		defer func() { crashed = recover() != nil }()
		_, _ = merge.NewService(crashStore{store, func(m, arg string) bool { return m == "ModifyBook" && arg == f.a }, true}).
			CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: opID})
	}()
	require.True(t, crashed)

	js, err := merge.NewService(store).ListCombineJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	a, err := store.GetBookByID(f.a)
	require.NoError(t, err)
	require.True(t, a.IsSoftDeleted())
	require.NotNil(t, js[0].Absorbed[0].MarkedForDeletionAt, "the stamp is journaled before the write")
	require.True(t, js[0].Absorbed[0].MarkedForDeletionAt.Equal(*a.MarkedForDeletionAt), "journal and row carry the same stamp")

	rv, err := NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err, "%+v", rv)
	after := snapCrash(t, store, f)
	require.Equal(t, before.Rows, after.Rows)
	require.Equal(t, before.AProgress, after.AProgress)
}

// finalPutFail fails (or panics at) the combine's last journal write, the
// one that records it applied, on a combine that carried an override.
type finalPutFail struct {
	*database.PebbleStore
	panicky bool
}

func (s finalPutFail) SetRaw(key string, value []byte) error {
	if strings.HasPrefix(key, "merge:combine-journal:") &&
		strings.Contains(string(value), `"status":"applied"`) && strings.Contains(string(value), `"override"`) {
		if s.panicky {
			panic("crash at final journal put")
		}
		return errors.New("injected final put failure")
	}
	return s.PebbleStore.SetRaw(key, value)
}

// B2: the override is journaled before it is written, so a combine whose
// final journal write fails (or that stops there) still undoes the override:
// the journal stays pending and reverting the operation replays it.
func TestRevert_CombineOverride_FinalJournalPutFails(t *testing.T) {
	for _, panicky := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicky], func(t *testing.T) {
			store := dbtest.NewStore(t)
			f := seedCrashFixture(t, store)
			before := snapCrash(t, store, f)
			kb, err := store.GetBookByID(f.keep)
			require.NoError(t, err)
			titleBefore := kb.Title
			opID := "op-" + ulid.Make().String()
			var res *merge.CombineResult
			crashed := false
			func() {
				defer func() { crashed = recover() != nil }()
				res, err = merge.NewService(finalPutFail{store, panicky}).CombineBooksWithOptions(
					[]string{f.keep, f.a, f.b}, f.keep, &merge.CombineOverride{Title: "OVERRIDDEN TITLE"}, merge.CombineOptions{OperationID: opID})
			}()
			require.Equal(t, panicky, crashed)
			if !panicky {
				require.NoError(t, err)
				require.NotEmpty(t, res.JournalPending, "the result says the journal is still pending")
				require.Equal(t, opID, res.OperationID, "the operation is still offered for revert")
				require.NotEmpty(t, res.JournalID)
			}
			kb, err = store.GetBookByID(f.keep)
			require.NoError(t, err)
			require.Equal(t, "OVERRIDDEN TITLE", kb.Title, "the override was written")

			rv, err := NewRevertService(store).RevertOperation(opID)
			require.NoError(t, err, "%+v", rv)
			kb, err = store.GetBookByID(f.keep)
			require.NoError(t, err)
			require.Equal(t, titleBefore, kb.Title, "override undone with the rest")
			require.Equal(t, before.Rows, snapCrash(t, store, f).Rows)
		})
	}
}

// B3: a combine that stopped before its file move changed no file numbers;
// renumbers made since on the survivor's own rows and on a's rows survive
// the revert of that combine.
func TestRevert_PendingCombine_KeepsLaterRenumber(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCrashFixture(t, store)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: f.keep, FilePath: "/tmp/crash/keep-own.mp3", Format: "mp3", TrackNumber: 1, Duration: 60}))
	opID := "op-" + ulid.Make().String()
	func() {
		defer func() { _ = recover() }()
		_, _ = merge.NewService(crashStore{store, func(m, _ string) bool { return m == "MoveBookFilesToBookBulk" }, false}).CombineBooksWithOptions(
			[]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: opID})
	}()
	renumber := func(book string, track int) {
		files, err := store.GetBookFiles(book)
		require.NoError(t, err)
		require.NotEmpty(t, files)
		for _, fl := range files {
			fl.DiscNumber, fl.TrackNumber = 2, track
			require.NoError(t, store.UpdateBookFile(fl.ID, &fl))
			track++
		}
	}
	renumber(f.keep, 7)
	renumber(f.a, 20)

	rv, err := NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err, "%+v", rv)
	for _, id := range []string{f.keep, f.a} {
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		require.NotEmpty(t, files)
		for _, fl := range files {
			require.Equal(t, 2, fl.DiscNumber, "file %s keeps the renumber made after a combine that never touched it", fl.FilePath)
		}
	}
}

// B1: the journaled soft-delete stamp still matches the row after the store
// is closed and reopened (the row is rebuilt from the Pebble codec).
func TestRevert_Combine_StampSurvivesReopen(t *testing.T) {
	path := t.TempDir() + "/db"
	store, err := database.NewPebbleStore(path)
	require.NoError(t, err)
	store.WaitForWarmup()
	f := seedCrashFixture(t, store)
	before := snapCrash(t, store, f)
	opID := "op-" + ulid.Make().String()
	_, err = merge.NewService(store).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: opID})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store2, err := database.NewPebbleStore(path)
	require.NoError(t, err)
	store2.WaitForWarmup()
	t.Cleanup(func() { _ = store2.Close() })
	rv, err := NewRevertService(store2).RevertOperation(opID)
	require.NoError(t, err, "%+v", rv)
	require.Equal(t, before.Rows, snapCrash(t, store2, f).Rows)
}

// lockRows renders a book's metadata field states without their update time
// (an undo rewrites a prior row, which stamps it anew).
func lockRows(t *testing.T, store database.Store, id string) []string {
	t.Helper()
	sts, err := store.GetMetadataFieldStates(id)
	require.NoError(t, err)
	var out []string
	for _, s := range sts {
		v := "<nil>"
		if s.OverrideValue != nil {
			v = *s.OverrideValue
		}
		out = append(out, s.Field+"="+v+"|locked="+map[bool]string{false: "n", true: "y"}[s.OverrideLocked]+"|src="+s.LockSource)
	}
	sort.Strings(out)
	return out
}

func seedPriorTitleLock(t *testing.T, store database.Store, id string) {
	t.Helper()
	prior := `"Prior Title"`
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: id, Field: database.FieldKeyTitle, OverrideValue: &prior, OverrideLocked: true,
	}))
}

func seedPriorAuthor(t *testing.T, store database.Store, id string) *database.Author {
	t.Helper()
	pa, err := store.CreateAuthor("Prior Author")
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: pa.ID, Role: "author"}}))
	_, err = store.ModifyBook(id, func(b *database.Book) error { b.AuthorID = &pa.ID; return nil })
	require.NoError(t, err)
	return pa
}

// keepMeta is what a combine override changes on the survivor.
type keepMeta struct {
	Title    string
	AuthorID *int
	Authors  []database.BookAuthor
	Locks    []string
}

func metaOf(t *testing.T, store database.Store, id string) keepMeta {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	return keepMeta{Title: b.Title, AuthorID: b.AuthorID, Authors: authorRows(t, store, id), Locks: lockRows(t, store, id)}
}

// B1/B2 at every write: a combine carrying a title+author override over a
// prior title lock and a prior author is stopped at each store write it
// makes -- before the write, and right after it lands -- and the revert of
// its operation puts everything back: book rows, file owners, the iTunes id,
// the reader's progress, and the survivor's title, author link, author rows
// and lock rows. The write list comes from a traced run, so a write added
// to the combine later is crashed too.
func TestRevert_CombineWithOverride_StoppedAtEveryWrite(t *testing.T) {
	override := &merge.CombineOverride{Title: "OVERRIDDEN TITLE", Author: "Jane Example"}
	seed := func(t *testing.T) (*database.PebbleStore, crashFixture) {
		store := dbtest.NewStore(t)
		f := seedCrashFixture(t, store)
		seedPriorTitleLock(t, store, f.keep)
		seedPriorAuthor(t, store, f.keep)
		return store, f
	}

	var writes []string
	{
		store, f := seed(t)
		_, err := merge.NewService(crashStore{store, func(m, arg string) bool {
			writes = append(writes, m+" "+arg)
			return false
		}, false}).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, override, merge.CombineOptions{OperationID: "op-trace"})
		require.NoError(t, err)
	}
	require.Greater(t, len(writes), 10, "traced writes: %v", writes)
	for _, want := range []string{"SetBookAuthors", "UpsertMetadataFieldState", "ModifyBook", "CreateBookFile", "SetUserBookState"} {
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
				store, f := seed(t)
				before := snapCrash(t, store, f)
				metaBefore := metaOf(t, store, f.keep)
				opID := "op-" + ulid.Make().String()
				n := 0
				crashed := false
				func() {
					defer func() { crashed = recover() != nil }()
					_, _ = merge.NewService(crashStore{store, func(string, string) bool { n++; return n == i }, post}).
						CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep, override, merge.CombineOptions{OperationID: opID})
				}()
				require.True(t, crashed, "write %d was reached", i)
				// Every row the stopped combine left, created ones included,
				// must still be there after the revert, on its owner.
				mid := snapCrash(t, store, f)

				if len(pointerRows(t, store, opID, undo.ChangeTypeMergeCombineJournal)) == 0 {
					// Stopped before its undo pointer: refused with nothing
					// changed.
					require.Equal(t, before, snapCrash(t, store, f))
					require.Equal(t, metaBefore, metaOf(t, store, f.keep))
					return
				}
				rv, err := NewRevertService(store).RevertOperation(opID)
				require.NoError(t, err, "%+v", rv)
				after := snapCrash(t, store, f)
				require.Equal(t, before.Rows, after.Rows)
				require.Equal(t, before.PIDOwner, after.PIDOwner)
				require.Equal(t, before.AProgress, after.AProgress)
				// A stop inside the follow (state copied, positions not yet)
				// leaves keep holding part of a copy of a's progress; with
				// only the before-snapshot journaled the undo cannot tell
				// that write from the reader's own, so it is kept. Nothing is
				// removed: a's progress is back in full.
				require.True(t, after.KProgress == before.KProgress || strings.HasPrefix(before.AProgress, after.KProgress),
					"keep has its own progress (%q) or a copy of a's (%q), got %q", before.KProgress, before.AProgress, after.KProgress)
				for path, owner := range after.Owner {
					require.Equal(t, f.paths[path], owner, "file %s on the book whose audio it is", path)
				}
				for path, owner := range before.Owner {
					require.Equal(t, owner, after.Owner[path], "file %s still there, on its book", path)
				}
				for path := range mid.Owner {
					require.Equal(t, f.paths[path], after.Owner[path], "row for %s (the combine left it) is on its owner after the revert", path)
				}
				require.Equal(t, metaBefore, metaOf(t, store, f.keep), "title, author link, author rows and lock rows back")
			})
		}
	}
}

func authorRows(t *testing.T, store database.Store, id string) []database.BookAuthor {
	t.Helper()
	as, err := store.GetBookAuthors(id)
	require.NoError(t, err)
	return as
}

// B2: reverting a combine with a title+author override restores the title,
// the author link and the author rows, and removes exactly the lock rows the
// combine added (a lock that was there before comes back as it was), whether
// or not the combine's final journal write landed.
func TestRevert_CombineOverride_LockRowsAndAuthorsBack(t *testing.T) {
	for _, failFinal := range []bool{false, true} {
		t.Run(map[bool]string{false: "final put lands", true: "final put fails"}[failFinal], func(t *testing.T) {
			store := dbtest.NewStore(t)
			f := seedCrashFixture(t, store)
			seedPriorTitleLock(t, store, f.keep)
			// A prior author too, so the undo has links to put back.
			pa := seedPriorAuthor(t, store, f.keep)
			locksBefore := lockRows(t, store, f.keep)
			require.Len(t, locksBefore, 1)
			authorsBefore := authorRows(t, store, f.keep)
			kb, err := store.GetBookByID(f.keep)
			require.NoError(t, err)
			titleBefore, authorIDBefore := kb.Title, kb.AuthorID
			_ = err

			opID := "op-" + ulid.Make().String()
			var db database.Store = store
			if failFinal {
				db = finalPutFail{store, false}
			}
			res, err := merge.NewService(db).CombineBooksWithOptions([]string{f.keep, f.a, f.b}, f.keep,
				&merge.CombineOverride{Title: "OVERRIDDEN TITLE", Author: "Jane Example"}, merge.CombineOptions{OperationID: opID})
			require.NoError(t, err)
			require.Equal(t, failFinal, res.JournalPending != "")
			kb, err = store.GetBookByID(f.keep)
			require.NoError(t, err)
			require.Equal(t, "OVERRIDDEN TITLE", kb.Title, "the override wrote the title over the prior lock")
			require.NotNil(t, kb.AuthorID)
			require.NotEqual(t, pa.ID, *kb.AuthorID, "the override linked another author")
			locksMid := lockRows(t, store, f.keep)
			require.NotContains(t, locksMid, locksBefore[0], "the override replaced the prior title lock row")
			require.Len(t, locksMid, 2, "title and author lock rows: %v", locksMid)

			rv, err := NewRevertService(store).RevertOperation(opID)
			require.NoError(t, err, "%+v", rv)
			kb, err = store.GetBookByID(f.keep)
			require.NoError(t, err)
			require.Equal(t, titleBefore, kb.Title)
			require.Equal(t, authorIDBefore, kb.AuthorID)
			require.Equal(t, authorsBefore, authorRows(t, store, f.keep))
			require.Equal(t, locksBefore, lockRows(t, store, f.keep), "exactly the lock rows from before the combine")
		})
	}
}

// S1: a combine stopped at a's soft-delete had already drained a's progress
// and persisted the after-snapshot; the revert puts the reader's progress on
// a and keep back exactly.
func TestRevert_CombineStoppedAtSoftDelete_ProgressBackExactly(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCrashFixture(t, store)
	before := snapCrash(t, store, f)
	opID := "op-" + ulid.Make().String()
	func() {
		defer func() { _ = recover() }()
		_, _ = merge.NewService(crashStore{store, func(m, arg string) bool { return m == "ModifyBook" && arg == f.a }, false}).CombineBooksWithOptions(
			[]string{f.keep, f.a, f.b}, f.keep, nil, merge.CombineOptions{OperationID: opID})
	}()
	require.NotEqual(t, before.AProgress, snapCrash(t, store, f).AProgress, "the follow drained a before the crash")

	rv, err := NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err, "%+v", rv)
	after := snapCrash(t, store, f)
	require.Equal(t, before.AProgress, after.AProgress)
	require.Equal(t, before.KProgress, after.KProgress, "the persisted after-snapshot lets the undo take the copy off keep")
}

// B4: the merge's survivor and loser are both purged since a sibling-moving
// merge; the reader's progress followed onto the moved sibling. There is no
// live book to put it back on, so the revert leaves it on the sibling,
// reports it, and the preflight says so instead of calling the change safe.
// Owner rule: listening data is never removed.
func TestRevert_SiblingMove_SurvivorAndLoserPurged_ProgressStaysOnHolder(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported", NoFile: true})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
	u, err := f.S.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.S.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: l, Status: database.UserBookStatusInProgress, ProgressPct: 60, LastActivityAt: time.Now()}))
	require.NoError(t, f.S.SetUserPosition(u.ID, l, "seg-1", 1234))
	res, err := merge.NewService(f.S).MergeBooks([]string{l, k}, "")
	require.NoError(t, err)
	require.Equal(t, ls, res.StateHolderID)
	require.NotEmpty(t, res.SiblingOperationID)

	m := f.Book(t, vptest.Spec{ID: "m", Group: "M", Primary: "true"})
	kf, err := f.S.GetBookFiles(k)
	require.NoError(t, err)
	var ids []string
	for _, x := range kf {
		ids = append(ids, x.ID)
	}
	if len(ids) > 0 {
		require.NoError(t, f.S.MoveBookFilesToBook(ids, k, m))
	}
	require.NoError(t, f.S.DeleteBook(k))
	require.NoError(t, f.S.DeleteBook(l))
	progressBefore := progressOf(t, f.S, u.ID, ls)
	require.Contains(t, progressBefore, "/60")

	pf, err := preflightUndoConflicts(f.S, res.SiblingOperationID)
	require.NoError(t, err)
	require.Equal(t, 0, pf.Safe, "%+v", pf)
	require.Len(t, pf.ContentChanged, 1, "the kept progress is reported before the revert: %+v", pf)

	rv, err := NewRevertService(f.S).RevertOperation(res.SiblingOperationID)
	require.NoError(t, err)
	require.NotEmpty(t, rv.Kept, "the revert says what it left: %+v", rv)
	require.Equal(t, progressBefore, progressOf(t, f.S, u.ID, ls), "the progress stays on the sibling")
	pos, err := f.S.ListUserPositionsForBook(u.ID, ls)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	keys, err := f.S.ScanPrefix(merge.PendingUserStateRepairPrefix)
	require.NoError(t, err)
	for _, kv := range keys {
		require.NotContains(t, kv.Key, k, "no pending repair whose winner is purged")
	}
}

// B3 on an applied split-merge journal: a source left live (its move
// failed, so its rows never moved) is renumbered after the merge; reverting
// the merge leaves those numbers alone.
func TestRevert_SplitMergeLeftLive_KeepsLaterRenumber(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	res, err := dedup.MergeSplitBookClusterWithOptions(failMoveFor{store, f.a}, f.keep, []string{f.a, f.b}, "", dedup.SplitMergeOptions{OperationID: "op-ll-renum"})
	require.NoError(t, err)
	j, err := merge.NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.True(t, j.Absorbed[0].LeftLive)
	files, err := store.GetBookFiles(f.a)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for i, fl := range files {
		fl.DiscNumber, fl.TrackNumber = 5, 50+i
		require.NoError(t, store.UpdateBookFile(fl.ID, &fl))
	}

	rv, err := NewRevertService(store).RevertOperation("op-ll-renum")
	require.NoError(t, err, "%+v", rv)
	files, err = store.GetBookFiles(f.a)
	require.NoError(t, err)
	for _, fl := range files {
		require.Equal(t, 5, fl.DiscNumber, "file %s", fl.FilePath)
		require.GreaterOrEqual(t, fl.TrackNumber, 50, "file %s", fl.FilePath)
	}
}
