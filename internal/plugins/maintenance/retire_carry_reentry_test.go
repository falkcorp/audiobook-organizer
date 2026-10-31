// file: internal/plugins/maintenance/retire_carry_reentry_test.go
// version: 1.0.1
// guid: 91d4f2a6-0b7e-4c35-8a19-d6e2c70b4f83
// last-edited: 2026-10-30

package maintenance

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
)

// handRemove takes book name out of the collection the way the owner would,
// outside any retire.
func (c *chainFixture) handRemove(name string) {
	c.t.Helper()
	col, err := c.f.s.GetCollection(c.col)
	require.NoError(c.t, err)
	var keep []string
	for _, id := range col.BookIDs {
		if id != c.id[name] {
			keep = append(keep, id)
		}
	}
	col.BookIDs = keep
	require.NoError(c.t, c.f.s.UpdateCollection(col))
}

// handAppend lists book name at the end of the collection, as the owner would.
func (c *chainFixture) handAppend(name string) {
	c.t.Helper()
	col, err := c.f.s.GetCollection(c.col)
	require.NoError(c.t, err)
	col.BookIDs = append(col.BookIDs, c.id[name])
	require.NoError(c.t, c.f.s.UpdateCollection(col))
}

// The owner takes survivor S out by hand while op1 (B took into S) stands; op2
// then retires d into S, which is not listed, so S takes d's slot. Undoing op1
// must put B back beside a and c, not into d's slot: op2's epoch of S began
// after op1's ended, and op1 has no say over where S is now.
func TestRetireCarry_SurvivorRemovedByHandThenReenteredUndoOldestFirst(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "B", "c", "d", "S"}, []string{"a", "B", "c", "d"})
	c.carry("op1", "B", "S")
	require.Equal(t, []string{"a", "S", "c", "d"}, c.members())
	c.handRemove("S")
	c.carry("op2", "d", "S")
	require.Equal(t, []string{"a", "c", "S"}, c.members())

	rr, err := audiobooks.NewRevertService(c.st).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Zero(t, rr.RestoredByIndex, "no placement by index: %+v", rr)
	require.Equal(t, []string{"a", "B", "c", "S"}, c.members(), "B beside a and c; S stays for op2")

	rr, err = audiobooks.NewRevertService(c.st).RevertOperation("op2")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.RestoredByIndex, "%+v", rr)
	require.Equal(t, []string{"a", "B", "c", "d"}, c.members())
}

// The same, undoing the later retire first: S stays for op1 (its took still
// stands) and leaves with op2, which listed it; undoing op1 then restores B.
func TestRetireCarry_SurvivorRemovedByHandThenReenteredUndoNewestFirst(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "B", "c", "d", "S"}, []string{"a", "B", "c", "d"})
	c.carry("op1", "B", "S")
	c.handRemove("S")
	c.carry("op2", "d", "S")
	require.Equal(t, []string{"a", "c", "S"}, c.members())

	rr, err := audiobooks.NewRevertService(c.st).RevertOperation("op2")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.RestoredByIndex, "%+v", rr)
	require.Equal(t, []string{"a", "c", "d"}, c.members(), "S came back only for op2 and leaves with it")

	rr, err = audiobooks.NewRevertService(c.st).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.RestoredByIndex, "%+v", rr)
	require.Equal(t, []string{"a", "B", "c", "d"}, c.members())
}

// The owner hand-adds a survivor whose own retire still stands; undoing the
// retire that put it in B's slot must put B back beside its neighbours, not
// into the slot the owner gave S.
func TestRetireCarry_ARetiredSurvivorListedByHandDoesNotTakeTheBooksSlot(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "B", "c", "d", "S", "T"}, []string{"a", "B", "c", "d"})
	c.carry("op1", "B", "S") // S takes B's slot
	c.carry("op2", "S", "T") // T takes S's slot
	require.Equal(t, []string{"a", "T", "c", "d"}, c.members())
	c.handAppend("S") // the owner lists S at the end

	rr, err := audiobooks.NewRevertService(c.st).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, []string{"a", "B", "T", "c", "d", "S"}, c.members(), "B back beside a and c")
}

