// file: internal/audiobooks/revert_merge_paths_test.go
// version: 1.0.0
// guid: 8e1f3a27-6c94-4b05-a2d8-0f7b5c19e364
// last-edited: 2026-10-18

package audiobooks

import (
	"testing"

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

// End-to-end: every merge core journals the pointer POST
// /operations/:id/revert follows (merge/undo_pointer.go), so a combine, a
// split-book merge and a sibling move are each undone by reverting the
// operation id the merge returns -- through RevertService, the operations
// revert handler's engine, on a real store.

// combineFixture is a keep book and two books to fold into it: a with two
// files and an iTunes id, b with one file.
type combineFixture struct{ keep, a, b, pid string }

func seedCombineFixture(t *testing.T, store database.Store) combineFixture {
	t.Helper()
	f := combineFixture{keep: ulid.Make().String(), a: ulid.Make().String(), b: ulid.Make().String(), pid: "PID-" + ulid.Make().String()}
	_, err := store.CreateBook(&database.Book{ID: f.keep, Title: "Keep", Format: "mp3", FilePath: "/tmp/revert-paths/keep/01.mp3"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: f.a, Title: "Part A", Format: "mp3", FilePath: "/tmp/revert-paths/a"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: f.b, Title: "Part B", Format: "mp3", FilePath: "/tmp/revert-paths/b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: f.keep, FilePath: "/tmp/revert-paths/keep/01.mp3", Format: "mp3", TrackNumber: 1, Duration: 60}))
	for i, p := range []string{"/tmp/revert-paths/a/02.mp3", "/tmp/revert-paths/a/03.mp3"} {
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: f.a, FilePath: p, Format: "mp3", DiscNumber: 2, TrackNumber: i + 7, Duration: 60}))
	}
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: f.b, FilePath: "/tmp/revert-paths/b/04.mp3", Format: "mp3", TrackNumber: 4, Duration: 60}))
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: f.pid, BookID: f.a}))
	return f
}

type bookSnap struct {
	Title    string
	FilePath string
	SoftDel  bool
}

type fileSnap struct {
	BookID      string
	Disc, Track int
}

// librarySnap is the part of the library a combine changes and its undo
// must put back: each book's row, every file row's owner and numbers, and
// the iTunes id's owner.
type librarySnap struct {
	Books    map[string]bookSnap
	Files    map[string]fileSnap
	PIDOwner string
}

func snapLibrary(t *testing.T, store database.Store, f combineFixture) librarySnap {
	t.Helper()
	out := librarySnap{Books: map[string]bookSnap{}, Files: map[string]fileSnap{}}
	for _, id := range []string{f.keep, f.a, f.b} {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, "book %s", id)
		out.Books[id] = bookSnap{Title: b.Title, FilePath: b.FilePath, SoftDel: b.IsSoftDeleted()}
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		for _, fl := range files {
			out.Files[fl.ID] = fileSnap{BookID: fl.BookID, Disc: fl.DiscNumber, Track: fl.TrackNumber}
		}
	}
	owner, err := store.GetBookByExternalID("itunes", f.pid)
	require.NoError(t, err)
	out.PIDOwner = owner
	return out
}

// requireCombined asserts the fixture was folded into keep.
func requireCombined(t *testing.T, store database.Store, f combineFixture) {
	t.Helper()
	s := snapLibrary(t, store, f)
	require.True(t, s.Books[f.a].SoftDel, "a retired")
	require.True(t, s.Books[f.b].SoftDel, "b retired")
	for id, fl := range s.Files {
		require.Equal(t, f.keep, fl.BookID, "file %s moved to keep", id)
	}
	require.Equal(t, f.keep, s.PIDOwner)
}

// pointerRows returns opID's merge journal pointer rows of changeType.
func pointerRows(t *testing.T, store database.Store, opID, changeType string) []*database.OperationChange {
	t.Helper()
	rows, err := store.GetOperationChanges(opID)
	require.NoError(t, err)
	var out []*database.OperationChange
	for _, r := range rows {
		if r.ChangeType == changeType && !r.Voided {
			out = append(out, r)
		}
	}
	return out
}

// revertAndRequireRestored reverts opID and asserts the library is back to
// before.
func revertAndRequireRestored(t *testing.T, store database.Store, opID string, f combineFixture, before librarySnap) {
	t.Helper()
	res, err := NewRevertService(store).RevertOperation(opID)
	require.NoError(t, err)
	require.Equal(t, 1, res.RestoredTypes[undo.ChangeTypeMergeCombineJournal], "%+v", res)
	require.Equal(t, before, snapLibrary(t, store, f), "the revert put every book, file row and id back")
}

// A combine no operation runs (POST /audiobooks/combine, a review approval)
// mints its own operation id; reverting it undoes the combine.
func TestRevert_CombineBooks_OwnOperationID(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	before := snapLibrary(t, store, f)

	res, err := merge.NewService(store).CombineBooks([]string{f.a, f.b, f.keep}, f.keep, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.OperationID)
	requireCombined(t, store, f)
	ptr := pointerRows(t, store, res.OperationID, undo.ChangeTypeMergeCombineJournal)
	require.Len(t, ptr, 1)
	require.Equal(t, res.JournalID, ptr[0].NewValue)
	require.Equal(t, f.keep, ptr[0].BookID, "the pointer is on the survivor, so GET /audiobooks/:id/changes lists it")

	revertAndRequireRestored(t, store, res.OperationID, f, before)

	// A second revert finds every row reverted and changes nothing.
	_, err = NewRevertService(store).RevertOperation(res.OperationID)
	require.Error(t, err)
	require.Equal(t, before, snapLibrary(t, store, f))
}

