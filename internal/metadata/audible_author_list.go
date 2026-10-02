// file: internal/metadata/audible_author_list.go
// version: 1.1.0
// guid: 6f2d9a14-3b7e-4c58-a1d0-8e5f7c2b9a63
// last-edited: 2026-10-01

package metadata

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AuthorLister lists every product a provider credits to an author name, one
// page at a time, for the author catalog harvest (catalog.harvest-authors).
//
// It is a separate interface from MetadataSource on purpose: MetadataSource
// answers "which product is this book?", returns BookMetadata (which has no
// author ASINs, no series ASIN, no language-as-filter semantics) and is
// consumed by the candidate pipeline. The harvest needs the provider's
// identity fields verbatim, so it gets its own product shape.
type AuthorLister interface {
	// ProviderID is the canonical provider id ("audible").
	ProviderID() string
	// ListByAuthor returns one page of products for an author NAME search.
	// page is 0-indexed. TotalResults is the provider's count for the whole
	// query, so a caller can stop without fetching an empty trailing page.
	ListByAuthor(ctx context.Context, name string, page, pageSize int) (AuthorPage, error)
	// LookupProduct fetches one product by its provider id, with the same
	// fields as a listing row. Used to read an owned book's author ASIN.
	LookupProduct(ctx context.Context, asin string) (*CatalogProduct, error)
}

// ErrCatalogProductNotFound: the provider has no product under that id (a
// 404, or a 200 with an empty product). It is an answer, not a failure: an
// owned ASIN Audible no longer sells gives no author identity, and retrying
// it will never change that. Callers must not count it as a lookup error.
var ErrCatalogProductNotFound = errors.New("catalog product not found")

// AuthorPage is one page of an author listing.
type AuthorPage struct {
	Products     []CatalogProduct
	TotalResults int
}

// CatalogContributor is an author or narrator credit on a product. ASIN is
// the provider's contributor id; Audible omits it for some credits (anthology
// contributors, older uploads), and an empty ASIN must be read as unknown,
// never as a mismatch.
type CatalogContributor struct {
	Name string
	ASIN string
}

// CatalogProductSeries is one series membership as the provider states it.
// Sequence is verbatim ("1", "2.5", "1-3", "" for a novella with no number).
type CatalogProductSeries struct {
	ASIN     string
	Title    string
	Sequence string
}

// CatalogProduct is one provider product, with the identity fields the
// catalog keeps. Raw is the product's own JSON object as received, kept so
// entries can be re-derived later without a refetch.
type CatalogProduct struct {
	ASIN                string
	Title               string
	Subtitle            string
	Authors             []CatalogContributor
	Narrators           []CatalogContributor
	Language            string
	FormatType          string
	ContentDeliveryType string
	RuntimeMin          int
	ReleaseDate         string
	Publisher           string
	Series              []CatalogProductSeries
	CoverURL            string
	Raw                 []byte
}

// audibleCatalogResponseGroups are the response groups an author listing
// asks for: contributors (author ASINs), product_attrs (language,
// format_type, runtime, release_date), series (title + sequence), media
// (cover). Verified against the live API 2026-10-01.
const audibleCatalogResponseGroups = "contributors,media,product_attrs,series"

// audibleRawCatalogResponse keeps every product as raw JSON so each one can be
// stored verbatim and decoded on its own.
type audibleRawCatalogResponse struct {
	Products     []jsontext.Value `json:"products"`
	TotalResults int              `json:"total_results"`
}

type audibleRawProductResponse struct {
	Product jsontext.Value `json:"product"`
}

var _ AuthorLister = (*AudibleClient)(nil)

// ListByAuthor implements AuthorLister. Audible's `page` is 0-indexed and
// `num_results` is capped at 50 by the API (verified 2026-10-01).
func (c *AudibleClient) ListByAuthor(ctx context.Context, name string, page, pageSize int) (AuthorPage, error) {
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	if page < 0 {
		page = 0
	}
	u := fmt.Sprintf("%s/catalog/products?author=%s&num_results=%d&page=%d&response_groups=%s",
		c.baseURL, url.QueryEscape(name), pageSize, page, audibleCatalogResponseGroups)
	body, err := c.getBody(ctx, u)
	if err != nil {
		return AuthorPage{}, fmt.Errorf("audible author listing %q page %d: %w", name, page, err)
	}
	var resp audibleRawCatalogResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return AuthorPage{}, fmt.Errorf("audible author listing %q page %d: decode: %w", name, page, err)
	}
	out := AuthorPage{TotalResults: resp.TotalResults, Products: make([]CatalogProduct, 0, len(resp.Products))}
	for i, raw := range resp.Products {
		p, err := decodeCatalogProduct(raw)
		if err != nil {
			return AuthorPage{}, fmt.Errorf("audible author listing %q page %d product %d: %w", name, page, i, err)
		}
		out.Products = append(out.Products, p)
	}
	return out, nil
}

