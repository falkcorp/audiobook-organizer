// file: internal/plugins/maintenance/retire_carry_chain_test.go
// version: 1.1.0
// guid: f3db16f2-7f5a-4c3e-a348-dea96c5942d4
// last-edited: 2026-10-22

package maintenance

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// chainFixture is a collection of named books, with a name for every id, the
// carry of one book into another under an operation (what every retire runs,
// carryUserContentInto), and the revert of an operation. Every fixture is
// synthetic.
type chainFixture struct {
	t   *testing.T
	f   *fragFixture
	id  map[string]string
	col string
	st  database.Store
}

func newChainFixture(t *testing.T, books, listed []string) *chainFixture {
	t.Helper()
	c := &chainFixture{t: t, f: hcSeed(t), id: map[string]string{}}
	c.st = c.f.s
	for _, n := range books {
		c.id[n] = c.f.book(t, "chain"+n, "Book "+n, c.f.path("lib/"+n), nil)
	}
	var ids []string
	for _, n := range listed {
		ids = append(ids, c.id[n])
	}
	c.col = c.f.rcCollection(t, "Shared", ids...)
	return c
}

func (c *chainFixture) names(ids []string) []string {
	out := make([]string, len(ids))
	for i, x := range ids {
		out[i] = x
		for k, v := range c.id {
			if v == x {
				out[i] = k
			}
		}
	}
	return out
}

func (c *chainFixture) members() []string { return c.names(c.f.rcMembers(c.t, c.col)) }

func (c *chainFixture) carry(op, retired, survivor string) {
	c.t.Helper()
	cs, err := resolveCarryStores(c.f.s)
	require.NoError(c.t, err)
	w := repairs.NewWriter(c.f.s, c.f.s, "chain", "bulk_update", "repairs-").WithJournal(c.f.s, c.f.s, op)
	_, err = carryUserContentInto(c.f.s, cs, w, c.id[survivor], c.id[retired])
	require.NoError(c.t, err)
}

func (c *chainFixture) revert(op string) *audiobooks.RevertResult {
	c.t.Helper()
	rr, err := audiobooks.NewRevertService(c.st).RevertOperation(op)
	require.NoError(c.t, err, "%s %+v", op, rr)
	require.Zero(c.t, rr.Failed, "%s %+v", op, rr)
	return rr
}

// A survivor (S1) listed only on borrowed account (op0 took b3's slot, op2
// stands on it for b2) is itself retired into S3 (op3; S3 is listed by op1's
// took). The older operations are reverted before the retire of S1 stops
// standing, in an order that leaves nothing standing on S1 when op3 is
// reverted: S1 must not come back into a list it never belonged to. The
// duplicate-copies fixer retires survivors this way.
func TestRetireCarry_ARetiredSurvivorDoesNotReturnToAListItNeverBelongedTo(t *testing.T) {
	t.Parallel()
	l0 := []string{"b0", "b1", "b2", "b3", "b4", "b5"}
	c := newChainFixture(t, []string{"b0", "b1", "b2", "b3", "b4", "b5", "S1", "S3"}, l0)
	c.carry("op0", "b3", "S1") // took
	c.carry("op1", "b1", "S3") // took
	c.carry("op2", "b2", "S1") // held, borrowing
	c.revert("op0")            // b3 back, S1 stays for op2
	c.carry("op3", "S1", "S3") // S1 itself retired into S3 (held)
	c.revert("op2")
	rr := c.revert("op3")
	require.Equal(t, 1, rr.BooksLeftOut, "S1 was not put back: %+v", rr)
	require.Len(t, rr.Kept, 1, "%+v", rr)
	require.Contains(t, rr.Kept[0], "not put back")
	require.Contains(t, rr.Summary(), "1 book(s) not put back")
	c.revert("op1")
	require.Equal(t, l0, c.members())
}

// The same history with the retire of S1 reverted while an operation still
// stands on S1: it goes back, and leaves with the last operation.
func TestRetireCarry_ARetiredSurvivorStillStoodOnReturnsAndLeavesWithTheLast(t *testing.T) {
	t.Parallel()
	l0 := []string{"b0", "b1", "b2", "b3", "b4", "b5"}
	c := newChainFixture(t, []string{"b0", "b1", "b2", "b3", "b4", "b5", "S1", "S3"}, l0)
	c.carry("op0", "b3", "S1")
	c.carry("op1", "b1", "S3")
	c.carry("op2", "b2", "S1")
	c.carry("op3", "S1", "S3")
	rr := c.revert("op3")
	require.Zero(t, rr.BooksLeftOut, "%+v", rr)
	c.revert("op2")
	c.revert("op0")
	c.revert("op1")
	require.Equal(t, l0, c.members())
}

