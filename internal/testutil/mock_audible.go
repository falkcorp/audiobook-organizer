// file: internal/testutil/mock_audible.go
// version: 1.0.0
// guid: 396d20d7-2e5f-429d-9742-10b0c3806fbb
// last-edited: 2026-10-06

package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// AudibleTestProduct is one synthetic Audible catalog product for
// MockAudibleServer, in the fields the Audible client maps.
type AudibleTestProduct struct {
	ASIN        string
	Title       string
	Authors     []string
	Narrators   []string
	Publisher   string
	Language    string
	ReleaseDate string // "YYYY-MM-DD": the audiobook release year
	Summary     string
	RuntimeMin  int
}

// MockAudibleServer mimics Audible's catalog search (GET /catalog/products).
// answer returns the products for the request's title query (nil = none).
//
// Integration tests that need an auto-fetch to APPLY a match use it: Open
// Library and Google Books are review-only sources (owner decision
// 2026-10-06), so their matches are fetched for review and never applied by
// FetchMetadataForBook, FetchMetadataForBookByTitle or the bulk fetch.
func MockAudibleServer(t *testing.T, answer func(title string) []AudibleTestProduct) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/catalog/products") {
			http.NotFound(w, r)
			return
		}
		var products []map[string]any
		for _, p := range answer(r.URL.Query().Get("title")) {
			people := func(names []string) []map[string]string {
				out := make([]map[string]string, 0, len(names))
				for _, n := range names {
					out = append(out, map[string]string{"name": n})
				}
				return out
			}
			prod := map[string]any{
				"asin": p.ASIN, "title": p.Title, "authors": people(p.Authors), "narrators": people(p.Narrators),
				"publisher_name": p.Publisher, "language": p.Language, "release_date": p.ReleaseDate,
				"merchandising_summary": p.Summary,
			}
			if p.RuntimeMin > 0 {
				prod["runtime_length_min"] = p.RuntimeMin
			}
			products = append(products, prod)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"products": products, "total_results": len(products)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// AudibleHobbitProduct is the synthetic Audible match the integration tests
// use for "The Hobbit".
var AudibleHobbitProduct = AudibleTestProduct{
	ASIN: "B0TESTHOB1", Title: "The Hobbit", Authors: []string{"J.R.R. Tolkien"},
	Publisher: "Houghton Mifflin", Language: "eng", ReleaseDate: "2012-09-21",
}
