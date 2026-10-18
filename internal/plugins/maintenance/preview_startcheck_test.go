// file: internal/plugins/maintenance/preview_startcheck_test.go
// version: 1.0.0
// guid: e9e9892c-f591-4b80-a47d-006e1625177c
// last-edited: 2026-10-18

package maintenance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// TestPreviewStartChecks: the start-time refusals three ops declare on their
// Preview, through the same Resolve /api/v3/ops calls. Each refused case used
// to be queued and then either do nothing (series-phantom-repair's report
// mode on a live run) or fail at run time (opchange ensure beside the
// injected dry_run), or pick a write kind the caller never chose
// (clone-into-library).
func TestPreviewStartChecks(t *testing.T) {
	p := &Plugin{}
	sp := previewResolver(p.seriesPhantomRepairDef())
	oc := previewResolver(p.opChangeBookIndexRebuildDef())
	cl := previewResolver(p.itunesCloneIntoLibraryDef())
	cases := []struct {
		name    string
		resolve func(params, mode string) (json.RawMessage, error)
		params  string
		mode    string
		wantErr string
		want    map[string]any
	}{
		// series-phantom-repair: report (the default) ignores dry_run.
		{"series live without mode", sp, `{}`, "live", "needs mode", nil},
		{"series live report", sp, `{"mode":"report"}`, "live", "needs mode", nil},
		{"series live empty mode", sp, `{"mode":""}`, "live", "needs mode", nil},
		{"series live null", sp, `{"mode":"null"}`, "live", "", map[string]any{"mode": "null", "dry_run": false}},
		{"series preview report", sp, `{}`, "preview", "", map[string]any{"dry_run": true}},
		{"series mode not a string", sp, `{"mode":3}`, "live", "must be a string", nil},
		// opchange-book-index-rebuild: ensure is the boot path and refuses dry_run.
		{"opchange ensure preview", oc, `{"ensure":true}`, "preview", "boot mode", nil},
		{"opchange ensure live", oc, `{"ensure":true}`, "live", "boot mode", nil},
		{"opchange ensure false", oc, `{"ensure":false}`, "live", "", map[string]any{"ensure": false, "dry_run": false, "dryRun": false}},
		{"opchange rebuild preview", oc, `{}`, "preview", "", map[string]any{"dry_run": true, "dryRun": true}},
		// itunes.clone-into-library: three write kinds, one per live run.
		{"clone preview", cl, `{"group_ids":["g1"]}`, "preview", "", map[string]any{"apply": false, "rollback": false, "repair": false, "group_ids": []any{"g1"}}},
		{"clone live names none", cl, `{"group_ids":["g1"]}`, "live", "exactly one", nil},
		{"clone live names two", cl, `{"apply":true,"rollback":true}`, "live", "exactly one", nil},
		{"clone live rollback", cl, `{"rollback":true,"group_ids":["g1"]}`, "live", "", map[string]any{"rollback": true, "group_ids": []any{"g1"}}},
		{"clone omitted mode follows the switch", cl, `{"repair":true}`, "", "", map[string]any{"repair": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.resolve(tc.params, tc.mode)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			require.Equal(t, tc.want, got)
		})
	}
}

// previewResolver is d.Preview.Resolve over string params.
func previewResolver(d sdk.OperationDef) func(string, string) (json.RawMessage, error) {
	return func(params, mode string) (json.RawMessage, error) {
		return d.Preview.Resolve(json.RawMessage(params), mode)
	}
}
