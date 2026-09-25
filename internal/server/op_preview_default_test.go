// file: internal/server/op_preview_default_test.go
// version: 1.0.0
// guid: 9d4e2f7a-3c61-4b85-8e02-5a7b1c9d6e34
// last-edited: 2026-09-25

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServerOps_DryRunRoutesThroughOpmode covers the internal/server ops whose
// mode moved onto opmode.ResolveDryRun on 2026-09-25, through the REAL
// registered Run (see w3runOpWithParams).
//
// itunes.path-repair is the one that matters: its params were a plain
// `DryRun bool`, so {} -- a POST /operations/trigger with no params, or a
// retry of a row saved without the key -- rewrote locations in the live
// iTunes library. A conflicting body must now be refused at the mode check,
// before the op reaches the (nil) iTunes service.
func TestServerOps_DryRunRoutesThroughOpmode(t *testing.T) {
	ops := []w3registrar{
		{"itunes.path-repair", (*Server).RegisterITunesPathRepairOp, true},
		{"operations.backfill-legacy-status", (*Server).RegisterLegacyStatusBackfillOp, true},
	}
	for _, r := range ops {
		for _, body := range []string{`{"dry_run":false,"dryRun":true}`, `{"dry_run":true,"dryRun":false}`} {
			t.Run(r.opID+" "+body, func(t *testing.T) {
				err, panicked := w3runOpWithParams(t, r, body)
				require.Falsef(t, panicked, "%s ran past the mode check on a conflicting body", r.opID)
				require.Error(t, err)
				require.Contains(t, err.Error(), "disagree")
			})
		}
	}
}

// TestITunesPathRepairParams_EmptyIsPreview pins the decode itself: the
// params the op receives for {} and for nil resolve to a preview, and the
// handler's explicit value survives the round trip in both directions.
func TestITunesPathRepairParams_EmptyIsPreview(t *testing.T) {
	for _, raw := range []string{``, `{}`, `null`} {
		var p itunesPathRepairOpParams
		if raw != "" {
			require.NoError(t, json.Unmarshal([]byte(raw), &p))
		}
		require.Nil(t, p.DryRun, "raw %q", raw)
		require.Nil(t, p.DryRunCamel, "raw %q", raw)
	}
	for _, want := range []bool{true, false} {
		b, err := json.Marshal(itunesPathRepairOpParams{DryRun: &want})
		require.NoError(t, err)
		var back itunesPathRepairOpParams
		require.NoError(t, json.Unmarshal(b, &back))
		require.NotNil(t, back.DryRun)
		require.Equal(t, want, *back.DryRun)
		require.True(t, strings.Contains(string(b), `"dry_run"`), "the handler must send the snake key: %s", b)
	}
}
