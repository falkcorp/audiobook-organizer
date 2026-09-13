// file: internal/metadata/openlibrary.go
// version: 1.14.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-13

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"

	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
)

// OpenLibraryClient handles metadata fetching from Open Library API.
// When olStore is set, local dump data is checked before hitting the API.
type OpenLibraryClient struct {
	httpClient *http.Client
	baseURL    string
	olStore    *openlibrary.OLStore
}

// NewOpenLibraryClient creates a new Open Library API client
func NewOpenLibraryClient() *OpenLibraryClient {
	return NewOpenLibraryClientWithBaseURL(resolveBaseURL("openlibrary", "https://openlibrary.org"))
}

// NewOpenLibraryClientWithBaseURL creates a client with a custom base URL.
func NewOpenLibraryClientWithBaseURL(baseURL string) *OpenLibraryClient {
	return &OpenLibraryClient{
		httpClient: providerhttp.Client(SourceIDOpenLibrary),
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Name returns the display name for this metadata source.
// ProviderID is the canonical id shared by config, the request-budget
// table and this client. Name() is a display string and must not be used
// as a key.
func (c *OpenLibraryClient) ProviderID() string { return SourceIDOpenLibrary }

func (c *OpenLibraryClient) Name() string {
	return "Open Library"
}

// SetOLStore attaches a local Open Library dump store for local-first lookups.
func (c *OpenLibraryClient) SetOLStore(store *openlibrary.OLStore) {
	c.olStore = store
}

// editionToMetadata converts an OLEdition to BookMetadata.
func editionToMetadata(ed *openlibrary.OLEdition, store *openlibrary.OLStore) BookMetadata {
	meta := BookMetadata{
		Title: ed.Title,
	}
	// Capture BOTH ISBN types (the edition carries separate arrays); the single
	// ISBN is kept for back-compat, preferring 13.
	if len(ed.ISBN13) > 0 {
		meta.ISBN13 = ed.ISBN13[0]
	}
	if len(ed.ISBN10) > 0 {
		meta.ISBN10 = ed.ISBN10[0]
	}
	switch {
	case meta.ISBN13 != "":
		meta.ISBN = meta.ISBN13
	case meta.ISBN10 != "":
		meta.ISBN = meta.ISBN10
	}
	if len(ed.Publishers) > 0 {
		meta.Publisher = ed.Publishers[0]
	}
	if len(ed.Covers) > 0 {
		meta.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", ed.Covers[0])
	}
	if store != nil && len(ed.Authors) > 0 {
		author, err := store.LookupAuthor(ed.Authors[0].Key)
		if err == nil && author != nil {
			meta.Author = author.Name
		}
	}
	meta.PublishYear = yearFromDate(ed.PublishDate)
	meta.Language = olLanguageFromRefs(ed.Languages)
	switch d := ed.Description.(type) {
	case string:
		meta.Description = strings.TrimSpace(d)
	case map[string]any:
		if v, ok := d["value"].(string); ok {
			meta.Description = strings.TrimSpace(v)
		}
	}
	return meta
}

// SearchResult represents a book search result from Open Library (search.json
// docs). Every field here is named in openLibrarySearchFields so the response
// carries it whatever Open Library's default field set is at the time.
type SearchResult struct {
	Title               string   `json:"title"`
	Subtitle            string   `json:"subtitle"`
	AuthorName          []string `json:"author_name"`
	FirstPublishYear    int      `json:"first_publish_year"`
	ISBN                []string `json:"isbn"`
	Publisher           []string `json:"publisher"`
	Language            []string `json:"language"`
	CoverI              int      `json:"cover_i"`
	EditionCount        int      `json:"edition_count"`
	NumberOfPagesMedian int      `json:"number_of_pages_median"`
}

// openLibrarySearchFields is the explicit search.json field list. Without it
// the response carries Open Library's default set, which is not a contract.
const openLibrarySearchFields = "key,title,subtitle,author_name,first_publish_year,isbn,publisher,language,cover_i,edition_count,number_of_pages_median"

// SearchResponse represents the API response from Open Library search
type SearchResponse struct {
	NumFound int            `json:"numFound"`
	Start    int            `json:"start"`
	Docs     []SearchResult `json:"docs"`
}

// BookMetadata represents enriched book metadata
type BookMetadata struct {
	Title          string
	Author         string
	Narrator       string
	Description    string
	Publisher      string
	PublishYear    int
	ISBN           string
	ASIN           string
	CoverURL       string
	Language       string
	Genre          string
	Series         string
	SeriesPosition string
	DurationSec    int // audio runtime in seconds (Audible: runtime_length_min × 60)

	// ISBN10 / ISBN13 preserve BOTH identifiers when a provider returns them.
	// The single ISBN above collapses to whichever the provider preferred, losing
	// the other; Book stores ISBN10 and ISBN13 in separate columns, so providers
	// that decode both (Google Books, Open Library, Hardcover) populate these and
	// the apply path writes each to its own column. ISBN stays as a fallback for
	// any provider/path that still sets only the single field.
	ISBN10 string
	ISBN13 string

	// Abridged is a tri-state identity signal from Audible's format_type
	// ("abridged" / "unabridged"): true = abridged, false = unabridged, nil =
	// unknown/not reported. An abridged edition is a different runtime and chapter
	// set from the unabridged one, so this discriminates editions during matching.
	Abridged *bool

	// Subtitle is the work's subtitle when the provider reports it separately from
	// the title (Audible, Audnexus). Book has no subtitle column today; carried as
	// signal for matching/provenance.
	Subtitle string

	// PageCount is the print page count (Hardcover). Weak identity signal, useful
	// for disambiguating editions.
	PageCount int

	// SeriesSecondary / SeriesSecondaryPosition hold a second series a book belongs
	// to (Audnexus seriesSecondary). The primary Series/SeriesPosition above is
	// unchanged; these capture the secondary membership that was previously dropped.
	SeriesSecondary         string
	SeriesSecondaryPosition string

	// PublishYearIsAudiobookRelease disambiguates the OVERLOADED PublishYear.
	// When true, PublishYear is the audiobook's release/issue year (Audible,
	// Audnexus). When false (the default), it is the work's original PRINT /
	// first-publication year (Open Library, Google Books, Hardcover, Wikipedia),
	// which is often decades earlier. ApplyMetadataToBook routes the year to the
	// correct Book field by this flag: a print/work year must NEVER be written to
	// AudiobookReleaseYear (it clobbers a correct release year and reaches the
	// file `year` tag via writeback), and a release year must never land in
	// PrintYear.
	PublishYearIsAudiobookRelease bool

	// Audible-specific ratings (1–5 scale). Performance and Story are
	// audiobook-specific dimensions not available from other sources.
	AudibleRatingOverall     float64
	AudibleRatingPerformance float64 // narrator/production quality
	AudibleRatingStory       float64 // story/content quality
	AudibleRatingCount       int     // number of star ratings
	AudibleNumReviews        int     // number of written reviews

	// Google Books rating (1–5 scale).
	GoogleRatingAverage float64
	GoogleRatingCount   int

	// CategoryTags contains genre/subject tags from Audible's category_ladders
	// response group. Each element is a ladder node name (e.g. "Science Fiction",
	// "Space Opera"). Applied as book_tags with source="audible_category".
	CategoryTags []string
}

// unambiguousLanguage returns the single distinct (case-insensitive) value in
// vals, or "" when vals is empty or holds differing values. Open Library search
// docs return Language as an UNORDERED aggregate across every edition, so
// Language[0] is not "most relevant" — a book whose first-listed edition is a
// translation would be mislabeled. That value is persisted as a
// metadata:language:<code> system tag driving the review language filter, so a
// guessed language is actively harmful: when ambiguous we set none, because no
// tag beats a wrong one.
func unambiguousLanguage(vals []string) string {
	first := ""
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if first == "" {
			first = v
		} else if !strings.EqualFold(v, first) {
			return "" // ambiguous — differing languages across editions
		}
	}
	return first
}

// SearchByTitle searches for books by title. Checks local dump store first if available.
func (c *OpenLibraryClient) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	if c.olStore != nil {
		editions, err := c.olStore.SearchByTitle(title)
		if err == nil && len(editions) > 0 {
			results := make([]BookMetadata, 0, len(editions))
			for i := range editions {
				results = append(results, editionToMetadata(&editions[i], c.olStore))
			}
			slog.Debug("SearchByTitle found results from local dump", "resultsCount", len(results), "title", title)
			return results, nil
		}
	}

	// Fall back to API
	query := url.QueryEscape(title)
	searchURL := fmt.Sprintf("%s/search.json?title=%s&limit=5&fields=%s", c.baseURL, query, openLibrarySearchFields)

	// Make HTTP request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search Open Library: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, StatusError(SourceIDOpenLibrary, resp)
	}

	// Parse response
	var searchResp SearchResponse
	if err := json.UnmarshalRead(resp.Body, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	return searchDocsToMetadata(searchResp.Docs), nil
}

