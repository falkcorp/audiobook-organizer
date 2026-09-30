// file: internal/server/handlers/metadata_cache_stale_test.go
// version: 1.1.0
// guid: bd448962-829a-45be-aa5f-86e10c847ff3
// last-edited: 2026-09-30

// The review rail's `stale` count and the refetch-all-stale set
// (StaleCachedBookIDs, behind POST batch-fetch-candidates {stale:true}) must be
// the same set. They were not on 2026-09-30: the chip read "3,511 stale" and
// the button refetched the 10 stale rows the client could see.
package handlers_test

import (
	"context"
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

func TestStaleCachedBookIDs_MatchesTheSummaryStaleCount(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)

	now := time.Now()
	old := now.Add(-2 * database.MetadataCacheTTL)
	recent := now.Add(-time.Hour)
	strptr := func(s string) *string { return &s }

	svc.EXPECT().ListCachedSummaries(mock.Anything).Return([]metafetch.MetadataCacheSummary{
		{BookID: "rev-stale", FetchedAt: old},             // stale, reviewable: IN
		{BookID: "rev-fresh", FetchedAt: now},             // fresh: OUT
		{BookID: "applied-stale", FetchedAt: old},         // stale, reviewable, audio_confirmed: IN
		{BookID: "empty-stale", FetchedAt: old},           // stale, no_candidates: IN
		{BookID: "nocache-stale", FetchedAt: old},         // stale, entry unreadable: dated off summary: IN
		{BookID: "resolved-stale", FetchedAt: old},        // stale, resolved_no_candidates (matched): IN
		{BookID: "broken-stale", FetchedAt: old},          // stale, decode error: IN
		{BookID: "rejected-stale", FetchedAt: old},        // stale but owner-marked no_match: OUT
		{BookID: "rejected-empty-stale", FetchedAt: old},  // resolved_no_candidates via no_match: OUT
		{BookID: "empty-looked-recently", FetchedAt: old}, // old candidates, recent empty search: OUT
		{BookID: "gone-stale", FetchedAt: old},            // orphaned: OUT
		{BookID: "untitled-stale", FetchedAt: old},        // stale, but no usable search title: OUT
	}, nil)

	store.EXPECT().GetBooksByIDs(mock.Anything).Return([]database.Book{
		{ID: "rev-stale", Title: "A"},
		{ID: "rev-fresh", Title: "B"},
		{ID: "applied-stale", Title: "C", MetadataReviewStatus: strptr("audio_confirmed")},
		{ID: "empty-stale", Title: "D"},
		{ID: "nocache-stale", Title: "E"},
		{ID: "resolved-stale", Title: "F", MetadataReviewStatus: strptr("matched")},
		{ID: "broken-stale", Title: "G"},
		{ID: "rejected-stale", Title: "H", MetadataReviewStatus: strptr("no_match")},
		{ID: "rejected-empty-stale", Title: "I", MetadataReviewStatus: strptr("no_match")},
		{ID: "empty-looked-recently", Title: "J"},
		// Empty title, no transcription, no path: the fetch skips it with
		// "no usable title", so it must not be counted stale.
		{ID: "untitled-stale", Title: ""},
	}, nil)
	store.EXPECT().GetBookByID("gone-stale").Return(nil, nil)

	raw, err := json.Marshal(map[string]any{"title": "T", "score": 0.9})
	require.NoError(t, err)
	with := func() *metafetch.MetadataCandidateCache {
		return &metafetch.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}
	}
	svc.EXPECT().GetCachedCandidates("rev-stale").Return(with(), true, nil)
	svc.EXPECT().GetCachedCandidates("rev-fresh").Return(with(), true, nil)
	svc.EXPECT().GetCachedCandidates("applied-stale").Return(with(), true, nil)
	svc.EXPECT().GetCachedCandidates("empty-stale").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	svc.EXPECT().GetCachedCandidates("nocache-stale").Return(nil, false, nil)
	svc.EXPECT().GetCachedCandidates("resolved-stale").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	svc.EXPECT().GetCachedCandidates("broken-stale").Return(&metafetch.MetadataCandidateCache{
		Candidates: []json.RawMessage{json.RawMessage(`"not an object"`)},
	}, true, nil)
	svc.EXPECT().GetCachedCandidates("rejected-stale").Return(with(), true, nil)
	svc.EXPECT().GetCachedCandidates("rejected-empty-stale").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	svc.EXPECT().GetCachedCandidates("untitled-stale").Return(with(), true, nil)
	store.EXPECT().GetBookAuthors(mock.Anything).Return(nil, nil).Maybe()
	svc.EXPECT().GetCachedCandidates("empty-looked-recently").Return(&metafetch.MetadataCandidateCache{
		Candidates: []json.RawMessage{raw}, LastEmptyFetchAt: &recent,
	}, true, nil)

	ids, err := handlers.StaleCachedBookIDs(context.Background(), store, svc)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"rev-stale", "applied-stale", "empty-stale", "nocache-stale", "resolved-stale", "broken-stale",
	}, ids)

	// The count the rail shows, from the same fixture.
	h := handlers.NewMetadataCacheHandler(store, svc, nil, nil, nil, nil)
	c, w := reviewCtx("all=true")
	h.GetCacheReviewResults(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Stale                int `json:"stale"`
			ResolvedNoCandidates int `json:"resolved_no_candidates"`
			ByCause              struct {
				Orphaned     int `json:"orphaned"`
				NoCandidates int `json:"no_candidates"`
				DecodeErrors int `json:"decode_errors"`
			} `json:"unreviewable_by_cause"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, len(ids), body.Data.Stale, "the chip's count must be the size of the set the button refetches")

	// The fixture reaches every bucket the resolver must cover, so the equality
	// above is not two zeros agreeing.
	assert.Equal(t, 1, body.Data.ByCause.Orphaned)
	assert.Equal(t, 2, body.Data.ByCause.NoCandidates)
	assert.Equal(t, 1, body.Data.ByCause.DecodeErrors)
	assert.Equal(t, 2, body.Data.ResolvedNoCandidates)

	// Every row's `stale` flag is the same predicate: across both buckets,
	// exactly the rows in the set say stale:true, so the web's stale chip view
	// (which only reads the flag) shows the set the count describes.
	flagged := map[string]bool{}
	for _, q := range []string{"all=true", "all=true&bucket=unreviewable"} {
		c, w := reviewCtx(q)
		h.GetCacheReviewResults(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var rows struct {
			Data struct {
				Results []struct {
					Book struct {
						ID string `json:"id"`
					} `json:"book"`
					Stale *bool `json:"stale"`
				} `json:"results"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
		for _, r := range rows.Data.Results {
			require.NotNil(t, r.Stale, "row %s carries no stale flag", r.Book.ID)
			if *r.Stale {
				flagged[r.Book.ID] = true
			}
		}
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	assert.Equal(t, want, flagged, "per-row stale flags must name exactly the refetch-all-stale set")
}
