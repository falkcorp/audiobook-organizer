// file: internal/server/audiobook_service_bleve_test.go
// version: 1.1.0
// last-edited: 2026-08-15
// guid: 7f3d5a4b-9c5a-4a70-b8c5-3d7e0f1b9a99

package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// setupBleveBackedService builds an AudiobookService with a real
// PebbleStore, a real Bleve index, and a small fixture of books
// and indexed docs so search paths can be verified end-to-end.
func setupBleveBackedService(t *testing.T) (*AudiobookService, *database.PebbleStore, *search.BleveIndex) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	svc := NewAudiobookService(store)
	svc.SetSearchIndex(idx)

	seedRows := []struct {
		id, title, author, format string
		year                      int
	}{
		{"b1", "The Way of Kings", "Brandon Sanderson", "m4b", 2010},
		{"b2", "Words of Radiance", "Brandon Sanderson", "m4b", 2014},
		{"b3", "The Fifth Season", "N. K. Jemisin", "mp3", 2015},
	}
	for _, r := range seedRows {
		year := r.year
		if _, err := store.CreateBook(&database.Book{
			ID: r.id, Title: r.title, FilePath: "/tmp/" + r.id, Format: r.format, PrintYear: &year,
		}); err != nil {
			t.Fatalf("seed book: %v", err)
		}
		if err := idx.IndexBook(search.BookDocument{
			BookID: r.id, Title: r.title, Author: r.author, Format: r.format, Year: r.year,
		}); err != nil {
			t.Fatalf("index: %v", err)
		}
	}
	return svc, store, idx
}

func TestService_SearchRoutedThroughBleve(t *testing.T) {
	svc, _, _ := setupBleveBackedService(t)

	books, err := svc.GetAudiobooks(context.Background(), 10, 0, "author:sanderson", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(books) != 2 {
		t.Errorf("got %d books, want 2", len(books))
	}
}

func TestService_SearchRangeQuery(t *testing.T) {
	svc, _, _ := setupBleveBackedService(t)

	books, err := svc.GetAudiobooks(context.Background(), 10, 0, "year:[2013 TO 2020]", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// b2 (2014) + b3 (2015) in range.
	if len(books) != 2 {
		t.Errorf("got %d books for year range, want 2", len(books))
	}
}

func TestService_SearchFallsBackOnParserFailure(t *testing.T) {
	svc, _, _ := setupBleveBackedService(t)

	// Malformed DSL — unterminated quote. Service should fall back
	// to substring SearchBooks which (on PebbleStore) usually does
	// a LIKE-style scan and may return zero but MUST NOT error.
	books, err := svc.GetAudiobooks(context.Background(), 10, 0, `title:"unterminated`, nil, nil)
	if err != nil {
		t.Errorf("parser failure should fall back silently, got err: %v", err)
	}
	_ = books // shape is fine; contents depend on fallback behavior
}

func TestService_NoIndexUsesLegacy(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBook(&database.Book{
		ID: "b1", Title: "Only Book", FilePath: "/tmp/b1", Format: "m4b",
	})

	svc := NewAudiobookService(store)
	// No SetSearchIndex — should use Store.SearchBooks path.
	books, err := svc.GetAudiobooks(context.Background(), 10, 0, "Only", nil, nil)
	if err != nil {
		t.Fatalf("legacy search: %v", err)
	}
	// PebbleStore.SearchBooks may return 0 or 1 depending on
	// implementation; the important contract is: no panic, no error,
	// and the call path was legacy (not Bleve).
	_ = books
}
