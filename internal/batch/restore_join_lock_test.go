// file: internal/batch/restore_join_lock_test.go
// version: 1.0.0
// guid: 0d5b8f63-2e9a-4c71-b4d6-9f1a3e7c2b58
// last-edited: 2026-10-02

package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A batch restore that also sets version_group_id moves the row out of its
// group and into another: merge.RestoreFromTrash is handed the joined group
// (joinGroups), so the restore waits for a hand-off on either group.
func TestBatchRestore_GroupChangeWaitsForGroupLocks(t *testing.T) {
	for _, held := range []string{"g", "h"} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			prev := config.AppConfig.RootDir
			config.AppConfig.RootDir = f.Root
			t.Cleanup(func() { config.AppConfig.RootDir = prev })
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			r := f.Book(t, vptest.Spec{ID: "r", Group: "g", Primary: "false"})
			f.SoftDelete(t, r)
			var resp *BatchResponse
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(held) },
				func() error {
					resp = NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
						IDs: []string{r}, Updates: map[string]any{"marked_for_deletion": false, "version_group_id": "h"},
					})
					return nil
				},
				func() bool { return f.GroupOf(t, r) == "g" })
			require.Equal(t, 1, resp.Success, "results: %+v", resp.Results)
			require.Equal(t, "h", f.GroupOf(t, r))
		})
	}
}
