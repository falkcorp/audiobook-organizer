// file: internal/metafetch/source_rank.go
// version: 1.1.0
// guid: c2618c2e-2a64-4761-bc76-c9bbd65b3792
// last-edited: 2026-09-27
//
// The one table that says which metadata provider is better than which.
// Owner decision 2026-09-27: the nightly metadata upgrade REPLACES a book's
// metadata when it came from a weaker source and a stronger source has a
// candidate that passes the bulk-apply gate (books filled from Open Library
// get Audible's fields). The upgrade job reads this table for both "which
// books are eligible" and "which candidate may replace them", so there is no
// second list to drift.

package metafetch

import (
	"sort"
	"strings"
)

// Source slugs, as MetadataSourceSlug produces them from each provider's
// metadata.MetadataSource.Name() ("Audible", "Audnexus (Audible)",
// "Hardcover", "Open Library", "Google Books", "Wikipedia"). They are also the
// <slug> in the book's metadata:source:<slug> tag.
const (
	SourceSlugAudible     = "audible"
	SourceSlugAudnexus    = "audnexus"
	SourceSlugHardcover   = "hardcover"
	SourceSlugOpenLibrary = "open_library"
	SourceSlugGoogleBooks = "google_books"
	SourceSlugWikipedia   = "wikipedia"
)

// sourceRanks ranks every metadata provider; higher is better. Audible and
// Audnexus (which serves Audible's catalog) share the top rank, so neither
// replaces the other. A slug missing from this table is unranked: it is never
// replaced and never replaces anything.
var sourceRanks = map[string]int{
	SourceSlugAudible:     50,
	SourceSlugAudnexus:    50,
	SourceSlugHardcover:   40,
	SourceSlugOpenLibrary: 30,
	SourceSlugGoogleBooks: 20,
	SourceSlugWikipedia:   10,
}

// sourceSlugAliases maps spellings of a slug that older code or docs used to
// the slug the tag writer produces. The tag writer (MetadataSourceTag) has
// always written open_library / google_books; these only keep a stray
// hand-written tag from reading as unranked.
var sourceSlugAliases = map[string]string{
	"openlibrary": SourceSlugOpenLibrary,
	"googlebooks": SourceSlugGoogleBooks,
}

// LowQualitySourceMaxRank is the highest rank the metadata upgrade treats as
// "low quality": books whose source ranks at or below it are visited. It is
// Open Library's rank, so hardcover books are NOT visited (a hardcover book is
// only ever upgraded by a hand-picked or review-lane apply).
var LowQualitySourceMaxRank = sourceRanks[SourceSlugOpenLibrary]

// MetadataSourceSlug turns a provider's display name ("Open Library",
// "Audnexus (Audible)") or an existing slug into the slug used in the
// metadata:source:<slug> tag and in the rank table. It returns "" for an
// empty name.
func MetadataSourceSlug(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Audnexus's name carries its upstream in parentheses; the slug names the
	// provider, not the upstream (Audible has its own slug).
	if strings.HasPrefix(strings.ToLower(name), "audnexus") {
		return SourceSlugAudnexus
	}
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = strings.ReplaceAll(slug, "(", "")
	slug = strings.ReplaceAll(slug, ")", "")
	slug = strings.ReplaceAll(slug, "-", "_")
	if canon, ok := sourceSlugAliases[slug]; ok {
		return canon
	}
	return slug
}

// SourceRank returns the rank of a source (display name or slug) and whether
// it is ranked at all.
func SourceRank(source string) (int, bool) {
	r, ok := sourceRanks[MetadataSourceSlug(source)]
	return r, ok
}

// SourceOutranks reports whether candidateSource may replace metadata that
// came from currentSource: both must be ranked and the candidate's rank must
// be strictly higher. An unranked current source is never replaced, and a
// candidate from the same or a lower rank never replaces anything.
func SourceOutranks(candidateSource, currentSource string) bool {
	cur, ok := SourceRank(currentSource)
	if !ok {
		return false
	}
	cand, ok := SourceRank(candidateSource)
	if !ok {
		return false
	}
	return cand > cur
}

// LowQualitySourceSlugs returns the slugs ranked at or below
// LowQualitySourceMaxRank, highest rank first (open_library, google_books,
// wikipedia). Derived from sourceRanks; there is no second list. The order
// only decides which source a book is attributed to should it ever carry two
// tags; the metadata upgrade visits books in book-ID order from a persisted
// sweep cursor, not source by source.
func LowQualitySourceSlugs() []string {
	var out []string
	for slug, r := range sourceRanks {
		if r <= LowQualitySourceMaxRank {
			out = append(out, slug)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if sourceRanks[out[i]] != sourceRanks[out[j]] {
			return sourceRanks[out[i]] > sourceRanks[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
