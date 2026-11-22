// file: internal/books/r2store_topup_test.go
// version: 0.1.0
// guid: 7c41e9a2-3d85-4b60-8e1f-a52d96b0c347
// last-edited: 2026-11-22

package books

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is the one default-build test that reads the cutover registry next to the
// frozen fixture, so it is in its own file: it goes with the cutover code in
// R3-02e-1, and nothing else about the fixture depends on it.

// TestR2StoreTopUpIsNotStale fails when a step the fixture stood in with a fresh
// install's record is now registered by the build, because the fixture is then
// not the store R2 leaves: regenerate it with r2storeRegenCommand. It reads the
// steps the build registers from the flipped manifest and the steps the
// fixture's top-up recorded from testdata/r2store.json.
func TestR2StoreTopUpIsNotStale(t *testing.T) {
	steps, err := cutoverStepsOf(flipOn().Manifest())
	require.NoError(t, err)
	live := map[string]bool{}
	for _, st := range steps {
		live[st.ID] = true
	}
	for _, id := range readR2StoreManifest(t).ToppedUp {
		require.False(t, live[id], "%s is registered by this build but the committed fixture stands it in with a fresh install's record: regenerate it: %s", id, r2storeRegenCommand)
	}
	// And the other way: a step the fixture ran for real must still be one the
	// build registers, or the generator no longer produces what is committed.
	for _, id := range r2StoreSteps {
		if !slices.Contains(readR2StoreManifest(t).ToppedUp, id) {
			require.True(t, live[id], "%s was run by the generator but this build does not register it: regenerate: %s", id, r2storeRegenCommand)
		}
	}
}
