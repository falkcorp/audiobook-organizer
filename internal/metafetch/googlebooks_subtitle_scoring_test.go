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

	// F1 base tier: the library book is titled just "A New Dawn". The bare
	// franchise title scored 0 here; the candidate must now score a full match.
	if got := computeF1Base(cand, SignificantWords("A New Dawn")); !floatNear(got, 1.0) {
		t.Errorf("F1 vs library title %q = %v, want 1.0", "A New Dawn", got)
	}
}

// TestComputeF1Base_SubtitleDoesNotDiluteMainTitle locks the other side of the
// Google Books title join: an ordinary subtitle must not cut the precision of a
// candidate whose main title is an exact match for the library's title.
func TestComputeF1Base_SubtitleDoesNotDiluteMainTitle(t *testing.T) {
	joined := metadata.BookMetadata{
		Title:    "Sapiens: A Brief History of Humankind",
		Subtitle: "A Brief History of Humankind",
	}
	if got := computeF1Base(joined, SignificantWords("Sapiens")); !floatNear(got, 1.0) {
		t.Errorf("F1 vs %q = %v, want 1.0 (main title is an exact match)", "Sapiens", got)
	}

	// Audible shape: Title is the book, Subtitle is separate. Unchanged.
	audible := metadata.BookMetadata{Title: "A New Dawn", Subtitle: "Star Wars"}
	if got := computeF1Base(audible, SignificantWords("A New Dawn")); !floatNear(got, 1.0) {
		t.Errorf("Audible-shaped F1 = %v, want 1.0", got)
	}

	// Audible shape: a separate franchise subtitle must NOT be scored alone,
	// or a library title of "Star Wars" would fully match every Star Wars book.
	if got := computeF1Base(audible, SignificantWords("Star Wars")); floatNear(got, 1.0) {
		t.Errorf("separate subtitle %q must not be scored alone, got %v", audible.Subtitle, got)
	}

	// No subtitle: a colon in the title is scored as a whole, as before.
	plain := metadata.BookMetadata{Title: "Dune: Messiah"}
	if got := computeF1Base(plain, SignificantWords("Dune")); floatNear(got, 1.0) {
		t.Errorf("title without a Subtitle must not be split for scoring, got %v", got)
	}
}
