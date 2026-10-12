// file: internal/server/handlers/dedup/book_pair_counts.go
// version: 1.2.0
// guid: 5b2e9c47-1d83-4a06-b7f5-8c3d0e6a1f92
// last-edited: 2026-10-12

package deduphandler

import (
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

const (
	// maxPairCountClusters / maxPairCountBooks bound one request. The Clusters
	// view sends one page (at most a few hundred books); anything wider is a
	// client bug, not a bigger page.
	maxPairCountClusters = 500
	maxPairCountBooks    = 2000
	maxPairCountPageIDs  = 2000
)

type pairCountCluster struct {
	Key     string   `json:"key"`
	BookIDs []string `json:"book_ids"`
}

type pairCountBook struct {
	BookID string `json:"book_id"`
	Count  int    `json:"count"`
}

type pairCountResult struct {
	Key string `json:"key"`
	// OtherPairs is the number of DISTINCT pairs that touch at least one book of
	// the cluster, match the list's filter, and are not on the page.
	OtherPairs int `json:"other_pairs"`
	// PerBook is each cluster book's own count of such pairs (books with none
	// are omitted). A pair joining two books of the cluster is counted once in
	// OtherPairs and once for each of its two books here.
	PerBook []pairCountBook `json:"per_book"`
}

// BookPairCounts handles POST /api/v1/dedup/candidates/book-pair-counts.
//
// The Clusters view's "N more pairs for these books are on other pages": for
// every cluster on the loaded page, how many pairs its books have that the
// list's filter matches but the page does not show. One candidate scan answers
// for the whole page; the view used to ask once per book (about 200 requests
// per page, repeated on every refresh).
//
// Body:
//
//	{
//	  "clusters": [{"key": "...", "book_ids": ["...", ...]}],
//	  "page_candidate_ids": [1, 2, ...],     // the pairs ON the page: excluded
//	  "status": "pending", "layer": "", "band": "", "q": "", "entity_id": "",
//	  "both_unmatched": false                // the list's own filter
//	}
//
// The filter fields mean what they mean on the list endpoint, and dead-book
// rows are excluded by the same rule (isDeadBookRow), so a count here equals
// the rows the list would show for those books.
func (h *Handler) BookPairCounts(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	var body struct {
		Clusters         []pairCountCluster `json:"clusters"`
		PageCandidateIDs []int64            `json:"page_candidate_ids"`
		Status           string             `json:"status"`
		Layer            string             `json:"layer"`
		Band             string             `json:"band"`
		Q                string             `json:"q"`
		// EntityID is the list's ?book= deep link: when set the counts are of
		// pairs touching that book too, so they match the scoped list.
		EntityID string `json:"entity_id"`
		// SeriesID is the list's series_id (the By-series view), as a string like
		// the query parameter it stands in for.
		SeriesID      string `json:"series_id"`
		BothUnmatched bool   `json:"both_unmatched"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if len(body.Clusters) > maxPairCountClusters || len(body.PageCandidateIDs) > maxPairCountPageIDs {
		httputil.RespondWithBadRequest(c, "too many clusters or page_candidate_ids in one request")
		return
	}

	// Which clusters each book belongs to. A book can sit in two clusters only
	// if the caller sent it twice; both are answered.
	clustersOf := make(map[string][]int)
	books := 0
	for i, cl := range body.Clusters {
		for _, id := range cl.BookIDs {
			if id == "" {
				continue
			}
			clustersOf[id] = append(clustersOf[id], i)
			books++
		}
	}
	if books > maxPairCountBooks {
		httputil.RespondWithBadRequest(c, "too many book_ids in one request")
		return
	}

	results := make([]pairCountResult, len(body.Clusters))
	for i, cl := range body.Clusters {
		results[i] = pairCountResult{Key: cl.Key, PerBook: []pairCountBook{}}
	}
	if len(clustersOf) == 0 {
		httputil.RespondWithOK(c, gin.H{"clusters": results})
		return
	}

	// The list's own filter, parsed by the list's own function, so a count here
	// means what the same parameter means there (including series_id and q).
	params := map[string]string{
		"entity_type": "book",
		"status":      body.Status,
		"layer":       body.Layer,
		"band":        body.Band,
		"q":           body.Q,
		"entity_id":   body.EntityID,
		"series_id":   body.SeriesID,
	}
	filter, ok := h.candidateFilterFromParams(c, func(k string) string { return params[k] })
	if !ok {
		return
	}
	filter.Limit = bothUnmatchedScanLimit
	candidates, _, err := es.ListCandidates(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list dedup candidates", err)
		return
	}

	onPage := make(map[int64]struct{}, len(body.PageCandidateIDs))
	for _, id := range body.PageCandidateIDs {
		onPage[id] = struct{}{}
	}
	lookup := newBookLookup(h.store, 0)
	distinct := make([]map[int64]struct{}, len(body.Clusters))
	perBook := make([]map[string]int, len(body.Clusters))
	for i := range distinct {
		distinct[i] = map[int64]struct{}{}
		perBook[i] = map[string]int{}
	}

	for _, cand := range candidates {
		if _, ok := onPage[cand.ID]; ok {
			continue
		}
		aClusters, aIn := clustersOf[cand.EntityAID]
		bClusters, bIn := clustersOf[cand.EntityBID]
		if !aIn && !bIn {
			continue
		}
		// Only pairs that touch a requested book pay for book reads.
		dead, derr := isDeadBookRow(cand, lookup.get)
		if derr != nil {
			httputil.InternalError(c, "failed to read a candidate's book", derr)
			return
		}
		if dead {
			continue
		}
		if body.BothUnmatched {
			keep, kerr := isBothUnmatchedPair(cand, lookup.get)
			if kerr != nil {
				httputil.InternalError(c, "failed to read a candidate's book", kerr)
				return
			}
			if !keep {
				continue
			}
		}
		if aIn {
			for _, ci := range aClusters {
				distinct[ci][cand.ID] = struct{}{}
				perBook[ci][cand.EntityAID]++
			}
		}
		if bIn {
			for _, ci := range bClusters {
				distinct[ci][cand.ID] = struct{}{}
				perBook[ci][cand.EntityBID]++
			}
		}
	}

	for i, cl := range body.Clusters {
		results[i].OtherPairs = len(distinct[i])
		for _, id := range cl.BookIDs {
			if n := perBook[i][id]; n > 0 {
				results[i].PerBook = append(results[i].PerBook, pairCountBook{BookID: id, Count: n})
				delete(perBook[i], id) // a repeated id is listed once
			}
		}
	}
	httputil.RespondWithOK(c, gin.H{"clusters": results})
}
