// file: internal/metadata/provider_keys.go
// version: 1.0.0
// guid: 5a7c2f18-6d94-4e3b-8021-c9f5b47e6a03
// last-edited: 2026-09-02

package metadata

import "strings"

// A provider is known by THREE different names in this codebase and they do not
// agree with each other:
//
//	config.MetadataSource.ID   "google-books"
//	providerhttp budget key    "googlebooks"
//	MetadataSource.Name()      "Google Books"
//
// Every one of those is load-bearing somewhere: config is what the user edits,
// the budget key is what the rate limiter is stored under, and Name() is what
// the per-provider concurrency semaphore keys on. Storing a rate limit under
// the wrong spelling is SILENTLY INERT — the value is written, never read, the
// provider quietly keeps its built-in budget, and the settings UI reports a
// number that has no effect on anything.
//
// canonicalProviderKeys collapses all three vocabularies onto the single key
// providerhttp budgets live under. Keys are lower-cased on lookup.
var canonicalProviderKeys = map[string]string{
	// openlibrary
	"openlibrary":  "openlibrary",
	"open library": "openlibrary",
	"open-library": "openlibrary",
	// googlebooks
	"googlebooks":  "googlebooks",
	"google-books": "googlebooks",
	"google books": "googlebooks",
	// audible
	"audible": "audible",
	// audnexus — Name() carries a parenthetical
	"audnexus":           "audnexus",
	"audnexus (audible)": "audnexus",
	// hardcover
	"hardcover": "hardcover",
	// wikipedia
	"wikipedia": "wikipedia",
	// cover art downloads
	"cover": "cover",
}

// CanonicalProviderKey resolves any known spelling of a provider — a config
// source ID, a MetadataSource display name, or the budget key itself — to the
// key providerhttp stores that provider's request budget under.
//
// It returns "" for anything unrecognised. Callers must treat that as an error
// worth reporting rather than inventing a key: a phantom budget under a
// misspelled name applies to no traffic at all, which looks exactly like a
// limit that is being respected.
func CanonicalProviderKey(s string) string {
	return canonicalProviderKeys[strings.ToLower(strings.TrimSpace(s))]
}
