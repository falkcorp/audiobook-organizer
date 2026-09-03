// file: internal/metadata/provider_keys_test.go
// version: 1.0.0
// guid: 8d43e1b7-0c26-4a95-b3f8-71e5c8a92d40
// last-edited: 2026-09-02

package metadata

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// TestEveryClientNameResolves walks the ACTUAL clients and asserts each one's
// Name() maps to a provider budget that really exists.
//
// This is the guard against the silent-inertness failure: three vocabularies
// exist for these providers (config id, budget key, display name) and they do
// not agree. A rate limit stored under a name nothing requests is written,
// never read, and leaves the provider on its built-in budget while the UI
// reports the configured number. Nothing errors; the setting just does nothing.
func TestEveryClientNameResolves(t *testing.T) {
	known := providerhttp.KnownProviders()

	clients := []MetadataSource{
		NewOpenLibraryClient(),
		NewGoogleBooksClient(""),
		NewAudibleClient(),
		NewAudnexusClient(),
		NewHardcoverClient("token"),
		NewWikipediaClient(),
	}

	for _, c := range clients {
		name := c.Name()
		key := CanonicalProviderKey(name)
		if key == "" {
			t.Errorf("client Name() %q does not resolve to a provider key; a limit set for it would be silently inert", name)
			continue
		}
		if !slices.Contains(known, key) {
			t.Errorf("client %q resolved to key %q, which providerhttp does not budget (known: %v)", name, key, known)
		}
	}
}

// TestEveryConfigSourceIDResolves pins the config-side vocabulary. These are the
// ids buildSourceChainFromConfig switches on, i.e. what a user's settings file
// actually contains.
func TestEveryConfigSourceIDResolves(t *testing.T) {
	known := providerhttp.KnownProviders()

	// Mirrors the switch in metafetch.buildSourceChainFromConfig.
	configIDs := []string{"openlibrary", "google-books", "audible", "audnexus", "hardcover", "wikipedia"}

	for _, id := range configIDs {
		key := CanonicalProviderKey(id)
		if key == "" {
			t.Errorf("config source id %q does not resolve to a provider key", id)
			continue
		}
		if !slices.Contains(known, key) {
			t.Errorf("config id %q resolved to %q, which providerhttp does not budget (known: %v)", id, key, known)
		}
	}
}

// TestUnknownProviderResolvesEmpty: an unrecognised name must resolve to "" so
// callers can report it, never to a plausible-looking invented key.
func TestUnknownProviderResolvesEmpty(t *testing.T) {
	for _, s := range []string{"", "   ", "goodreads", "google_books"} {
		if got := CanonicalProviderKey(s); got != "" {
			t.Errorf("CanonicalProviderKey(%q) = %q, want \"\"", s, got)
		}
	}
}
