// file: internal/server/metadata_bulk_fetch_log.go
// version: 1.1.0
// guid: c6c22a1e-80d9-4499-b80a-4a4d4112fa4d
// last-edited: 2026-09-28
//
// Per-book outcome lines and count wording for the bulk metadata fetch
// (runBulkMetadataFetchAll / runBulkMetadataFetchForBookIDs) op log.

package server

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// bulkFetchCounts renders the bulk fetch's running outcome counts. "found"
// is the ledger's "cached" status: the providers returned candidates and they
// were cached for review.
func bulkFetchCounts(found, notFound, skipped, errored int64) string {
	return fmt.Sprintf("found %d, not found %d, skipped %d, errors %d", found, notFound, skipped, errored)
}

// bulkFetchBook is one book as the bulk fetch's op log names it: the book's
// own title and author (as stored, placeholders and all), its id and path for
// a title that identifies nothing, and the query actually searched.
type bulkFetchBook struct {
	id, title, author, path string
	// query is what was searched (metabatch.ResolveCandidateSearchQuery);
	// its Source is SearchQuerySourceTitle for a book searched by its title.
	query metabatch.CandidateSearchQuery
}

func (b bulkFetchBook) label() string { return opLogBookRef(b.title, b.author, b.id, b.path) }

// searchedTitle is the title the providers were asked for.
func (b bulkFetchBook) searchedTitle() string {
	if b.query.Title != "" {
		return b.query.Title
	}
	return b.title
}

// via says how a book with no usable title of its own was searched, e.g.
// ` [searched transcribed_title by "Planet Hulk"]`; "" for its own title.
func (b bulkFetchBook) via() string {
	if b.query.Title == "" || b.query.Source == "" || b.query.Source == metabatch.SearchQuerySourceTitle {
		return ""
	}
	return fmt.Sprintf(" [searched %s by %s]", b.query.Source, opLogQuoted(b.query.Title))
}

// authorPhrase names the author hint the search sent (metafetch.SearchAuthorHint):
// a placeholder author is never sent, and the line says so.
func (b bulkFetchBook) authorPhrase() string {
	if a := metafetch.SearchAuthorHint(b.author); a != "" {
		return "author " + opLogQuoted(a)
	}
	if strings.TrimSpace(b.author) != "" && authorname.IsPlaceholderAuthor(b.author) {
		return "no author hint (placeholder " + opLogQuoted(b.author) + " not sent)"
	}
	return "no author hint"
}

// bulkFetchOutcomeLine renders one book's bulk-fetch outcome for the op log
// and returns the operations.ProgressReporter level for it ("info", or "warn"
// for a provider error). status is out.Status() (a metafetch.FetchStatus*).
func bulkFetchOutcomeLine(b bulkFetchBook, out metafetch.ChainOutcome, status string) (level, msg string) {
	book := b.label()
	cache := ""
	if out.CacheHit {
		cache = " (from cache)"
	}
	switch status {
	case metafetch.FetchStatusCached:
		variant := ""
		if out.Variant != "" {
			variant = " via title variant " + opLogQuoted(out.Variant)
		}
		top := ""
		if len(out.Results) > 0 {
			top = fmt.Sprintf(", top %s", opLogBookLabel(out.Results[0].Title, out.Results[0].Author))
		}
		return "info", fmt.Sprintf("found: %s → %d candidate(s) from %s%s%s%s%s",
			book, len(out.Results), logger.SanitizeLogValue(out.SourceName), top, variant, b.via(), cache)
	case metafetch.FetchStatusFetchError:
		errText := ""
		if out.Err != nil {
			errText = out.Err.Error()
		}
		return "warn", fmt.Sprintf("error: %s — provider %s failed, left retryable: %s%s",
			book, logger.SanitizeLogValue(out.ErrSource), logger.SanitizeLogValue(errText), b.via())
	default:
		src := b.query.Source
		if src == "" {
			src = metabatch.SearchQuerySourceTitle
		}
		return "info", fmt.Sprintf("no match: %s — searched %s %s, %s: no provider returned a result%s",
			book, src, opLogQuoted(b.searchedTitle()), b.authorPhrase(), cache)
	}
}

// bulkFetchSkippedNoTitleLine is the op-log line for a book the bulk fetch
// declines to search because neither its title nor any fallback (transcribed
// title, folder name) is usable. kind says what its own title is.
func bulkFetchSkippedNoTitleLine(b bulkFetchBook, kind string) string {
	return fmt.Sprintf("skipped: %s — %s, not searched: %s", b.label(), kind, metabatch.SkipDetailNoUsableTitle)
}
