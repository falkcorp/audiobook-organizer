// file: internal/server/maintenance_orphan_row_test.go
// version: 3.0.0
// guid: 2d6a14f8-9c05-4e73-b8a1-3f70e2d54c69
// last-edited: 2026-08-23

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// blockingProbeJob is a maintenance job whose Run parks until the test releases
// it, so an op built from it is guaranteed to still be ACTIVE when the next
// request arrives.
//
// v1 of this test used the fast-returning dryRunProbeJob and simply hoped the
// first op had not finished yet. It passed locally and failed in CI, where the
// first run completed between the two POSTs: with nothing active to merge into,
// the second request legitimately started its own run and there was no orphan to
// clean up. The precondition caught it rather than letting the test pass
// vacuously, but "caught by a guard" is not the same as deterministic — hence
// this job.
type blockingProbeJob struct {
	id      string
	release chan struct{}
}

func (j *blockingProbeJob) ID() string          { return j.id }
func (j *blockingProbeJob) Name() string        { return "Blocking Probe " + j.id }
func (j *blockingProbeJob) Description() string { return "Test probe; parks until released." }
func (j *blockingProbeJob) Category() string    { return "test" }
func (j *blockingProbeJob) DefaultParams() any  { return struct{}{} }
func (j *blockingProbeJob) CanResume() bool     { return false }

func (j *blockingProbeJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}

// Run parks until released or the run context is cancelled. The ctx case is what
// keeps a blocked run from outliving the test's store: cleanup shuts the registry
// down, which cancels the context.
func (j *blockingProbeJob) Run(ctx context.Context, _ maintenance.JobStore, _ maintenance.ProgressReporter, _ bool) error {
	select {
	case <-j.release:
	case <-ctx.Done():
	}
	return nil
}

var probeBlocking = &blockingProbeJob{
	id:      "orphan-row-blocking-probe",
	release: make(chan struct{}),
}

func init() {
	// maintenance.Register panics on a duplicate ID and has no Unregister, so
	// this is registered exactly once for the package's whole test run — the same
	// arrangement the dry-run probes use.
	maintenance.Register(probeBlocking)
}

// TestRunMaintenanceJob_MintsV2RowOnly pins what replaced the orphan-row
// pathology, rather than the cleanup that used to paper over it.
//
// runMaintenanceJob used to create a v1 operations row BEFORE enqueueing, so a
// request that merged into an already-active run ended up with a row twinned to
// nothing. propagateLegacyOpStatus only ever mirrors onto the WINNER's legacy
// id, so that row never left "pending" — and resumeInterruptedOperations swept
// exactly those rows on every restart and re-resumed them, forever. That was not
// hypothetical: it is the pathology legacy_op_status.go was written to end after
// every maintenance-job row of 2026-08-14 sat at "pending" while the jobs had in
// fact completed.
//
// Retiring the v1 minter removes the orphan by removing the row. The cleanup
// this test used to assert on (DeleteOperationWithLogs on the merged request's
// row) is gone with it, so the assertion moves to the invariant that makes the
// cleanup unnecessary: a maintenance run writes NO v1 row at all, and the id
// handed back to the caller resolves as a v2 operation.
//
// The merge assertion is worth keeping for a second reason. The params are now
// identical across the two requests — the per-request legacy_op_id stamp is
// gone — so this is the first time the dedupe fires on plain byte-equality
// rather than on sameParamsIgnoringLegacyID's key-wise path.
func TestRunMaintenanceJob_MintsV2RowOnly(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	// Deferred AFTER cleanup so it runs BEFORE it (LIFO): a parked run must be
	// let go before the store beneath it closes.
	defer close(probeBlocking.release)

	jobID := probeBlocking.ID()

	before := countOperationRows(t, server)

	first := postMaintenanceJobExpectingAccepted(t, server, jobID)
	second := postMaintenanceJobExpectingAccepted(t, server, jobID)

	// Precondition. The first op is either still queued (registry not started) or
	// parked inside Run (registry started); both count as ACTIVE, so the second
	// request must merge. If this ever fires, the dedupe regressed.
	if first != second {
		t.Fatalf("the second request did not merge into the first (%q vs %q); "+
			"with legacy_op_id gone the two param blobs are byte-identical, so "+
			"the dedupe should fire on the exact comparison", first, second)
	}

	if got := countOperationRows(t, server) - before; got != 0 {
		t.Fatalf("running a maintenance job left %d new v1 operation rows, want 0; "+
			"the v1 minter is supposed to be retired, and any row it writes sits at "+
			"\"pending\" and is re-resumed on every restart", got)
	}

	// The returned id must resolve as a v2 row for the job that was asked for.
	// Without this the count above would be satisfied by a handler that returned
	// a dangling id, leaving the caller nothing to poll.
	row, err := server.Ops().GetOperationV2(first)
	if err != nil {
		t.Fatalf("GetOperationV2(%q): %v", first, err)
	}
	if row == nil {
		t.Fatalf("the id returned to the caller (%q) resolves to no v2 operation row", first)
	}
	if want := maintenanceOpID(jobID); row.DefID != want {
		t.Fatalf("returned op has def_id %q, want %q", row.DefID, want)
	}

	// And it must NOT be a v1 id. A handler that still minted a v1 row and
	// returned it would satisfy every assertion above except this one.
	if legacy, lerr := server.Ops().GetOperationByID(first); lerr == nil && legacy != nil {
		t.Fatalf("the returned id %q also names a v1 operations row (type %q); "+
			"the v1 minter was not actually retired", first, legacy.Type)
	}
}

// postMaintenanceJobExpectingAccepted POSTs the job with no body and returns the
// operation_id, failing the test on any non-202.
func postMaintenanceJobExpectingAccepted(t *testing.T, s *Server, jobID string) string {
	t.Helper()
	w := postMaintenanceJob(t, s, jobID, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			OperationID string `json:"operation_id"`
		} `json:"data"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %s: %v", w.Body.String(), err)
	}
	opID := resp.Data.OperationID
	if opID == "" {
		opID = resp.OperationID
	}
	if opID == "" {
		t.Fatalf("no operation_id in response %s", w.Body.String())
	}
	return opID
}

func countOperationRows(t *testing.T, s *Server) int {
	t.Helper()
	_, total, err := s.Ops().ListOperations(1, 0)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	return total
}
