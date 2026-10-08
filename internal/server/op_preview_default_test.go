// file: internal/server/op_preview_default_test.go
// version: 1.0.0
// guid: 9d4e2f7a-3c61-4b85-8e02-5a7b1c9d6e34
// last-edited: 2026-09-25

package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServerOps_DryRunRoutesThroughOpmode covers the internal/server ops whose
// mode moved onto opmode.ResolveDryRun on 2026-09-25, through the REAL
// registered Run (see w3runOpWithParams).

func TestServerOps_DryRunRoutesThroughOpmode(t *testing.T) {
	ops := []w3registrar{
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
