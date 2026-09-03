// file: internal/metadata/provider_keys.go
// version: 2.1.0
// guid: 5a7c2f18-6d94-4e3b-8021-c9f5b47e6a03
// last-edited: 2026-09-02

package metadata

// Canonical provider ids.
//
// These are the SAME strings as config.MetadataSource.ID (see the defaults in
// internal/config/config.go) and the same keys providerhttp budgets are stored
// under. One vocabulary, defined once.
//
// It was briefly three. Config said "google-books", the HTTP client asked for
// "googlebooks", and the client's display Name() was "Google Books", so a rate
// limit stored under one spelling was read under another: written, never
// consulted, provider silently left on its built-in budget while the settings
// UI reported the configured number. Nothing errored. The fix is for everyone
// to use the id the provider already has, not to translate between spellings.
const (
	SourceIDAudible     = "audible"
	SourceIDAudnexus    = "audnexus"
	SourceIDGoogleBooks = "google-books"
	SourceIDHardcover   = "hardcover"
	SourceIDOpenLibrary = "openlibrary"
	SourceIDWikipedia   = "wikipedia"
)

// ProviderIdentified is implemented by sources that know their canonical
// provider id.
//
// Deliberately OPTIONAL rather than folded into MetadataSource: the id is
// needed for request budgeting and concurrency, not for searching, and the
// search interface is implemented by a mockery mock plus a handful of test
// stubs that have no meaningful id. Widening MetadataSource would force all of
// them to answer a question they do not care about.
type ProviderIdentified interface {
	ProviderID() string
}

// ProviderIDOf returns a source's canonical provider id, unwrapping decorators
// such as ProtectedSource. It returns "" when the source does not declare one
// (test stubs, mocks), which callers must treat as "use the default budget"
// rather than as a usable key — a budget stored under "" applies to no traffic
// at all, which looks exactly like a limit being honoured.
func ProviderIDOf(src MetadataSource) string {
	if p, ok := src.(ProviderIdentified); ok {
		return p.ProviderID()
	}
	return ""
}

// ProviderKey returns the stable key for a source: its canonical provider id,
// falling back to the display name only for sources that declare none (test
// stubs and mocks).
//
// This is the ONE helper for "what do we key this source's data under". Both
// the per-provider concurrency semaphore and the metadata-fetch cache use it,
// so the two cannot drift onto different vocabularies -- which is exactly how
// a configured limit ended up applying to no traffic.
func ProviderKey(src MetadataSource) string {
	if id := ProviderIDOf(src); id != "" {
		return id
	}
	return src.Name()
}
