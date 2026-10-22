// file: internal/plugins/maintenance/retire_carry_stack_test.go
// version: 1.4.0
// guid: 5b2d7e91-3c46-4a08-9f15-e6a0d83c7b24
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Several retires into one survivor, in one operation or in two, each carry
// ratings and list places that the next one sees, and each must undo without
// losing what the others carried (retire_carry.go, undo/user_content_follow.go).
// Every fixture here is synthetic.

// opaqueStore is a decorator that does not opt into database.StoreUnwrapper,
// so its collection and playlist capabilities cannot be resolved.
type opaqueStore struct{ OpsStore }

func countOf(ids []string, id string) int {
	n := 0
	for _, x := range ids {
		if x == id {
			n++
		}
	}
	return n
}

// A store whose collections cannot be reached refuses the retire BEFORE its
// first write: the survivor takes nothing, the retired book stays live, the
// retired book's reading progress stays where it was, and the journal is empty.
func TestRetireCarry_NoCollectionCapabilityRefusesBeforeAnyWrite(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	d.rcRate(t, "L", database.UpdateBookRatingRequest{Overall: rcFloat(4)})
	col := d.rcCollection(t, "Favourites", l)
	pl := d.rcPlaylist(t, "Road trip", l)
	u, err := d.s.CreateUser("reader", "reader@example.com", "bcrypt", "x", []string{"user"}, "active")
	require.NoError(t, err)
	files, err := d.s.GetBookFiles(l)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	require.NoError(t, d.s.SetUserPosition(u.ID, l, files[0].ID, 100))

	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-apply")
	_, err = retireInto(context.Background(), d.p, opaqueStore{d.s}, w, time.Now, dcFixerID, l, s, nil)
	require.ErrorContains(t, err, "unavailable")
	require.ErrorContains(t, err, "nothing was written")

	changes, err := d.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	require.Empty(t, changes, "nothing journaled")
	require.True(t, d.live(t, "L"), "the retired book is still live")
	sb, err := d.s.GetBookByID(s)
	require.NoError(t, err)
	require.Nil(t, sb.UserRatingOverall, "the survivor took nothing")
	require.Equal(t, []string{l}, d.rcMembers(t, col))
	require.Equal(t, []string{l}, d.rcPlaylistMembers(t, pl))
	pos, err := d.s.ListUserPositionsForBook(u.ID, l)
	require.NoError(t, err)
	require.Len(t, pos, 1, "the position stayed on the retired book")
	pos, err = d.s.ListUserPositionsForBook(u.ID, s)
	require.NoError(t, err)
	require.Empty(t, pos)
}

// Five children retired into one survivor in ONE operation: stacked rating
// fills (each child contributes a different field), two children in one list,
// the survivor already in another. The whole operation then reverts to the
// exact prior state.
func TestRetireCarry_FiveChildrenOneOperationRevertRestoresEverything(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	children := make([]string, 5)
	rows := make([]string, 5)
	for i := range children {
		n := string(rune('1' + i))
		children[i] = f.hcChild(t, "c"+n, "Stray "+n, "hash-p0"+n)
		rows[i] = hcRowID(parent, children[i])
	}
	f.rcRate(t, "c1", database.UpdateBookRatingRequest{Overall: rcFloat(1)})
	f.rcRate(t, "c2", database.UpdateBookRatingRequest{Story: rcFloat(2)})
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Performance: rcFloat(3)})
	f.rcRate(t, "c4", database.UpdateBookRatingRequest{Notes: rcStr("n4")})
	f.rcRate(t, "c5", database.UpdateBookRatingRequest{Overall: rcFloat(5)})
	colX := f.rcCollection(t, "Both children", children[0], "x", children[1], children[2])
	colY := f.rcCollection(t, "Already listed", children[4], parent, children[0])
	plX := f.rcPlaylist(t, "Road trip", children[3], "y", children[4])

	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", rows, nil)
	require.Equal(t, 5, out.Applied, "%+v", out.Rows)

	p, err := f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.NotNil(t, p.UserRatingOverall)
	require.Contains(t, []float64{1, 5}, *p.UserRatingOverall, "the first child retired fills the overall rating")
	require.Equal(t, 2.0, *p.UserRatingStory)
	require.Equal(t, 3.0, *p.UserRatingPerformance)
	require.Equal(t, "n4", *p.UserRatingNotes)
	for _, c := range children {
		require.False(t, f.liveID(t, c))
		require.Zero(t, countOf(f.rcMembers(t, colX), c))
		require.Zero(t, countOf(f.rcMembers(t, colY), c))
		require.Zero(t, countOf(f.rcPlaylistMembers(t, plX), c))
	}
	require.Equal(t, 1, countOf(f.rcMembers(t, colX), parent), "two children, one place")
	require.Equal(t, 1, countOf(f.rcMembers(t, colY), parent))
	require.Equal(t, 1, countOf(f.rcPlaylistMembers(t, plX), parent))

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Zero(t, rr.NotRestorable, "%+v", rr)

	p, err = f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.Nil(t, p.UserRatingOverall)
	require.Nil(t, p.UserRatingStory)
	require.Nil(t, p.UserRatingPerformance)
	require.Nil(t, p.UserRatingNotes)
	require.Equal(t, []string{children[0], "x", children[1], children[2]}, f.rcMembers(t, colX))
	require.Equal(t, []string{children[4], parent, children[0]}, f.rcMembers(t, colY))
	require.Equal(t, []string{children[3], "y", children[4]}, f.rcPlaylistMembers(t, plX))
	for _, c := range children {
		require.True(t, f.liveID(t, c))
	}
}

