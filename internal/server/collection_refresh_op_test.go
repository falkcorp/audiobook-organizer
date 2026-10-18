// file: internal/server/collection_refresh_op_test.go
// version: 1.0.0
// guid: 9a4e1c27-5d83-4b60-8f12-0c7b3d6e9a58
// last-edited: 2026-10-18

package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
)

// The refresh op is registered in a booted server, is a previewed writer gated on
// collections.manage, and an omitted mode is a preview: only the hourly task's
// explicit dry_run=false writes.
func TestCollectionRefreshOp_IsRegisteredAsAGatedPreviewWriter(t *testing.T) {
	srv, ids, _ := bootRegisteredOpIDs(t)
	require.True(t, ids[collectionRefreshOpID], "the op must be registered")

	def, ok := srv.opRegistry.Def(collectionRefreshOpID)
	require.True(t, ok)
	assert.False(t, def.Preview.IsZero(), "a writer declares its preview")
	assert.True(t, canWrite(def))
	assert.Equal(t, []auth.Permission{auth.PermCollectionsManage}, def.Permissions)
	assert.Equal(t, collectionRefreshOpID, def.ConcurrencyKey, "two refreshes never run at once")

	for raw, want := range map[string]bool{`{}`: true, ``: true, `{"dry_run":true}`: true, `{"dry_run":false}`: false} {
		var p collectionRefreshParams
		if raw != "" {
			require.NoError(t, json.Unmarshal([]byte(raw), &p))
		}
		got, err := opmode.ResolveDryRun(collectionRefreshOpID, p.DryRun, p.DryRunCamel)
		require.NoError(t, err)
		assert.Equal(t, want, got, "params %q", raw)
	}
}
