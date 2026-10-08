// file: internal/server/batch_apply_review_snapshot_pin_test.go
// version: 1.1.0
// guid: 2b7d4e91-6c05-4a38-9f1e-7d3a8c52e0b4
// last-edited: 2026-10-02

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// The review page serves its rows from a snapshot that can be one rebuild
// behind the cache (stale-while-revalidate). An Apply clicked on such a row
// pins the candidate and hash the snapshot served; the apply recomputes from
// the cache as it is now, so a pin from a stale snapshot is refused and the
// book is not written.
func TestApply_PinFromAStaleReviewSnapshotIsRefused(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	tenHours := 36000
	book, err := store.CreateBook(&database.Book{Title: "Big Cats 1", FilePath: "/lib/A/Big Cats/Big Cats 1.m4b", Format: "m4b", Duration: &tenHours})
	require.NoError(t, err)

	shownCand := metafetch.MetadataCandidate{Title: "Big Cats", Author: "Ann Author", Source: "Google Books", ISBN13: "9780000000001", Score: 0.95, Description: "long"}
	newCand := metafetch.MetadataCandidate{Title: "Big Cats: Revised", Author: "Ann Author", Source: "Google Books", ISBN13: "9780000000002", Score: 0.95}
	put := func(c metafetch.MetadataCandidate) {
		raw, merr := json.Marshal(c)
		require.NoError(t, merr)
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: book.ID, Candidates: []json.RawMessage{raw}, FetchedAt: time.Now(),
			SearchFingerprint: metafetch.FingerprintPrefix + "x",
		}))
	}
	put(shownCand)

	h := handlers.NewMetadataCacheHandler(store, metafetch.NewService(store), nil, nil, nil)
	serve := func() metabatch.CandidateResult {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/x?all=true&view=index", nil)
		h.GetCacheReviewResults(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body struct {
			Data struct {
				Results []metabatch.CandidateResult `json:"results"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Len(t, body.Data.Results, 1)
		return body.Data.Results[0]
	}
	serve() // builds the snapshot

	// The cache moves on; the very next request is still served the old
	// snapshot (its rebuild runs in the background).
	put(newCand)
	row := serve()
	require.Equal(t, "Big Cats", row.Candidate.Title, "served from the stale snapshot")
	require.Equal(t, metafetch.CandidateHash(shownCand), row.CandidateHash)

	// What the lane sends for that row: the shown candidate and the served hash.
	pin := metafetch.PinOf(*row.Candidate)
	pin.ContentHash = row.CandidateHash
	pin.Origin = metafetch.PinOriginRow

	svc := &fakeApplySvc{candidates: candidateJSON(t, newCand)}
	books := fakeBooks{book.ID: book}
	out := applyCachedCandidateForBookTimed(svc, books, book.ID, true, nil, metafetch.NewApplyPhaseTimings(), nil, &pin, "")
	require.False(t, out.Applied)
	require.Equal(t, applySkipStaleCandidate, out.Reason)
	require.Empty(t, svc.appliedIDs)

	// No rebuild started: the first build began inside the snapshot's
	// minimum rebuild interval, so the page keeps serving it -- which is
	// exactly the window this pin check exists for.
}
