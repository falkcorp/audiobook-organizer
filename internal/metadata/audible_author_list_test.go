// file: internal/metadata/audible_author_list_test.go
// version: 1.0.0
// guid: 24e988cf-2c53-4950-81d6-f7907a14f619
// last-edited: 2026-10-01

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListByAuthor_SkipsUndecodableProduct: one malformed product must not
// fail the page. It is skipped and counted, so a caller can still add it to
// the products received and reach total_results.
func TestListByAuthor_SkipsUndecodableProduct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_results":3,"products":[
			{"asin":"A1","title":"One","language":"english"},
			{"asin":42,"title":"Bad"},
			{"asin":"A3","title":"Three","language":"english"}]}`))
	}))
	t.Cleanup(srv.Close)
	pg, err := NewAudibleClientWithBaseURL(srv.URL).ListByAuthor(context.Background(), "Some Author", 0, 50)
	if err != nil {
		t.Fatalf("one bad product failed the page: %v", err)
	}
	if len(pg.Products) != 2 || pg.Skipped != 1 || pg.TotalResults != 3 {
		t.Fatalf("page = %d products, %d skipped, total %d; want 2, 1, 3", len(pg.Products), pg.Skipped, pg.TotalResults)
	}
	if pg.Products[0].ASIN != "A1" || pg.Products[1].ASIN != "A3" {
		t.Errorf("kept products %q, %q; want A1, A3", pg.Products[0].ASIN, pg.Products[1].ASIN)
	}
}