// twoOps retires c3 in op-apply-1 and c4 in op-apply-2 into one survivor
// that both rated and listed, and returns the ids.
type twoOps struct {
	f      *fragFixture
	parent string
	c3, c4 string
	colX   string
	plX    string
}

func seedTwoOps(t *testing.T) *twoOps {
	t.Helper()
	f := hcSeed(t)
	o := &twoOps{f: f, parent: f.ids["parent"]}
	o.c3 = f.hcChild(t, "c3", "Stray Three", "hash-p03")
	o.c4 = f.hcChild(t, "c4", "Stray Four", "hash-p04")
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Overall: rcFloat(4), Story: rcFloat(1)})
	f.rcRate(t, "c4", database.UpdateBookRatingRequest{Story: rcFloat(2), Notes: rcStr("note")})
	o.colX = f.rcCollection(t, "Shared", o.c3, o.c4, "y")
	o.plX = f.rcPlaylist(t, "Shared list", o.c3, "y", o.c4)
	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply-1", []string{hcRowID(o.parent, o.c3)}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Equal(t, []string{o.parent, o.c4, "y"}, f.rcMembers(t, o.colX), "op 1: the survivor takes c3's slot")
	out = f.apply(t, "op-plan", "op-apply-2", []string{hcRowID(o.parent, o.c4)}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Equal(t, []string{o.parent, "y"}, f.rcMembers(t, o.colX), "op 2: c4 removed, the survivor already listed")
	require.Equal(t, []string{o.parent, "y"}, f.rcPlaylistMembers(t, o.plX))
	return o
}

func (o *twoOps) revert(t *testing.T, op string) {
	t.Helper()
	rr, err := audiobooks.NewRevertService(o.f.s).RevertOperation(op)
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%s: %+v", op, rr)
	require.Zero(t, rr.NotRestorable, "%s: %+v", op, rr)
}

// Reverting the EARLIER operation first must not lose what the later one
// carried, and must not leave the survivor behind either. The later op's
// retire of c4 was made because the survivor was already listed (in c3's
// slot), so putting c3 back keeps the survivor listed until c4's own revert,
// which then takes it out again: both orders end with the original lists.
func TestRetireCarry_RevertingTheEarlierOperationFirstEndsWithTheOriginalLists(t *testing.T) {
	t.Parallel()
	o := seedTwoOps(t)
	f := o.f

	o.revert(t, "op-apply-1")
	require.True(t, f.liveID(t, o.c3))
	require.False(t, f.liveID(t, o.c4))
	col := f.rcMembers(t, o.colX)
	require.Equal(t, 1, countOf(col, o.c3), "c3 is back in its list")
	require.Equal(t, 1, countOf(col, o.parent), "the survivor stands in for c4 until op 2 is reverted")
	require.Equal(t, 0, countOf(col, o.c4))
	pl := f.rcPlaylistMembers(t, o.plX)
	require.Equal(t, 1, countOf(pl, o.c3))
	require.Equal(t, 1, countOf(pl, o.parent))
	p, err := f.s.GetBookByID(o.parent)
	require.NoError(t, err)
	require.Nil(t, p.UserRatingOverall, "op 1's field is restored")
	require.Nil(t, p.UserRatingStory, "op 1 filled story (c3 retired first)")
	require.Equal(t, "note", *p.UserRatingNotes, "op 2's field is kept")

	o.revert(t, "op-apply-2")
	require.True(t, f.liveID(t, o.c4))
	require.Equal(t, []string{o.c3, o.c4, "y"}, f.rcMembers(t, o.colX), "the exact original list, the survivor not left behind")
	require.Equal(t, []string{o.c3, "y", o.c4}, f.rcPlaylistMembers(t, o.plX))
	require.Zero(t, countOf(f.rcMembers(t, o.colX), o.parent))
	require.Zero(t, countOf(f.rcPlaylistMembers(t, o.plX), o.parent))
	p, err = f.s.GetBookByID(o.parent)
	require.NoError(t, err)
	require.Nil(t, p.UserRatingNotes)
}

