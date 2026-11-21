// file: internal/server/identity_merge_unavailable_test.go
// version: 0.1.0
// guid: 4f6a1d83-b0c7-49e2-8a15-d3e7c92b60f4
// last-edited: 2026-11-21

package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// F4-08 B10l guard: the merge service a server builds is unavailable exactly
// when the server is wired with core identity (the signal B10b2 uses for
// permanence), and answers as HEAD does without it.
func TestUserMerge_AvailabilityFollowsTheIdentityWiring(t *testing.T) {
	newStore := func(t *testing.T) database.Store {
		store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/merge-unavailable.pebble")
		require.NoError(t, err)
		t.Cleanup(func() { store.Close() })
		return store
	}
	head := newTestServerWith(t, newStore(t))
	require.NotNil(t, head.UserMerge())
	require.NoError(t, head.UserMerge().CheckAvailable())

	core := newTestServerWith(t, newStore(t), WithIdentity(adoptIdentity(t, nil, nil, nil)))
	require.NotNil(t, core.UserMerge())
	err := core.UserMerge().CheckAvailable()
	require.True(t, errors.Is(err, usermerge.ErrUnavailableUnderIdentity), "got %v", err)
}