// SearchByTitleAndAuthor searches for books by title and author
func (c *OpenLibraryClient) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	// Build search query
	titleQuery := url.QueryEscape(title)
	authorQuery := url.QueryEscape(author)
	searchURL := fmt.Sprintf("%s/search.json?title=%s&author=%s&limit=5&fields=%s", c.baseURL, titleQuery, authorQuery, openLibrarySearchFields)

	// Make HTTP request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search Open Library: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, StatusError(SourceIDOpenLibrary, resp)
	}

	// Parse response
	var searchResp SearchResponse
	if err := json.UnmarshalRead(resp.Body, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	return searchDocsToMetadata(searchResp.Docs), nil
}

// GetBookByISBN fetches book details by ISBN. Checks local dump store first if available.
func (c *OpenLibraryClient) GetBookByISBN(ctx context.Context, isbn string) (*BookMetadata, error) {
	if c.olStore != nil {
		ed, err := c.olStore.LookupByISBN(isbn)
		if err == nil && ed != nil {
			meta := editionToMetadata(ed, c.olStore)
			slog.Debug("GetBookByISBN found ISBN in local dump", "isbn", isbn)
			return &meta, nil
		}
	}

	// Fall back to API
	apiURL := fmt.Sprintf("%s/isbn/%s.json", c.baseURL, isbn)

	// Make HTTP request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch book by ISBN: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("book not found with ISBN: %s", isbn)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, StatusError(SourceIDOpenLibrary, resp)
	}

	var ed olEditionAPI
	if err := json.UnmarshalRead(resp.Body, &ed); err != nil {
		return nil, fmt.Errorf("failed to decode book response: %w", err)
	}
	metadata := ed.toMetadata(isbn)
	return &metadata, nil
}

