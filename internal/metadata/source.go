// file: internal/metadata/source.go
// version: 1.4.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-07-13

package metadata

import "context"

// SourceProducesAudiobookReleaseYear reports whether the named metadata source
// populates PublishYear with an audiobook RELEASE/issue year (Audible,
// Audnexus) rather than a print/work publication year. The candidate-apply path
// no longer has the source's BookMetadata (and its PublishYearIsAudiobookRelease
// flag) in scope — only the candidate's Source name survives — so it derives the
// year kind from the source name here. Uses the clients' own Name() values so it
// can never drift from what search stores in MetadataCandidate.Source.
func SourceProducesAudiobookReleaseYear(sourceName string) bool {
	switch sourceName {
	case (&AudibleClient{}).Name(), (&AudnexusClient{}).Name():
		return true
	default:
		return false
	}
}

// MetadataSource is a pluggable metadata provider.
type MetadataSource interface {
	Name() string
	SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error)
	SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error)
}

// SearchContext carries richer context than just title+author for
// sources that can use it. Primary use case: Audnexus can only look
// books up by ASIN, not by title — if the book already has an ASIN
// in our DB, the fetch service should use that instead of giving up
// on the source entirely. Hardcover can use ISBN for a direct match
// that's more precise than the fuzzy search_books endpoint.
//
// Every field is optional. Sources must treat an empty field as
// "unknown" and fall back to title/author search via the base
// interface.
type SearchContext struct {
	Title    string
	Author   string
	Narrator string
	ISBN10   string
	ISBN13   string
	ASIN     string
	Series   string
}

// ContextualSearch is an OPTIONAL interface a metadata source may
// implement to consume SearchContext. Sources that don't implement
// it just get called via the base SearchByTitle / SearchByTitleAndAuthor
// methods.
//
// When a source DOES implement it, the fetch service calls
// SearchByContext FIRST and only falls back to the title/author
// methods if SearchByContext returns no results. This lets Audnexus
// skip straight to LookupByASIN when we already have an ASIN, and
// lets Hardcover do a cleaner GraphQL search when we have an ISBN.
//
// ctx is a real Go context.Context threaded from the caller so a batch/import
// cancel aborts an in-flight lookup promptly (Audnexus's 9-region ASIN loop in
// particular). sc carries the richer search fields.
type ContextualSearch interface {
	SearchByContext(ctx context.Context, sc *SearchContext) ([]BookMetadata, error)
}
