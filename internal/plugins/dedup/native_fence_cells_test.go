// file: internal/plugins/dedup/native_fence_cells_test.go
// version: 0.1.0
// guid: 8c1e6f3a-47d2-4b95-a0e8-3d5b9f1c7a24
// last-edited: 2026-11-28

package dedup

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/parity"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
)

// The native window-index-build kind, run by core's real executor (the parity
// kit boots it, over a real store), and stopped while a file's write is next.
// Core never puts a HEAD fence on the run's context, so the cells also pin that
// precondition: opswriter.FenceFromContext of the context a native run gives its
// work answers nil ("always writable"). What stops the write is core's fence
// (nativeFence): the writer here is the spy itself, NOT core's gated
// tasks.Writer, because the owner's writer builder (IndexKindDeps.NewWriter) is
// not this package's to vouch for; the next write must still be refused.
//
// The lease-loss cell does not exist for this kind, on purpose: it takes no
// scan stand-down hold (no rc.StandDown), so no lease can lapse under it. What
// ends a run it cannot finish is a cancel, its own timeout, a shutdown, or the
// watchdog's abandon, and the first two are the cells below.

// windowSpy parks every pending file's member read until the test lets it on,
// so the stop lands after the read and before the write.
type windowSpy struct {
	*indexSpy
	parked  chan string
	proceed chan struct{}
	puts    atomic.Int64
	flags   atomic.Int64
	once    sync.Once
}

func (s *windowSpy) WindowIndexMembers(id string) ([]fingerprint.WindowBand, bool, error) {
	if s.hasWindow[id] {
		return nil, true, nil
	}
	select {
	case s.parked <- id:
	default:
	}
	<-s.proceed
	return nil, false, nil
}

func (s *windowSpy) PutWindowIndex(id string) (int, error) {
	s.puts.Add(1)
	return s.indexSpy.PutWindowIndex(id)
}

func (s *windowSpy) SetWindowIndexBuilt() error {
	s.flags.Add(1)
	return s.indexSpy.SetWindowIndexBuilt()
}

func (s *windowSpy) release() { s.once.Do(func() { close(s.proceed) }) }

func newWindowSpy(t *testing.T) *windowSpy {
	t.Helper()
	base, _ := newIndexSpy()
	s := &windowSpy{indexSpy: base, parked: make(chan string, 8), proceed: make(chan struct{})}
	t.Cleanup(s.release)
	return s
}

// startParked boots the native kind over spy with attempt timeout, starts a
// Live run and waits for a file's member read to park.
func startParked(t *testing.T, spy *windowSpy, timeout time.Duration) (*parity.Kit, tasks.RunID) {
	t.Helper()
	deps := IndexKindDeps{Store: spy, NewWriter: func(tasks.Writer) indexKindWriter { return spy }}
	k := parity.NewKit(t)
	k.Boot(parity.Defs((&Plugin{}).windowIndexBuildNativeTimeout(deps, nil, timeout)))
	id := k.Start(windowIndexBuildKind, nil)
	select {
	case <-spy.parked:
	case <-time.After(30 * time.Second):
		t.Fatalf("the run never read a file's member list (state %s)", k.Get(id).State)
	}
	return k, id
}

func requireNothingWritten(t *testing.T, spy *windowSpy) {
	t.Helper()
	require.EqualValues(t, 0, spy.puts.Load(), "no file's index rows are written after the stop")
	require.EqualValues(t, 0, spy.flags.Load(), "the index is not marked built after the stop")
	require.NotContains(t, spy.recorded(), "SetWindowIndexBuilt")
}

func TestNativeFence_WindowIndexBuild_CancelRefusesTheNextWrite(t *testing.T) {
	// The precondition: nothing of HEAD's rides a run core starts.
	require.NoError(t, opswriter.FenceFromContext(context.Background()).Check(), "a context with no HEAD fence is always writable: the defect's precondition")

	spy := newWindowSpy(t)
	k, id := startParked(t, spy, time.Hour)
	require.NoError(t, k.X.CancelAs(context.Background(), id, tasks.Actor{UserID: "alfred"}))
	k.WaitState(id, tasks.StateStopping, tasks.StateCanceled)
	time.Sleep(150 * time.Millisecond) // the context ends right after the stop commits
	spy.release()
	r := k.WaitState(id, tasks.StateCanceled, tasks.StateFailed)
	requireNothingWritten(t, spy)
	require.NotEqual(t, tasks.StateSucceeded, r.State)
}

func TestNativeFence_WindowIndexBuild_TimeoutRefusesTheNextWrite(t *testing.T) {
	const timeout = 400 * time.Millisecond
	spy := newWindowSpy(t)
	k, id := startParked(t, spy, timeout)
	time.Sleep(timeout + 400*time.Millisecond) // the attempt's deadline has passed: its context is done
	spy.release()
	r := k.WaitState(id, tasks.StateTimedOut, tasks.StateFailed, tasks.StateCanceled)
	requireNothingWritten(t, spy)
	require.NotEqual(t, tasks.StateSucceeded, r.State)
}

// A run nothing stops writes: the same harness, never stopped, indexes both
// pending files and marks the index built, so the cells above fail for the stop
// and not because the harness cannot write.
func TestNativeFence_WindowIndexBuild_UnstoppedRunWrites(t *testing.T) {
	spy := newWindowSpy(t)
	k, id := startParked(t, spy, time.Hour)
	spy.release()
	r := k.WaitState(id, tasks.StateSucceeded, tasks.StateFailed)
	require.Equal(t, tasks.StateSucceeded, r.State, "run error %q", r.Error)
	require.EqualValues(t, 2, spy.puts.Load())
	require.EqualValues(t, 1, spy.flags.Load())
}
