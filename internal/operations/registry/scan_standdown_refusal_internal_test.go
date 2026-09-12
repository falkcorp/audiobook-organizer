// file: internal/operations/registry/scan_standdown_refusal_internal_test.go
// version: 1.0.0
// guid: 9d4e2b17-6c3a-4f81-a05e-3b8c7d1f2e94
// last-edited: 2026-09-12

package registry

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// TestTryAcquireScanStandDown_RefusalDrainsDroppedScans pins the refusal
// interleave. A no-wait request registers its holder, and in the window before
// it sees the claimed scan the worker pickup gate drops a scan as
// interrupted_quiesced and parks it on scanGate.dropped (a holder was live: the
// request's own). The request then refuses. As the last holder out, its refusal
// must drain that list and re-queue the scan; an inline holder delete left it
// parked until some later holder's release or the next restart.
//
// The interleave is two adjacent lines apart, so the test builds the state it
// leaves behind directly: a claimed library.scan stub in r.running and a parked
// scan on scanGate.dropped when TryAcquire runs.
func TestTryAcquireScanStandDown_RefusalDrainsDroppedScans(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	// resumeQuiescedOp's first step. Returning a row that is no longer
	// quiesced keeps the test on the drain itself rather than the re-queue
	// machinery; mockery fails the test if the call never happens.
	store.EXPECT().GetOperationV2("dropped-scan").
		Return(&database.OperationV2Row{ID: "dropped-scan", DefID: scanStandDownDefID, Status: "queued"}, nil).
		Once()

	r := New(store, slog.Default(), 1, nil)
	r.mu.Lock()
	r.running["claimed-scan"] = &runHandle{id: "claimed-scan", defID: scanStandDownDefID}
	r.mu.Unlock()
	r.scanGate.mu.Lock()
	r.scanGate.dropped = append(r.scanGate.dropped, "dropped-scan")
	r.scanGate.mu.Unlock()

	if _, err := r.TryAcquireScanStandDown("http:apply:1", "apply"); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("TryAcquireScanStandDown err = %v, want ErrScanRunning", err)
	}

	r.scanGate.mu.Lock()
	defer r.scanGate.mu.Unlock()
	if len(r.scanGate.dropped) != 0 {
		t.Errorf("refusal left scans parked on scanGate.dropped: %v", r.scanGate.dropped)
	}
	if _, ok := r.scanGate.holders["http:apply:1"]; ok {
		t.Error("refused request is still registered as a holder")
	}
}