// finalMarkFails fails the next multi-id MarkOperationChangesReverted (the
// end-of-run stamp of RevertOperation), once. Everything else goes through.
type finalMarkFails struct {
	*database.PebbleStore
	armed atomic.Bool
}

func (s *finalMarkFails) Unwrap() database.Store { return s.PebbleStore }

func (s *finalMarkFails) MarkOperationChangesReverted(op string, ids []string) error {
	if len(ids) > 1 && s.armed.CompareAndSwap(true, false) {
		return errors.New("injected: process died before the end-of-run stamp")
	}
	return s.PebbleStore.MarkOperationChangesReverted(op, ids)
}

// The list write stamps the row with its marker, so a revert whose end-of-run
// stamp is lost is not retried against a list a newer retire has changed since:
// X is retired again by op5 (S1 takes its slot) and the retry of op2 finds the
// row done. Stamping the marker alone left the row standing, and the retry put
// X back although op5 stands.
func TestRetireCarry_ARevertWhoseEndOfRunStampWasLostIsNotRetriedAgainstANewerRetire(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "S3", "b", "S1"}, []string{"a", "X", "S3", "b"})
	st := &finalMarkFails{PebbleStore: c.f.s}
	c.st = st
	c.carry("op2", "X", "S3") // held
	st.armed.Store(true)
	_, err := audiobooks.NewRevertService(st).RevertOperation("op2")
	require.Error(t, err, "the end-of-run stamp was to fail")
	require.Equal(t, []string{"a", "X", "S3", "b"}, c.members(), "the list write landed")
	c.carry("op5", "X", "S1") // X retired again; S1 takes its slot
	require.Equal(t, []string{"a", "S1", "S3", "b"}, c.members())

	_, err = audiobooks.NewRevertService(st).RevertOperation("op2")
	require.ErrorContains(t, err, "already been reverted", "every row of op2 was stamped with its list")
	require.Equal(t, []string{"a", "S1", "S3", "b"}, c.members(), "op5 stands: X stays out, S1 in its slot")
	rr := c.revert("op5")
	require.Equal(t, []string{"a", "X", "S3", "b"}, c.members())
	require.Zero(t, rr.Superseded, "%+v", rr)
}

// voidFails fails, once, the void of a retire's collection row (the retired
// book's row, not the survivor-side marker next to it): the carry that was
// refused cannot take its own row back.
type voidFails struct {
	database.Store
	armed atomic.Bool
}

func (s *voidFails) CreateOperationChange(c *database.OperationChange) error {
	if kind, _, ok := undo.UserContentFromField(c.FieldName); ok && kind == undo.UserContentCollection && c.Voided && s.armed.CompareAndSwap(true, false) {
		return errors.New("injected: void of the row failed")
	}
	return s.Store.CreateOperationChange(c)
}

// A carry whose write is refused voids its row and its marker. When the void
// of the row fails and the void of the marker lands, the row stands for a
// retire that never happened, with no marker. It is stale once a newer retire
// takes the book out for real: the revert of the stale row must not put the
// book back over the newer retire; it writes nothing, marks the row done and
// says so in Kept. This is the one state in which a row stands with no marker,
// and why the guard (undo.NewerCarryStands) is kept: without it the revert
// lists a book that a standing retire removed.
func TestRetireCarry_ARowLeftByAPartiallyVoidedCarryIsSupersededNotRetried(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "S3", "b", "S1"}, []string{"a", "X", "S3", "b"})
	cs, err := resolveCarryStores(c.f.s)
	require.NoError(t, err)
	// A record taken from a list that is not the one now written: the carry's
	// write finds the list changed and refuses.
	l := c.f.rcMembers(t, c.col)
	_, rec, ok := undo.CarryMembership(append([]string{"unrelated"}, l...), c.id["X"], c.id["S3"])
	require.True(t, ok)
	vs := &voidFails{Store: c.f.s}
	vs.armed.Store(true)
	w := repairs.NewWriter(c.f.s, c.f.s, "chain", "bulk_update", "repairs-").WithJournal(c.f.s, vs, "op2")
	err = carryMembership(w, undo.UserContentCollection, c.col, c.id["S3"], c.id["X"], rec, func(carry func([]string) ([]string, error)) error {
		_, e := cs.members.UpdateCollectionBooks(c.col, carry)
		return e
	})
	require.ErrorIs(t, err, errCarryStale)
	require.ErrorContains(t, err, "void of the row failed")
	require.Equal(t, []string{"a", "X", "S3", "b"}, c.members(), "the refused carry wrote nothing")

	c.carry("op5", "X", "S1")
	require.Equal(t, []string{"a", "S1", "S3", "b"}, c.members())

	rep, err := undo.PreflightUndoConflicts(c.f.s, "op2")
	require.NoError(t, err)
	require.Empty(t, rep.CheckFailed, "%+v", rep)
	require.Equal(t, 1, rep.AlreadyRestored, "the revert writes nothing for this row, and the preflight says so: %+v", rep)

	rr := c.revert("op2")
	require.Equal(t, 1, rr.Superseded, "%+v", rr)
	require.Equal(t, 0, rr.Restored, "%+v", rr)
	require.Len(t, rr.Kept, 1, "%+v", rr)
	require.Contains(t, rr.Kept[0], "retired again")
	require.Equal(t, []string{"a", "S1", "S3", "b"}, c.members(), "op5 stands: nothing was put back")
	c.revert("op5")
	require.Equal(t, []string{"a", "X", "S3", "b"}, c.members())
}

