// file: internal/books/adapters/metadata/adapters.go
// version: 0.1.0
// guid: 22f8603e-ab2b-4e24-a253-62da22c40a9c
// last-edited: 2026-11-20

// Package metadata is books' six metadata adapters for the integrations flip
// (F7-09 T5): books.audible, books.audnexus, books.google-books,
// books.hardcover, books.openlibrary and books.wikipedia, each HEAD's client
// behind core's metadata.Provider contract (05-metadata M3.10), and the chain
// constructor that replaces HEAD's `switch src.ID`.
//
// It is dark. Nothing registers the descriptors until the flip hands them to
// Registrar.Adapter, and HEAD's chain (internal/metafetch) stays the one in
// force; this package has no caller.
//
// The adapters take everything HEAD's clients read from process globals from
// the instance instead: the HTTP client is Env.HTTP (core's gate, not
// providerhttp.Client), the base URL is the base_url setting, and a credential
// is read from Env.Secrets at the moment of each request, so a rotated key takes
// effect without a rebuild and an unreadable one fails the request instead of
// sending it anonymously.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	cmeta "github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/secrets"

	headmeta "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
)

// Query is what a books metadata search asks: HEAD's SearchContext, the title,
// author, narrator, ISBNs, ASIN and series of one book (05-metadata M3.10).
type Query = headmeta.SearchContext

// Result is one candidate record: HEAD's BookMetadata.
type Result = headmeta.BookMetadata

// The adapter IDs: "books." and the source ID HEAD's config gives the row
// (headmeta.SourceID*). M6 writes an instance's adapter as "books."+its
// instance ID, so these are the instance IDs HEAD's six defaults keep.
const (
	Audible     integrations.AdapterID = "books." + headmeta.SourceIDAudible
	Audnexus    integrations.AdapterID = "books." + headmeta.SourceIDAudnexus
	GoogleBooks integrations.AdapterID = "books." + headmeta.SourceIDGoogleBooks
	Hardcover   integrations.AdapterID = "books." + headmeta.SourceIDHardcover
	OpenLibrary integrations.AdapterID = "books." + headmeta.SourceIDOpenLibrary
	Wikipedia   integrations.AdapterID = "books." + headmeta.SourceIDWikipedia
)

// The settings fields. base_url is the one destination; api_key and api_token
// are secret fields, which core requires to be top-level properties named by a
// secret-name segment ([a-z][a-z0-9_-]*): M6's dotted credentials.<k> settings
// cannot be declared here (see the package tests).
const (
	fieldBaseURL  = "base_url"
	fieldAPIKey   = "api_key"
	fieldAPIToken = "api_token"
)

// urlSettings are the settings of a source that takes only a base URL. HEAD's
// hardcover and wikipedia clients never read one, so for those two the field is
// accepted (M6 writes it whenever a stored source row has one) and not used.
type urlSettings struct {
	BaseURL string `json:"base_url,omitzero" schema:"format=uri"`
}

// googleBooksSettings add the optional API key: Google Books answers without one
// at a lower quota, as HEAD's client did.
type googleBooksSettings struct {
	BaseURL string      `json:"base_url,omitzero" schema:"format=uri"`
	APIKey  secrets.Ref `json:"api_key,omitzero"`
}

// hardcoverSettings add the API token, required: HEAD left a Hardcover source
// without one out of its chain.
type hardcoverSettings struct {
	BaseURL  string      `json:"base_url,omitzero" schema:"format=uri"`
	APIToken secrets.Ref `json:"api_token"`
}

// source is one adapter's static description.
type source struct {
	id      integrations.AdapterID
	display string
	info    string
	schema  func() (config.Schema, error)
	egress  integrations.EgressPolicy
	limits  integrations.Limits
	accepts func(Query) bool
	newInst func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error)
}

// Options are what the adapters need from the app that no instance setting can
// carry.
type Options struct {
	// OLStore is books.openlibrary's local dump store, as HEAD's
	// OpenLibraryClient.SetOLStoreSource took it: called on every lookup, nil
	// while there is no usable store. nil: every lookup goes to the network.
	OLStore func() *openlibrary.OLStore
}

