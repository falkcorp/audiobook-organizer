// file: internal/plugins/acoustid/candidate_step_skipped_test.go
// version: 0.1.0
// guid: f156c537-4bc5-4eff-919b-d5902c6d619d
// last-edited: 2026-11-28

package acoustid

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// With no candidate store (embedding disabled, or the dedup candidate store the
// books module does not wire yet) the scan summary and the reset pass skip their
// candidate step as HEAD's did. They say so in the run's log: a count of zero
// pending candidates or zero dropped is otherwise indistinguishable from a
// library that had none.

func capturedLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestScanCore_SaysItSkippedTheCandidateCount(t *testing.T) {
	lg, buf := capturedLogger()
	env := scanEnv{kindEnv: headEnv(&coreReporter{}, true), Engine: newKit().eng}
	env.Logger = lg
	res, err := scanCore(context.Background(), env)
	require.NoError(t, err)
	requireResult(t, scanResult{}, res)
	require.Contains(t, buf.String(), "pending candidates not counted, no candidate store")

	lg, buf = capturedLogger()
	env.Logger = lg
	env.Candidates = fakeCandidates{pending: 2}
	_, err = scanCore(context.Background(), env)
	require.NoError(t, err)
	require.NotContains(t, buf.String(), "not counted", "a scan with a candidate store counts and says nothing of skipping")
}

func TestResetAllCore_SaysItSkippedTheCandidateDrop(t *testing.T) {
	var wl writeLog
	hs := resetFixture()
	recordWrites(hs, &wl)
	env := (&Plugin{store: hs}).headResetAllEnv(&coreReporter{})
	lg, buf := capturedLogger()
	env.Logger = lg
	res, err := resetAllCore(context.Background(), env)
	require.NoError(t, err)
	require.Zero(t, res.CandidatesDeleted)
	require.Contains(t, buf.String(), "dedup candidates not dropped, no candidate store")

	wl = writeLog{}
	env = (&Plugin{store: hs, resetCandidates: resetCands(&wl)}).headResetAllEnv(&coreReporter{})
	lg, buf = capturedLogger()
	env.Logger = lg
	_, err = resetAllCore(context.Background(), env)
	require.NoError(t, err)
	require.NotContains(t, buf.String(), "not dropped", "a reset with a candidate store drops and says nothing of skipping")
}