// A combine an operation runs journals under that operation's id.
func TestRevert_CombineBooks_UnderCallerOperation(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	before := snapLibrary(t, store, f)

	res, err := merge.NewService(store).CombineBooksWithOptions([]string{f.a, f.b, f.keep}, f.keep, nil, merge.CombineOptions{OperationID: "op-combine"})
	require.NoError(t, err)
	require.Equal(t, "op-combine", res.OperationID)
	require.Len(t, pointerRows(t, store, "op-combine", undo.ChangeTypeMergeCombineJournal), 1)

	revertAndRequireRestored(t, store, "op-combine", f, before)
}

// noLedgerStore hides the undo ledger: a merge.Store that cannot journal the
// pointer.
type noLedgerStore struct {
	merge.Store
}

func (noLedgerStore) CreateOperationChange(*database.OperationChange) error {
	return errNoLedger
}

var errNoLedger = &undo.ReferentError{Reason: undo.ReasonFieldUnreadable, Detail: "ledger offline"}

// A combine whose pointer cannot be journaled is refused before its first
// write: nothing moves, and its journal is emptied so it undoes to nothing.
func TestCombineBooks_RefusedWhenPointerCannotBeJournaled(t *testing.T) {
	store := dbtest.NewStore(t)
	f := seedCombineFixture(t, store)
	before := snapLibrary(t, store, f)

	ms := merge.NewService(noLedgerStore{Store: store})
	_, err := ms.CombineBooks([]string{f.a, f.b, f.keep}, f.keep, nil)
	require.ErrorContains(t, err, "undo pointer could not be journaled")
	require.Equal(t, before, snapLibrary(t, store, f), "a refused combine changes nothing")
	js, err := ms.ListCombineJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	require.Empty(t, js[0].Absorbed, "the journal names nothing to restore")
}

// A split-book merge with no OnJournal journals the pointer itself: under the
// caller's op id, or one of its own.
func TestRevert_SplitBookMerge(t *testing.T) {
	for _, opID := range []string{"", "op-split"} {
		store := dbtest.NewStore(t)
		f := seedCombineFixture(t, store)
		before := snapLibrary(t, store, f)

		res, err := dedup.MergeSplitBookClusterWithOptions(store, f.keep, []string{f.a, f.b}, "", dedup.SplitMergeOptions{OperationID: opID})
		require.NoError(t, err)
		require.Empty(t, res.Errors)
		require.NotEmpty(t, res.OperationID)
		if opID != "" {
			require.Equal(t, opID, res.OperationID)
		}
		requireCombined(t, store, f)
		ptr := pointerRows(t, store, res.OperationID, undo.ChangeTypeMergeCombineJournal)
		require.Len(t, ptr, 1)
		require.Equal(t, res.JournalID, ptr[0].NewValue)

		revertAndRequireRestored(t, store, res.OperationID, f, before)
	}
}

// siblingFixture: K (primary of H) and loser L (primary of G) with its
// non-primary sibling LS. Merging L into K carries LS into H.
func siblingFixture(t *testing.T) (*vptest.Fixture, string, string, string) {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	return f, k, l, ls
}

// A merge that moves a sibling journals the sibling-journal pointer (its own
// id, or the caller's); reverting it puts the sibling back in its group. The
// merge's loser stays retired, as with the retired sibling-undo route.
func TestRevert_MergeBooks_SiblingMove(t *testing.T) {
	for _, opID := range []string{"", "op-link"} {
		f, k, l, ls := siblingFixture(t)
		res, err := merge.NewService(f.S).MergeBooksWithOptions([]string{l, k}, k, merge.MergeOptions{OperationID: opID})
		require.NoError(t, err)
		require.NotEmpty(t, res.SiblingJournalID)
		require.NotEmpty(t, res.SiblingOperationID)
		if opID != "" {
			require.Equal(t, opID, res.SiblingOperationID)
		}
		require.Equal(t, "H", f.GroupOf(t, ls), "the merge carried the sibling along")
		ptr := pointerRows(t, f.S, res.SiblingOperationID, undo.ChangeTypeMergeSiblingJournal)
		require.Len(t, ptr, 1)
		require.Equal(t, res.SiblingJournalID, ptr[0].NewValue)
		require.Equal(t, k, ptr[0].BookID)

		rv, err := NewRevertService(f.S).RevertOperation(res.SiblingOperationID)
		require.NoError(t, err)
		require.Equal(t, 1, rv.RestoredTypes[undo.ChangeTypeMergeSiblingJournal])
		require.Equal(t, "G", f.GroupOf(t, ls), "the sibling is back in its group")
		f.RequireSinglePrimary(t, "G", ls)
		f.RequireSinglePrimary(t, "H", k)
	}

	// A merge that moves no sibling writes no journal and no pointer.
	f := vptest.New(t)
	a := f.Book(t, vptest.Spec{ID: "a", Group: "A", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "B", Primary: "true"})
	res, err := merge.NewService(f.S).MergeBooks([]string{b, a}, a)
	require.NoError(t, err)
	require.Empty(t, res.SiblingJournalID)
	require.Empty(t, res.SiblingOperationID)
}

// A merge whose sibling pointer cannot be journaled is refused before any
// membership write, and its journal is marked aborted.
func TestMergeBooks_RefusedWhenSiblingPointerCannotBeJournaled(t *testing.T) {
	f, k, l, ls := siblingFixture(t)
	ms := merge.NewService(noLedgerStore{Store: f.S})
	_, err := ms.MergeBooks([]string{l, k}, k)
	require.ErrorContains(t, err, "no undo pointer")
	require.Equal(t, "G", f.GroupOf(t, ls))
	require.Equal(t, "G", f.GroupOf(t, l))
	js, err := ms.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	require.Equal(t, merge.SiblingJournalAborted, js[0].Status)
}
