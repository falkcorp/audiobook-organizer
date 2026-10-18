// file: internal/server/wire_review_claim_test.go
// version: 1.0.0
// guid: 2c06d748-d485-44de-9d5a-2ad16e6fedd7
// last-edited: 2026-10-18

package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The review handler and the regroup reconcile resolve their claim-aware store
// methods with database.AsCapability. A decorator without Unwrap hides capabilities
// (store_capability.go), and since 01-P81j a missing claimer is not a degraded mode:
// every approve and reject answers 500 REVIEW_CLAIM_UNAVAILABLE, and the reconcile
// skips its purge. The handler tests inject a *PebbleStore directly, so only a test
// through the real server wiring proves the lookups resolve there.

// TestReviewRoutes_ClaimCapabilityResolvesThroughServerWiring drives approve (with
// apply on, through the real claim and finish), approve as separate, reject and the
// count through the real router.
func TestReviewRoutes_ClaimCapabilityResolvesThroughServerWiring(t *testing.T) {
	f := setupCredGuardServerWith(t, func(c *config.Config) { c.ReviewApplyEnabled = true })
	seedHold := func(key string) database.ReviewItem {
		it, err := f.store.UpsertReviewItem(database.ReviewItem{
			Kind: "regroup.multidisc", DedupKey: key, FolderRef: "/synthetic/" + key, Summary: "s",
			// No members: ApplyMultidisc finds fewer than two present and no-ops,
			// so the apply path runs end to end without touching a book.
			Payload: `{"folder":"/synthetic/` + key + `","recommendedAction":"combine","memberBookIDs":[]}`,
		})
		require.NoError(t, err)
		return it
	}
	applied, separate, rejected := seedHold("a"), seedHold("s"), seedHold("r")

	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/review/items/"+applied.ID+"/approve", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/review/items/"+separate.ID+"/approve", map[string]any{"action": "separate"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/review/items/"+rejected.ID+"/reject", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	for id, want := range map[string]string{
		applied.ID:  database.ReviewStatusApplied,
		separate.ID: database.ReviewStatusApproved,
		rejected.ID: database.ReviewStatusRejected,
	} {
		got, err := f.store.GetReviewItem(id)
		require.NoError(t, err)
		assert.Equal(t, want, got.Status, id)
	}

	w = f.do(t, f.sessionToken, http.MethodGet, "/api/v1/review/count", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"applying":0`)
}

// TestOpsStore_ResolvesReviewPendingDeleter: the regroup reconcile resolves its
// conditional delete from the maintenance plugin's OpsStore. If this lookup failed
// the purge would be skipped with only a warning in the op log.
func TestOpsStore_ResolvesReviewPendingDeleter(t *testing.T) {
	f := setupCredGuardServer(t)
	_, ok := database.AsCapability[database.ReviewItemPendingDeleter](f.srv.OpsStore())
	assert.True(t, ok, "OpsStore() %T hides database.ReviewItemPendingDeleter", f.srv.OpsStore())
	_, ok = database.AsCapability[database.ReviewItemApplyClaimer](f.srv.storeForWiring())
	assert.True(t, ok, "storeForWiring() %T hides database.ReviewItemApplyClaimer", f.srv.storeForWiring())
}
