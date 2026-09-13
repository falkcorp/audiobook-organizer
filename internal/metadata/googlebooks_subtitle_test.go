// file: internal/metadata/googlebooks_subtitle_test.go
// version: 1.0.0
// guid: 7b2e4c19-3f5a-4d8e-a061-9c4d2b7e5f13
// last-edited: 2026-09-13

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// starWarsANewDawnFixture is how Google Books actually files John Jackson
// Miller's "A New Dawn": the franchise is the title, the book is the subtitle.
const starWarsANewDawnFixture = `{
	"totalItems": 1,
	"items": [{
		"volumeInfo": {
			"title": "Star Wars",
			"subtitle": "A New Dawn",
			"authors": ["John Jackson Miller"],
			"publishedDate": "2014-09-02",
			"language": "en"
		}
	}]
}`

// TestGoogleBooksClient_SubtitleFoldedIntoTitle locks the fix for candidates
// that came out titled just "Star Wars": the subtitle must be carried, and the
// title the scorer and review UI see must name the real book.
func TestGoogleBooksClient_SubtitleFoldedIntoTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(starWarsANewDawnFixture))
	}))
	defer server.Close()

	results, err := NewGoogleBooksClientWithBaseURL(server.URL).SearchByTitle(context.Background(), "A New Dawn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Subtitle != "A New Dawn" {
		t.Errorf("Subtitle = %q, want %q", r.Subtitle, "A New Dawn")
	}
	if r.Title != "Star Wars: A New Dawn" {
		t.Errorf("Title = %q, want %q", r.Title, "Star Wars: A New Dawn")
	}
}

func TestGoogleBooksFullTitle(t *testing.T) {
	tests := []struct {
		name, title, subtitle, want string
	}{
		{"no subtitle", "The Hobbit", "", "The Hobbit"},
		{"franchise title", "Star Wars", "A New Dawn", "Star Wars: A New Dawn"},
		{"whitespace trimmed", " Star Wars ", " A New Dawn ", "Star Wars: A New Dawn"},
		{"subtitle already in title", "Star Wars: A New Dawn", "A New Dawn", "Star Wars: A New Dawn"},
		{"identical, case-insensitive", "Dune", "dune", "Dune"},
		{"empty title", "", "A New Dawn", "A New Dawn"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := googleBooksFullTitle(tc.title, tc.subtitle); got != tc.want {
				t.Errorf("googleBooksFullTitle(%q, %q) = %q, want %q", tc.title, tc.subtitle, got, tc.want)
			}
		})
	}
}