// The owner takes S out by hand while op1 stands, then op2 lists it again at
// d's slot. Undoing op2 alone takes S out again: S is listed only on op2's
// account, op1's took ended with the hand removal.
func TestRetireCarry_AReenteredSurvivorLeavesWithTheLaterRetire(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "B", "c", "d", "S"}, []string{"a", "B", "c", "d"})
	c.carry("op1", "B", "S")
	c.handRemove("S")
	c.carry("op2", "d", "S")
	require.Equal(t, []string{"a", "c", "S"}, c.members())

	rr, err := audiobooks.NewRevertService(c.st).RevertOperation("op2")
	require.NoError(t, err, "%+v", rr)
	require.Equal(t, []string{"a", "c", "d"}, c.members(), "S came back only for op2")
	rr, err = audiobooks.NewRevertService(c.st).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Equal(t, []string{"a", "B", "c", "d"}, c.members())
}

// The owner moves survivor S to the end of the list while a retire of B into S
// stands. The revert puts B back beside its neighbours, leaves S where the owner
// put it, and says so in Kept and ListsEditedByHand: it must not swap B into the
// slot the owner moved S out of, nor swap it into S's new place.
func TestRetireCarry_ASurvivorMovedByHandIsKeptAndReported(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "B", "c", "d", "S"}, []string{"a", "B", "c", "d"})
	c.carry("op1", "B", "S")
	require.Equal(t, []string{"a", "S", "c", "d"}, c.members())
	c.handRemove("S")
	c.handAppend("S")
	require.Equal(t, []string{"a", "c", "d", "S"}, c.members())

	rr, err := audiobooks.NewRevertService(c.st).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, []string{"a", "B", "c", "d", "S"}, c.members(), "B beside its neighbours, S where the owner put it")
	require.Equal(t, 1, rr.ListsEditedByHand, "%+v", rr)
	require.Len(t, rr.Kept, 1, "%+v", rr)
	require.Contains(t, rr.Kept[0].Text, "left where you placed it")
	require.Contains(t, rr.Summary(), "arranged by hand")
}

// stepClock is a wall clock that the test moves, backwards included.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (s *stepClock) now() time.Time { s.mu.Lock(); defer s.mu.Unlock(); return s.t }
func (s *stepClock) add(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.t = s.t.Add(d)
}

// The wall clock steps back a day between two retires into the same survivor
// (NTP, a VM resume). The journal clock keeps their rows in the order they were
// written, so the reverts decide exactly in either order. Stamped from the wall
// clock, the second retire's rows would predate the first's and the survivor
// would stay in, or leave, the list wrongly.
func TestRetireCarry_AWallClockStepBackBetweenRetiresDoesNotReorderTheJournal(t *testing.T) {
	for _, order := range [][]string{{"op1", "op2"}, {"op2", "op1"}} {
		t.Run(order[0]+" first", func(t *testing.T) {
			t.Parallel()
			c := newChainFixture(t, []string{"a", "B", "c", "D", "e", "S"}, []string{"a", "B", "c", "D", "e"})
			wall := &stepClock{t: time.Now()}
			c.f.s.SetJournalWallClock(wall.now)
			c.carry("op1", "B", "S") // S takes B's slot
			wall.add(-24 * time.Hour)
			c.carry("op2", "D", "S") // S is listed: D only leaves
			wall.add(-time.Hour)
			require.Equal(t, []string{"a", "S", "c", "e"}, c.members())

			for _, op := range order {
				rr, err := audiobooks.NewRevertService(c.st).RevertOperation(op)
				require.NoError(t, err, "%s %+v", op, rr)
				require.Zero(t, rr.Failed, "%s %+v", op, rr)
				require.Zero(t, rr.RestoredByIndex, "%s %+v", op, rr)
				require.Zero(t, rr.ListsEditedByHand, "%s %+v", op, rr)
			}
			require.Equal(t, []string{"a", "B", "c", "D", "e"}, c.members(), "exact after %v", order)
		})
	}
}
