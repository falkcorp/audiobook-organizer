// file: internal/plugins/maintenance/repairs_ops_actor_test.go
// version: 1.0.0
// guid: a5ef5037-5850-466a-98c8-b0db67c38f1b
// last-edited: 2026-10-29

package maintenance

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type actorOpReader struct {
	row *database.OperationV2Row
	err error
}

func (r actorOpReader) GetOperationV2(string) (*database.OperationV2Row, error) { return r.row, r.err }

// applyOpActor is the user on the apply op's row: none when no user enqueued
// it or the row is gone, and an error (never a guessed actor) when the read
// fails.
func TestApplyOpActor(t *testing.T) {
	alfred := "user-alfred"
	got, err := applyOpActor(actorOpReader{row: &database.OperationV2Row{ID: "op-1", ActorUserID: &alfred}}, "op-1")
	require.NoError(t, err)
	require.Equal(t, "user-alfred", got)

	got, err = applyOpActor(actorOpReader{row: &database.OperationV2Row{ID: "op-2"}}, "op-2")
	require.NoError(t, err)
	require.Empty(t, got, "a system-enqueued op has no actor")

	got, err = applyOpActor(actorOpReader{}, "op-3")
	require.NoError(t, err)
	require.Empty(t, got, "a discarded op has no row")

	got, err = applyOpActor(actorOpReader{err: errors.New("store down")}, "op-4")
	require.Error(t, err)
	require.Empty(t, got)

	got, err = applyOpActor(actorOpReader{row: &database.OperationV2Row{ActorUserID: &alfred}}, "")
	require.NoError(t, err)
	require.Empty(t, got, "a dry run has no op id and reads nothing")
}
