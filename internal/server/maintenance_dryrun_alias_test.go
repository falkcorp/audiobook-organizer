// file: internal/server/maintenance_dryrun_alias_test.go
// version: 1.0.0
// guid: 3e8b1f6c-9a24-4d7e-b05f-2c6a9d1e4b83
// last-edited: 2026-09-25

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The maintenance-job family must honor both spellings of the mode flag, as
// the opmode contract promises every op. Before this was routed through
// opmode.ResolveDryRunDefault, both the HTTP dispatcher and the v2 Run closure
// read only dry_run, so {"dryRun":false} fell to the advertised default. For
// the 30 jobs that now advertise dry_run:true, that turned an explicit live
// request into a silent preview behind a 202. A request that sent both
// spellings with different values was accepted and one of them was ignored.

// TestRunMaintenanceJob_CamelDryRunIsHonored: the HTTP route.
func TestRunMaintenanceJob_CamelDryRunIsHonored(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}
	probeAdvertisesTrue.mu.Lock()
	probeAdvertisesTrue.runs = nil
	probeAdvertisesTrue.mu.Unlock()

	w := postMaintenanceJob(t, server, probeAdvertisesTrue.ID(), `{"dryRun": false}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	if got := probeAdvertisesTrue.awaitRun(t); got {
		t.Fatal(`{"dryRun":false} ran as a preview; the camelCase live flag was ignored`)
	}
}

// TestRunMaintenanceJob_ConflictingDryRunSpellingsAreRefused: the HTTP route
// answers 400 and enqueues nothing.
func TestRunMaintenanceJob_ConflictingDryRunSpellingsAreRefused(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}
	probeAdvertisesTrue.mu.Lock()
	probeAdvertisesTrue.runs = nil
	probeAdvertisesTrue.mu.Unlock()

	w := postMaintenanceJob(t, server, probeAdvertisesTrue.ID(), `{"dry_run": true, "dryRun": false}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for disagreeing dry_run/dryRun; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "disagree") {
		t.Errorf("400 body should name the disagreement; got %s", w.Body.String())
	}
	time.Sleep(500 * time.Millisecond)
	probeAdvertisesTrue.mu.Lock()
	n := len(probeAdvertisesTrue.runs)
	probeAdvertisesTrue.mu.Unlock()
	if n != 0 {
		t.Fatalf("job ran %d time(s) despite a 400 response", n)
	}
}

// TestMaintenanceJobOp_CamelDryRunIsHonored: the v2 Run closure, reached by a
// direct enqueue (/operations/trigger, a requeue, a resume) that never passes
// through the dispatcher.
func TestMaintenanceJobOp_CamelDryRunIsHonored(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}
	probeAdvertisesTrue.mu.Lock()
	probeAdvertisesTrue.runs = nil
	probeAdvertisesTrue.mu.Unlock()

	raw := json.RawMessage(`{"job_id":"` + probeAdvertisesTrue.ID() + `","dryRun":false}`)
	if _, err := server.opRegistry.EnqueueOp(context.Background(), maintenanceOpID(probeAdvertisesTrue.ID()), raw); err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if got := probeAdvertisesTrue.awaitRun(t); got {
		t.Fatal(`direct enqueue with {"dryRun":false} ran as a preview; the camelCase live flag was ignored`)
	}
}

// TestMaintenanceJobOp_ConflictingDryRunSpellingsFail: the v2 Run closure
// fails the op and never calls the job.
func TestMaintenanceJobOp_ConflictingDryRunSpellingsFail(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}
	probeAdvertisesTrue.mu.Lock()
	probeAdvertisesTrue.runs = nil
	probeAdvertisesTrue.mu.Unlock()

	raw := json.RawMessage(`{"job_id":"` + probeAdvertisesTrue.ID() + `","dry_run":true,"dryRun":false}`)
	opID, err := server.opRegistry.EnqueueOp(context.Background(), maintenanceOpID(probeAdvertisesTrue.ID()), raw)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	var status, msg string
	for time.Now().Before(deadline) {
		row, gerr := server.Ops().GetOperationV2(opID)
		if gerr == nil && row != nil && row.Status != "queued" && row.Status != "running" {
			status = row.Status
			if row.ErrorMessage != nil {
				msg = *row.ErrorMessage
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("op status = %q, want failed for disagreeing dry_run/dryRun (error %q)", status, msg)
	}
	if !strings.Contains(msg, "disagree") {
		t.Errorf("failure should name the disagreement; got %q", msg)
	}
	probeAdvertisesTrue.mu.Lock()
	n := len(probeAdvertisesTrue.runs)
	probeAdvertisesTrue.mu.Unlock()
	if n != 0 {
		t.Fatalf("job ran %d time(s) although its params disagree on the mode", n)
	}
}
