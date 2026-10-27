// file: internal/server/maintenance_continue_in_place_test.go
// version: 1.1.0
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

// yieldProbeJob is a maintenance job whose Run returns whatever it is told to
// and whose policy is chosen by the test.
type yieldProbeJob struct {
	result error
	policy maintenance.ExecutionPolicy
}

func (j *yieldProbeJob) ID() string          { return "yield-probe-job" }
func (j *yieldProbeJob) Name() string        { return "Yield Probe Job" }
func (j *yieldProbeJob) Description() string { return "test double for the continue-in-place summary" }
func (j *yieldProbeJob) Category() string    { return "test" }
func (j *yieldProbeJob) DefaultParams() any  { return struct{}{} }
func (j *yieldProbeJob) CanResume() bool     { return true }
func (j *yieldProbeJob) Policy() maintenance.ExecutionPolicy {
	return j.policy
}
func (j *yieldProbeJob) Run(context.Context, maintenance.JobStore, maintenance.ProgressReporter, bool) error {
	return j.result
}

// yieldProbeReporter carries the op id the run's activity lines are keyed on and
// says whether the attempt made progress, as the registry's own reporter does.
type yieldProbeReporter struct {
	opsregistry.Reporter
	progressed bool
}

func (yieldProbeReporter) OpID() string                { return "op-yield-probe" }
func (r yieldProbeReporter) AttemptMadeProgress() bool { return r.progressed }

// yieldProbe is one scenario around the REAL registered closure.
type yieldProbe struct {
	result     error
	policy     maintenance.ExecutionPolicy
	progressed bool
	cancelled  bool // the run's context is already done when Run returns
}

// runYieldProbe runs the probe and returns the summary entries the feed received
// for the run, and the error the closure returned.
func runYieldProbe(t *testing.T, p yieldProbe) (summaries int, runErr error) {
	t.Helper()
	w := activity.NewWriter(nil, 16)
	s := &Server{store: &database.MockStore{}, activityWriter: w}
	reg := maintReg(t)
	require.NoError(t, s.registerMaintenanceJobOp(reg, &yieldProbeJob{result: p.result, policy: p.policy}))
	def, ok := reg.Def(maintenanceOpID("yield-probe-job"))
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if p.cancelled {
		cancel()
	}
	runErr = def.Run(ctx, json.RawMessage(`{"dry_run":true}`), yieldProbeReporter{progressed: p.progressed})

	for _, e := range drainActivity(w) {
		if e.OperationID == "op-yield-probe" && e.Summary == "Yield Probe Job" {
			summaries++
		}
	}
	return summaries, runErr
}

// A run the registry will restart in place is not finished, so it must not put a
// "done" summary in the activity feed. Every sentinel the registry would NOT
// honour ends the operation and still gets its summary. The control (a finished
// run) is what makes the zeros mean something.
func TestMaintenanceOp_YieldSummaryFollowsTheRegistrysDecision(t *testing.T) {
	yield := fmt.Errorf("probe: %w", maintenance.ErrContinueInPlace)
	restart := maintenance.RestartPolicy()
	drop := maintenance.DefaultPolicy() // ResumeDrop: the registry cannot restart it in place

	cases := []struct {
		name      string
		probe     yieldProbe
		wantN     int
		wantErrIs error
	}{
		{"finished run (control)", yieldProbe{policy: restart, progressed: true}, 1, nil},
		{"honoured yield", yieldProbe{result: yield, policy: restart, progressed: true}, 0, opsregistry.ErrContinueInPlace},
		{"yield without progress", yieldProbe{result: yield, policy: restart, progressed: false}, 1, opsregistry.ErrContinueInPlace},
		{"yield after the context ended", yieldProbe{result: yield, policy: restart, progressed: true, cancelled: true}, 1, opsregistry.ErrContinueInPlace},
		{"yield from a job that does not resume in place", yieldProbe{result: yield, policy: drop, progressed: true}, 1, opsregistry.ErrContinueInPlace},
		{"ordinary failure", yieldProbe{result: errors.New("probe: broke"), policy: restart, progressed: true}, 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := runYieldProbe(t, tc.probe)
			assert.Equal(t, tc.wantN, n, "summary entries")
			if tc.probe.result == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tc.wantErrIs != nil {
				assert.True(t, errors.Is(err, tc.wantErrIs), "the sentinel reaches the registry unchanged")
			}
		})
	}
}