// olEditionAPI is the edition JSON served by /isbn/{isbn}.json (and
// /books/{olid}.json). Authors are only {key} references there, so author
// names are not available from this response without a second request.
type olEditionAPI struct {
	Title         string              `json:"title"`
	Subtitle      string              `json:"subtitle"`
	Publishers    []string            `json:"publishers"`
	PublishDate   string              `json:"publish_date"`
	NumberOfPages int                 `json:"number_of_pages"`
	Covers        []int               `json:"covers"`
	Series        []string            `json:"series"`
	Languages     []openlibrary.OLRef `json:"languages"`
	Contributors  []olContributor     `json:"contributors"`
	ISBN10        []string            `json:"isbn_10"`
	ISBN13        []string            `json:"isbn_13"`
	Description   olText              `json:"description"`
}

// olContributor is one edition contributors[] entry: {"role": "Translator",
// "name": "Jane Doe"}.
type olContributor struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

// olText decodes Open Library's text fields, which are either a bare string
// or {"type": "/type/text", "value": "..."}.
type olText string

// UnmarshalJSON accepts both shapes; anything else decodes as empty.
func (t *olText) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = olText(s)
		return nil
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &obj); err == nil {
		*t = olText(obj.Value)
		return nil
	}
	*t = ""
	return nil
}

func (ed olEditionAPI) toMetadata(queriedISBN string) BookMetadata {
	meta := BookMetadata{
		ISBN:        queriedISBN,
		Title:       strings.TrimSpace(ed.Title),
		Subtitle:    strings.TrimSpace(ed.Subtitle),
		PublishYear: yearFromDate(ed.PublishDate),
		PageCount:   ed.NumberOfPages,
		Description: strings.TrimSpace(string(ed.Description)),
	}
	if len(ed.ISBN13) > 0 {
		meta.ISBN13 = ed.ISBN13[0]
	}
	if len(ed.ISBN10) > 0 {
		meta.ISBN10 = ed.ISBN10[0]
	}
	// One edition's publishers are that edition's imprint(s): the first is the
	// publisher of record, unlike search.json's cross-edition aggregate.
	if len(ed.Publishers) > 0 {
		meta.Publisher = strings.TrimSpace(ed.Publishers[0])
	}
	if len(ed.Covers) > 0 && ed.Covers[0] > 0 {
		meta.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", ed.Covers[0])
	}
	meta.Language = olLanguageFromRefs(ed.Languages)
	// A single series entry is unambiguous; several are different series (or
	// the same one spelled twice) and picking one would be a guess.
	if len(ed.Series) == 1 {
		meta.Series, meta.SeriesPosition = parseOLSeries(ed.Series[0])
	}
	// Contributors carry roles. Narrators go to Narrator; editors,
	// translators, illustrators etc. are dropped -- none of them is an author.
	var narrators []string
	for _, c := range ed.Contributors {
		name := strings.TrimSpace(c.Name)
		if name != "" && ClassifyContributorRoleName(c.Role) == RoleNarrator {
			narrators = append(narrators, name)
		}
	}
	if len(narrators) > 0 {
		meta.Narrator = strings.Join(narrators, ", ")
	}
	return meta
}

