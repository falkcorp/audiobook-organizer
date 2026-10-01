// file: internal/server/metadata_results_order_test.go
// version: 1.0.0
// guid: 3f8e2a71-6c4d-4b19-a5e0-9d7b1c2f4e86
// last-edited: 2026-10-01

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// GET /library/metadata-results built its list from a map, so every request
// came back in a new random order and offset paging returned an arbitrary
// slice: on prod, 12 pages of 1,000 "matched" rows held only 10,350 distinct
// books out of 42,748. Paging through the whole set must visit every book
// exactly once, in the same order on every request.
func TestListMetadataResults_PagesVisitEveryBookOnce(t *testing.T) {
	resetMetadataResultsCache(t)
	gin.SetMode(gin.TestMode)

	const n = 257
	latest := make(map[string]database.OperationResult, n)
	for i := range n {
		id := fmt.Sprintf("book-%03d", i)
		latest[id] = database.OperationResult{BookID: id, Status: "matched", CreatedAt: time.Unix(int64(i), 0)}
	}
	primeMetadataResultsCache(latest, map[string]int{"matched": n}, time.Now())

	s := &Server{}
	page := func(offset int) []string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/library/metadata-results?status=matched&limit=50&offset=%d", offset), nil)
		s.handleListMetadataResults(c)
		if w.Code != http.StatusOK {
			t.Fatalf("offset %d: status %d: %s", offset, w.Code, w.Body.String())
		}
		var body struct {
			Data struct {
				Items []struct {
					BookID string `json:"book_id"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		ids := make([]string, len(body.Data.Items))
		for i, it := range body.Data.Items {
			ids[i] = it.BookID
		}
		return ids
	}

	walk := func() []string {
		var all []string
		for off := 0; off < n; off += 50 {
			all = append(all, page(off)...)
		}
		return all
	}
	first := walk()
	seen := map[string]bool{}
	for i, id := range first {
		if seen[id] {
			t.Fatalf("book %s returned twice while paging", id)
		}
		seen[id] = true
		if i > 0 && first[i-1] >= id {
			t.Fatalf("not in book-ID order at %d: %s then %s", i, first[i-1], id)
		}
	}
	if len(seen) != n {
		t.Fatalf("paging visited %d of %d books", len(seen), n)
	}
	// Map order is randomised per iteration; a second walk must agree.
	for i, id := range walk() {
		if id != first[i] {
			t.Fatalf("second walk differs at %d: %s vs %s", i, id, first[i])
		}
	}
}