// Descriptors are the six adapters for Registrar.Adapter, in HEAD's default
// priority order. It returns an error only if a settings type cannot be
// reflected, which a test rules out.
func Descriptors(opts Options) ([]integrations.Descriptor, error) {
	srcs := sources(opts)
	out := make([]integrations.Descriptor, 0, len(srcs))
	for _, s := range srcs {
		schema, err := s.schema()
		if err != nil {
			return nil, fmt.Errorf("metadata adapter %s: settings schema: %w", s.id, err)
		}
		out = append(out, cmeta.Adapter(integrations.Meta{
			ID:          s.id,
			DisplayName: s.display,
			Version:     "1.0.0",
			InfoURL:     s.info,
			Settings:    schema,
			Egress:      s.egress,
			Limits:      s.limits,
		}, s.build))
	}
	return out, nil
}

// build makes one instance. It does no I/O: no request is made until Search.
func (s source) build(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (cmeta.Provider[Query, Result], error) {
	p, err := s.newInst(env, cfg)
	if err != nil {
		return nil, err
	}
	p.ref, p.accepts = env.Ref, s.accepts
	if p.wrap != nil {
		return p.wrap(p), nil
	}
	return p, nil
}

// httpClient is the instance's gated client, or an error when the Registry gave
// none (every request would be refused anyway).
func httpClient(env integrations.Env) (*http.Client, error) {
	if env.HTTP == nil {
		return nil, errors.New("metadata adapter: the instance has no HTTP client")
	}
	return env.HTTP.Client(), nil
}

// baseURL is the instance's base_url setting, "" for the client's default.
func baseURL(cfg integrations.InstanceConfig) string {
	s, _ := cfg.Settings[fieldBaseURL].(string)
	return s
}

// hasSecret is whether the instance's effective settings carry a ref for the
// secret field (a stored one, or the operator's value core binds as one).
func hasSecret(cfg integrations.InstanceConfig, field string) bool {
	_, ok := cfg.Settings[field].(secrets.Ref)
	return ok
}

// secretValue reads the secret field at request time. optional is for a field
// the instance does not set at all: it reads as the empty string (HEAD's
// anonymous Google Books). A field the instance does set, whose secret is
// unreadable, unset in the store or withheld, is always an error: sending
// anonymously instead would spend the shared quota and look like a working key.
func secretValue(ctx context.Context, env integrations.Env, field string, optional bool) (string, error) {
	v, err := env.Secrets.Secret(ctx, field)
	switch {
	case err == nil:
		return v.Reveal(), nil
	case optional && errors.Is(err, integrations.ErrNotConfigured):
		return "", nil
	}
	return "", err
}

// titleOrID is HEAD's default accepts: a query with a title is askable.
func titleOrID(q Query) bool {
	return q.Title != "" || q.ASIN != "" || q.ISBN13 != "" || q.ISBN10 != ""
}

// titled is for sources HEAD only ever asked by title.
func titled(q Query) bool { return q.Title != "" }

// byASIN is Audnexus: it has no title search (its SearchByTitle answers nothing
// and its author search never finds a book), so only a query carrying an ASIN is
// worth a request against its 2 rps budget.
func byASIN(q Query) bool { return q.ASIN != "" }

// limits is HEAD's budget for a provider (providerhttp defaultLimits), with core's
// Timeout, which is the per-attempt bound after the bucket wait, where HEAD
// had one (AttemptTimeout: Audnexus 10 s) and HEAD's Timeout otherwise.
func limits(rps float64, burst int, retries int, timeout time.Duration) integrations.Limits {
	return integrations.Limits{RPS: rps, Burst: burst, MaxRetries: retries, Timeout: timeout}
}

// sources is the six adapters in HEAD's default priority order (config.go
// default metadata_sources).
func sources(opts Options) []source {
	return []source{
		{
			id: Audible, display: "Audible", info: "https://api.audible.com/1.0/catalog/products",
			schema: config.SchemaFor[urlSettings], limits: limits(2, 2, 3, 30*time.Second), accepts: titled,
			newInst: func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error) {
				hc, err := httpClient(env)
				if err != nil {
					return nil, err
				}
				c := headmeta.NewAudibleClientFrom(hc, baseURL(cfg))
				return &provider{src: func(context.Context) (headmeta.MetadataSource, error) { return c, nil }, wrap: func(p *provider) cmeta.Provider[Query, Result] { return &audibleProvider{provider: p, client: c} }}, nil
			},
		},
		{
			id: OpenLibrary, display: "Open Library", info: "https://openlibrary.org/developers/api",
			schema: config.SchemaFor[urlSettings], limits: limits(3, 3, 3, 30*time.Second), accepts: titled,
			newInst: func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error) {
				hc, err := httpClient(env)
				if err != nil {
					return nil, err
				}
				c := headmeta.NewOpenLibraryClientFrom(hc, baseURL(cfg))
				if opts.OLStore != nil {
					c.SetOLStoreSource(opts.OLStore)
				}
				return &provider{src: func(context.Context) (headmeta.MetadataSource, error) { return c, nil }}, nil
			},
		},
		{
			id: Audnexus, display: "Audnexus (Audible)", info: "https://api.audnex.us/",
			schema: config.SchemaFor[urlSettings], limits: limits(2, 2, 3, 10*time.Second), accepts: byASIN,
			newInst: func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error) {
				hc, err := httpClient(env)
				if err != nil {
					return nil, err
				}
				c := headmeta.NewAudnexusClientFrom(hc, baseURL(cfg))
				return &provider{src: func(context.Context) (headmeta.MetadataSource, error) { return c, nil }}, nil
			},
		},
		{
			id: GoogleBooks, display: "Google Books", info: "https://developers.google.com/books/docs/v1/using",
			schema: config.SchemaFor[googleBooksSettings], limits: googleBooksLimits(), accepts: titled,
			newInst: func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error) {
				hc, err := httpClient(env)
				if err != nil {
					return nil, err
				}
				return &provider{src: func(ctx context.Context) (headmeta.MetadataSource, error) {
					key, err := secretValue(ctx, env, fieldAPIKey, !hasSecret(cfg, fieldAPIKey))
					if err != nil {
						return nil, err
					}
					return headmeta.NewGoogleBooksClientFrom(hc, baseURL(cfg), key), nil
				}}, nil
			},
		},
		{
			egress: integrations.EgressPublicOnly,
			id:     Hardcover, display: "Hardcover", info: "https://docs.hardcover.app/api/getting-started/",
			schema: config.SchemaFor[hardcoverSettings], limits: limits(1, 1, 3, 30*time.Second), accepts: titleOrID,
			newInst: func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error) {
				hc, err := httpClient(env)
				if err != nil {
					return nil, err
				}
				return &provider{src: func(ctx context.Context) (headmeta.MetadataSource, error) {
					tok, err := secretValue(ctx, env, fieldAPIToken, false)
					if err != nil {
						return nil, err
					}
					return headmeta.NewHardcoverClientFrom(hc, tok), nil
				}}, nil
			},
		},
		{
			egress: integrations.EgressPublicOnly,
			id:     Wikipedia, display: "Wikipedia", info: "https://www.mediawiki.org/wiki/API:Main_page",
			schema: config.SchemaFor[urlSettings], limits: limits(2, 2, 3, 30*time.Second), accepts: titled,
			newInst: func(env integrations.Env, cfg integrations.InstanceConfig) (*provider, error) {
				hc, err := httpClient(env)
				if err != nil {
					return nil, err
				}
				c := headmeta.NewWikipediaClientFrom(hc)
				return &provider{src: func(context.Context) (headmeta.MetadataSource, error) { return c, nil }}, nil
			},
		},
	}
}

// googleBooksLimits are HEAD's pace plus its shared daily budget (owner decision
// 2026-10-06): the key's 1,000 lookups a day, background lookups stopping at 800
// so 200 stay for a person waiting on the search dialog.
func googleBooksLimits() integrations.Limits {
	l := limits(3, 3, 3, 30*time.Second)
	l.Daily = &integrations.DailyLimits{
		Total:      headmeta.DefaultGoogleBooksDailyLimit,
		Background: headmeta.DefaultGoogleBooksBackgroundDailyLimit,
	}
	return l
}
