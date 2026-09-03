// file: internal/metadata/provider_keys_test.go
// version: 2.1.0
// guid: 8d43e1b7-0c26-4a95-b3f8-71e5c8a92d40
// last-edited: 2026-09-02

package metadata

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

func allClients() []MetadataSource {
	return []MetadataSource{
		NewOpenLibraryClient(),
		NewGoogleBooksClient(""),
		NewAudibleClient(),
		NewAudnexusClient(),
		NewHardcoverClient("token"),
		NewWikipediaClient(),
	}
}

// TestEveryClientDeclaresABudgetedID walks the REAL clients and asserts each
// declares a provider id that providerhttp actually budgets.
//
// This is the guard against a rate limit that is stored and never read. If a
// client's id and its budget key drift apart, the limit applies to no traffic:
// nothing errors, the provider quietly keeps its built-in default, and the
// settings page reports the configured number as though it were in effect.
func TestEveryClientDeclaresABudgetedID(t *testing.T) {
	known := providerhttp.KnownProviders()

	for _, c := range allClients() {
		id := ProviderIDOf(c)
		if id == "" {
			t.Errorf("client %q declares no provider id; its configured limits would be silently ignored", c.Name())
			continue
		}
		if !slices.Contains(known, id) {
			t.Errorf("client %q has id %q, which providerhttp does not budget (known: %v)", c.Name(), id, known)
		}
	}
}

// TestProviderIDMatchesConfigDefaults pins the ids against the vocabulary the
// user's config file actually uses — the switch in
// metafetch.buildSourceChainFromConfig and the defaults in internal/config.
// If these drift, a configured limit lands under an id no client claims.
func TestProviderIDMatchesConfigDefaults(t *testing.T) {
	configIDs := []string{
		SourceIDOpenLibrary, SourceIDGoogleBooks, SourceIDAudible,
		SourceIDAudnexus, SourceIDHardcover, SourceIDWikipedia,
	}
	// These literals are what internal/config ships as defaults. Written out
	// rather than referenced so a change to either side fails loudly instead of
	// both moving together silently.
	want := []string{"openlibrary", "google-books", "audible", "audnexus", "hardcover", "wikipedia"}
	for i, id := range configIDs {
		if id != want[i] {
			t.Errorf("provider id constant %q does not match the config default %q", id, want[i])
		}
	}
}

// TestProtectedSourceForwardsProviderID: the breaker decorator wraps every
// source in the live chain. If it swallows the id, every provider looks
// unidentified at runtime and silently falls back to default budgets — while
// unit tests on the bare clients still pass.
func TestProtectedSourceForwardsProviderID(t *testing.T) {
	for _, c := range allClients() {
		wrapped := NewProtectedSource(c, 5, 0)
		if got, want := ProviderIDOf(wrapped), ProviderIDOf(c); got != want {
			t.Errorf("ProtectedSource(%q) reported id %q, want %q", c.Name(), got, want)
		}
	}
}

// TestDisplayNameIsNotTheID keeps the two vocabularies deliberately separate.
// Name() is a human-facing label that may be reworded or disambiguated
// ("Audnexus (Audible)"); the id is a stable key. Collapsing them would make an
// editorial change to a label silently detach a provider from its budget.
func TestDisplayNameIsNotTheID(t *testing.T) {
	for _, c := range allClients() {
		if c.Name() == ProviderIDOf(c) {
			t.Errorf("client %q uses its display name as its id; a label reword would then change a lookup key", c.Name())
		}
	}
}

// TestUnidentifiedSourceReturnsEmpty: a source that declares no id must resolve
// to "", never to an invented key. A budget under a phantom key applies to no
// traffic while reading as a configured limit.
func TestUnidentifiedSourceReturnsEmpty(t *testing.T) {
	if got := ProviderIDOf(unidentifiedSource{}); got != "" {
		t.Errorf("ProviderIDOf(unidentified) = %q, want \"\"", got)
	}
}

type unidentifiedSource struct{}

func (unidentifiedSource) Name() string { return "Some Stub" }
func (unidentifiedSource) SearchByTitle(_ context.Context, _ string) ([]BookMetadata, error) {
	return nil, nil
}
func (unidentifiedSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]BookMetadata, error) {
	return nil, nil
}
