// file: internal/server/handlers/metadata_cache_identity_stale_test.go
// version: 1.0.0
// guid: 67f60b15-7a67-4a45-a66a-d2c5e7f8502f
// last-edited: 2026-10-10

package handlers

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestGetCacheReviewResults_RetitledPendingBookIsIdentityStale: a retitle
// keeps the book's cache row, marked Stale (database.UpdateBook). The review
// queue must not offer its old-title candidates as a pending review, where a
// "No match" verdict would drop the book from the refetch that replaces them.
// After the incremental snapshot rebuild (the path that rebuilt each row's
// summary without Stale) the pending book is out of `results`, listed in the
// unreviewable bucket as identity_stale with no candidate, and counted under
// unreviewable_by_cause.identity_stale and `unreviewable`.
//
// A reviewed book keeps its row in its verdict bucket: every title-changing
// apply leaves a Stale row on an applied book that no refetch revisits, and
// counting it as unreviewable would be a backlog nothing clears.
func TestGetCacheReviewResults_RetitledPendingBookIsIdentityStale(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 60)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	ctx := context.Background()

	before, _ := serveReview(t, h, "all=true")
	var pending, applied string
	for _, r := range before.Data.Results {
		switch {
		case pending == "" && r.Status == "matched":
			pending = r.Book.ID
		case applied == "" && r.Status == "applied":
			applied = r.Book.ID
		}
	}
	require.NotEmpty(t, pending, "the seed holds a pending reviewable book")
	require.NotEmpty(t, applied, "the seed holds an applied reviewable book")
	require.Zero(t, before.Data.UnreviewableByCause["identity_stale"])

	for _, id := range []string{pending, applied} {
		_, err := store.ModifyBook(id, func(b *database.Book) error { b.Title += " (retitled)"; return nil })
		require.NoError(t, err)
		entry, err := store.GetMetadataCache(id)
		require.NoError(t, err)
		require.NotNil(t, entry, "the retitle keeps the row")
		require.True(t, entry.Stale, "the retitle marks the row Stale")
	}

	// The rebuild over the published snapshot: incremental, re-reading only
	// the rows the change logs name.
	require.NoError(t, h.WarmReviewSnapshot(ctx))
	h.reviewSnap.mu.Lock()
	snap := h.reviewSnap.snap
	h.reviewSnap.mu.Unlock()
	require.True(t, snap.incremental, "the rebuild under test is the incremental one")

	after, _ := serveReview(t, h, "all=true")
	for _, r := range after.Data.Results {
		require.NotEqual(t, pending, r.Book.ID, "the retitled pending book is not offered for review")
	}
	require.Equal(t, before.Data.Matched-1, after.Data.Matched)
	require.Equal(t, 1, after.Data.UnreviewableByCause["identity_stale"])
	require.Equal(t, before.Data.Unreviewable+1, after.Data.Unreviewable)

	// The reviewed book stays where its verdict files it.
	require.Equal(t, before.Data.TotalApplied, after.Data.TotalApplied)
	found := false
	for _, r := range after.Data.Results {
		if r.Book.ID == applied {
			found = true
			require.Equal(t, "applied", r.Status)
		}
	}
	require.True(t, found, "the retitled applied book keeps its row in the applied bucket")

	// The unreviewable bucket lists it, with no candidate, and its count is
	// the chip's.
	un, _ := serveReview(t, h, "all=true&bucket=unreviewable")
	listed := 0
	for _, r := range un.Data.Results {
		if r.Status != unreviewableStatusIdentityStale {
			continue
		}
		listed++
		require.Equal(t, pending, r.Book.ID)
		require.Nil(t, r.Candidate, "an identity-stale row serves no candidate")
	}
	require.Equal(t, after.Data.UnreviewableByCause["identity_stale"], listed)
}
