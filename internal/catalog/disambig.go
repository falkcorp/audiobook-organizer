// file: internal/catalog/disambig.go
// version: 1.0.0
// guid: 9a1c6e38-2f4b-4d7a-8c05-6b3e1d9f2a87
// last-edited: 2026-10-01

package catalog

import (
	"context"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Audible's author= filter is a NAME search, so a listing can mix homonyms
// (two different "John Smith"s) and anthologies that merely include the
// name. Disambiguation (R10) works from the owner's own books: every owned,
// ASIN-tagged book by this author names, on its provider product, the
// author's Audible author ASIN.

// Resolution is what the owner's books say about an author's identity.
type Resolution struct {
	// AuthorASINs are the distinct author ASINs found, sorted.
	AuthorASINs []string
	// Conflict: more than one author ASIN. Never resolved by picking one;
	// recorded on the author state as a census item.
	Conflict bool
	// Lookups is how many product lookups were made; LookupErrors how many
	// failed. A failed lookup leaves the resolution incomplete, and the
	// caller records the author as partial so it is retried.
	Lookups      int
	LookupErrors int
}

// LookupFunc fetches one product by ASIN.
type LookupFunc func(ctx context.Context, asin string) (*metadata.CatalogProduct, error)

// ResolveAuthorASINs reads the author ASIN for name off each owned ASIN's
// product. Products the listing already returned are read for free; only the
// owned ASINs absent from it cost a lookup.
//
// Only the credit whose folded name equals ours is read. Taking every author
// ASIN on an owned co-authored product would pull the co-author's whole solo
// catalog into this author's harvest.
func ResolveAuthorASINs(ctx context.Context, name string, ownedASINs []string, fetched map[string]metadata.CatalogProduct, lookup LookupFunc) (Resolution, error) {
	fold := authorjunk.FoldKey(name)
	var res Resolution
	add := func(p *metadata.CatalogProduct) {
		for _, a := range p.Authors {
			if a.ASIN == "" || authorjunk.FoldKey(a.Name) != fold {
				continue
			}
			asin := strings.ToUpper(a.ASIN)
			if !slices.Contains(res.AuthorASINs, asin) {
				res.AuthorASINs = append(res.AuthorASINs, asin)
			}
		}
	}
	for _, owned := range ownedASINs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if p, ok := fetched[owned]; ok {
			add(&p)
			continue
		}
		if lookup == nil {
			continue
		}
		res.Lookups++
		p, err := lookup(ctx, owned)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.LookupErrors++
			continue
		}
		add(p)
	}
	slices.Sort(res.AuthorASINs)
	res.Conflict = len(res.AuthorASINs) > 1
	return res, nil
}

// Decision is the verdict on one listed product for one harvested author.
type Decision struct {
	Keep     bool
	NameOnly bool
	Conflict bool
}

// Decide applies the resolution to one product.
//
//   - With author ASINs: keep a product crediting any of them. A product
//     crediting the name with NO ASIN is kept, flagged name_only. A product
//     crediting the name under a different ASIN is a homonym: dropped.
//   - With none (the owner has no ASIN-tagged book by this author): keep
//     products crediting the name, all flagged name_only.
//   - A product not crediting the name at all is dropped either way.
func Decide(p metadata.CatalogProduct, name string, res Resolution) Decision {
	fold := authorjunk.FoldKey(name)
	nameNoASIN := false
	for _, a := range p.Authors {
		asin := strings.ToUpper(strings.TrimSpace(a.ASIN))
		if asin != "" && slices.Contains(res.AuthorASINs, asin) {
			return Decision{Keep: true, Conflict: res.Conflict}
		}
		if authorjunk.FoldKey(a.Name) != fold {
			continue
		}
		if asin == "" || len(res.AuthorASINs) == 0 {
			nameNoASIN = true
		}
	}
	if nameNoASIN {
		return Decision{Keep: true, NameOnly: true, Conflict: res.Conflict}
	}
	return Decision{}
}

// LanguageMatches reports whether a product's language passes the harvest's
// language filter. An empty provider language is kept (the field is
// sometimes missing); a different one is not.
func LanguageMatches(productLang, want string) bool {
	w := strings.ToLower(strings.TrimSpace(want))
	if w == "" {
		return true
	}
	pl := strings.ToLower(strings.TrimSpace(productLang))
	return pl == "" || pl == w
}
