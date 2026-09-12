// file: internal/server/handlers/metadata_cache_buckets_test.go
// version: 1.1.0
// guid: 9e04b3d7-6c81-4a25-b3f0-72d9a1c86e53
// last-edited: 2026-09-12

// The review rail's chips were reporting a different library than the one the
// reviewer was looking at. Four separate defects, all visible in one screenshot
// on 2026-09-08 ("11 stale", "11375 unreviewable"):
//
//   - `stale` was counted inside the REVIEWABLE loop, so every stale row with no
//     candidates -- exactly the rows a refetch would help -- could not be
//     counted. The chip read 11 while 2,658 such rows sat in the unreviewable
//     bucket, understating the backlog 242x.
//   - Books already ruled on but holding no candidate were filed as
//     "unreviewable", reporting settled work as a permanent backlog.
//   - `audio_confirmed` was missing from the status switch, so books confirmed
//     against their own transcribed audio were reported as awaiting review.
//   - Staleness was dated from when CANDIDATES were stored rather than when the
//     book was last SEARCHED FOR, so a book whose providers came back empty an
//     hour ago stayed permanently overdue and was re-picked forever.
//
// These are the numbers the user reads off the screen, so they are asserted
// directly against the payload rather than through any intermediate helper.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// reviewBucketsBody is the slice of the payload these four claims live in.
type reviewBucketsBody struct {
	Data struct {
		Results      []map[string]any `json:"results"`
		TotalCount   int              `json:"total_count"`
		Matched      int              `json:"matched"`
		NoMatch      int              `json:"no_match"`
		TotalApplied int              `json:"total_applied"`
		Errors       int              `json:"errors"`
		Stale        int              `json:"stale"`
		Unreviewable int              `json:"unreviewable"`
		ByCause      struct {
			Orphaned     int `json:"orphaned"`
			NoCandidates int `json:"no_candidates"`
			DecodeErrors int `json:"decode_errors"`
		} `json:"unreviewable_by_cause"`
		ResolvedNoCandidates int `json:"resolved_no_candidates"`
	} `json:"data"`
}

func TestGetCacheReviewResults_BucketsAndStaleness(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)

	now := time.Now()
	// Comfortably past MetadataCacheTTL (30 days), so there is no boundary race.
	old := now.Add(-2 * database.MetadataCacheTTL)

	strptr := func(s string) *string { return &s }

	// Five books, one per behaviour under test.
	summaries := []metafetch.MetadataCacheSummary{
		{BookID: "pending-fresh", FetchedAt: now},
		{BookID: "audio-confirmed", FetchedAt: now},
		{BookID: "resolved-empty", FetchedAt: now},
		{BookID: "stale-empty", FetchedAt: old},
		{BookID: "preserved", FetchedAt: old},
	}
	svc.EXPECT().ListCachedSummaries(mock.Anything).Return(summaries, nil)

	store.EXPECT().GetBooksByIDs(mock.Anything).Return([]database.Book{
		// Nobody has ruled on this one: status defaults to "matched", which
		// means PENDING review, not reviewed.
		{ID: "pending-fresh"},
		// Confirmed against the book's own transcribed audio by
		// metafetch/service_apply.go. A verdict, not a pending row.
		{ID: "audio-confirmed", MetadataReviewStatus: strptr("audio_confirmed")},
		// Ruled on, but its candidates are gone -- the shape an empty refetch
		// used to produce before metafetch.cacheSearchResponse stopped
		// overwriting on empty.
		{ID: "resolved-empty", MetadataReviewStatus: strptr("matched")},
		// Never ruled on, no candidates, and long past the TTL. This is the row
		// the stale chip could not see.
		{ID: "stale-empty"},
		// Has candidates from a fetch two months ago, but the last SEARCH was
		// just now and came back empty, so the candidates were preserved.
		{ID: "preserved"},
	}, nil)
	store.EXPECT().GetBookByID(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()

	raw, err := json.Marshal(map[string]any{"title": "T"})
	require.NoError(t, err)
	withCandidate := func() *metafetch.MetadataCandidateCache {
		return &metafetch.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}
	}

	svc.EXPECT().GetCachedCandidates("pending-fresh").Return(withCandidate(), true, nil)
	svc.EXPECT().GetCachedCandidates("audio-confirmed").Return(withCandidate(), true, nil)
	svc.EXPECT().GetCachedCandidates("resolved-empty").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	svc.EXPECT().GetCachedCandidates("stale-empty").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	preserved := withCandidate()
	preserved.LastEmptyFetchAt = &now
	svc.EXPECT().GetCachedCandidates("preserved").Return(preserved, true, nil)

	h := handlers.NewMetadataCacheHandler(store, svc, nil, nil, nil, nil)
	c, w := reviewCtx("limit=0&offset=0")
	h.GetCacheReviewResults(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body reviewBucketsBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	// --- Claim 1: audio_confirmed is a verdict, not a pending row. ----------
	// The three reviewable rows are pending-fresh, audio-confirmed, preserved.
	assert.Len(t, body.Data.Results, 3)
	assert.Equal(t, 3, body.Data.TotalCount)
	assert.Equal(t, 1, body.Data.TotalApplied, "audio_confirmed must count as applied, not as awaiting review")
	assert.Equal(t, 2, body.Data.Matched, "only pending-fresh and preserved are still awaiting a verdict")

	// --- Claim 2: already-resolved rows leave the unreviewable bucket. ------
	assert.Equal(t, 1, body.Data.ResolvedNoCandidates, "resolved-empty has a verdict and nothing to show")
	assert.Equal(t, 1, body.Data.ByCause.NoCandidates, "only stale-empty is an actual refetch candidate")
	assert.Equal(t, 0, body.Data.ByCause.Orphaned)
	assert.Equal(t, 0, body.Data.ByCause.DecodeErrors)
	assert.Equal(t, 1, body.Data.Unreviewable,
		"resolved-empty must NOT be counted here: it is settled work, not a backlog")

	// --- Claim 3: stale is counted outside the reviewable set. --------------
	// stale-empty is past the TTL and has no candidates. Counted inside the
	// reviewable loop -- which is what this did before -- it was invisible.
	assert.Equal(t, 1, body.Data.Stale,
		"a stale row with no candidates must still be counted; it is exactly what a refetch would fix")

	// --- Claim 4: staleness is dated from the last SEARCH, not the last ----
	// --- successful fetch. -------------------------------------------------
	// `preserved` carries candidates from two months ago but was searched for
	// just now, and the providers had nothing. Refetching it again today would
	// change nothing, so it must not be reported as overdue -- otherwise every
	// stale pass picks the same unmatchable books forever, which is what "we
	// pressed the stale button yesterday and they are still stale" looked like.
	var preservedRow map[string]any
	for _, r := range body.Data.Results {
		if book, ok := r["book"].(map[string]any); ok && book["id"] == "preserved" {
			preservedRow = r
		}
	}
	require.NotNil(t, preservedRow, "the preserved row must be returned")
	assert.Equal(t, true, preservedRow["is_fresh"],
		"a book searched for an hour ago is not one a Refresh would improve")

	// The summary and the per-row flag must agree: `preserved` is the only row
	// where FetchedAt and last-checked diverge, so it is the one that would
	// expose a predicate used in one place but not the other.
	assert.Equal(t, 1, body.Data.Stale, "the summary must use the same predicate as is_fresh")
}
