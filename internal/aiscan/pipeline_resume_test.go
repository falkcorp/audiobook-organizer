// file: internal/aiscan/pipeline_resume_test.go
// version: 1.0.0
// guid: 2d81b4c7-95fe-4a30-8b16-7c0e4f9a2531
// last-edited: 2026-08-22

package aiscan

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// --- decideResume -----------------------------------------------------------

func phases(statuses ...string) []database.ScanPhase {
	out := make([]database.ScanPhase, 0, len(statuses))
	for i, s := range statuses {
		out = append(out, database.ScanPhase{PhaseType: "p" + string(rune('0'+i)), Status: s})
	}
	return out
}

// TestDecideResume covers the decision that a crash-restart depends on. Getting
// resumeAttach wrong hangs the op until its 24h timeout; getting resumeLaunch
// wrong re-runs a paid whole-library LLM pass against OpenAI.
func TestDecideResume(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		phases []database.ScanPhase
		want   resumeAction
	}{
		{"no phases at all launches", "realtime", nil, resumeLaunch},
		{"all pending launches (realtime)", "realtime", phases("pending", "pending"), resumeLaunch},
		{"all pending launches (batch)", "batch", phases("pending", "pending"), resumeLaunch},

		// Batch: OpenAI still holds the job and PollBatchPhases will collect it.
		{"batch submitted attaches", "batch", phases("submitted", "pending"), resumeAttach},
		{"batch processing attaches", "batch", phases("processing", "pending"), resumeAttach},
		{"batch complete attaches", "batch", phases("complete", "submitted"), resumeAttach},

		// Realtime: the in-flight HTTP requests died with the process.
		{"realtime processing is impossible", "realtime", phases("processing", "pending"), resumeImpossible},
		{"realtime complete is impossible", "realtime", phases("complete", "pending"), resumeImpossible},

		// An unset mode is not "batch" and must take the safe branch rather than
		// waiting forever on a driver that does not exist.
		{"empty mode is not batch", "", phases("processing"), resumeImpossible},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, decideResume(tc.mode, tc.phases))
		})
	}
}

// TestDecideResumeOnlyPendingCountsAsUnstarted is the mutation guard for the
// status comparison: flipping `!= "pending"` to `== "complete"` (or similar)
// would let a half-run scan be re-launched from scratch.
func TestDecideResumeOnlyPendingCountsAsUnstarted(t *testing.T) {
	for _, s := range []string{"processing", "submitted", "complete", "failed", "canceled"} {
		require.NotEqual(t, resumeLaunch, decideResume("batch", phases(s)),
			"status %q means work began; re-launching would repeat it", s)
	}
	require.Equal(t, resumeLaunch, decideResume("batch", phases("pending")))
}

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
