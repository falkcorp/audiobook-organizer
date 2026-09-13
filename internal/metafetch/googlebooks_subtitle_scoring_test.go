// file: internal/metafetch/googlebooks_subtitle_scoring_test.go
// version: 1.0.0
// guid: 5e8a1d34-9c62-4b7f-8e20-3a6f1c9d4b78
// last-edited: 2026-09-13

package metafetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// TestGoogleBooksSubtitle_ScoredTitleNamesTheBook runs a Google Books volume
// filed as title "Star Wars" / subtitle "A New Dawn" through the real client
// and into the transcription scorer. Before the fix the candidate's title was
// the bare franchise name, so it could never exactly match the book's real
// title and only matched by substring on "Star Wars".
func TestGoogleBooksSubtitle_ScoredTitleNamesTheBook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"totalItems":1,"items":[{"volumeInfo":{
			"title":"Star Wars","subtitle":"A New Dawn","authors":["John Jackson Miller"]}}]}`))
	}))
	defer server.Close()

	results, err := metadata.NewGoogleBooksClientWithBaseURL(server.URL).
		SearchByTitleAndAuthor(context.Background(), "A New Dawn", "John Jackson Miller")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	cand := results[0]

	if !strings.Contains(cand.Title, "A New Dawn") {
		t.Fatalf("scored/displayed title %q must contain the real title %q", cand.Title, "A New Dawn")
	}
	if cand.Subtitle != "A New Dawn" {
		t.Errorf("Subtitle = %q, want %q", cand.Subtitle, "A New Dawn")
	}

	// The audio intro names the full title; the candidate must match it exactly
	// (the exact-title boost), not merely by the franchise substring.
	const base = 1.0
	got, boosted := transcriptionBoost(base, cand, transcriptionHints{title: "Star Wars: A New Dawn"})
	if !boosted {
		t.Fatalf("expected a title match against the transcribed full title")
	}
	if want := base * scoringKnobs().TranscriptionTitleExactBoost; !floatNear(got, want) {
		t.Errorf("score = %v, want exact-title boost %v", got, want)
	}

	// A transcript naming a DIFFERENT book in the same franchise must not match
	// the title at all.
	if _, boosted := transcriptionBoost(base, cand, transcriptionHints{title: "Star Wars: Thrawn"}); boosted {
		t.Errorf("a different Star Wars title must not boost this candidate")
	}
}
