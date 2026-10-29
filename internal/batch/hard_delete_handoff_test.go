// file: internal/batch/hard_delete_handoff_test.go
// version: 1.0.0
// guid: 8d2f6a41-b7c3-4e90-95a1-3c0e7f4d1b82
// last-edited: 2026-10-29

package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A batch hard delete of a version group's primary hands the flag on, as the
// single-book delete does (audiobooks TestDeleteAudiobook_HardDeletedPrimaryHandsOn).
// Before 2026-10-29 the batch path never called the hand-off and the group was
// left with no primary at all: the survivor stayed an explicit non-primary, so
// the group dropped out of the Audiobookshelf listing. The survivor is organized
// and has a file under the library root, so it is eligible and the hand-off
// elects it rather than holding.
func TestExecuteOperations_HardDeleteOfPrimaryHandsFlagOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	// The primary owns no file row (a hard delete refuses a book that does).
	primary := f.Book(t, vptest.Spec{ID: "primary", Group: "g", Primary: "true", NoFile: true})
	next := f.Book(t, vptest.Spec{ID: "next", Group: "g", Primary: "false"})
	require.Equal(t, "false", f.Flag(t, next), "control: the survivor starts as a non-primary")

	resp := NewBatchService(f.S).ExecuteOperations(hardDeleteOps(primary))

	require.Equal(t, 1, resp.Success, "%+v", resp.Results)
	gone, err := f.S.GetBookByID(primary)
	require.NoError(t, err)
	require.Nil(t, gone, "the primary is deleted")
	f.RequireSinglePrimary(t, "g", next)
}

// A book that is not the group's primary is deleted without disturbing the
// incumbent: the hand-off keeps a healthy incumbent.
func TestExecuteOperations_HardDeleteOfNonPrimaryKeepsTheIncumbent(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	extra := f.Book(t, vptest.Spec{ID: "extra", Group: "g", Primary: "false", NoFile: true})

	resp := NewBatchService(f.S).ExecuteOperations(hardDeleteOps(extra))

	require.Equal(t, 1, resp.Success, "%+v", resp.Results)
	f.RequireSinglePrimary(t, "g", inc)
}

// The bulk endpoint's handler announces each hard delete that went through by
// reading the result at the operation's own index, so ExecuteOperations answers
// every operation with exactly one result, in order, whatever the operation is
// and however it ends: a success, a refusal, an unknown book and an unknown
// action alike. The handler re-checks the lengths, but this is the contract.
func TestExecuteOperations_OneResultPerOperationInOrder(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	gone := f.Book(t, vptest.Spec{ID: "bane", Group: "g", Primary: "true", NoFile: true})
	kept := f.Book(t, vptest.Spec{ID: "two-face", Group: "g", Primary: "false"})

	req := &BatchOperationsRequest{Caller: &TrashGrants{Trash: true, Restore: true}, Operations: []BatchOperationItem{
		{ID: gone, Action: "delete", HardDelete: true},
		{ID: kept, Action: "delete", HardDelete: true}, // owns a file: refused
		{ID: "no-such-book", Action: "delete", HardDelete: true},
		{ID: kept, Action: "delete"},
		{ID: kept, Action: "restore"},
		{ID: kept, Action: "update", Updates: map[string]any{"title": "Fear"}},
		{ID: kept, Action: "teleport"},
	}}

	resp := NewBatchService(f.S).ExecuteOperations(req)

	require.Len(t, resp.Results, len(req.Operations), "%+v", resp.Results)
	for i, op := range req.Operations {
		require.Equal(t, op.ID, resp.Results[i].ID, "result %d belongs to operation %d", i, i)
	}
	require.True(t, resp.Results[0].Success, "the hard delete of a book that owns nothing went through")
	require.False(t, resp.Results[1].Success, "a book that owns a file is refused")
	require.False(t, resp.Results[2].Success)
	require.False(t, resp.Results[6].Success, "an unknown action is a failure with a result")
}
