// file: internal/repairs/no_standdown_test.go
// version: 1.0.0
// guid: 8e1c4a7d-2f95-4b30-a6d8-5c9e0b3f7a12
// last-edited: 2026-10-05

package repairs

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// readOnlyFixer opts out of the scan stand-down and writes nothing: it counts
// its applies, as the lost-candidates refetch does its provider calls.
type readOnlyFixer struct {
	*trimFixer
	applied atomic.Int64
}

func (*readOnlyFixer) NoScanStandDown() bool { return true }

func (f *readOnlyFixer) Apply(context.Context, *Writer, Row) error {
	f.applied.Add(1)
	return nil
}

// A NoScanStandDown fixer's apply never acquires the stand-down: a library
// scan is not parked for the length of its run.
func TestRunApply_NoScanStandDownFixerTakesNoStandDown(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &readOnlyFixer{trimFixer: &trimFixer{s: s}}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, scanRunning: true}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, res.Applied, "%+v", res.Rows)
	require.EqualValues(t, 2, f.applied.Load())
	require.Zero(t, sd.acquires, "no stand-down for a fixer that writes no library row")
	require.False(t, res.StandDownHeld)
	require.True(t, sd.scanRunning, "the scan was never parked")
}

// The opt-out is enforced: a NoScanStandDown fixer that writes anyway is
// refused by the Writer, and the book is left as it was.
type writingNoStandDown struct{ *trimFixer }

func (writingNoStandDown) NoScanStandDown() bool { return true }

func TestRunApply_NoScanStandDownWriterRefusesWrites(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := writingNoStandDown{&trimFixer{s: s}}
	plan := planFor(t, s, f)
	before := s.title("b1")
	sd := &fakeStandDown{renewsLeft: -1}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	require.Equal(t, OutcomeFailed, res.Rows[0].Outcome, "%+v", res.Rows[0])
	require.Contains(t, res.Rows[0].Error, "without the scan stand-down")
	require.Equal(t, before, s.title("b1"))
	require.Zero(t, sd.acquires)

	w := NewWriter(s, s, "t", "bulk_update", "rp-")
	w.restrictToNothing()
	_, err = w.AddBookTag("b1", "franchise:x", "src")
	require.ErrorIs(t, err, ErrNoWritesWriter)
}

// Every other fixer keeps the stand-down (the default is unchanged).
func TestRunApply_DefaultFixerStillTakesTheStandDown(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, scanRunning: true}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, sd.acquires)
	require.True(t, res.StandDownHeld)
	require.False(t, SkipsScanStandDown(f))
}
