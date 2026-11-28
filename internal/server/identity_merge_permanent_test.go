// file: internal/server/identity_merge_permanent_test.go
// version: 0.1.0
// guid: 109002f3-250c-4a28-ae49-d992f32a43b2
// last-edited: 2026-11-21

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// F4-08 B10b2 (A9): the merge service a server builds learns that merges are
// permanent from the server's wiring: core identity (WithIdentity) means the
// account store is core's and every journal is final; a server without it
// answers as HEAD does.
func TestUserMerge_PermanenceFollowsTheIdentityWiring(t *testing.T) {
	const jid = "j-bruce"
	journal := []byte(`{"id":"` + jid + `","status":"applied","survivor_id":"u-bruce","absorbed_id":"u-dick"}`)
	newStore := func(t *testing.T) database.Store {
		store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/merge-permanent.pebble")
		require.NoError(t, err)
		t.Cleanup(func() { store.Close() })
		require.NoError(t, store.SetRaw(database.UserMergeJournalPrefix+jid, journal))
		return store
	}

	head := newTestServerWith(t, newStore(t))
	require.NotNil(t, head.UserMerge())
	for _, action := range []usermerge.Action{usermerge.ActionUndo, usermerge.ActionResume} {
		require.NoError(t, head.UserMerge().CheckAction(action, jid), "%s without identity is HEAD's", action)
	}

	core := newTestServerWith(t, newStore(t), WithIdentity(adoptIdentity(t, nil, nil, nil)))
	require.NotNil(t, core.UserMerge())
	for _, action := range []usermerge.Action{usermerge.ActionUndo, usermerge.ActionResume} {
		require.ErrorIs(t, core.UserMerge().CheckAction(action, jid), usermerge.ErrMergePermanent, "%s under identity", action)
	}
	require.NoError(t, core.UserMerge().CheckAction(usermerge.ActionApply, jid), "an apply is not a reversal")
	require.ErrorIs(t, core.UserMerge().CheckAction(usermerge.ActionUndo, "j-nope"), usermerge.ErrJournalNotFound)
}
