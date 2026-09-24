// file: internal/batch/primary_handoff_test.go
// version: 1.0.1
// guid: 6a0e4d82-3f1b-4c79-b5a2-d8e3c14f7b59
// last-edited: 2026-09-24

package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// Setting is_primary_version=true crowns the book and demotes the
// incumbent instead of leaving two primaries.
func TestBatchUpdate_SetPrimaryDemotesIncumbent(t *testing.T) {
	f := vptest.New(t)
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	pick := f.Book(t, vptest.Spec{ID: "pick", Group: "g", Primary: "false"})
	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{pick}, Updates: map[string]any{"is_primary_version": true}})
	require.Equal(t, 1, resp.Success)
	f.RequireSinglePrimary(t, "g", pick)
	require.Equal(t, "false", f.Flag(t, inc))
}

// Soft-deleting a group's primary through a batch operation hands it on.
func TestBatchDelete_PrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	next := f.Book(t, vptest.Spec{ID: "next", Group: "g", Primary: "false"})
	resp := NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{Operations: []BatchOperationItem{{ID: inc, Action: "delete"}}})
	require.Equal(t, 1, resp.Success)
	f.RequireSinglePrimary(t, "g", next)
}

// Demoting a group's sole primary through a batch update sticks: the group is
// not re-elected, since that could crown the same book against the user.
func TestBatchUpdate_ExplicitDemotionIsNotUndone(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{inc}, Updates: map[string]any{"is_primary_version": false}})
	require.Equal(t, 1, resp.Success)
	require.Equal(t, "false", f.Flag(t, inc))
	require.Empty(t, f.LivePrimaries(t, "g"))
}
