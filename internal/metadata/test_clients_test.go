// file: internal/metadata/test_clients_test.go
// version: 1.0.0
// guid: 7c1e5b90-3a42-4d6f-9b8e-2f0a6d4c1e73
// last-edited: 2026-10-12

package metadata

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// The production constructors read their base URL from config or hardcode it,
// so tests that point a client at an httptest server build the struct here.
// The HTTP client is the production one (providerhttp.Client), so the
// transport under test is the real one.

func newAudnexusTestClient(baseURL string) *AudnexusClient {
	return &AudnexusClient{
		httpClient: providerhttp.Client(SourceIDAudnexus),
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

func newHardcoverTestClient(baseURL, apiToken string) *HardcoverClient {
	return &HardcoverClient{
		httpClient: providerhttp.Client(SourceIDHardcover),
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiToken:   apiToken,
	}
}

func newWikipediaTestClient(baseURL, wikidataURL string) *WikipediaClient {
	return &WikipediaClient{
		httpClient:  providerhttp.Client(SourceIDWikipedia),
		baseURL:     strings.TrimRight(baseURL, "/"),
		wikidataURL: strings.TrimRight(wikidataURL, "/"),
	}
}
