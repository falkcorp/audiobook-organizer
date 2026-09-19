// file: internal/server/maintenance_result_bridge_test.go
// version: 1.0.0
// guid: 412f096a-5425-4739-b76e-41cc5089d2d4
// last-edited: 2026-09-19

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// resultProbeJob stores a structured result through maintenance.SetResult. The
// bridge must land it on the run's v2 row, which is what
// GET /operations/:id/result serves -- the chapter-group jobs depend on it.
type resultProbeJob struct{}

func (resultProbeJob) ID() string          { return "test-probe-result-bridge" }
func (resultProbeJob) Name() string        { return "Result Bridge Probe" }
func (resultProbeJob) Description() string { return "Test probe; stores a structured result." }
func (resultProbeJob) Category() string    { return "test" }
func (resultProbeJob) CanResume() bool     { return false }
func (resultProbeJob) DefaultParams() any  { return struct{}{} }
func (resultProbeJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
func (resultProbeJob) Run(ctx context.Context, _ maintenance.JobStore, _ maintenance.ProgressReporter, _ bool) error {
	return maintenance.SetResult(ctx, map[string]any{"groups_found": 7})
}

func init() { maintenance.Register(resultProbeJob{}) }

func TestMaintenanceBridge_SetResultLandsOnTheV2Row(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	opID := postMaintenanceJobExpectingAccepted(t, server, resultProbeJob{}.ID())
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		row, err := server.storeForWiring().GetOperationV2(opID)
		if err != nil {
			t.Fatalf("GetOperationV2: %v", err)
		}
		if row != nil && row.ResultData != nil {
			if !strings.Contains(*row.ResultData, `"groups_found":7`) {
				t.Fatalf("result = %s, want the job's payload", *row.ResultData)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job's structured result never reached the v2 row")
}
