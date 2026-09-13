// file: internal/metadata/provider_fields_test.go
// version: 1.0.0
// guid: 1270d9f8-04bd-4897-aa87-7890b09e0ddf
// last-edited: 2026-09-13

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// googleVolumeFixture is shaped like a real Google Books volumes search item:
// volumeInfo with subtitle, pageCount/printedPageCount, categories, every
// imageLinks size, and an authors list where one credit carries its role.
const googleVolumeFixture = `{
  "kind": "books#volumes",
  "totalItems": 1,
  "items": [{
    "kind": "books#volume",
    "id": "abc123",
    "volumeInfo": {
      "title": "The Book of Dragons",
      "subtitle": "An Anthology",
      "authors": ["Jonathan Strahan (Editor)", "Garth Nix", "Rebecca Roanhorse", "Kate Reading (Narrator)"],
      "publisher": "Harper Voyager",
      "publishedDate": "2020-07-07",
      "description": "Dragons, dragons, dragons.",
      "industryIdentifiers": [
        {"type": "ISBN_10", "identifier": "0062877151"},
        {"type": "ISBN_13", "identifier": "9780062877154"},
        {"type": "OTHER", "identifier": "UOM:39015012345678"}
      ],
      "pageCount": 576,
      "printedPageCount": 560,
      "printType": "BOOK",
      "categories": ["Fiction / Fantasy / Collections & Anthologies"],
      "mainCategory": "Fiction",
      "averageRating": 4,
      "ratingsCount": 12,
      "maturityRating": "NOT_MATURE",
      "imageLinks": {
        "smallThumbnail": "http://books.google.com/books/content?id=abc123&zoom=5",
        "thumbnail": "http://books.google.com/books/content?id=abc123&zoom=1",
        "small": "http://books.google.com/books/content?id=abc123&zoom=2",
        "medium": "http://books.google.com/books/content?id=abc123&zoom=3",
        "large": "http://books.google.com/books/content?id=abc123&zoom=4",
        "extraLarge": "http://books.google.com/books/content?id=abc123&zoom=6"
      },
      "language": "en"
    }
  }]
}`

func TestGoogleBooks_MapsEveryUsefulField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(googleVolumeFixture))
	}))
	defer srv.Close()

	res, err := NewGoogleBooksClientWithBaseURL(srv.URL).SearchByTitle(context.Background(), "The Book of Dragons")
	if err != nil || len(res) != 1 {
		t.Fatalf("search: %v (%d results)", err, len(res))
	}
	m := res[0]
	checks := []struct {
		name      string
		got, want any
	}{
		{"Title", m.Title, "The Book of Dragons: An Anthology"},
		{"Subtitle", m.Subtitle, "An Anthology"},
		{"Author (editor dropped, narrator split out)", m.Author, "Garth Nix, Rebecca Roanhorse"},
		{"Narrator", m.Narrator, "Kate Reading"},
		{"Publisher", m.Publisher, "Harper Voyager"},
		{"PublishYear", m.PublishYear, 2020},
		{"PublishYearIsAudiobookRelease", m.PublishYearIsAudiobookRelease, false},
		{"Description", m.Description, "Dragons, dragons, dragons."},
		{"ISBN10", m.ISBN10, "0062877151"},
		{"ISBN13", m.ISBN13, "9780062877154"},
		{"ISBN", m.ISBN, "9780062877154"},
		{"PageCount (printed preferred)", m.PageCount, 560},
		{"Genre", m.Genre, "Fiction"},
		{"CoverURL (largest, https)", m.CoverURL, "https://books.google.com/books/content?id=abc123&zoom=6"},
		{"Language", m.Language, "en"},
		{"GoogleRatingAverage", m.GoogleRatingAverage, 4.0},
		{"GoogleRatingCount", m.GoogleRatingCount, 12},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	wantTags := []string{"Fiction", "Fiction / Fantasy / Collections & Anthologies"}
	if !slices.Equal(m.CategoryTags, wantTags) {
		t.Errorf("CategoryTags = %v, want %v", m.CategoryTags, wantTags)
	}
}

