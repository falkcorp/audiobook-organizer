// file: internal/plugins/maintenance/vacuum_preview_behaviour_test.go
// version: 1.0.0
// guid: 7a66c1f3-318d-4fff-a308-a5d6ccb697d4
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestPreviewBehaviour_VacuumActivityDB runs the registered op with the params
// the /api/v3/ops start path would queue for each mode and checks what reached
// the deps: a preview calls VacuumActivityDB only with dryRun=true (the
// read-only report) and never the live vacuum, and a live run calls it with
// dryRun=false. The declared Preview carries both spellings, so v3 writes both.
func TestPreviewBehaviour_VacuumActivityDB(t *testing.T) {
	def := (&Plugin{}).vacuumActivityDBDef()
	if got, want := def.Preview.ModeKeys(), []string{"dry_run", "dryRun"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Preview mode keys = %v, want %v", got, want)
	}
	for _, tc := range []struct {
		mode      string
		wantCalls []bool
	}{
		{"preview", []bool{true}},
		{"live", []bool{false}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			deps := &vacuumDeps{report: database.ActivityVacuumReport{Supported: true}}
			def := New(deps).vacuumActivityDBDef()
			params, err := def.Preview.Resolve(json.RawMessage(`{}`), tc.mode)
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tc.mode, err)
			}
			if err := def.Run(context.Background(), params, &resultReporter{}); err != nil {
				t.Fatalf("Run(%s) with %s: %v", tc.mode, params, err)
			}
			if !reflect.DeepEqual(deps.calls, tc.wantCalls) {
				t.Fatalf("mode %s (params %s): VacuumActivityDB called with dryRun %v, want %v", tc.mode, params, deps.calls, tc.wantCalls)
			}
		})
	}
}
