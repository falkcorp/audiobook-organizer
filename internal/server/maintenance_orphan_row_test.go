// file: internal/server/maintenance_orphan_row_test.go
// version: 2.0.0
// guid: 2d6a14f8-9c05-4e73-b8a1-3f70e2d54c69
// last-edited: 2026-08-22

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

// TestRunMaintenanceJob_MergedRequestLeavesNoOrphanRow covers the row that
// enqueue-dedupe would otherwise strand.
//
// runMaintenanceJob creates a v1 operations row BEFORE enqueueing, so a request
// that merges into an already-active run ends up with a row twinned to nothing.
// propagateLegacyOpStatus only ever mirrors onto the WINNER's legacy id, so that
// row would never leave "pending" — and resumeInterruptedOperations sweeps
// exactly those rows on every restart and re-resumes them, forever.
//
// That is not hypothetical: it is the pathology legacy_op_status.go was written
// to end after every maintenance-job row of 2026-08-14 sat at "pending" while
// the jobs had in fact completed. Enabling dedupe without handling this would
// have reintroduced the same stuck row through a different door.
//
// The assertion is on the ROW COUNT, not on the handler's return value. A test
// that only checked both responses carry the same operation_id would pass with
// the orphan still sitting in the table.
func TestRunMaintenanceJob_MergedRequestLeavesNoOrphanRow(t *testing.T) {
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
	// request must merge. If this ever fires, the test is not exercising the
	// orphan path and the count assertion below would be checking nothing.
	if first != second {
		t.Fatalf("the second request did not merge into the first (%q vs %q); "+
			"without a merge there is no orphan row to clean up and this test "+
			"is not exercising what it claims", first, second)
	}

	after := countOperationRows(t, server)

	if got := after - before; got != 1 {
		t.Fatalf("two merged requests left %d new v1 operation rows, want 1; "+
			"the merged request's row was not cleaned up and will sit at "+
			"\"pending\" and be re-resumed on every restart", got)
	}

	// Positive control: the one surviving row must be the WINNER's. Without this,
	// a handler that deleted BOTH rows and returned a dangling id would satisfy
	// the count above while leaving the run with no v1 twin at all.
	row, err := server.Ops().GetOperationByID(first)
	if err != nil {
		t.Fatalf("GetOperationByID(%q): %v", first, err)
	}
	if row == nil {
		t.Fatalf("the winning run's own v1 row %q is gone; the cleanup deleted "+
			"the wrong row and propagateLegacyOpStatus now has nothing to mirror onto", first)
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
