// file: internal/metadata/adapter_clients.go
// version: 0.1.0
// guid: 3fe83e9a-e0e5-4a2a-b98d-fff9d67708ec
// last-edited: 2026-11-20

package metadata

import (
	"net/http"
	"strings"
)

// Constructors for the books.<id> metadata adapters (F7-09 T5, internal/books/
// adapters/metadata). They are the HEAD clients with the three things HEAD reads
// from process globals handed in instead: the HTTP client (the adapter's
// Env.HTTP, whose transport is core's gate, not providerhttp.Client), the base
// URL (the instance's base_url setting, not config.AppConfig.MetadataSources),
// and the credential (the instance's secret, not config.AppConfig). Nothing here
// reads config or providerhttp, so an instance cannot be served another
// instance's budget, URL or key. The HEAD constructors above are unchanged and
// remain the path every caller uses until the F7-09 fold.
//
// An empty baseURL is the client's own default (the Default*BaseURL constants,
// pinned against the HEAD constructors by TestDefaultBaseURLsMatchHEAD).

// The default base URL of each source whose client takes one.
const (
	DefaultAudibleBaseURL     = "https://api.audible.com/1.0"
	DefaultAudnexusBaseURL    = "https://api.audnex.us"
	DefaultGoogleBooksBaseURL = "https://www.googleapis.com/books/v1"
	DefaultOpenLibraryBaseURL = "https://openlibrary.org"
)

// baseOr is baseURL without a trailing slash, or def when baseURL is empty.
func baseOr(baseURL, def string) string {
	if baseURL == "" {
		baseURL = def
	}
	return strings.TrimRight(baseURL, "/")
}

// NewAudibleClientFrom is an Audible client over hc and baseURL.
func NewAudibleClientFrom(hc *http.Client, baseURL string) *AudibleClient {
	return &AudibleClient{httpClient: hc, baseURL: baseOr(baseURL, DefaultAudibleBaseURL)}
}

// NewAudnexusClientFrom is an Audnexus client over hc and baseURL.
func NewAudnexusClientFrom(hc *http.Client, baseURL string) *AudnexusClient {
	return &AudnexusClient{httpClient: hc, baseURL: baseOr(baseURL, DefaultAudnexusBaseURL)}
}

// NewGoogleBooksClientFrom is a Google Books client over hc and baseURL that sends
// apiKey (empty: anonymous, HEAD's behaviour without a key).
func NewGoogleBooksClientFrom(hc *http.Client, baseURL, apiKey string) *GoogleBooksClient {
	return &GoogleBooksClient{httpClient: hc, baseURL: baseOr(baseURL, DefaultGoogleBooksBaseURL), apiKey: apiKey}
}

// NewHardcoverClientFrom is a Hardcover client over hc that sends apiToken. Its
// endpoint is HEAD's fixed one: HEAD's client never read a configured base URL.
func NewHardcoverClientFrom(hc *http.Client, apiToken string) *HardcoverClient {
	return &HardcoverClient{httpClient: hc, baseURL: "https://api.hardcover.app/v1/graphql", apiToken: apiToken}
}

// NewOpenLibraryClientFrom is an Open Library client over hc and baseURL, with no
// local dump store until the caller sets one (SetOLStoreSource).
func NewOpenLibraryClientFrom(hc *http.Client, baseURL string) *OpenLibraryClient {
	return &OpenLibraryClient{httpClient: hc, baseURL: baseOr(baseURL, DefaultOpenLibraryBaseURL)}
}

// NewWikipediaClientFrom is a Wikipedia/Wikidata client over hc, at HEAD's fixed
// endpoints: HEAD's client never read a configured base URL.
func NewWikipediaClientFrom(hc *http.Client) *WikipediaClient {
	return &WikipediaClient{
		httpClient:  hc,
		baseURL:     "https://en.wikipedia.org/w/api.php",
		wikidataURL: "https://www.wikidata.org/w/api.php",
	}
}
