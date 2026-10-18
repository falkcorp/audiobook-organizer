// file: internal/plugins/maintenance/preview_behaviour_vg_test.go
// version: 1.0.0
// guid: 9f90a0ee-337b-4860-9e4f-e4c180531e66
// last-edited: 2026-10-18

package maintenance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPreviewBehaviour_VersionGroupPrimaryRepair runs the op on what
// /api/v3/ops would queue (its own Preview declaration's Resolve, decoded by
// Run's own parse) and checks the effect, not the params: a preview writes
// nothing, a live run writes. The op decodes two mode keys (apply is the older
// spelling of dry_run=false) and both are written, so neither half can be
// decided by a key the declaration left out.
func TestPreviewBehaviour_VersionGroupPrimaryRepair(t *testing.T) {
	def := (&Plugin{}).versionGroupPrimaryRepairDef()
	require.Equal(t, []string{"dry_run", "apply"}, def.Preview.ModeKeys())

	for _, tc := range []struct {
		mode      string
		wantWrite bool
	}{{"preview", false}, {"live", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newVGRepairFixture(t)
			f.seed(t)
			raw, err := def.Preview.Resolve(json.RawMessage(`{"group_ids":["vg-double"]}`), tc.mode)
			require.NoError(t, err)
			params, err := parseVGPrimaryRepairParams(raw)
			require.NoError(t, err)
			rep, err := f.run(t, fakeDeps{store: f.s}, params)
			require.NoError(t, err)
			hist, err := f.s.GetBookChangeHistory("A", 100)
			require.NoError(t, err)
			if !tc.wantWrite {
				require.True(t, rep.DryRun, "a preview ran as a write (params %s)", raw)
				require.Zero(t, rep.Applied)
				require.Equal(t, "true", f.flag(t, "B"), "a preview demoted B (params %s)", raw)
				require.Empty(t, hist, "a preview wrote change history (params %s)", raw)
				return
			}
			require.False(t, rep.DryRun, "a live run previewed (params %s)", raw)
			require.Equal(t, 1, rep.Applied, "a live run applied nothing (params %s)", raw)
			require.Equal(t, "false", f.flag(t, "B"), "a live run left B primary (params %s)", raw)
		})
	}
}
