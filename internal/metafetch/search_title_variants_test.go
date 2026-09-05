// file: internal/metafetch/search_title_variants_test.go
// version: 1.0.0
// guid: 5b1c7d0e-3a4f-4e8b-9c2d-7f6a1e0b9d31
// last-edited: 2026-09-05

package metafetch

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The titles below are the first books prod's 2026-09-05 bulk fetch judged
// not_found with every provider live; each is a real book that Audible returns
// for the decoration-free query.
func TestStripSeriesDecoration(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Eternal Dominion, Book 04 - Assertions", "Eternal Dominion - Assertions"},
		{"Path Of The Voidwalker - BK07", "Path Of The Voidwalker"},
		{"Pip & Flinx - Book 1 For Love of Mother Not", "Pip & Flinx - For Love of Mother Not"},
		{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart Book 6)", "Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart)"},
		{"Dune", "Dune"},
		{"Book 4", "Book 4"}, // nothing but a decoration: unchanged, never ""
		{"1984", "1984"},     // a bare number is not a series slot
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := stripSeriesDecoration(tt.in); got != tt.want {
				t.Errorf("stripSeriesDecoration(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtraTitleVariants(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"Eternal Dominion, Book 04 - Assertions", []string{"Eternal Dominion - Assertions", "Eternal Dominion", "Assertions"}},
		{"Path Of The Voidwalker - BK07", []string{"Path Of The Voidwalker"}},
		{"Pip & Flinx - Book 1 For Love of Mother Not", []string{"Pip & Flinx - For Love of Mother Not", "Pip & Flinx", "For Love of Mother Not"}},
		{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart Book 6)", []string{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart)", "Champion of Deania", "A Cultivating Gamelit Harem Adventure (Spellheart)"}},
		// A plain title yields nothing: the literal queries already covered it,
		// so a book they found costs no extra provider calls.
		{"A Plain Title", nil},
		{"Dune", nil},
		// The literal-query dedup is case-insensitive.
		{"dune - bk02", nil},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := extraTitleVariants(tt.raw, stripChapterFromTitle(tt.raw))
			if tt.raw == "dune - bk02" {
				// stripChapterFromTitle leaves this alone, so the only variant
				// is "dune", which must survive: it differs from both literals.
				if !reflect.DeepEqual(got, []string{"dune"}) {
					t.Fatalf("variants = %v, want [dune]", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extraTitleVariants(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// The bulk walk: a provider that only answers the decoration-free title is a
// hit, and the variants are tried after both literal titles.
func TestWalkSourceChain_SeriesDecoratedTitleFallsBackToVariants(t *testing.T) {
	// Hits on the SECOND variant, so a walk that kept going after a hit would
	// be seen querying "Assertions" after it.
	src := &recordingSource{name: "audible", hitOn: "Eternal Dominion"}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "Bern Dean", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.Status() != FetchStatusCached {
		t.Fatalf("status = %q, want %q (queries: %v)", out.Status(), FetchStatusCached, src.queries)
	}
	// Literal titles first (with author, then without), variants after.
	if len(src.queries) < 3 || src.queries[0] != "Eternal Dominion, Book 04 - Assertions" {
		t.Fatalf("literal title must be tried first; queries: %v", src.queries)
	}
	if src.queries[len(src.queries)-1] != "Eternal Dominion" {
		t.Fatalf("walk must stop at the variant that hit; queries: %v", src.queries)
	}
}

// A hit on the literal title never reaches the variants: the extra calls are
// paid only by books the literal queries missed.
func TestWalkSourceChain_LiteralHitSkipsVariants(t *testing.T) {
	src := &recordingSource{name: "audible", hitOn: "Eternal Dominion, Book 04 - Assertions"}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.Status() != FetchStatusCached {
		t.Fatalf("status = %q, want %q", out.Status(), FetchStatusCached)
	}
	if len(src.queries) != 1 {
		t.Fatalf("expected exactly the literal query, got %v", src.queries)
	}
}

// The interactive search (the review dialog's lookup) takes the same fallback.
func TestSearchMetadataForBook_SeriesDecoratedTitleFallsBackToVariants(t *testing.T) {
	src := &recordingSource{name: "audible", hitOn: "Eternal Dominion"}
	book := &database.Book{ID: "b1", Title: "Eternal Dominion, Book 04 - Assertions"}
	mock := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { return book, nil },
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "", "", "", SearchOptions{})
	if err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected the variant hit to surface; queries: %v", src.queries)
	}
	if src.queries[len(src.queries)-1] != "Eternal Dominion" {
		t.Fatalf("search must stop at the variant that hit; queries: %v", src.queries)
	}
}

// A literal-title hit on the interactive path never reaches the variants.
func TestSearchMetadataForBook_LiteralHitSkipsVariants(t *testing.T) {
	src := &recordingSource{name: "audible", hitOn: "Eternal Dominion, Book 04 - Assertions"}
	book := &database.Book{ID: "b1", Title: "Eternal Dominion, Book 04 - Assertions"}
	mock := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { return book, nil },
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{src})

	if _, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	for _, q := range src.queries {
		if q != "Eternal Dominion, Book 04 - Assertions" {
			t.Fatalf("variant %q was queried after a literal hit; queries: %v", q, src.queries)
		}
	}
}

// And a plain title makes no extra call on the interactive path either.
func TestSearchMetadataForBook_PlainTitleMakesNoVariantCalls(t *testing.T) {
	src := &recordingSource{name: "audible"} // never hits
	book := &database.Book{ID: "b1", Title: "A Plain Title"}
	mock := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { return book, nil },
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{src})

	if _, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	for _, q := range src.queries {
		if q != "A Plain Title" {
			t.Fatalf("unexpected variant query %q in %v", q, src.queries)
		}
	}
}
