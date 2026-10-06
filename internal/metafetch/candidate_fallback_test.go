// file: internal/metafetch/candidate_fallback_test.go
// version: 1.1.0
// guid: ea9f0acf-53b5-4342-885a-5843289a5fa7
// last-edited: 2026-10-06

package metafetch

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The DailyBudget tests moved with the type to internal/metadata/dailyquota.

// TestActiveSourceNamesByID_ConfiguredChain: the production chain wraps every
// client in metadata.NewChainSource, and the fallback plan is built from the
// provider ids it reports. If the wrapper hid the id, the plan would be empty
// and Google Books would be asked for every book with no budget -- while every
// test built on unwrapped fakes stayed green. Building clients makes no
// network call.
func TestActiveSourceNamesByID_ConfiguredChain(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 1},
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 2},
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 3,
			Credentials: map[string]string{"apiKey": "test-placeholder"}},
	}
	mfs := NewService(nil)
	byID := mfs.ActiveSourceNamesByID()
	names := mfs.ActiveSourceNames()
	for _, id := range CandidateFallbackProviderIDs {
		name, ok := byID[id]
		if !ok {
			t.Fatalf("configured chain reports no source with provider id %q (got %v)", id, byID)
		}
		if !slices.Contains(names, name) {
			t.Fatalf("provider %q maps to %q, which is not among the active source names %v", id, name, names)
		}
	}
}

// S5: a search restricted to the fallback provider (OnlySources: Open
// Library) never looks the book's ASIN up on Audible/Audnexus -- those
// requests are not the fallback's to spend, and an Audnexus answer must
// never be reported as the fallback provider's. An unrestricted search still
// looks it up.
func TestSearch_OnlySourcesFallbackSkipsASINLookup(t *testing.T) {
	asin := "B0OWNBOOK1"
	book := &database.Book{ID: "b1", Title: "Some Obscure Book", ASIN: &asin}
	ol := &fakeProvider{id: metadata.SourceIDOpenLibrary, name: "Open Library",
		answer: func(string, string) []metadata.BookMetadata { return nil }}
	aud := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible",
		answer: func(string, string) []metadata.BookMetadata { return nil }}
	var lookups atomic.Int64
	run := func(opts SearchOptions) *SearchMetadataResponse {
		t.Helper()
		svc := fanoutHarness(t, book, aud, ol)
		svc.asinLookupOverride = func(_ context.Context, _, a string) (*metadata.BookMetadata, error) {
			lookups.Add(1)
			return &metadata.BookMetadata{Title: "Some Obscure Book", ASIN: a}, nil
		}
		resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "", "", "", opts)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		return resp
	}
	resp := run(SearchOptions{OnlySources: []string{"Open Library"}})
	if got := lookups.Load(); got != 0 {
		t.Fatalf("fallback-only search made %d ASIN lookups, want 0", got)
	}
	for _, c := range resp.Results {
		if c.Source != "Open Library" {
			t.Fatalf("fallback-only search returned a %q candidate", c.Source)
		}
	}
	run(SearchOptions{})
	if got := lookups.Load(); got == 0 {
		t.Fatal("unrestricted search made no ASIN lookup; the gate must only apply to restricted searches")
	}
}
