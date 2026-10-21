// file: internal/plugins/maintenance/retire_carry_stack_test.go
// version: 1.0.0
// guid: 5b2d7e91-3c46-4a08-9f15-e6a0d83c7b24
// last-edited: 2026-10-21

package maintenance

import (
	"context"
	"sync/atomic"
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
// carried: the later op's retire of c4 was made because the survivor was
// already listed (in c3's slot), so putting c3 back keeps the survivor listed
// until c4's own revert says otherwise. The ratings revert field by field.
func TestRetireCarry_RevertingTheEarlierOperationKeepsTheLaterOnesCarry(t *testing.T) {
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
	col = f.rcMembers(t, o.colX)
	require.Equal(t, 1, countOf(col, o.c3))
	require.Equal(t, 1, countOf(col, o.c4), "c4 is back")
	require.Equal(t, 1, countOf(col, "y"))
	pl = f.rcPlaylistMembers(t, o.plX)
	require.Equal(t, 1, countOf(pl, o.c3))
	require.Equal(t, 1, countOf(pl, o.c4))
	p, err = f.s.GetBookByID(o.parent)
	require.NoError(t, err)
	require.Nil(t, p.UserRatingNotes)
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
}

// The survivor-side marker that makes the previous test work is journaled on
// the survivor, by the op that removed the retired book because the survivor
// was already listed.
func TestRetireCarry_AMarkerIsJournaledOnTheSurvivorWhenItWasAlreadyListed(t *testing.T) {
	t.Parallel()
	o := seedTwoOps(t)
	changes, err := o.f.s.GetOperationChanges("op-apply-2")
	require.NoError(t, err)
	markers := 0
	for _, c := range changes {
		k, _, ok := undo.UserContentFromField(c.FieldName)
		if ok && (k == undo.UserContentCollectionHeld || k == undo.UserContentPlaylistHeld) {
			markers++
			require.Equal(t, o.parent, c.BookID)
			require.Equal(t, o.c4, c.NewValue)
		}
	}
	require.Equal(t, 2, markers, "one for the collection, one for the playlist")
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

// countingCarryStore counts the listings a retire's carry makes.
type countingCarryStore struct {
	*database.PebbleStore
	cols, pls atomic.Int32
}

func (s *countingCarryStore) ListCollections(typ string, limit, offset int) ([]database.Collection, int, error) {
	s.cols.Add(1)
	return s.PebbleStore.ListCollections(typ, limit, offset)
}

func (s *countingCarryStore) ListUserPlaylists(typ string, limit, offset int) ([]database.UserPlaylist, int, error) {
	s.pls.Add(1)
	return s.PebbleStore.ListUserPlaylists(typ, limit, offset)
}

// An apply operation that retires many books lists the collections and the
// playlists once, not once per retire (and a fresh operation lists again).
func TestRetireCarry_AnOperationListsTheListsOnce(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	cs := &countingCarryStore{PebbleStore: f.s}
	col := f.rcCollection(t, "Favourites")
	var ids []string
	for _, n := range []string{"1", "2", "3", "4"} {
		c := f.hcChild(t, "c"+n, "Stray "+n, "hash-p0"+n)
		ids = append(ids, c)
		_, err := f.s.AddBooksToCollection(col, []string{c})
		require.NoError(t, err)
	}

	w := f.fragWriter(t, "op-apply")
	for _, c := range ids {
		_, err := retireInto(context.Background(), f.p, cs, w, time.Now, fragFixerID, c, parent, nil)
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, cs.cols.Load(), "one collection listing for four retires")
	require.EqualValues(t, 1, cs.pls.Load(), "one playlist listing for four retires")
	require.Equal(t, []string{parent}, f.rcMembers(t, col), "all four carried: one place for the survivor")

	// A stale index is rebuilt: a clock past the TTL lists again.
	now := time.Now().Add(2 * carryIndexMaxAge)
	f.p.carry.now = func() time.Time { return now }
	c5 := f.hcChild(t, "c5", "Stray 5", "hash-p05")
	_, err := retireInto(context.Background(), f.p, cs, w, time.Now, fragFixerID, c5, parent, nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, cs.cols.Load(), "a stale index is rebuilt")
}

// A list the index missed (the owner added the retired book after it was
// built) is not carried by the stale index, but a list the index names is
// always re-read before it is written: removing the book from the list after
// the index was built leaves the survivor out of it.
func TestRetireCarry_TheIndexIsAHintEveryListIsReRead(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	parent := f.ids["parent"]
	c1 := f.hcChild(t, "c1", "Stray 1", "hash-p01")
	c2 := f.hcChild(t, "c2", "Stray 2", "hash-p02")
	col := f.rcCollection(t, "Favourites", c1, c2)
	w := f.fragWriter(t, "op-apply")
	_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, c1, parent, nil)
	require.NoError(t, err)
	// The owner removes c2 from the list; the index (built before) still names it.
	_, err = f.s.RemoveBookFromCollection(col, c2)
	require.NoError(t, err)
	_, err = retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, c2, parent, nil)
	require.NoError(t, err)
	require.Equal(t, []string{parent}, f.rcMembers(t, col), "c2 was not in the list: nothing carried for it, and the survivor stays")
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
