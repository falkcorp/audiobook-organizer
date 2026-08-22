// file: internal/server/maintenance_orphan_row_test.go
// version: 1.0.0
// guid: 2d6a14f8-9c05-4e73-b8a1-3f70e2d54c69
// last-edited: 2026-08-22

package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

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

	jobID := probeAdvertisesTrue.ID()

	before := countOperationRows(t, server)

	first := postMaintenanceJobExpectingAccepted(t, server, jobID)
	second := postMaintenanceJobExpectingAccepted(t, server, jobID)

	// Precondition. The registry is not started in this harness, so the first
	// op is still queued and therefore still ACTIVE when the second request
	// arrives. If that ever stops holding, the second enqueue starts its own run
	// legitimately and the orphan assertion below would be checking nothing.
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