// LookupProduct implements AuthorLister.
func (c *AudibleClient) LookupProduct(ctx context.Context, asin string) (*CatalogProduct, error) {
	u := fmt.Sprintf("%s/catalog/products/%s?response_groups=%s",
		c.baseURL, url.PathEscape(asin), audibleCatalogResponseGroups)
	body, err := c.getBody(ctx, u)
	if err != nil {
		var pse *ProviderStatusError
		if errors.As(err, &pse) && pse.Status == http.StatusNotFound {
			return nil, fmt.Errorf("audible product %s: %w", asin, ErrCatalogProductNotFound)
		}
		return nil, fmt.Errorf("audible product %s: %w", asin, err)
	}
	var resp audibleRawProductResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("audible product %s: decode: %w", asin, err)
	}
	if len(resp.Product) == 0 {
		return nil, fmt.Errorf("audible product %s: empty response: %w", asin, ErrCatalogProductNotFound)
	}
	p, err := decodeCatalogProduct(resp.Product)
	if err != nil {
		return nil, fmt.Errorf("audible product %s: %w", asin, err)
	}
	if p.ASIN == "" {
		return nil, fmt.Errorf("audible product %s: response has no asin", asin)
	}
	return &p, nil
}

// getBody issues a GET on the provider's throttled client and returns the
// body of a 200. Any other status is a ProviderStatusError so the throttle
// registry can classify it.
func (c *AudibleClient) getBody(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Audible/3.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, StatusError(SourceIDAudible, resp)
	}
	return io.ReadAll(resp.Body)
}

// decodeCatalogProduct turns one raw product object into a CatalogProduct.
func decodeCatalogProduct(raw jsontext.Value) (CatalogProduct, error) {
	var p audibleProduct
	if err := json.Unmarshal(raw, &p); err != nil {
		return CatalogProduct{}, fmt.Errorf("decode product: %w", err)
	}
	out := CatalogProduct{
		ASIN:                strings.TrimSpace(p.ASIN),
		Title:               strings.TrimSpace(p.Title),
		Subtitle:            strings.TrimSpace(p.Subtitle),
		Language:            strings.ToLower(strings.TrimSpace(p.Language)),
		FormatType:          strings.ToLower(strings.TrimSpace(p.FormatType)),
		ContentDeliveryType: strings.TrimSpace(p.ContentDeliveryType),
		ReleaseDate:         strings.TrimSpace(p.ReleaseDate),
		Publisher:           strings.TrimSpace(p.PublisherName),
		Raw:                 append([]byte(nil), raw...),
	}
	if out.ReleaseDate == "" {
		out.ReleaseDate = strings.TrimSpace(p.IssueDate)
	}
	if p.RuntimeLengthMin != nil && *p.RuntimeLengthMin > 0 {
		out.RuntimeMin = *p.RuntimeLengthMin
	}
	for _, a := range p.Authors {
		out.Authors = append(out.Authors, CatalogContributor{Name: strings.TrimSpace(a.Name), ASIN: strings.TrimSpace(a.ASIN)})
	}
	for _, n := range p.Narrators {
		out.Narrators = append(out.Narrators, CatalogContributor{Name: strings.TrimSpace(n.Name), ASIN: strings.TrimSpace(n.ASIN)})
	}
	for _, s := range p.Series {
		out.Series = append(out.Series, CatalogProductSeries{ASIN: strings.TrimSpace(s.ASIN), Title: strings.TrimSpace(s.Title), Sequence: strings.TrimSpace(s.Sequence)})
	}
	for _, size := range []string{"500", "252", "128"} {
		if img := p.ProductImages[size]; img != "" {
			out.CoverURL = img
			break
		}
	}
	return out, nil
}
