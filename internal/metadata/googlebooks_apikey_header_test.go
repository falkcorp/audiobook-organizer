// file: internal/metadata/googlebooks_apikey_header_test.go
// version: 1.0.0
// guid: ba8660df-fc36-41ee-b9d4-45791c58a66b
// last-edited: 2026-10-30

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The API key must travel in a header: a URL query is printed by *url.Error and
// by every log line that formats the request.
func TestGoogleBooks_APIKeyIsSentAsHeaderNotQuery(t *testing.T) {
	var gotQuery, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotHeader = r.Header.Get("X-Goog-Api-Key")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	c := &GoogleBooksClient{httpClient: srv.Client(), baseURL: srv.URL, apiKey: "SECRETKEY"}
	if _, err := c.SearchByTitle(context.Background(), "Year One"); err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if gotHeader != "SECRETKEY" {
		t.Fatalf("X-Goog-Api-Key = %q, want the key", gotHeader)
	}
	if u, _ := http.NewRequest(http.MethodGet, "http://x/?"+gotQuery, nil); u.URL.Query().Has("key") {
		t.Fatalf("the key leaked into the query: %q", gotQuery)
	}
}

func TestGoogleBooks_NoKeyNoHeader(t *testing.T) {
	var gotHeader []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Values("X-Goog-Api-Key")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	c := &GoogleBooksClient{httpClient: srv.Client(), baseURL: srv.URL}
	if _, err := c.SearchByTitle(context.Background(), "Year One"); err != nil {
		t.Fatal(err)
	}
	if len(gotHeader) != 0 {
		t.Fatalf("header sent without a key: %v", gotHeader)
	}
}
