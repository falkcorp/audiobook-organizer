// file: internal/maintenance/jobs/preview_behaviour_test.go
// version: 1.0.0
// guid: ed206294-a7e6-4b17-9d5e-56b475029ed8
// last-edited: 2026-10-18

package jobs

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestPreviewBehaviour_RepointVersionPrimary runs the job on what
// /api/v3/ops would queue and checks the flags it leaves.
//
// The spec is built the way internal/server's maintenanceJobPreview builds
// it (the job's Policy().Params, else opmode.DryRunParams), and dry_run is
// resolved the way the op's run closure resolves it
// (opmode.ResolveDryRunDefault with the job's advertised default, true): the
// server package cannot reach this package's fixtures, so the two lines are
// mirrored here and named.
//
// The job writes only when apply && !dry_run. Declared with dry_run alone,
// a live run sent dry_run=false and no apply and silently previewed; the
// live half below is what catches that.
func TestPreviewBehaviour_RepointVersionPrimary(t *testing.T) {
	job := &repointVersionPrimaryJob{}
	params := job.Policy().Params
	if params == nil {
		params = opmode.DryRunParams{}
	}
	spec := opsregistry.PreviewSpec{Params: params}
	if got := spec.ModeKeys(); !reflect.DeepEqual(got, []string{"apply", "dry_run"}) && !reflect.DeepEqual(got, []string{"dry_run", "apply"}) {
		t.Errorf("repoint-version-primary mode keys = %v, want dry_run and apply", got)
	}

	for _, tc := range []struct {
		mode      string
		wantWrite bool
	}{{"preview", false}, {"live", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			s := ddRealStore(t)
			imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
			raw, err := spec.Resolve(json.RawMessage(`{}`), tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			var dry opmode.DryRunParams
			if err := json.Unmarshal(raw, &dry); err != nil {
				t.Fatal(err)
			}
			dryRun, err := opmode.ResolveDryRunDefault("maintenance.repoint-version-primary", dry.DryRun, dry.DryRunCamel, true)
			if err != nil {
				t.Fatal(err)
			}
			res := rvpRun(t, s, string(raw), dryRun)
			wrote := res.Counts[bucketRepointed] > 0 || rvpFlag(t, s, imported[0].ID) != "false" || rvpFlag(t, s, twins[0].ID) != "true"
			if wrote != tc.wantWrite {
				t.Fatalf("mode %s (params %s, dry_run=%v) wrote=%v, want %v; counts %+v", tc.mode, raw, dryRun, wrote, tc.wantWrite, res.Counts)
			}
		})
	}
}
