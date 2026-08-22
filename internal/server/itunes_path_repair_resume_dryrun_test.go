// file: internal/server/itunes_path_repair_resume_dryrun_test.go
// version: 1.0.0
// guid: 5a9f27c4-8e13-42db-b6a0-71c3fd48e902
// last-edited: 2026-08-22

package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// TestResumeLegacyITunesPathRepair_ResumesInDryRun pins the safety property that
// an interrupted iTunes path repair never comes back as an apply.
//
// The bug: resumeLegacyOp re-enqueued "itunes.path-repair" with NIL params.
// EnqueueOp normalizes nil to "{}", which decodes to the zero
// itunesPathRepairOpParams — and its DryRun field is FALSE. A dry run that was
// interrupted by a deploy therefore resumed in APPLY mode and rewrote locations
// in the live iTunes library, with nothing in the original request asking for
// it. The maintenance-job path had already been fixed for this exact class of
// bug (see maintenance_dryrun_default_test.go); the iTunes path had not.
//
// This asserts on the params PERSISTED ON THE ENQUEUED V2 ROW rather than on
// what the repairer received, because the row is what a later resume or a
// restart re-reads. A test that stubbed the repairer would pass even if the
// stored params were wrong.
func TestResumeLegacyITunesPathRepair_ResumesInDryRun(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}

	since := time.Now().Add(-time.Minute)

	opID := ulid.Make().String()
	if _, err := server.Ops().CreateOperation(opID, "itunes_path_repair", nil); err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}

	server.resumeLegacyOp(opID, "itunes_path_repair")

	store, ok := any(server.Ops()).(database.OpsV2Store)
	if !ok {
		t.Skip("store does not expose the v2 surface in this build")
	}
	rows, err := store.ListOperationsV2Since(since, 200)
	if err != nil {
		t.Fatalf("ListOperationsV2Since: %v", err)
	}

	var found int
	for _, row := range rows {
		if row.DefID != "itunes.path-repair" {
			continue
		}
		found++
		var p itunesPathRepairOpParams
		if err := json.Unmarshal([]byte(row.Params), &p); err != nil {
			t.Fatalf("decode params %q: %v", row.Params, err)
		}
		if !p.DryRun {
			t.Fatalf("resumed itunes.path-repair with dry_run=false (params=%q) — "+
				"an interrupted preview would rewrite the live iTunes library", row.Params)
		}
	}
	if found == 0 {
		t.Fatal("resumeLegacyOp enqueued no itunes.path-repair op; the resume branch did not fire")
	}
}