func TestGoogleBooks_PageCountFallsBackAndCoverFallsBackToThumbnail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"volumeInfo":{"title":"T","pageCount":300,
			"imageLinks":{"smallThumbnail":"http://books.google.com/s","thumbnail":"http://books.google.com/t"}}}]}`))
	}))
	defer srv.Close()
	res, err := NewGoogleBooksClientWithBaseURL(srv.URL).SearchByTitle(context.Background(), "T")
	if err != nil || len(res) != 1 {
		t.Fatalf("search: %v", err)
	}
	if res[0].PageCount != 300 {
		t.Errorf("PageCount = %d, want 300 from pageCount", res[0].PageCount)
	}
	if res[0].CoverURL != "https://books.google.com/t" {
		t.Errorf("CoverURL = %q, want the thumbnail (largest offered)", res[0].CoverURL)
	}
	if res[0].Genre != "" || res[0].CategoryTags != nil {
		t.Errorf("no categories must give no genre/tags, got %q %v", res[0].Genre, res[0].CategoryTags)
	}
}

// olSearchFixture is shaped like a real search.json response.
const olSearchFixture = `{
  "numFound": 1, "start": 0, "numFoundExact": true,
  "docs": [{
    "key": "/works/OL5735363W",
    "title": "The Way of Kings",
    "subtitle": "Book One of the Stormlight Archive",
    "author_name": ["Brandon Sanderson", "Michael Kramer - narrator", "John Joseph Adams - editor"],
    "first_publish_year": 2010,
    "isbn": ["0765326353", "9780765326355"],
    "publisher": ["Tor", "Tor Books", "Gollancz"],
    "language": ["eng"],
    "cover_i": 8231856,
    "edition_count": 42,
    "number_of_pages_median": 1007
  }]
}`

func TestOpenLibrarySearch_MapsEveryUsefulField(t *testing.T) {
	var gotFields string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		_, _ = w.Write([]byte(olSearchFixture))
	}))
	defer srv.Close()
	c := NewOpenLibraryClientWithBaseURL(srv.URL)

	for name, search := range map[string]func() ([]BookMetadata, error){
		"SearchByTitle": func() ([]BookMetadata, error) { return c.SearchByTitle(context.Background(), "The Way of Kings") },
		"SearchByTitleAndAuthor": func() ([]BookMetadata, error) {
			return c.SearchByTitleAndAuthor(context.Background(), "The Way of Kings", "Sanderson")
		},
	} {
		res, err := search()
		if err != nil || len(res) != 1 {
			t.Fatalf("%s: %v (%d)", name, err, len(res))
		}
		for _, f := range []string{"subtitle", "number_of_pages_median", "isbn", "publisher", "language", "cover_i"} {
			if !strings.Contains(gotFields, f) {
				t.Errorf("%s: fields param %q does not request %s", name, gotFields, f)
			}
		}
		m := res[0]
		checks := []struct {
			field     string
			got, want any
		}{
			{"Title", m.Title, "The Way of Kings"},
			{"Subtitle", m.Subtitle, "Book One of the Stormlight Archive"},
			{"Author", m.Author, "Brandon Sanderson"},
			{"Narrator", m.Narrator, "Michael Kramer"},
			{"PublishYear", m.PublishYear, 2010},
			{"PageCount", m.PageCount, 1007},
			{"ISBN10", m.ISBN10, "0765326353"},
			{"ISBN13", m.ISBN13, "9780765326355"},
			{"Publisher (editions disagree)", m.Publisher, ""},
			{"Language", m.Language, "eng"},
			{"CoverURL", m.CoverURL, "https://covers.openlibrary.org/b/id/8231856-L.jpg"},
		}
		for _, ch := range checks {
			if ch.got != ch.want {
				t.Errorf("%s: %s = %#v, want %#v", name, ch.field, ch.got, ch.want)
			}
		}
	}
}

func TestOpenLibrarySearch_PublisherKeptWhenEditionsAgree(t *testing.T) {
	got := searchDocsToMetadata([]SearchResult{{Title: "T", Publisher: []string{"Tor", "tor"}}})
	if got[0].Publisher != "Tor" {
		t.Errorf("Publisher = %q, want Tor", got[0].Publisher)
	}
}

// olEditionFixture is shaped like a real /isbn/{isbn}.json edition.
const olEditionFixture = `{
  "key": "/books/OL24381223M",
  "title": "The Way of Kings",
  "subtitle": "Book One of the Stormlight Archive",
  "authors": [{"key": "/authors/OL1394865A"}],
  "publishers": ["Tor"],
  "publish_date": "August 31, 2010",
  "number_of_pages": 1007,
  "covers": [8231856, 111],
  "series": ["The Stormlight Archive ; bk. 1"],
  "languages": [{"key": "/languages/eng"}],
  "contributors": [
    {"role": "Narrator", "name": "Michael Kramer"},
    {"role": "Illustrator", "name": "Isaac Stewart"},
    {"role": "Translator", "name": "Somebody Else"}
  ],
  "by_statement": "Brandon Sanderson",
  "isbn_10": ["0765326353"],
  "isbn_13": ["9780765326355"],
  "description": {"type": "/type/text", "value": "Roshar is a world of stone and storms."},
  "physical_format": "Hardcover"
}`

func TestOpenLibraryISBN_MapsEveryUsefulField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(olEditionFixture))
	}))
	defer srv.Close()
	m, err := NewOpenLibraryClientWithBaseURL(srv.URL).GetBookByISBN(context.Background(), "9780765326355")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		field     string
		got, want any
	}{
		{"Title", m.Title, "The Way of Kings"},
		{"Subtitle", m.Subtitle, "Book One of the Stormlight Archive"},
		{"PublishYear (Month D, YYYY)", m.PublishYear, 2010},
		{"PageCount", m.PageCount, 1007},
		{"Publisher", m.Publisher, "Tor"},
		{"Series", m.Series, "The Stormlight Archive"},
		{"SeriesPosition", m.SeriesPosition, "1"},
		{"Language", m.Language, "eng"},
		{"Narrator (illustrator/translator dropped)", m.Narrator, "Michael Kramer"},
		{"Author (not in edition JSON)", m.Author, ""},
		{"ISBN", m.ISBN, "9780765326355"},
		{"ISBN10", m.ISBN10, "0765326353"},
		{"ISBN13", m.ISBN13, "9780765326355"},
		{"Description", m.Description, "Roshar is a world of stone and storms."},
		{"CoverURL", m.CoverURL, "https://covers.openlibrary.org/b/id/8231856-L.jpg"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %#v, want %#v", c.field, c.got, c.want)
		}
	}
}

func TestOpenLibraryISBN_StringDescriptionAndSeveralSeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"T","description":"plain text","series":["A ; 1","B ; 2"],
			"languages":[{"key":"/languages/eng"},{"key":"/languages/fre"}]}`))
	}))
	defer srv.Close()
	m, err := NewOpenLibraryClientWithBaseURL(srv.URL).GetBookByISBN(context.Background(), "0000000000")
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "plain text" {
		t.Errorf("Description = %q", m.Description)
	}
	if m.Series != "" || m.SeriesPosition != "" {
		t.Errorf("two series must not pick one, got %q #%q", m.Series, m.SeriesPosition)
	}
	if m.Language != "" {
		t.Errorf("two languages must give none, got %q", m.Language)
	}
}