// layoutOf resolves a list layout written with placeholders: "k0".."k9" are
// the retired children, "P" is the survivor, anything else is a plain book.
func layoutOf(layout []string, kids []string, parent string) []string {
	out := make([]string, len(layout))
	for i, e := range layout {
		switch {
		case e == "P":
			out[i] = parent
		case len(e) == 2 && e[0] == 'k':
			out[i] = kids[e[1]-'0']
		default:
			out[i] = e
		}
	}
	return out
}

func permsOf(n int) [][]int {
	if n <= 1 {
		return [][]int{{0}}
	}
	var out [][]int
	for _, p := range permsOf(n - 1) {
		for at := 0; at <= len(p); at++ {
			q := append(append(append([]int{}, p[:at]...), n-1), p[at:]...)
			out = append(out, q)
		}
	}
	return out
}

// revertInEveryOrder retires the children into the survivor in operations of
// the given shape (opKids[i] are the children retired by operation i, in
// order), then reverts the operations in every order: each time every list, in
// its exact original order, must come back, the survivor must be out of the
// lists it was not in, and nothing may be reported as a guess
// (RevertResult.RestoredByIndex, Kept).
func revertInEveryOrder(t *testing.T, opKids [][]int, colLayouts, plLayouts [][]string) {
	t.Helper()
	nKids := 0
	for _, ks := range opKids {
		nKids += len(ks)
	}
	for _, perm := range permsOf(len(opKids)) {
		t.Run(fmt.Sprint(perm), func(t *testing.T) {
			t.Parallel()
			f := hcSeed(t)
			parent := f.ids["parent"]
			kids := make([]string, nKids)
			for i := range kids {
				n := fmt.Sprint(i + 1)
				kids[i] = f.hcChild(t, "c"+n, "Stray "+n, "hash-p0"+n)
			}
			var cols, pls []string
			var wantCols, wantPls [][]string
			for i, l := range colLayouts {
				want := layoutOf(l, kids, parent)
				cols = append(cols, f.rcCollection(t, fmt.Sprintf("Shared %d", i), want...))
				wantCols = append(wantCols, want)
			}
			for i, l := range plLayouts {
				want := layoutOf(l, kids, parent)
				pls = append(pls, f.rcPlaylist(t, fmt.Sprintf("Shared list %d", i), want...))
				wantPls = append(wantPls, want)
			}
			f.plan(t, "op-plan")
			for i, ks := range opKids {
				rows := make([]string, len(ks))
				for j, k := range ks {
					rows[j] = hcRowID(parent, kids[k])
				}
				out := f.apply(t, "op-plan", fmt.Sprintf("op-apply-%d", i), rows, nil)
				require.Equal(t, len(ks), out.Applied, "%+v", out.Rows)
			}
			for _, i := range perm {
				rr, err := audiobooks.NewRevertService(f.s).RevertOperation(fmt.Sprintf("op-apply-%d", i))
				require.NoError(t, err)
				require.Zero(t, rr.Failed, "op %d: %+v", i, rr)
				require.Zero(t, rr.RestoredByIndex, "op %d: %+v", i, rr)
				require.Empty(t, rr.Kept, "op %d", i)
			}
			for i, c := range cols {
				require.Equal(t, wantCols[i], f.rcMembers(t, c), "collection %d, revert order %v", i, perm)
			}
			for i, p := range pls {
				require.Equal(t, wantPls[i], f.rcPlaylistMembers(t, p), "playlist %d, revert order %v", i, perm)
			}
			for _, k := range kids {
				require.True(t, f.liveID(t, k))
			}
		})
	}
}

