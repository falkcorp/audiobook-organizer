// file: internal/plugins/maintenance/retire_carry_chain_test.go
// version: 1.0.0
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
// end-of-run stamp of RevertOperation), once, and with oldBuild also rewrites
// the atomic list-and-stamp call to stamp only the first id (the survivor-side
// marker), as a build that stamped the row apart from the list did. Everything
// else goes through.
type finalMarkFails struct {
	*database.PebbleStore
	armed    atomic.Bool
	oldBuild bool
}

func (s *finalMarkFails) Unwrap() database.Store { return s.PebbleStore }

func (s *finalMarkFails) MarkOperationChangesReverted(op string, ids []string) error {
	if len(ids) > 1 && s.armed.CompareAndSwap(true, false) {
		return errors.New("injected: process died before the end-of-run stamp")
	}
	return s.PebbleStore.MarkOperationChangesReverted(op, ids)
}

func (s *finalMarkFails) UpdateCollectionBooksAndMark(id string, fn func([]string) ([]string, error), mark database.OperationMark) (*database.Collection, error) {
	if s.oldBuild && len(mark.ChangeIDs) > 1 {
		mark.ChangeIDs = mark.ChangeIDs[:1]
	}
	return s.PebbleStore.UpdateCollectionBooksAndMark(id, fn, mark)
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

// A row that an older build left standing with its marker stamped (it stamped
// the marker with the list and the row apart, and died between) is stale once a
// newer retire takes the book out again: the retry writes nothing, marks the
// row done and says so in Kept.
func TestRetireCarry_AStaleRowLeftByAnOlderBuildIsSupersededNotRetried(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "S3", "b", "S1"}, []string{"a", "X", "S3", "b"})
	st := &finalMarkFails{PebbleStore: c.f.s, oldBuild: true}
	c.st = st
	c.carry("op2", "X", "S3") // held
	st.armed.Store(true)
	_, err := audiobooks.NewRevertService(st).RevertOperation("op2")
	require.Error(t, err)
	require.Equal(t, []string{"a", "X", "S3", "b"}, c.members())
	c.carry("op5", "X", "S1")
	require.Equal(t, []string{"a", "S1", "S3", "b"}, c.members())

	rr := c.revert("op2")
	require.Equal(t, 1, rr.Superseded, "%+v", rr)
	require.Equal(t, 0, rr.Restored, "%+v", rr)
	require.Len(t, rr.Kept, 1, "%+v", rr)
	require.Contains(t, rr.Kept[0], "retired again")
	require.Equal(t, []string{"a", "S1", "S3", "b"}, c.members(), "op5 stands: nothing was put back")
	c.revert("op5")
	require.Equal(t, []string{"a", "X", "S3", "b"}, c.members())
}
