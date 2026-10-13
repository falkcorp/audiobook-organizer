// file: internal/catalog/matcher.go
// version: 1.0.0
// guid: a3e5c812-7b4d-4f09-9c26-d18f0e6b7a35
// last-edited: 2026-10-12

package catalog

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Matcher is MatchBook bound to one catalog: what the metadata search holds
// (metafetch.CatalogMatcher) because it cannot import this package. It fills
// in what only the catalog knows -- the provider and marketplace the harvest
// files entries under, for the B1 point read -- and, from the authority lists,
// the one author ASIN a credit has when it has exactly one (the local
// evidence that settles a homonym block).
type Matcher struct {
	store       *database.CatalogStore
	marketplace func() string
	authorities authority.Reader
}

// NewMatcher binds st. marketplace reads catalog.marketplace at each match
// (the harvest reads it at each run; empty means the harvest's default), nil
// meaning the default; authorities may be nil (no author ASINs are supplied,
// so a homonym block is skipped unless the harvest state settles it).
func NewMatcher(st *database.CatalogStore, marketplace func() string, authorities authority.Reader) *Matcher {
	return &Matcher{store: st, marketplace: marketplace, authorities: authorities}
}

// MatchBook implements metafetch.CatalogMatcher.
func (m *Matcher) MatchBook(q metadata.CatalogQuestion) (metadata.CatalogMatch, error) {
	cfg := Settings{}
	if m.marketplace != nil {
		cfg.Marketplace = m.marketplace()
	}
	cfg.normalize() // the provider and marketplace the harvest files entries under
	q.Provider, q.Marketplace = cfg.Provider, cfg.Marketplace
	if len(q.AuthorASINs) == 0 && m.authorities != nil {
		q.AuthorASINs = m.authorASINs(q.Credits)
	}
	return MatchBook(m.store, q)
}

// authorASINs is, per credit, the credit's contributor ASIN in the authority
// lists when it has exactly one ("" otherwise, including a homonym and a read
// fault: no evidence). nil when no credit has one.
func (m *Matcher) authorASINs(credits []string) []string {
	idx := authority.NewIndex(m.authorities)
	out := make([]string, len(credits))
	any := false
	for i, c := range credits {
		e, err := idx.Lookup(c)
		if err != nil || e == nil || e.HomonymASINs || len(e.ASINs) != 1 {
			continue
		}
		out[i] = strings.ToUpper(strings.TrimSpace(e.ASINs[0]))
		any = any || out[i] != ""
	}
	if !any {
		return nil
	}
	return out
}
