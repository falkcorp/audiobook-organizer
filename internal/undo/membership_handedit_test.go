// file: internal/undo/membership_handedit_test.go
// version: 1.0.0
// guid: 2f8b6d41-93ae-4c07-a5d2-6e10b7c3f958
// last-edited: 2026-10-22

package undo

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newHandSim(list ...string) *advSim {
	var st advStats
	var log []string
	return &advSim{list: slices.Clone(list), clock: time.Unix(1_700_000_000, 0), rng: rand.New(rand.NewPCG(1, 2)),
		rows: map[string]*database.OperationChange{}, markers: map[string]*database.OperationChange{}, touched: map[string]bool{}, st: &st, log: &log}
}

// The owner moved survivor S to the end of the list while a retire of B into S
// (S took B's slot) stands. Reverting must not swap B into the slot where the
// owner put S: B goes back beside its neighbours, S stays where the owner put
// it, and the revert says so (MemberResult.Kept), never silently.
func TestHandEdit_SurvivorMovedByHandIsKeptAndReported(t *testing.T) {
	s := newHandSim("a", "B", "c", "d")
	id := s.retire("op1", "B", "S")
	require.Equal(t, []string{"a", "S", "c", "d"}, s.list)
	s.list = []string{"a", "c", "d", "S"} // moved by hand

	got, err := s.revertRow(id)
	require.NoError(t, err)
	require.Equal(t, outWrote, got)
	require.Equal(t, []string{"a", "B", "c", "d", "S"}, s.list, "B beside its neighbours, S where the owner put it")
	require.Len(t, s.notes, 1)
	require.Contains(t, s.notes[0], "left where you placed it")
}

// A book nothing orders against B was put right beside S, so the slot cannot be
// confirmed from the records: B takes the survivor's slot as recorded and the
// revert says the slot was not confirmed.
func TestHandEdit_ABookBesideTheSurvivorMakesTheSlotUnconfirmedAndReported(t *testing.T) {
	s := newHandSim("a", "B", "c", "d")
	id := s.retire("op1", "B", "S")
	s.list = []string{"a", "X", "S", "c", "d"} // X added by hand

	got, err := s.revertRow(id)
	require.NoError(t, err)
	require.Equal(t, outWrote, got)
	require.Equal(t, []string{"a", "X", "B", "c", "d"}, s.list)
	require.Len(t, s.notes, 1)
	require.Contains(t, s.notes[0], "could not confirm")
}

// The owner re-ordered two books the retire's records order (a before c): the
// records no longer describe the list, so nothing found from them is certain.
func TestHandEdit_ReorderedNeighboursAreReportedNotTrusted(t *testing.T) {
	s := newHandSim("a", "B", "c", "d")
	id := s.retire("op1", "B", "S")
	s.list = []string{"c", "S", "a", "d"} // a and c swapped by hand

	got, err := s.revertRow(id)
	require.NoError(t, err)
	require.Equal(t, outWrote, got)
	require.Len(t, s.notes, 1, "a list the records contradict is reported")
}

// No hand edit: nothing is reported (the in-slot swap is confirmed by the
// records, including with books of other retires around it).
func TestHandEdit_ACleanListIsNeverReported(t *testing.T) {
	s := newHandSim("a", "B", "c", "D", "e")
	id1 := s.retire("op1", "B", "S")
	id2 := s.retire("op2", "D", "T")
	for _, id := range []string{id1, id2} {
		got, err := s.revertRow(id)
		require.NoError(t, err)
		require.Equal(t, outWrote, got)
	}
	require.Equal(t, []string{"a", "B", "c", "D", "e"}, s.list)
	require.Empty(t, s.notes)
}