// Three operations retire three children into one survivor (the first replaces
// a slot, the other two are removed because the survivor is listed). Whatever
// order they are reverted in, every retired book is back in its exact original
// position and the survivor is out of the lists. A retire records the books
// beside the retired one, and the revert finds the slot by them, so the order
// the operations are undone in does not matter.
func TestRetireCarry_ThreeOperationsRevertInAnyOrder(t *testing.T) {
	t.Parallel()
	revertInEveryOrder(t, [][]int{{0}, {1}, {2}},
		[][]string{{"k0", "k1", "x", "k2"}},
		[][]string{{"x", "k0", "k2", "k1"}})
}

// Four operations, a survivor that is a member of some of the lists and not
// of others, children scattered and adjacent in different lists: every one of
// the 24 revert orders (the worst cases include oldest first, newest first and
// interleaved) restores every list exactly.
func TestRetireCarry_FourOperationsRevertInAnyOrder(t *testing.T) {
	t.Parallel()
	revertInEveryOrder(t, [][]int{{0}, {1}, {2}, {3}},
		[][]string{
			{"k2", "x", "k0", "k1", "y", "k3"},
			{"x", "k0", "P", "k1", "k3", "k2"},
			{"k3", "k2", "k1", "k0"},
		},
		[][]string{
			{"k3", "k1", "x", "k0", "y", "k2"},
			{"P", "k2", "k0", "y", "k3", "k1"},
		})
}

// Operations that retire several children each: the rows of one operation are
// undone together, the operations in any order.
func TestRetireCarry_MultiRowOperationsRevertInAnyOrder(t *testing.T) {
	t.Parallel()
	revertInEveryOrder(t, [][]int{{0, 1}, {2}, {3}},
		[][]string{
			{"y", "k1", "k0", "x", "k3", "k2"},
			{"k3", "P", "k2", "k0", "x", "k1"},
		},
		[][]string{{"k0", "x", "k3", "k1", "y", "k2"}})
}

// A journal written before retires recorded their neighbours has no anchors,
// and there is no second restore path for it: the revert would have to guess a
// position from an index that later edits made meaningless, so it refuses such a
// row, writes nothing for it and says so (NotRestorable, with the reason), never
// silently. The lists stay as they are.
func TestRetireCarry_RowsWithoutAnchorsAreReportedNotRestorableAndWriteNothing(t *testing.T) {
	t.Parallel()
	o := seedTwoOps(t)
	f := o.f
	// Rewrite the journal as it was before the anchors: same rows, same ids,
	// the records without Prev, Next and the anchored flag.
	var rows []*database.OperationChange
	for _, op := range []string{"op-apply-1", "op-apply-2"} {
		changes, err := f.s.GetOperationChanges(op)
		require.NoError(t, err)
		for _, c := range changes {
			kind, _, ok := undo.UserContentFromField(c.FieldName)
			if !ok || kind == undo.UserContentRating {
				continue
			}
			rows = append(rows, c)
		}
	}
	for _, c := range rows {
		rec, err := undo.DecodeMembership(c.OldValue)
		require.NoError(t, err)
		require.True(t, rec.Anchored, "a fresh retire records its neighbours")
		rec.Anchored, rec.Prev, rec.Next = false, "", ""
		c.OldValue, err = undo.EncodeMembership(rec)
		require.NoError(t, err)
		require.NoError(t, f.s.CreateOperationChange(c))
	}
	colBefore, plBefore := f.rcMembers(t, o.colX), f.rcPlaylistMembers(t, o.plX)

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply-2")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Zero(t, rr.RestoredByIndex, "there is no restore by index any more: %+v", rr)
	require.Positive(t, rr.NotRestorable, "the unanchored rows are reported: %+v", rr)
	require.Equal(t, colBefore, f.rcMembers(t, o.colX), "nothing was written for a row that cannot be restored exactly")
	require.Equal(t, plBefore, f.rcPlaylistMembers(t, o.plX))
	require.Contains(t, rr.Summary(), "cannot be undone automatically")
	require.Contains(t, rr.Summary(), "user_content_follow:(unparsable)")
}

