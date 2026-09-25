// file: internal/maintenance/jobs/preview_default_guard_test.go
// version: 1.0.0
// guid: 0c7d2b1e-5f3a-4e68-9a41-7d2e8b6c3f15
// last-edited: 2026-09-25

package jobs

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// noPreviewMode lists the maintenance jobs that do NOT advertise
// dry_run:true, each with the reason. Every other registered job must.
//
// The dispatcher (server/maintenance_dispatcher.go) and the v2 Run closure
// (server/maintenance_job_op.go) both fall back to the job's ADVERTISED
// dry_run when a request omits it. So a job advertising false runs LIVE on an
// empty body. The owner's 2026-09-25 rule is that every writing operation
// previews unless told otherwise; this test enforces it over the job registry
// itself, so a new job cannot ship with a live default without an entry here.
//
// Advertising true for a job whose Run ignores dryRun would be worse than
// false: the UI would offer a "preview" that writes. So jobs with no preview
// mode stay listed here rather than flipped.
var noPreviewMode = map[string]string{
	// Run(..., _ bool): read-only reports that record results only.
	"relink-report":           "read-only report; Run ignores dryRun",
	"scan-duplicate-files":    "read-only scan; Run ignores dryRun",
	"scan-duration-mismatch":  "read-only scan; Run ignores dryRun",
	"scan-metadata-hash-dups": "read-only scan; Run ignores dryRun",

	// No dry_run key in DefaultParams at all.
	"bulk-fetch-metadata":   "no preview mode; fetches and applies metadata",
	"generate-itl-tests":    "no preview mode; writes test fixtures, not library data",
	"revert-metadata-fetch": "no preview mode; reverts only the fetch_op_ids it is given",
	"scan-chapter-groups":   "read-only scan; the writing twin merge-chapter-groups previews by default",
}

func TestMaintenanceJobs_PreviewByDefault(t *testing.T) {
	all := maintenance.All()
	if len(all) == 0 {
		t.Fatal("no maintenance jobs registered; the guard would pass vacuously")
	}
	seen := map[string]bool{}
	for _, job := range all {
		id := job.ID()
		raw, err := json.Marshal(job.DefaultParams())
		if err != nil {
			t.Errorf("%s: marshal DefaultParams: %v", id, err)
			continue
		}
		var dp struct {
			DryRun *bool `json:"dry_run"`
		}
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &dp); err != nil {
				t.Errorf("%s: DefaultParams is not a JSON object: %s", id, raw)
				continue
			}
		}
		advertisesPreview := dp.DryRun != nil && *dp.DryRun
		reason, exempt := noPreviewMode[id]
		switch {
		case exempt && advertisesPreview:
			t.Errorf("%s advertises dry_run:true but is listed in noPreviewMode (%q); remove the entry", id, reason)
		case !exempt && !advertisesPreview:
			t.Errorf("%s: DefaultParams %s does not advertise dry_run:true, so an empty request runs it LIVE. "+
				"Advertise dry_run:true (and honor dryRun in Run), or list it in noPreviewMode with the reason it has no preview mode", id, raw)
		}
		seen[id] = true
	}
	for id := range noPreviewMode {
		if !seen[id] {
			t.Errorf("noPreviewMode lists %q but no such job is registered; delete the entry", id)
		}
	}
}
