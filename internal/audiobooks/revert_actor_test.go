// file: internal/audiobooks/revert_actor_test.go
// version: 1.0.0
// guid: 6e064528-8806-40ea-bcad-637c751f4d0b
// last-edited: 2026-10-29

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// WithActor returns a copy that carries the user and leaves the receiver as it
// was, so a shared service is never made to carry one request's user.
func TestRevertService_WithActorCopiesAndLeavesTheReceiver(t *testing.T) {
	rs := &RevertService{}
	actor := RevertActor{UserID: "user-alfred", Name: "alfred"}
	with := rs.WithActor(actor)
	require.Equal(t, actor, with.actor)
	require.Equal(t, RevertActor{}, rs.actor)
	require.NotSame(t, rs, with)
}

// The combine undo reverses a retire on copies of the service, so the user a
// revert runs for must survive both: the merge-lock-held copy and the
// RetireUndoer it hands the merge service.
func TestRevertService_ActorSurvivesTheCombineUndoCopies(t *testing.T) {
	actor := RevertActor{UserID: "user-selina", Name: "selina"}
	rs := (&RevertService{}).WithActor(actor)
	require.Equal(t, actor, rs.withMergeLockHeld().actor)
	u, ok := rs.retireUndoer().(retireUndoer)
	require.True(t, ok)
	require.Equal(t, actor, u.rs.withMergeLockHeld().actor)
}