// Reverting in reverse order restores the original lists exactly.
func TestRetireCarry_RevertingInReverseOrderRestoresExactly(t *testing.T) {
	t.Parallel()
	o := seedTwoOps(t)
	f := o.f

	o.revert(t, "op-apply-2")
	require.Equal(t, []string{o.parent, o.c4, "y"}, f.rcMembers(t, o.colX), "op 1's state")
	o.revert(t, "op-apply-1")
	require.Equal(t, []string{o.c3, o.c4, "y"}, f.rcMembers(t, o.colX))
	require.Equal(t, []string{o.c3, "y", o.c4}, f.rcPlaylistMembers(t, o.plX))
	p, err := f.s.GetBookByID(o.parent)
	require.NoError(t, err)
	require.Nil(t, p.UserRatingOverall)
	require.Nil(t, p.UserRatingStory)
	require.Nil(t, p.UserRatingNotes)
	require.Zero(t, countOf(f.rcMembers(t, o.colX), o.parent))
}

// Each carry on a list journals a marker on the survivor: "took" when the
// survivor replaced the retired book in its slot, "held" when the retired book
// was only removed because the survivor was already listed. They are what the
// two reverts above read.
func TestRetireCarry_AMarkerIsJournaledOnTheSurvivorForEveryListCarry(t *testing.T) {
	t.Parallel()
	o := seedTwoOps(t)
	count := func(op string) map[string]int {
		changes, err := o.f.s.GetOperationChanges(op)
		require.NoError(t, err)
		kinds := map[string]int{}
		for _, c := range changes {
			k, _, ok := undo.UserContentFromField(c.FieldName)
			if ok && undo.IsMarker(k) {
				require.Equal(t, o.parent, c.BookID, "a marker is journaled on the survivor")
				kinds[k]++
			}
		}
		return kinds
	}
	require.Equal(t, map[string]int{undo.UserContentCollectionTook: 1, undo.UserContentPlaylistTook: 1}, count("op-apply-1"))
	require.Equal(t, map[string]int{undo.UserContentCollectionHeld: 1, undo.UserContentPlaylistHeld: 1}, count("op-apply-2"))
}

// The revert counts a marker row apart from the restored rows: it restores
// nothing, so it is not in Restored (and not in RestoredTypes), and the
// summary says so.
func TestRetireCarry_RevertReportsMarkersApartFromRestored(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Overall: rcFloat(4)})
	f.rcCollection(t, "Favourites", "other-1", child)
	f.rcPlaylist(t, "Road trip", child)
	f.rcRetire(t, child)

	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	markers, others := 0, 0
	for _, c := range changes {
		if k, _, ok := undo.UserContentFromField(c.FieldName); ok && undo.IsMarker(k) {
			markers++
		} else {
			others++
		}
	}
	require.Equal(t, 2, markers)

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, markers, rr.Markers, "%+v", rr)
	require.Equal(t, len(changes)-markers, rr.Restored, "the markers are not counted restored: %+v", rr)
	require.Equal(t, 3, rr.RestoredTypes[undo.ChangeTypeUserContentFollow], "the rating, collection and playlist rows only: %+v", rr.RestoredTypes)
	require.Contains(t, rr.Summary(), "2 marker row(s) cleared")
	// Every row, markers included, is marked reverted: a second revert has nothing to do.
	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.ErrorContains(t, err, "already been reverted")
}

// Preflight predicts what the revert will do for carried rows: restorable
// while the lists and the rating hold what the retire wrote, refused once the
// owner has changed them.
func TestRetireCarry_PreflightPredictsTheRevert(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Overall: rcFloat(4)})
	colA := f.rcCollection(t, "Favourites", "other-1", child)
	plA := f.rcPlaylist(t, "Road trip", child)
	parent := f.rcRetire(t, child)

	rep, err := undo.PreflightUndoConflicts(f.s, "op-apply")
	require.NoError(t, err)
	require.Empty(t, rep.CheckFailed, "%+v", rep)

	_, err = f.s.RemoveBookFromCollection(colA, parent)
	require.NoError(t, err)
	rep, err = undo.PreflightUndoConflicts(f.s, "op-apply")
	require.NoError(t, err)
	require.Len(t, rep.CheckFailed, 1, "the collection row is refused: the survivor is no longer there: %+v", rep)

	pl, err := f.s.GetUserPlaylist(plA)
	require.NoError(t, err)
	pl.BookIDs = []string{"other-9"}
	require.NoError(t, f.s.UpdateUserPlaylist(pl))
	f.rcRate(t, "parent", database.UpdateBookRatingRequest{Overall: rcFloat(1)})
	rep, err = undo.PreflightUndoConflicts(f.s, "op-apply")
	require.NoError(t, err)
	require.Len(t, rep.CheckFailed, 3, "collection, playlist and rating: %+v", rep)

	// The prediction is the revert's: it refuses the same three.
	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.ErrorContains(t, err, "partially reverted")
	require.Equal(t, 3, rr.Failed, "%+v", rr)
}

