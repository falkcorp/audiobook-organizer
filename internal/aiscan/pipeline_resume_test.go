// file: internal/aiscan/pipeline_resume_test.go
// version: 1.1.0
// guid: 2d81b4c7-95fe-4a30-8b16-7c0e4f9a2531
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The launch-vs-attach table that lived here tested decideResume, which
// decided per SCAN. It was replaced by drive's per-PHASE decision, exercised
// end to end across simulated restarts in pipeline_durable_test.go.

// --- finishScan -------------------------------------------------------------

func newAttachedPM(scanID int) (*PipelineManager, chan error) {
	done := make(chan error, 1)
	pm := &PipelineManager{
		cancels: map[int]context.CancelFunc{},
		sinks:   map[int]ProgressSink{},
		dones:   map[int]chan error{scanID: done},
	}
	return pm, done
}

func TestFinishScanDeliversOutcome(t *testing.T) {
	pm, done := newAttachedPM(1)
	want := errors.New("phase blew up")

	pm.finishScan(1, want)

	require.ErrorIs(t, <-done, want)
	require.Empty(t, pm.dones, "per-scan state must be dropped")
	require.Empty(t, pm.sinks)
	require.Empty(t, pm.cancels)
}

// TestFinishScanIsIdempotent is the load-bearing one. groups_scan and full_scan
// run concurrently and failPhase has 18 call sites, so two finishes racing is
// the normal case. Without the delete-under-mutex guard the second close()
// panics and takes the process with it.
func TestFinishScanIsIdempotent(t *testing.T) {
	pm, done := newAttachedPM(1)

	const racers = 8
	var wg sync.WaitGroup
	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			pm.finishScan(1, errors.New("failure"))
		}()
	}
	wg.Wait() // must not panic

	require.Error(t, <-done)
	_, stillOpen := <-done
	require.False(t, stillOpen, "channel should be closed exactly once")
}

func TestFinishScanWithNoWaiterIsNoop(t *testing.T) {
	pm := &PipelineManager{
		cancels: map[int]context.CancelFunc{},
		sinks:   map[int]ProgressSink{},
		dones:   map[int]chan error{},
	}
	// A scan advanced by PollBatchPhases after a restart, before its op resumes,
	// has no waiter. This must not panic or block.
	require.NotPanics(t, func() { pm.finishScan(42, nil) })
}

// --- report -----------------------------------------------------------------

type recordingSink struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (s *recordingSink) UpdateProgress(current, total int, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, message)
	return s.err
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func TestReportGoesToTheAttachedSink(t *testing.T) {
	sink := &recordingSink{}
	pm := &PipelineManager{sinks: map[int]ProgressSink{5: sink}}

	pm.report(5, 40, 100, "Phase groups_scan complete")

	require.Equal(t, 1, sink.count())
	require.Equal(t, "Phase groups_scan complete", sink.calls[0])
}

func TestReportWithNoSinkIsNoop(t *testing.T) {
	pm := &PipelineManager{sinks: map[int]ProgressSink{}}
	require.NotPanics(t, func() { pm.report(5, 40, 100, "no one is listening") })
}

// A sink error must not fail the scan: progress is advisory, and the scan's real
// state lives in the scan store.
func TestReportSwallowsSinkError(t *testing.T) {
	sink := &recordingSink{err: errors.New("reporter closed")}
	pm := &PipelineManager{sinks: map[int]ProgressSink{5: sink}}
	require.NotPanics(t, func() { pm.report(5, 1, 2, "x") })
	require.Equal(t, 1, sink.count())
}

// --- phaseProgressPct -------------------------------------------------------

func TestPhaseProgressPctCapsBelowComplete(t *testing.T) {
	require.Equal(t, 0, phaseProgressPct(0))
	require.Equal(t, 20, phaseProgressPct(1))
	require.Equal(t, 80, phaseProgressPct(4))
	// Never 100 from phase counting — only the success path reports 100, so a
	// stalled scan cannot sit at "complete" in the UI.
	require.Equal(t, 90, phaseProgressPct(5))
	require.Equal(t, 90, phaseProgressPct(99))
}