// X is retired into S2 (R, took S2 into X's slot) and S2 into S3 (D). R is
// reverted while D still stands, then D: the retired survivor is not listed, so
// X goes back beside its neighbours instead of into S2's slot, and S2 comes back
// only if it belonged to the list (it did not: it stays out and D reports a book
// left out). Every row restores exactly whichever of S3's two histories it is
// (S3 an original member, held; S3 listed by D, took).
func TestRetireCarry_OlderRetireFirstInASurvivorChain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		listed []string
		after  []string // after R and D
		midR   []string // after reverting R
	}{
		{"D held (S3 an original member)", []string{"a", "X", "b", "S3"}, []string{"a", "b", "S3"}, []string{"a", "X", "b", "S3"}},
		{"D took (S3 not listed)", []string{"a", "X", "b"}, []string{"a", "S3", "b"}, []string{"a", "X", "S3", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newChainFixture(t, []string{"a", "X", "b", "S2", "S3"}, tc.listed)
			c.carry("opR", "X", "S2")
			c.carry("opD", "S2", "S3")
			require.Equal(t, tc.after, c.members())

			rep, err := undo.PreflightUndoConflicts(c.f.s, "opR")
			require.NoError(t, err)
			require.Empty(t, rep.CheckFailed, "the revert restores this row exactly, so the preflight must not refuse it: %+v", rep)
			require.Zero(t, rep.NotRestorable, "%+v", rep)

			rr := c.revert("opR")
			require.Equal(t, tc.midR, c.members())
			require.Zero(t, rr.ChangedSince, "%+v", rr)
			require.Zero(t, rr.RestoredByIndex, "the books beside X order it: %+v", rr)

			rep, err = undo.PreflightUndoConflicts(c.f.s, "opD")
			require.NoError(t, err)
			require.Empty(t, rep.CheckFailed, "%+v", rep)
			rr = c.revert("opD")
			require.Equal(t, tc.listed, c.members())
			require.Equal(t, 1, rr.BooksLeftOut, "S2 was listed only for R, which is reverted: %+v", rr)
		})
	}
}

// A survivor S2 that is still borrowed (op1 took X's slot, op2 stands on it for
// Y) is retired into S3 (op3, held). Reverting op3 newest first puts S2 back in
// its place by its neighbours, not by its recorded index. The journal writes a
// retire's marker and its row at two instants (CreateOperationChange stamps each
// write), so this also pins that the graph matches a row to its own operation
// and not to a shared timestamp.
func TestRetireCarry_AReturningSurvivorIsPlacedByTheGraph(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "Y", "b", "S2", "S3"}, []string{"a", "X", "Y", "b", "S3"})
	c.carry("op1", "X", "S2")
	c.carry("op2", "Y", "S2")
	c.carry("op3", "S2", "S3")
	require.Equal(t, []string{"a", "b", "S3"}, c.members())
	rr := c.revert("op3")
	require.Equal(t, []string{"a", "S2", "b", "S3"}, c.members())
	require.Zero(t, rr.RestoredByIndex, "placed by index, not by the graph: %v", rr.Kept)
}

// The same history reverted with the older operations after the newest.
func TestRetireCarry_AReturningSurvivorOutOfOrder(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "Y", "b", "S2", "S3"}, []string{"a", "X", "Y", "b", "S3"})
	c.carry("op1", "X", "S2")
	c.carry("op2", "Y", "S2")
	c.carry("op3", "S2", "S3")
	for _, op := range []string{"op3", "op1", "op2"} {
		rr := c.revert(op)
		require.Zero(t, rr.RestoredByIndex, "%s placed by index: %v", op, rr.Kept)
	}
	require.Equal(t, []string{"a", "X", "Y", "b", "S3"}, c.members())
}
