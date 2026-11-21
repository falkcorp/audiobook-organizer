// file: internal/books/adapters/metadata/provider.go
// version: 0.1.0
// guid: 8824e10a-62e5-4d3b-88b2-7932c918f6aa
// last-edited: 2026-11-20

package metadata

import (
	"context"
	"errors"

	"github.com/falkcorp/mediacore/integrations"
	cmeta "github.com/falkcorp/mediacore/integrations/metadata"

	headmeta "github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// provider is one metadata instance: a HEAD client (built per request when it
// carries a credential) behind Provider[Query, Result].
type provider struct {
	ref     integrations.Ref
	src     func(ctx context.Context) (headmeta.MetadataSource, error)
	accepts func(Query) bool
	// wrap, when set, is the provider the adapter returns instead of this one
	// (books.audible's, which adds the author listing).
	wrap func(*provider) cmeta.Provider[Query, Result]
}

var (
	_ cmeta.Provider[Query, Result] = (*provider)(nil)
	_ cmeta.Caps[Query]             = (*provider)(nil)
)

// Ref is the instance's identity.
func (p *provider) Ref() integrations.Ref { return p.ref }

// Close releases nothing: the connection pool belongs to the instance's HTTP.
func (*provider) Close() error { return nil }

// Health does not probe. A source has no cheap authenticated endpoint, every
// request spends its budget (Google Books' is 1,000 a day, and a probe per
// health interval would take some of it), and the Registry already reports a
// missing or unreadable required secret and an invalid setting before it calls
// this. A source that stops answering is reported by the chain's outcomes and
// the gate's holds and breaker.
func (*provider) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK, Message: "not probed: a request spends the source's budget"}
}

// Accepts says whether the query has what this source can ask by.
func (p *provider) Accepts(q Query) bool { return p.accepts(q) }

// Search is HEAD's per-source ladder for one query (internal/metafetch
// service_fetch.go): the context search first when the client has one (ASIN and
// ISBN lookups), then title with author when there is an author, then title alone,
// stopping at the first that returns records. The caller runs the title variants
// as separate searches.
//
// Where HEAD recorded only the title-only rung's error, a failure here is
// reported when no rung answered: an earlier rung's error is not forgotten
// because a later one returned nothing without an error. An empty answer from a
// rung that did not fail is an answer; the rung errors alone are not "no
// results" (05-metadata M3.5).
func (p *provider) Search(ctx context.Context, q Query) ([]Result, error) {
	src, err := p.src(ctx)
	if err != nil {
		return nil, err
	}
	var errs []error
	answered := false
	rung := func(rs []Result, err error) bool {
		if err != nil {
			errs = append(errs, err)
			return false
		}
		answered = true
		return len(rs) > 0
	}
	var rs []Result
	if cs, ok := src.(headmeta.ContextualSearch); ok {
		rs, err = cs.SearchByContext(ctx, &q)
		if rung(rs, err) {
			return rs, nil
		}
	}
	if q.Title != "" && q.Author != "" {
		rs, err = src.SearchByTitleAndAuthor(ctx, q.Title, q.Author)
		if rung(rs, err) {
			return rs, nil
		}
	}
	if q.Title != "" {
		rs, err = src.SearchByTitle(ctx, q.Title)
		if rung(rs, err) {
			return rs, nil
		}
	}
	if !answered {
		if len(errs) == 0 {
			return nil, nil // nothing was askable; Accepts keeps the chain from asking
		}
		return nil, errors.Join(errs...)
	}
	return nil, nil
}

// AuthorLister is the optional interface of books.audible (05-metadata M3.10):
// the author-catalog harvest asserts it on the leased provider. It is HEAD's
// interface, so the harvest's own code does not change.
type AuthorLister = headmeta.AuthorLister

// audibleProvider is books.audible's provider: the search provider plus the
// author listing. It is a distinct type so that only this adapter's provider
// satisfies AuthorLister, and the harvest's type assertion on any other
// source's provider fails, as it should.
type audibleProvider struct {
	*provider
	client *headmeta.AudibleClient
}

var _ AuthorLister = (*audibleProvider)(nil)

// ProviderID is the canonical provider id.
func (*audibleProvider) ProviderID() string { return headmeta.SourceIDAudible }

// ListByAuthor is one page of the author's products.
func (p *audibleProvider) ListByAuthor(ctx context.Context, name string, page, pageSize int) (headmeta.AuthorPage, error) {
	return p.client.ListByAuthor(ctx, name, page, pageSize)
}

// LookupProduct is one product by its provider id.
func (p *audibleProvider) LookupProduct(ctx context.Context, asin string) (*headmeta.CatalogProduct, error) {
	return p.client.LookupProduct(ctx, asin)
}
