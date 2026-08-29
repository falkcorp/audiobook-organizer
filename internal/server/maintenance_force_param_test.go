// file: internal/server/maintenance_force_param_test.go
// version: 1.0.0
// guid: 3d9e51c7-8a24-4f0b-b6d3-1c47ae90b25f
// last-edited: 2026-08-29

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// These tests cover the PLUMBING, not any one job's use of it.
//
// MaintenanceJob.Run takes exactly one parameter — dryRun — so a job with any
// other custom parameter has no argument to receive it on. The documented route
// was store.GetOperationParams(opID), which reads opstate:<opID>:params; nothing
// on the maintenance path writes that key any more (its writer went with the v1
// op minter in #2784), so a job reading it silently gets nothing.
//
// That is how recompute-book-aggregates' Force flag came to be declared in
// DefaultParams, advertised in two operator-facing strings, and read by nothing:
// it was dropped at the dispatcher's request binding, absent from
// maintenanceJobOpParams, and had no channel into Run even if it had survived
// both. The fix threads the v2 row's own params through the context, next to the
// operation id that already travels that way.
//
// The assertion below is on what reaches Run, deliberately — the same choice
// dryRunProbeJob makes for dry_run. A test asserting on the persisted params row
// would still pass if the ctx line were deleted.

// paramsProbeJob records the raw params blob its Run received on the context.
type paramsProbeJob struct {
	id string

	mu   sync.Mutex
	runs []json.RawMessage
}

func (j *paramsProbeJob) ID() string          { return j.id }
func (j *paramsProbeJob) Name() string        { return "Params Probe " + j.id }
func (j *paramsProbeJob) Description() string { return "Test probe; records the raw params it receives." }
func (j *paramsProbeJob) Category() string    { return "test" }
func (j *paramsProbeJob) CanResume() bool     { return false }

func (j *paramsProbeJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
		Force  bool `json:"force"`
	}{DryRun: false, Force: false}
}

func (j *paramsProbeJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}

func (j *paramsProbeJob) Run(ctx context.Context, _ maintenance.JobStore, _ maintenance.ProgressReporter, _ bool) error {
	j.mu.Lock()
	j.runs = append(j.runs, maintenance.RawParamsFromCtx(ctx))
	j.mu.Unlock()
	return nil
}

// awaitParams blocks until the job has run once and returns the params it saw.
// A timeout here means the request never reached Run at all, which is a
// different defect from receiving the wrong params, so it is reported apart from
// the value assertion.
func (j *paramsProbeJob) awaitParams(t *testing.T) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		var first json.RawMessage
		n := len(j.runs)
		if n > 0 {
			first = j.runs[0]
		}
		j.mu.Unlock()
		if n > 0 {
			return first
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %q never ran within 20s", j.id)
	return nil
}

var forceProbe = &paramsProbeJob{id: "test-probe-force-params"}

func init() {
	// maintenance.Register panics on a duplicate ID and has no Unregister, so
	// this registers exactly once for the package's whole test run.
	maintenance.Register(forceProbe)
}

// TestRunMaintenanceJob_ForceReachesTheJob is the end-to-end plumbing assertion:
// an operator's {"force": true} must survive the dispatcher's request binding,
// maintenanceJobOpParams, EnqueueOp's marshal, and the op Run closure, and be
// readable inside the job.
//
// Every one of those four layers dropped it before this change. Deleting any one
// of them again fails this test.
func TestRunMaintenanceJob_ForceReachesTheJob(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	forceProbe.mu.Lock()
	forceProbe.runs = nil
	forceProbe.mu.Unlock()

	w := postMaintenanceJob(t, server, forceProbe.ID(), `{"dry_run": false, "force": true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	raw := forceProbe.awaitParams(t)
	if len(raw) == 0 {
		t.Fatal("the job received NO params on its context; a custom parameter has no route into Run")
	}

	var got struct {
		JobID  string `json:"job_id"`
		DryRun bool   `json:"dry_run"`
		Force  bool   `json:"force"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode params %s: %v", raw, err)
	}
	if !got.Force {
		t.Errorf("force reached the job as false; params were %s — the flag is inert", raw)
	}
	if got.JobID != forceProbe.ID() {
		t.Errorf("job_id = %q, want %q; the job is reading someone else's params", got.JobID, forceProbe.ID())
	}
}

// TestRunMaintenanceJob_ForceDefaultsFalse is the discriminating negative. A
// dispatcher that hardcoded Force: true, or a job-side reader that returned true
// for any params blob, would pass the test above.
func TestRunMaintenanceJob_ForceDefaultsFalse(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	forceProbe.mu.Lock()
	forceProbe.runs = nil
	forceProbe.mu.Unlock()

	// No force key at all — the shape every existing client sends.
	w := postMaintenanceJob(t, server, forceProbe.ID(), `{"dry_run": false}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	raw := forceProbe.awaitParams(t)
	var got struct {
		Force bool `json:"force"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode params %s: %v", raw, err)
	}
	if got.Force {
		t.Errorf("force reached the job as TRUE with no force key in the request; params were %s", raw)
	}
}
