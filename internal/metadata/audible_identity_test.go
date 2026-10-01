// file: internal/metadata/audible_identity_test.go
// version: 1.0.0
// guid: 5d0b7e23-9c41-4a8f-b6e2-7f13a9c4d861
// last-edited: 2026-10-01

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The JSON below is the shape api.audible.com returned for
// title=Red Rising&author=Pierce Brown with product_details requested
// (probed 2026-10-01): isbn sits at the product's top level.
const audibleIdentityFixture = `{"total_results":1,"products":[{
  "asin":"B00I2VWW5U","title":"Red Rising","subtitle":"",
  "authors":[{"asin":"B00G9C8OQU","name":"Pierce Brown"}],
  "narrators":[{"name":"Tim Gerard Reynolds"}],
  "series":[{"asin":"B07PGF9FWW","title":"Red Rising","sequence":"1"}],
  "runtime_length_min":972,"format_type":"unabridged","isbn":"9781470380281"}]}`

func TestAudibleSearchIdentities(t *testing.T) {
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(audibleIdentityFixture))
	}))
	defer srv.Close()

	c := NewAudibleClientWithBaseURL(srv.URL)
	got, err := c.SearchIdentities(context.Background(), AudibleIdentityQuery{Title: "Red Rising", Author: "Pierce Brown"})
	if err != nil {
		t.Fatalf("SearchIdentities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d products, want 1", len(got))
	}
	p := got[0]
	if p.ASIN != "B00I2VWW5U" || p.Title != "Red Rising" || p.ISBN != "9781470380281" || p.RuntimeMin != 972 {
		t.Fatalf("decoded %+v", p)
	}
	if len(p.Authors) != 1 || p.Authors[0] != "Pierce Brown" {
		t.Fatalf("authors = %v", p.Authors)
	}
	if len(p.Series) != 1 || p.Series[0].Title != "Red Rising" || p.Series[0].Sequence != "1" {
		t.Fatalf("series = %v", p.Series)
	}
	if g := gotQuery["response_groups"]; len(g) != 1 || g[0] != audibleIdentityResponseGroups {
		t.Fatalf("response_groups = %v, want %q (product_details carries the ISBN)", g, audibleIdentityResponseGroups)
	}
	if gotQuery["title"][0] != "Red Rising" || gotQuery["author"][0] != "Pierce Brown" || gotQuery["num_results"][0] != "20" {
		t.Fatalf("query = %v", gotQuery)
	}
	if _, ok := gotQuery["keywords"]; ok {
		t.Fatalf("keywords sent although empty: %v", gotQuery)
	}
}

func TestAudibleSearchIdentities_EmptyQueryRefused(t *testing.T) {
	c := NewAudibleClientWithBaseURL("http://127.0.0.1:1")
	if _, err := c.SearchIdentities(context.Background(), AudibleIdentityQuery{}); err == nil {
		t.Fatal("empty query did not error")
	}
}

func TestAudibleSearchIdentities_HTTPErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewAudibleClientWithBaseURL(srv.URL)
	if _, err := c.SearchIdentities(context.Background(), AudibleIdentityQuery{Keywords: "9781470380281"}); err == nil {
		t.Fatal("HTTP 500 did not error")
	}
}
