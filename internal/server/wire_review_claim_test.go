// file: internal/server/wire_review_claim_test.go
// version: 1.2.0
// guid: 2c06d748-d485-44de-9d5a-2ad16e6fedd7
// last-edited: 2026-10-18

package server

import (
	"net/http"
	"testing"
	"time"

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

// TestReviewRoutes_AbandonApplyIsWiredAndReachesTheStore: the abandon route exists,
// takes the session's permission, and its refusals are the store's (coded 409s, not a
// 404 for a missing route or a 500 for a missing capability). The item claimed here is
// held by this process, so it is in progress, not interrupted; the handler tests cover
// a real abandon.
func TestReviewRoutes_AbandonApplyIsWiredAndReachesTheStore(t *testing.T) {
	f := setupCredGuardServerWith(t, func(c *config.Config) { c.ReviewApplyEnabled = true })
	seed := func(key string) database.ReviewItem {
		it, err := f.store.UpsertReviewItem(database.ReviewItem{Kind: "regroup.multidisc", DedupKey: key,
			FolderRef: "/synthetic/" + key, Summary: "s", Payload: `{"recommendedAction":"combine"}`})
		require.NoError(t, err)
		return it
	}
	open, held := seed("open"), seed("held")
	claimer, ok := database.AsCapability[database.ReviewItemApplyClaimer](f.store)
	require.True(t, ok)
	_, err := claimer.ClaimReviewItemForApply(held.ID, "combine", time.Time{})
	require.NoError(t, err)

	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/review/items/"+open.ID+"/abandon-apply", nil)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "REVIEW_APPLY_NOT_INTERRUPTED")
	w = f.do(t, f.sessionToken, http.MethodPost, "/api/v1/review/items/"+held.ID+"/abandon-apply", nil)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "REVIEW_APPLY_IN_PROGRESS")

	w = f.do(t, f.sessionToken, http.MethodGet, "/api/v1/review/count", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"needs_attention":0`)
	assert.Contains(t, w.Body.String(), `"applying":1`)
}

// TestOpsStore_ResolvesReviewPendingDeleter: the regroup reconcile resolves its
// conditional delete, together with the pending-hold lister, from the maintenance
// plugin's OpsStore (maintenance.reviewHoldStore is exactly these two). If this
// lookup failed the purge would not run and the op would fail with "stale-purge
// SKIPPED"; the maintenance package pins the composite on the concrete store.
func TestOpsStore_ResolvesReviewPendingDeleter(t *testing.T) {
	f := setupCredGuardServer(t)
	_, ok := database.AsCapability[interface {
		database.ReviewItemPendingDeleter
		ListReviewItems(filter database.ReviewFilter) ([]database.ReviewItem, int, error)
	}](f.srv.OpsStore())
	assert.True(t, ok, "OpsStore() %T hides the review hold lister and database.ReviewItemPendingDeleter", f.srv.OpsStore())
	_, ok = database.AsCapability[database.ReviewItemApplyClaimer](f.srv.storeForWiring())
	assert.True(t, ok, "storeForWiring() %T hides database.ReviewItemApplyClaimer", f.srv.storeForWiring())
}
