// file: internal/server/handlers/dedup/book_pair_counts_test.go
// version: 1.1.0
// guid: c27f8a15-3e94-4b61-9d0a-5f1e7b3c8d46
// last-edited: 2026-10-12

package deduphandler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type pairCountsResp struct {
	Data struct {
		Clusters []struct {
			Key        string `json:"key"`
			OtherPairs int    `json:"other_pairs"`
			PerBook    []struct {
				BookID string `json:"book_id"`
				Count  int    `json:"count"`
			} `json:"per_book"`
		} `json:"clusters"`
	} `json:"data"`
}

func TestBookPairCounts_OnePassCountsPairsOffThePage(t *testing.T) {
	h, d := newHandler(t)
	// Page: pairs p1 (b1-b2) and p2 (b2-b3). Elsewhere: p3 (b3-b9), p4 (b1-b7)
	// and p5 (b1-b3) -- the last joins two books of the cluster, so it must be
	// counted ONCE for the cluster though it touches two of its books.
	p1, _, _ := insertCandidate(t, d.es, "b1", "b2")
	p2, _, _ := insertCandidate(t, d.es, "b2", "b3")
	insertCandidate(t, d.es, "b3", "b9")
	insertCandidate(t, d.es, "b1", "b7")
	insertCandidate(t, d.es, "b1", "b3")
	insertCandidate(t, d.es, "n1", "n2") // unrelated
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()

	w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{
			"clusters":           []map[string]any{{"key": "b1", "book_ids": []string{"b1", "b2", "b3"}}},
			"page_candidate_ids": []int64{p1, p2},
			"status":             "pending",
		}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	var r pairCountsResp
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(r.Data.Clusters) != 1 || r.Data.Clusters[0].OtherPairs != 3 {
		t.Fatalf("want 3 distinct off-page pairs; body=%s", w.Body.String())
	}
	per := map[string]int{}
	for _, b := range r.Data.Clusters[0].PerBook {
		per[b.BookID] = b.Count
	}
	// b1: p4, p5. b3: p3, p5. b2: none off-page, so omitted.
	if per["b1"] != 2 || per["b3"] != 2 || len(per) != 2 {
		t.Fatalf("per-book counts = %v want b1:2 b3:2; body=%s", per, w.Body.String())
	}
}

func TestBookPairCounts_AnswersEveryClusterInOneRequest(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "b1", "b9")
	insertCandidate(t, d.es, "c1", "c9")
	insertCandidate(t, d.es, "c1", "c8")
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()

	w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{
			"clusters": []map[string]any{
				{"key": "b1", "book_ids": []string{"b1", "b2"}},
				{"key": "c1", "book_ids": []string{"c1", "c2"}},
			},
			"status": "pending",
		}, nil)
	var r pairCountsResp
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if len(r.Data.Clusters) != 2 || r.Data.Clusters[0].OtherPairs != 1 || r.Data.Clusters[1].OtherPairs != 2 {
		t.Fatalf("want [1 2]; body=%s", w.Body.String())
	}
}

func TestBookPairCounts_HonoursTheListFilterAndDeadRows(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "b1", "b8") // embedding layer, pending
	insertCandidateWithBand(t, d.es, "b1", "b7", "HIGH")
	insertCandidate(t, d.es, "b1", "dead")
	d.store.EXPECT().GetBookByID("dead").Return(nil, nil)
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()

	w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{
			"clusters": []map[string]any{{"key": "b1", "book_ids": []string{"b1", "b2"}}},
			"status":   "pending",
			"band":     "HIGH",
		}, nil)
	var r pairCountsResp
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if r.Data.Clusters[0].OtherPairs != 1 {
		t.Fatalf("band filter not applied (or dead row counted); body=%s", w.Body.String())
	}

	w = doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{
			"clusters": []map[string]any{{"key": "b1", "book_ids": []string{"b1", "b2"}}},
			"status":   "pending",
		}, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if r.Data.Clusters[0].OtherPairs != 2 {
		t.Fatalf("unfiltered: want 2 live pairs (the dead-book row excluded); body=%s", w.Body.String())
	}
}

func TestBookPairCounts_BothUnmatchedKeepsOnlyUnmatchedPairs(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "b1", "b8")
	insertCandidate(t, d.es, "b1", "m1")
	asin := "B000000001"
	d.store.EXPECT().GetBookByID("m1").Return(&database.Book{ID: "m1", ASIN: &asin}, nil)
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()

	w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{
			"clusters":       []map[string]any{{"key": "b1", "book_ids": []string{"b1", "b2"}}},
			"status":         "pending",
			"both_unmatched": true,
		}, nil)
	var r pairCountsResp
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if r.Data.Clusters[0].OtherPairs != 1 {
		t.Fatalf("want only the both-unmatched pair; body=%s", w.Body.String())
	}
}

func TestBookPairCounts_RefusesOversizedAndMalformedRequests(t *testing.T) {
	h, _ := newHandler(t)
	ids := make([]int64, 2001)
	w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{"clusters": []any{}, "page_candidate_ids": ids}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized page ids: status=%d want 400", w.Code)
	}
	w = w2rawReq(t, h.BookPairCounts, "/api/v1/dedup/candidates/book-pair-counts", `{"clusters":`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status=%d want 400", w.Code)
	}
}

func TestBookPairCounts_NoClustersIsEmptyNotAScan(t *testing.T) {
	h, _ := newHandler(t)
	w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
		map[string]any{"clusters": []any{}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestBookPairCounts_EntityIDScopesTheCountsLikeTheList(t *testing.T) {
	// Under the ?book= deep link the list shows only that book's pairs, so the
	// "pairs on other pages" count must too.
	h, d := newHandler(t)
	insertCandidate(t, d.es, "b1", "b8")
	insertCandidate(t, d.es, "b2", "b9") // in the cluster, but not the deep-linked book's
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()

	count := func(entityID string) int {
		w := doReq(t, h.BookPairCounts, http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts",
			map[string]any{
				"clusters":  []map[string]any{{"key": "b1", "book_ids": []string{"b1", "b2"}}},
				"status":    "pending",
				"entity_id": entityID,
			}, nil)
		var r pairCountsResp
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		return r.Data.Clusters[0].OtherPairs
	}
	if got := count("b1"); got != 1 {
		t.Fatalf("scoped to b1: got %d want 1", got)
	}
	if got := count(""); got != 2 {
		t.Fatalf("unscoped: got %d want 2", got)
	}
}
