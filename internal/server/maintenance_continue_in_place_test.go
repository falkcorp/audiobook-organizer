// file: internal/server/maintenance_continue_in_place_test.go
// version: 1.0.0
// guid: 2c9e5b70-8d14-4a63-b1f7-3e6a0d9c4f58
// last-edited: 2026-10-26

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// yieldProbeJob is a maintenance job whose Run returns whatever it is told to.
type yieldProbeJob struct{ result error }

func (j *yieldProbeJob) ID() string          { return "yield-probe-job" }
func (j *yieldProbeJob) Name() string        { return "Yield Probe Job" }
func (j *yieldProbeJob) Description() string { return "test double for the continue-in-place summary" }
func (j *yieldProbeJob) Category() string    { return "test" }
func (j *yieldProbeJob) DefaultParams() any  { return struct{}{} }
func (j *yieldProbeJob) CanResume() bool     { return true }
func (j *yieldProbeJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
func (j *yieldProbeJob) Run(context.Context, maintenance.JobStore, maintenance.ProgressReporter, bool) error {
	return j.result
}

// yieldProbeReporter carries the op id the run's activity lines are keyed on.
type yieldProbeReporter struct{ opsregistry.Reporter }

func (yieldProbeReporter) OpID() string { return "op-yield-probe" }

// runYieldProbe runs the REAL registered closure around a job returning result
// and returns the summary entries the feed received for the run.
func runYieldProbe(t *testing.T, result error) (runErr error, summaries int) {
	t.Helper()
	w := activity.NewWriter(nil, 16)
	s := &Server{store: &database.MockStore{}, activityWriter: w}
	reg := maintReg(t)
	require.NoError(t, s.registerMaintenanceJobOp(reg, &yieldProbeJob{result: result}))
	def, ok := reg.Def(maintenanceOpID("yield-probe-job"))
	require.True(t, ok)

	runErr = def.Run(context.Background(), json.RawMessage(`{"dry_run":true}`), yieldProbeReporter{})

	for _, e := range drainActivity(w) {
		if e.OperationID == "op-yield-probe" && e.Summary == "Yield Probe Job" {
			summaries++
		}
	}
	return runErr, summaries
}

// A run that yields is not finished, so it must not put a "done" summary in the
// activity feed; the same job returning nil does (the positive control that makes
// the zero below mean something).
func TestMaintenanceOp_YieldedRunEmitsNoSummary(t *testing.T) {
	err, n := runYieldProbe(t, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n, "control: a finished run emits its summary entry")

	yield := fmt.Errorf("probe: %w", maintenance.ErrContinueInPlace)
	err, n = runYieldProbe(t, yield)
	require.Error(t, err)
	assert.True(t, errors.Is(err, opsregistry.ErrContinueInPlace), "the sentinel reaches the registry unchanged")
	assert.Zero(t, n, "a yielded run is mid-flight: no summary entry")

	err, n = runYieldProbe(t, errors.New("probe: broke"))
	require.Error(t, err)
	assert.Equal(t, 1, n, "an ordinary failure still emits its summary")
}
