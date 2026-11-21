// file: internal/metadata/adapter_clients_test.go
// version: 0.1.0
// guid: 6e716abf-036b-4eef-9dc2-5a48b603fe32
// last-edited: 2026-11-20

package metadata

import (
	"net/http"
	"testing"
)

// The *From constructors take the same default endpoints HEAD's own take, so a
// books.<id> instance with no base_url talks to where HEAD's client did.
func TestFromConstructors_DefaultEndpointsMatchHEAD(t *testing.T) {
	hc := &http.Client{}
	for name, c := range map[string][2]string{
		"audible":      {NewAudibleClient().baseURL, NewAudibleClientFrom(hc, "").baseURL},
		"audnexus":     {NewAudnexusClient().baseURL, NewAudnexusClientFrom(hc, "").baseURL},
		"google-books": {NewGoogleBooksClient("").baseURL, NewGoogleBooksClientFrom(hc, "", "").baseURL},
		"hardcover":    {NewHardcoverClient("").baseURL, NewHardcoverClientFrom(hc, "").baseURL},
		"openlibrary":  {NewOpenLibraryClient().baseURL, NewOpenLibraryClientFrom(hc, "").baseURL},
		"wikipedia":    {NewWikipediaClient().baseURL, NewWikipediaClientFrom(hc).baseURL},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: HEAD dials %q, the From constructor %q", name, c[0], c[1])
		}
	}
	if a, b := NewWikipediaClient().wikidataURL, NewWikipediaClientFrom(hc).wikidataURL; a != b {
		t.Errorf("wikidata: HEAD %q, From %q", a, b)
	}
	for name, c := range map[string][2]string{
		"audible": {DefaultAudibleBaseURL, NewAudibleClient().baseURL}, "audnexus": {DefaultAudnexusBaseURL, NewAudnexusClient().baseURL},
		"google-books": {DefaultGoogleBooksBaseURL, NewGoogleBooksClient("").baseURL}, "openlibrary": {DefaultOpenLibraryBaseURL, NewOpenLibraryClient().baseURL},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: constant %q, HEAD %q", name, c[0], c[1])
		}
	}
}

func TestFromConstructors_UseTheGivenClientBaseURLAndCredential(t *testing.T) {
	hc := &http.Client{}
	if c := NewGoogleBooksClientFrom(hc, "http://mirror.invalid/books/v1/", "k"); c.httpClient != hc || c.baseURL != "http://mirror.invalid/books/v1" || c.apiKey != "k" {
		t.Errorf("google-books: %+v", c)
	}
	if c := NewHardcoverClientFrom(hc, "tok"); c.httpClient != hc || c.apiToken != "tok" {
		t.Errorf("hardcover: %+v", c)
	}
	if c := NewAudibleClientFrom(hc, "http://mirror.invalid/"); c.httpClient != hc || c.baseURL != "http://mirror.invalid" {
		t.Errorf("audible: %+v", c)
	}
}