// searchDocsToMetadata converts search.json docs to BookMetadata. Shared by
// SearchByTitle and SearchByTitleAndAuthor, which used to carry two copies of
// this loop that differed only in whether every author or just the first was
// kept (now: every author, role-filtered).
func searchDocsToMetadata(docs []SearchResult) []BookMetadata {
	results := make([]BookMetadata, 0, len(docs))
	for _, doc := range docs {
		meta := BookMetadata{
			Title:       strings.TrimSpace(doc.Title),
			Subtitle:    strings.TrimSpace(doc.Subtitle),
			PublishYear: doc.FirstPublishYear,
			PageCount:   doc.NumberOfPagesMedian,
		}
		// author_name is plain strings; a credit can still carry its role in
		// the text. Authors only in Author; a narrator goes to Narrator.
		authors, narrators := PartitionCredits(doc.AuthorName)
		if len(authors) > 0 {
			meta.Author = strings.Join(authors, ", ")
		}
		if len(narrators) > 0 {
			meta.Narrator = strings.Join(narrators, ", ")
		}
		// publisher is an UNORDERED aggregate across every edition, like
		// language: publisher[0] is whichever edition Solr listed first, so a US
		// book could be credited to its UK or translated edition's publisher.
		// Set it only when the editions agree.
		meta.Publisher = unambiguousLanguage(doc.Publisher)

		// doc.ISBN is a single mixed array; classify by length so both ISBN types
		// are preserved. The single ISBN is kept for back-compat (prefer 13).
		for _, isbn := range doc.ISBN {
			switch len(isbn) {
			case 13:
				if meta.ISBN13 == "" {
					meta.ISBN13 = isbn
				}
			case 10:
				if meta.ISBN10 == "" {
					meta.ISBN10 = isbn
				}
			}
		}
		switch {
		case meta.ISBN13 != "":
			meta.ISBN = meta.ISBN13
		case meta.ISBN10 != "":
			meta.ISBN = meta.ISBN10
		case len(doc.ISBN) > 0:
			meta.ISBN = doc.ISBN[0]
		}

		// Only set language when the editions agree — see unambiguousLanguage.
		meta.Language = unambiguousLanguage(doc.Language)

		if doc.CoverI > 0 {
			meta.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", doc.CoverI)
		}
		results = append(results, meta)
	}
	return results
}

// fourDigitYear finds a standalone 4-digit year: not part of a longer number,
// but allowed right after a letter ("c1999", the copyright-date form).
var fourDigitYear = regexp.MustCompile(`(?:^|[^0-9])(1[0-9]{3}|20[0-9]{2})(?:[^0-9]|$)`)

// yearFromDate extracts the year from an Open Library publish_date, which is
// free text: "1937", "1937-09-21", "September 21, 1937", "Sep 1937". The old
// fmt.Sscanf("%d") read only a LEADING number, so every "Month D, YYYY" date
// produced 0 (or the day of the month).
func yearFromDate(s string) int {
	m := fourDigitYear.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	return y
}

// olLanguageFromRefs turns [{"key": "/languages/eng"}] into "eng" -- the same
// MARC code search.json's language field carries -- only when the edition
// names exactly one distinct language.
func olLanguageFromRefs(refs []openlibrary.OLRef) string {
	codes := make([]string, 0, len(refs))
	for _, r := range refs {
		codes = append(codes, strings.TrimPrefix(strings.TrimSpace(r.Key), "/languages/"))
	}
	return unambiguousLanguage(codes)
}

var olSeriesSep = regexp.MustCompile(`^(.*?)\s*(?:;|#|,\s*(?:bk|book|no|vol|v)\.?)\s*(?:(?:bk|book|no|vol|v|volume)\.?\s*)?([0-9]+(?:\.[0-9]+)?)\s*\)?\s*$`)

// parseOLSeries splits an edition series string ("Mistborn ; bk. 1",
// "The Expanse ; 3", "(Discworld #5)") into name and position. With no
// explicit separator the whole string is the name and there is no position:
// a bare trailing number ("Fahrenheit 451") is not guessed at.
func parseOLSeries(raw string) (name, position string) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "("), ")"))
	if m := olSeriesSep.FindStringSubmatch(s); m != nil && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1]), m[2]
	}
	return s, ""
}