func TestParseOLSeries(t *testing.T) {
	for in, want := range map[string][2]string{
		"The Stormlight Archive ; bk. 1": {"The Stormlight Archive", "1"},
		"The Expanse ; 3":                {"The Expanse", "3"},
		"(Discworld #5)":                 {"Discworld", "5"},
		"Mistborn, book 2":               {"Mistborn", "2"},
		"Fahrenheit 451":                 {"Fahrenheit 451", ""},
		"Penguin classics":               {"Penguin classics", ""},
	} {
		n, p := parseOLSeries(in)
		if n != want[0] || p != want[1] {
			t.Errorf("parseOLSeries(%q) = %q, %q; want %q, %q", in, n, p, want[0], want[1])
		}
	}
}

func TestYearFromDate(t *testing.T) {
	for in, want := range map[string]int{
		"1937": 1937, "1937-09-21": 1937, "September 21, 1937": 1937, "Sep 1937": 1937, "c1999": 1999, "": 0, "n.d.": 0,
	} {
		if got := yearFromDate(in); got != want {
			t.Errorf("yearFromDate(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestClassifyContributor(t *testing.T) {
	for in, want := range map[string]struct {
		name string
		role ContributorRole
	}{
		"John Joseph Adams - editor": {"John Joseph Adams", RoleOther},
		"Jane Doe (Translator)":      {"Jane Doe", RoleOther},
		"Jane Doe [ed.]":             {"Jane Doe", RoleOther},
		"Jane Doe, ed.":              {"Jane Doe", RoleOther},
		"Doe, Ed":                    {"Doe, Ed", RoleAuthor},
		"edited by Jane Doe":         {"Jane Doe", RoleOther},
		"Read by Kate Reading":       {"Kate Reading", RoleNarrator},
		"Kate Reading - narrator":    {"Kate Reading", RoleNarrator},
		"Ursula K. Le Guin":          {"Ursula K. Le Guin", RoleAuthor},
		"Jean-Luc Picard":            {"Jean-Luc Picard", RoleAuthor},
	} {
		n, r := ClassifyContributor(in)
		if n != want.name || r != want.role {
			t.Errorf("ClassifyContributor(%q) = %q,%d; want %q,%d", in, n, r, want.name, want.role)
		}
	}
	for role, want := range map[string]ContributorRole{
		"": RoleAuthor, "Author": RoleAuthor, "Narrator": RoleNarrator, "Read by": RoleNarrator,
		"Editor": RoleOther, "Translator": RoleOther, "Illustrator": RoleOther, "Cover art": RoleOther,
	} {
		if got := ClassifyContributorRoleName(role); got != want {
			t.Errorf("ClassifyContributorRoleName(%q) = %d, want %d", role, got, want)
		}
	}
}

func TestCategoryTagSource(t *testing.T) {
	for src, want := range map[string]string{
		"Google Books": "google_books_category",
		"Open Library": "openlibrary_subject",
		"Audible":      "audible_category",
		"":             "audible_category",
	} {
		if got := CategoryTagSource(src); got != want {
			t.Errorf("CategoryTagSource(%q) = %q, want %q", src, got, want)
		}
	}
}