// A list is read for every retire, never from an earlier listing: a list the
// owner adds the retired book to between two retires of one operation is
// carried, one it removes the book from is left alone, and a chain (the
// survivor is itself retired later in the operation) finds the list the first
// carry put it in.
func TestRetireCarry_EveryRetireReadsTheListsAfresh(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	c1 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	c2 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	c3 := f.hcChild(t, "c3", "Stray 3", "hash-p03")
	col := f.rcCollection(t, "Favourites", c1, c2)
	w := f.fragWriter(t, "op-apply")
	_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, c1, parent, nil)
	require.NoError(t, err)
	require.Equal(t, []string{parent, c2}, f.rcMembers(t, col))

	// Between the retires the owner removes c2 from the list, adds c3 to it
	// and makes a second list with c3.
	_, err = f.s.RemoveBookFromCollection(col, c2)
	require.NoError(t, err)
	_, err = f.s.AddBooksToCollection(col, []string{c3})
	require.NoError(t, err)
	col2 := f.rcCollection(t, "New", c3)
	_, err = retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, c2, parent, nil)
	require.NoError(t, err)
	require.Equal(t, []string{parent, c3}, f.rcMembers(t, col), "c2 was no longer listed: nothing carried for it")
	_, err = retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, c3, parent, nil)
	require.NoError(t, err)
	require.Equal(t, []string{parent}, f.rcMembers(t, col), "a list added mid-op is carried, the survivor already listed")
	require.Equal(t, []string{parent}, f.rcMembers(t, col2), "so is a list made mid-op")
}

// The carry runs for the other Repairs retire fixers too.
func TestRetireCarry_DuplicateCopiesCarryAndRevert(t *testing.T) {
	d := newDCFixture(t)
	s, l := d.dune(t)
	d.rcRate(t, "L", database.UpdateBookRatingRequest{Overall: rcFloat(4)})
	col := d.rcCollection(t, "Favourites", "other-1", l)
	pl := d.rcPlaylist(t, "Road trip", l, s)

	res := d.planFor(t, dcFixerID, "op-plan", nil)
	row := findRow(t, res, dupRowID(s, l))
	require.True(t, row.Applicable(), row.SkipReason)
	out := d.applyFor(t, dcFixerID, "op-plan", "op-apply", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	sb, err := d.s.GetBookByID(s)
	require.NoError(t, err)
	require.Equal(t, 4.0, *sb.UserRatingOverall)
	require.Equal(t, []string{"other-1", s}, d.rcMembers(t, col))
	require.Equal(t, []string{s}, d.rcPlaylistMembers(t, pl), "the survivor was already listed")

	rr, err := audiobooks.NewRevertService(d.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	sb, err = d.s.GetBookByID(s)
	require.NoError(t, err)
	require.Nil(t, sb.UserRatingOverall)
	require.Equal(t, []string{"other-1", l}, d.rcMembers(t, col))
	require.Equal(t, []string{l, s}, d.rcPlaylistMembers(t, pl))
	require.True(t, d.live(t, "L"))
}

func TestRetireCarry_ConsolidationLeftoversCarryAndRevert(t *testing.T) {
	f := newLFFixture(t)
	l, c := f.whm(t)
	f.rcRate(t, "L", database.UpdateBookRatingRequest{Overall: rcFloat(3)})
	col := f.rcCollection(t, "Favourites", l, "other-1")
	pl := f.rcPlaylist(t, "Road trip", "other-2", l)

	f.planLF(t, "op-plan")
	out := f.applyLF(t, "op-plan", "op-apply", []string{"leftover:" + l})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	cb, err := f.s.GetBookByID(c)
	require.NoError(t, err)
	require.Equal(t, 3.0, *cb.UserRatingOverall)
	require.Equal(t, []string{c, "other-1"}, f.rcMembers(t, col))
	require.Equal(t, []string{"other-2", c}, f.rcPlaylistMembers(t, pl))

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	cb, err = f.s.GetBookByID(c)
	require.NoError(t, err)
	require.Nil(t, cb.UserRatingOverall)
	require.Equal(t, []string{l, "other-1"}, f.rcMembers(t, col))
	require.Equal(t, []string{"other-2", l}, f.rcPlaylistMembers(t, pl))
}
