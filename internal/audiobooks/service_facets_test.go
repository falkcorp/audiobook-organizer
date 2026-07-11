// file: internal/audiobooks/service_facets_test.go
// version: 1.0.0
// guid: 3a5e9c1b-6d2f-4a80-9b1c-7e4f0a2d5c6b
// last-edited: 2026-07-11

// Tests for AudiobookService.FacetCounts (INIT-4 T4): the nil-index
// sentinel path and the pass-through-to-BleveIndex.FacetCounts path.

package audiobooks

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// TestAudiobookService_FacetCounts_NilIndex pins the fail-open sentinel:
// a service with no wired search index returns ErrSearchIndexUnavailable
// and nil maps, never a panic.
func TestAudiobookService_FacetCounts_NilIndex(t *testing.T) {
	svc := &AudiobookService{}

	genres, languages, tags, err := svc.FacetCounts()
	if !errors.Is(err, ErrSearchIndexUnavailable) {
		t.Fatalf("err = %v, want ErrSearchIndexUnavailable", err)
	}
	if genres != nil || languages != nil || tags != nil {
		t.Errorf("expected nil maps on nil-index error, got genres=%v languages=%v tags=%v", genres, languages, tags)
	}
}

// TestAudiobookService_FacetCounts_WithIndex pins the pass-through path:
// once SetSearchIndex wires a real (non-nil) index, FacetCounts returns
// the same value->count maps BleveIndex.FacetCounts would.
func TestAudiobookService_FacetCounts_WithIndex(t *testing.T) {
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	docs := []search.BookDocument{
		{BookID: "b1", Title: "One", Genre: "Fantasy", Language: "en", Tags: []string{"epic"}},
		{BookID: "b2", Title: "Two", Genre: "Fantasy", Language: "en", Tags: []string{"epic"}},
	}
	if err := idx.IndexBookBatch(docs); err != nil {
		t.Fatalf("index batch: %v", err)
	}

	svc := &AudiobookService{}
	svc.SetSearchIndex(idx)

	genres, languages, tags, err := svc.FacetCounts()
	if err != nil {
		t.Fatalf("FacetCounts: %v", err)
	}
	wantGenres := map[string]int{"fantasy": 2}
	if !reflect.DeepEqual(genres, wantGenres) {
		t.Errorf("genres = %v, want %v", genres, wantGenres)
	}
	wantLanguages := map[string]int{"en": 2}
	if !reflect.DeepEqual(languages, wantLanguages) {
		t.Errorf("languages = %v, want %v", languages, wantLanguages)
	}
	wantTags := map[string]int{"epic": 2}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Errorf("tags = %v, want %v", tags, wantTags)
	}
}

// TestAudiobookService_FacetCounts_SetNilRevertsToSentinel pins
// SetSearchIndex(nil) reverting the service back to the sentinel path, per
// its own doc comment ("Calling with nil reverts to the Store.SearchBooks
// fallback").
func TestAudiobookService_FacetCounts_SetNilRevertsToSentinel(t *testing.T) {
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	svc := &AudiobookService{}
	svc.SetSearchIndex(idx)
	svc.SetSearchIndex(nil)

	if _, _, _, err := svc.FacetCounts(); !errors.Is(err, ErrSearchIndexUnavailable) {
		t.Fatalf("err = %v, want ErrSearchIndexUnavailable", err)
	}
}
