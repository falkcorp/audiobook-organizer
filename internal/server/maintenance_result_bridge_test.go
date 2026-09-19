// file: internal/server/maintenance_result_bridge_test.go
// version: 1.1.0
// guid: 412f096a-5425-4739-b76e-41cc5089d2d4
// last-edited: 2026-09-19

package server

import (
	"context"
	"encoding/json"
	"net/http"
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

// A real bulk chapter merge needs an explicit "dry_run": false. An empty body,
// {}, or an explicit null must all resolve to the advertised dry-run default,
// and that resolved value is what the run's v2 row carries.
func TestRunMaintenanceJob_MergeChapterGroupsWithoutExplicitFalseIsADryRun(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	for _, body := range []string{"", `{}`, `{"dry_run": null}`, `{"min_files": 3}`} {
		w := postMaintenanceJob(t, server, "merge-chapter-groups", body)
		if w.Code != http.StatusAccepted {
			t.Fatalf("body %q: status = %d, want 202; %s", body, w.Code, w.Body.String())
		}
		var resp struct {
			Data struct {
				OperationID string `json:"operation_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Data.OperationID == "" {
			t.Fatalf("body %q: no operation id in %s (%v)", body, w.Body.String(), err)
		}
		row, err := server.Ops().GetOperationV2(resp.Data.OperationID)
		if err != nil || row == nil {
			t.Fatalf("body %q: GetOperationV2: %v", body, err)
		}
		var saved maintenanceJobOpParams
		if err := json.Unmarshal([]byte(row.Params), &saved); err != nil {
			t.Fatalf("body %q: decode params %s: %v", body, row.Params, err)
		}
		if !saved.DryRun {
			t.Fatalf("body %q resolved to a REAL merge; params %s", body, row.Params)
		}
	}
}
